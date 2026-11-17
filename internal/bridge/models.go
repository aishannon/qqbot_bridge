package bridge

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"qqbot_bridge/internal/agent"
	"qqbot_bridge/internal/config"
)

// modelDefaultArg 让 /model 恢复跟随 CLI 默认模型。
const modelDefaultArg = "default"

// modelProvider 返回指定驱动的模型目录能力；不支持时给出明确原因。
//
// 模型是各 CLI 自己的概念：CodeBuddy 读本地缓存和 --help，Cursor 走自己的
// 模型列表。桥接只用这层拿目录，不关心来源；同一实例里两个会话可能用不同
// 驱动，所以这里必须带上驱动名，不能读全局配置。
func (b *Bridge) modelProvider(name string) (agent.ModelProvider, error) {
	driver, err := b.open(name)
	if err != nil {
		return nil, err
	}
	provider, ok := driver.(agent.ModelProvider)
	if !ok {
		return nil, fmt.Errorf("当前 agent 驱动（%s）不支持模型列表", driver.Name())
	}
	return provider, nil
}

// modelCatalog 查询某个驱动的模型目录，可能触发一次刷新。
func (b *Bridge) modelCatalog(ctx context.Context, name string) (agent.ModelCatalog, error) {
	provider, err := b.modelProvider(name)
	if err != nil {
		return agent.ModelCatalog{}, err
	}
	return provider.Models(ctx)
}

// modelRate 只读某个驱动缓存里的倍率，不触发刷新。
func (b *Bridge) modelRate(name, id string) (string, time.Time, bool) {
	provider, err := b.modelProvider(name)
	if err != nil {
		return "", time.Time{}, false
	}
	return provider.ModelRate(id)
}

// modelFor 取会话实际使用的模型：会话自己的设置 > 全局默认 > CLI 默认（空串）。
func modelFor(cfg config.Config, sess config.Session) string {
	if sess.Model != "" {
		return sess.Model
	}
	return cfg.Agent.Model
}

// modelSource 说明当前模型来自哪里。
func modelSource(cfg config.Config, sess config.Session) string {
	switch {
	case sess.Model != "":
		return "本会话"
	case cfg.Agent.Model != "":
		return "全局默认"
	default:
		return "CLI 默认"
	}
}

// modelFooter 组装消息末尾的「Agent · 模型」标签。
//
// 同一实例里不同会话可能用不同驱动和模型，只写模型 ID 时看不出是哪个
// agent 跑的，所以两者都带上。没显式配置模型时读 CLI 自己的默认模型，
// 让用户看到真正在跑的是哪个模型，而不是一句「CLI 默认」。
func (b *Bridge) modelFooter(sess config.Session) string {
	cfg := b.cfg.Get()
	driver := cfg.DriverOf(sess)
	title := agentTitle(driver)

	id, fromCLI := b.effectiveModel(driver, cfg, sess)
	if id == "" {
		return fmt.Sprintf("Agent：%s · 模型：CLI 默认", title)
	}

	model := "`" + id + "`"
	if name := b.modelName(driver, id); name != "" && !strings.EqualFold(name, id) {
		model = fmt.Sprintf("%s（`%s`）", name, id)
	}
	if fromCLI {
		return fmt.Sprintf("Agent：%s · 模型：%s · CLI 默认", title, model)
	}
	return fmt.Sprintf("Agent：%s · 模型：%s", title, model)
}

// effectiveModel 返回实际会跑的模型 ID：会话/全局配置优先，否则读 CLI 的
// 默认模型；fromCLI 表示这个 ID 不是桥接配置的。
func (b *Bridge) effectiveModel(driver string, cfg config.Config, sess config.Session) (id string, fromCLI bool) {
	if id := modelFor(cfg, sess); id != "" {
		return id, false
	}
	if def, ok := b.defaultModel(driver); ok {
		return def, true
	}
	return "", false
}

// defaultModel 返回驱动自己的默认模型 ID，驱动不支持时返回 false。
func (b *Bridge) defaultModel(driver string) (string, bool) {
	d, err := b.open(driver)
	if err != nil {
		return "", false
	}
	def, ok := d.(agent.DefaultModeler)
	if !ok {
		return "", false
	}
	return def.DefaultModel()
}

// modelName 只读缓存取模型展示名，不触发刷新：确认消息要立刻发出。
func (b *Bridge) modelName(driver, id string) string {
	d, err := b.open(driver)
	if err != nil {
		return ""
	}
	namer, ok := d.(agent.ModelNamer)
	if !ok {
		return ""
	}
	name, _ := namer.ModelName(id)
	return name
}

// withModel 返回带上指定模型的副本，用于切换后展示。
func withModel(s config.Session, model string) config.Session {
	s.Model = model
	return s
}

// describeModel 把模型 ID 展示成「名称（ID）· 倍率」；不在目录里就只给 ID。
// CLI 根本不提供倍率时（NoRates）不写「倍率未知」。
func describeModel(c agent.ModelCatalog, id string) string {
	if id == "" {
		return "CLI 默认"
	}
	if m, ok := c.Find(id); ok {
		if c.NoRates {
			return fmt.Sprintf("%s（`%s`）", m.Name, m.ID)
		}
		return fmt.Sprintf("%s（`%s`）· %s", m.Name, m.ID, m.RateText())
	}
	return "`" + id + "`"
}

// modelListLimit 是一条 /models 消息里最多列出的模型数。
// Cursor 账号下动辄两三百个，全列出来会超过单条消息长度，剩下的靠关键词筛选。
const modelListLimit = 40

// filterModels 按关键词筛选：空格分隔的每个词都要出现在 ID 或名称里（不区分大小写）。
func filterModels(models []agent.ModelInfo, filter string) []agent.ModelInfo {
	words := strings.Fields(strings.ToLower(filter))
	if len(words) == 0 {
		return models
	}
	var out []agent.ModelInfo
	for _, m := range models {
		hay := strings.ToLower(m.ID + " " + m.Name)
		if !slices.ContainsFunc(words, func(w string) bool { return !strings.Contains(hay, w) }) {
			out = append(out, m)
		}
	}
	return out
}

// updatedText 展示倍率更新时间；不是今天的数据要带上日期，免得被当成当天倍率。
func updatedText(t, now time.Time) string {
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return "今天 " + t.Format("15:04")
	}
	return t.Format("01-02 15:04")
}

// modelListView 是一次 /models 渲染所需的上下文。
type modelListView struct {
	title    string // agent 展示名，如 CodeBuddy、Cursor
	current  string // 当前会话实际使用的模型 ID
	source   string // 当前模型的来源
	canReset bool   // 会话单独设过模型时，给出「恢复全局默认」入口
	filter   string // /models 后面跟的关键词
}

// renderModelList 渲染模型列表：Markdown 版每行带切换标签，纯文本版带 ID 方便手打。
func renderModelList(c agent.ModelCatalog, v modelListView, now time.Time) (string, string) {
	var md, plain strings.Builder

	models := filterModels(c.Models, v.filter)
	head := fmt.Sprintf("\n当前：%s · 来源：%s\n", describeModel(c, v.current), v.source)
	if c.NoRates {
		head += fmt.Sprintf("共 %d 个模型，%s 不提供倍率，按 CLI 默认顺序\n", len(c.Models), v.title)
	} else {
		head += fmt.Sprintf("倍率更新于 %s，按倍率从低到高\n", updatedText(c.UpdatedAt, now))
	}
	if c.RefreshErr != nil {
		head += "⚠️ 这次刷新失败，显示的是旧数据\n"
	}
	if v.filter != "" {
		head += fmt.Sprintf("筛选「%s」：%d 个\n", v.filter, len(models))
	}
	md.WriteString("**" + v.title + " 模型**" + head + "\n")
	plain.WriteString(v.title + " 模型" + strings.ReplaceAll(head, "`", "") + "\n")

	rest := 0
	if len(models) > modelListLimit {
		rest = len(models) - modelListLimit
		models = models[:modelListLimit]
	}
	rate := func(m agent.ModelInfo) string {
		if c.NoRates {
			return ""
		}
		return " · " + m.RateText()
	}
	for _, m := range models {
		if m.ID == v.current {
			fmt.Fprintf(&md, "★ **%s**%s\n", m.Name, rate(m))
			fmt.Fprintf(&plain, "★ %s%s · %s\n", m.Name, rate(m), m.ID)
			continue
		}
		fmt.Fprintf(&md, "%s%s %s\n", m.Name, rate(m), cmdInput("/model "+m.ID, "切换"))
		fmt.Fprintf(&plain, "%s%s · %s\n", m.Name, rate(m), m.ID)
	}
	if v.filter != "" && len(models) == 0 {
		md.WriteString("没有匹配的模型，发送 `/models` 查看全部。\n")
		plain.WriteString("没有匹配的模型，发送 /models 查看全部。\n")
	}
	if rest > 0 {
		fmt.Fprintf(&md, "\n…还有 %d 个没列出，发送 `/models <关键词>` 筛选，例如 `/models opus`\n", rest)
		fmt.Fprintf(&plain, "\n…还有 %d 个没列出，发送 /models <关键词> 筛选，例如 /models opus\n", rest)
	}

	if v.canReset {
		fmt.Fprintf(&md, "\n本会话恢复全局默认：%s\n", cmdInput("/model "+modelDefaultArg, "/model "+modelDefaultArg))
	}
	plain.WriteString("\n发送 /model <ID> 切换当前会话的模型，/model default 恢复全局默认。")
	return md.String(), plain.String()
}

// sendModelList 处理 /models [关键词]：查询可能要刷新几秒，由调用方放到独立 goroutine。
func (b *Bridge) sendModelList(ctx context.Context, t chatTarget, filter string) {
	cfg := b.cfg.Get()
	sess, ok := currentSession(cfg)
	// 模型目录属于会话所在的 agent：会话切到 Cursor 时只能看到 Cursor 的模型
	name := cfg.Agent.Driver
	view := modelListView{source: "CLI 默认", filter: strings.TrimSpace(filter)}
	if ok {
		name = b.driverName(sess)
		view.current, view.source = modelFor(cfg, sess), modelSource(cfg, sess)
		view.canReset = sess.Model != ""
	}
	view.title = agentTitle(name)

	catalog, err := b.modelCatalog(ctx, name)
	if err != nil {
		b.logf("warn", "查询模型列表失败：%v", err)
		b.reply(ctx, t, "**查询模型列表失败**："+err.Error())
		return
	}
	markdown, plain := renderModelList(catalog, view, time.Now())
	r := t.replier(b.api, 1, cfg.Push.Markdown)
	r.SetLogger(b.logf)
	r.Rich(ctx, markdown, plain)
}

// switchModel 处理 /model <ID|名称|default>，只改当前会话。
// 模型按会话生效，runTask 每轮都读会话配置，所以不用重启，也不影响其他会话。
func (b *Bridge) switchModel(ctx context.Context, arg string) string {
	cfg := b.cfg.Get()
	sess, ok := currentSession(cfg)
	if !ok {
		return "还没有可用会话，先发送 `/new <目录>` 新建一个。"
	}
	driverName := b.driverName(sess)
	catalog, err := b.modelCatalog(ctx, driverName)
	if err != nil {
		return "**切换失败，模型未变**：" + err.Error()
	}

	target := ""
	if !strings.EqualFold(arg, modelDefaultArg) {
		m, ok := catalog.Find(arg)
		if !ok {
			return "没有模型 `" + arg + "`，模型未变。发送 `/models` 查看 " + agentTitle(driverName) + " 的可选模型。"
		}
		target = m.ID
	}
	name := displayName(sess)
	if target == sess.Model {
		if target == "" {
			return "会话「" + name + "」已经在跟随全局默认（" + describeModel(catalog, modelFor(cfg, sess)) + "），无需切换。"
		}
		return "会话「" + name + "」已经是 " + describeModel(catalog, target) + "，无需切换。"
	}

	previous := describeModel(catalog, modelFor(cfg, sess))
	if err := b.cfg.Update(func(c *config.Config) error {
		for i := range c.Sessions {
			if c.Sessions[i].ID == sess.ID {
				c.Sessions[i].Model = target
			}
		}
		return nil
	}); err != nil {
		return "**切换失败，模型未变**：" + err.Error()
	}

	b.logf("info", "会话 %s 切换模型：%q -> %q", sess.ID, sess.Model, target)
	label := describeModel(catalog, modelFor(b.cfg.Get(), withModel(sess, target)))
	if target == "" {
		label += " · 全局默认"
	}
	return fmt.Sprintf("**已切换模型**：会话「%s」→ %s\n原模型：%s\n只影响这个会话，下一条指令生效；正在执行的任务不受影响。", name, label, previous)
}
