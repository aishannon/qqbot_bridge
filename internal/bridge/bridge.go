// Package bridge 把 QQ 单聊/群聊消息和本机 agent 会话连起来。
//
// 一条入站消息要么是斜杠指令（/list、/switch、/status…），要么原样交给
// 当前会话的 agent 执行；执行过程中收集工具调用与文本，结束后把过程、
// 结果和这一轮新生成的截图按 QQ 的被动回复配额发回去。
//
// 文件分工：
//
//	bridge.go    消息入口、白名单、去重、驱动解析
//	cmd.go       斜杠指令分发与帮助
//	session.go   会话的增删改查与路径工具
//	status.go    /status 文本与 Git 概况
//	task.go      任务执行、中断、进度、截图回传
//	sync.go      transcript 跟读与 IDE 历史库镜像
//	models.go    /models /model 指令
//	discover.go  自动注册 agent 正在使用的目录
//	util.go      纯文本格式化小工具
package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"qqbot_bridge/internal/agent"
	"qqbot_bridge/internal/config"
	"qqbot_bridge/internal/gateway"
	"qqbot_bridge/internal/msglog"
)

// typingInterval 是「正在输入」保活间隔：单次状态最多维持 60 秒，留出余量。
const typingInterval = 50 * time.Second

// progressInterval 是长任务的进度推送间隔。
const progressInterval = 2 * time.Minute

// basePrompt 追加给所有 agent，约束它在手机 QQ 上的输出形态。
const basePrompt = "你正在通过 QQ 单聊接收指令，回复会以 Markdown 消息发到用户手机：" +
	"可以用加粗、列表、行内代码和短代码块，但不要用表格，先给结论，正文尽量控制在 300 字以内。" +
	"用户随消息发来的图片/文件已经保存到工作目录的 .inbox/ 下，指令里会给绝对路径。" +
	"需要用户看到界面时，把截图保存到当前工作目录下（例如 .shots/shot.png），" +
	"桥接服务会自动把这一轮新生成的图片发回给用户。"

// teamPrompt 是 CodeBuddy 独有的组队说明：TeamCreate / 带 name 的 Agent
// 是 CodeBuddy 的工具，别的 agent 没有，发过去只会让它找不到工具。
const teamPrompt = "用户要求组队、团队或多人并行时，必须先调用 TeamCreate 建队，再用带 name 参数的 Agent 派生成员，" +
	"不要用普通子代理顶替；成员的每条消息都会以「@角色名」单独推送到用户手机。"

// promptFor 返回追加给指定驱动的系统说明。
func promptFor(driver string) string {
	if driver == config.DriverCodeBuddy {
		return basePrompt + teamPrompt
	}
	return basePrompt
}

// agentTitle 返回驱动在用户可见文案里的名字。
func agentTitle(driver string) string {
	switch driver {
	case config.DriverCodeBuddy, "":
		return "CodeBuddy"
	case config.DriverCursor:
		return "Cursor"
	default:
		return driver
	}
}

// AgentTitle 返回当前配置的 agent 展示名（CodeBuddy、Cursor…），
// 供 QQ 文案和 Web 管理端使用，不需要真的构造驱动。
func (b *Bridge) AgentTitle() string { return agentTitle(b.cfg.Get().Agent.Driver) }

// AgentTitleFor 返回任意驱动名对应的展示名，供 Web 端按会话渲染。
func AgentTitleFor(driver string) string { return agentTitle(driver) }

// LogFunc 是日志回调。
type LogFunc func(level, format string, args ...any)

// Bridge 是消息路由器。
type Bridge struct {
	cfg  *config.Store
	api  *gateway.API
	logf LogFunc

	mu       sync.Mutex
	tasks    map[string]agent.Task   // sessionID -> 正在执行的任务
	starting map[string]bool         // sessionID -> 正在启动，防止同一会话并发起两个进程
	seen     map[string]time.Time    // msg_id -> 首次处理时间，用于去重
	last     map[string]TaskSnapshot // sessionID -> 最近一次任务快照

	// msgs 记录收到的每条消息，供 Web 管理端查看；为 nil 时不记录。
	msgs *msglog.Log

	// ideMu 串行化 IDE 历史同步，避免任务中的周期同步和结束同步同时写索引。
	ideMu sync.Mutex

	// openDriver 按驱动名解析 agent 驱动，测试时替换掉，避免真的调用 CLI。
	openDriver func(cfg config.Config, name string) (agent.Driver, error)
}

// SetMessageLog 设置收到消息的记录器。
func (b *Bridge) SetMessageLog(l *msglog.Log) { b.msgs = l }

// defaultOpenDriver 是 openDriver 的默认实现：按驱动名从注册表取驱动。
func defaultOpenDriver(cfg config.Config, name string) (agent.Driver, error) {
	if name == "" {
		name = cfg.Agent.Driver
	}
	return agent.Open(name, agent.Settings{
		Command:      cfg.CommandFor(name),
		DiscoverRoot: cfg.Discover.Root,
	})
}

// open 按驱动名构造驱动实例。驱动是无状态的（每轮任务各自起进程、模型缓存是包级），
// 所以不缓存，按会话需要随取随构造。
func (b *Bridge) open(name string) (agent.Driver, error) {
	open := b.openDriver
	if open == nil {
		open = defaultOpenDriver
	}
	return open(b.cfg.Get(), name)
}

// driverForSession 返回某个会话该用的驱动：会话指定优先，否则全局默认。
func (b *Bridge) driverForSession(sess config.Session) (agent.Driver, error) {
	return b.open(b.cfg.Get().DriverOf(sess))
}

// driverName 返回会话使用的驱动名，不构造驱动（只需要展示名或比较时用）。
func (b *Bridge) driverName(sess config.Session) string {
	return b.cfg.Get().DriverOf(sess)
}

// defaultDriver 返回全局默认驱动：自动发现、启动预检、User-Agent 用它。
func (b *Bridge) defaultDriver() (agent.Driver, error) {
	return b.open(b.cfg.Get().Agent.Driver)
}

// recordMessage 记录一条收到的消息。
//
// 被受理（含重复推送）的消息会带上收到那一刻的当前会话，说明它属于哪个对话；
// 被拒绝的消息没有对话可归属，不带。
func (b *Bridge) recordMessage(t chatTarget, nick, status, kind, content string, attachments int, msgID string) {
	if b.msgs == nil {
		return
	}
	cfg := b.cfg.Get()
	if nick == "" {
		nick = cfg.Bind.Names[t.from]
	}
	e := msglog.Entry{
		Group:       t.groupID,
		From:        t.from,
		Name:        nick,
		Status:      status,
		Kind:        kind,
		Content:     content,
		Attachments: attachments,
		MsgID:       msgID,
	}
	if status != msglog.StatusDenied {
		e.Command = kind != msglog.KindAttachment && strings.HasPrefix(strings.TrimSpace(content), "/")
		if sess, ok := currentSession(cfg); ok {
			e.Session = sess.ID
			e.SessionName = displayName(sess)
			e.SessionDir = sess.Dir
			e.Conversation = sess.AgentID
		}
	}
	b.msgs.Add(e)
}

// TaskSnapshot 是给控制台展示的任务状态。
type TaskSnapshot struct {
	Running   bool      `json:"running"`
	Prompt    string    `json:"prompt"`
	SessionID string    `json:"sessionId"`
	StartedAt time.Time `json:"startedAt"`

	Tools  []string `json:"tools"`
	Result string   `json:"result"`
	Error  string   `json:"error"`
}

// New 创建桥接器。
func New(cfg *config.Store, api *gateway.API, logf LogFunc) *Bridge {
	if logf == nil {
		logf = func(string, string, ...any) {}
	}
	return &Bridge{
		cfg:        cfg,
		api:        api,
		logf:       logf,
		tasks:      make(map[string]agent.Task),
		starting:   make(map[string]bool),
		seen:       make(map[string]time.Time),
		last:       make(map[string]TaskSnapshot),
		openDriver: defaultOpenDriver,
	}
}

// HandleEvent 处理网关事件：单聊消息交给指令/任务管道，按钮点击走互动处理。
func (b *Bridge) HandleEvent(ctx context.Context, ev gateway.Event) {
	if ev.Type == gateway.EventInteractionCreate {
		b.handleInteraction(ctx, ev)
		return
	}
	if ev.Type != gateway.EventC2CMessage && ev.Type != gateway.EventGroupAtMessage {
		return
	}
	var msg gateway.InboundMessage
	if err := json.Unmarshal(ev.Data, &msg); err != nil {
		b.logf("warn", "解析消息事件失败：%v", err)
		return
	}

	target := chatTarget{msgID: msg.ID}
	if ev.Type == gateway.EventGroupAtMessage {
		target.groupID = msg.GroupOpen
	}
	if msg.Author != nil {
		// 群聊的发送者放在 member_openid，单聊放在 user_openid
		target.from = msg.Author.MemberOpen
		if target.from == "" {
			target.from = msg.Author.UserOpenID
		}
		if target.from == "" {
			target.from = msg.Author.ID
		}
	}
	if target.from == "" {
		return
	}
	if !target.isGroup() {
		target.userID = target.from
	}

	nick := ""
	if msg.Author != nil {
		nick = msg.Author.Username
	}
	if nick == "" && msg.Member != nil {
		nick = msg.Member.Nick
	}
	b.rememberName(target.from, nick)

	// 白名单：单聊和群聊共用一套名单，不在名单里的只回一句无权限。
	// `/whoami` 例外——不给 openid 就没法把自己加进名单。
	bound := b.cfg.Get().Bind
	if !bound.Allowed(target.from) {
		if strings.EqualFold(strings.TrimSpace(msg.Content), "/whoami") {
			// 陌生人靠 /whoami 拿 openid，同样要留痕，管理端才能在「最近被拒绝」里看到他
			b.recordMessage(target, nick, msglog.StatusDenied, msglog.KindText, msg.Content, 0, msg.ID)
			b.reply(ctx, target, "你的 openid：`"+target.from+"`，把它发给管理员加入白名单。")
			return
		}
		b.recordMessage(target, nick, msglog.StatusDenied, msglog.KindText, msg.Content, len(msg.Attachments), msg.ID)
		b.logf("warn", "拒绝非白名单用户：%s（群=%s）", target.from, target.groupID)
		b.reply(ctx, target, "无权限使用这个机器人。先发 `/whoami` 拿到你的 openid，让管理员加入白名单后再试。")
		return
	}
	if b.isDuplicate(msg.ID) {
		// 网关重推同一 msg_id 时直接丢弃；不打日志的话，用户端看起来就是
		// 「消息发出去了但机器人没反应」，排查时完全无迹可循。
		b.recordMessage(target, nick, msglog.StatusDuplicate, msglog.KindText, msg.Content, len(msg.Attachments), msg.ID)
		b.logf("info", "忽略重复推送的消息：%s", msg.ID)
		return
	}

	content := strings.TrimSpace(msg.Content)
	display := ""
	kind := msglog.KindText
	attached := 0
	rawText := "" // 带附件时记录用户原话，不记录拼进去的本地路径

	// 语音优先：平台已经把语音转写成文字放在附件里，不需要自己识别
	if att, isVoice := pickVoice(msg.Attachments); isVoice {
		text := voiceText(att, content)
		if text == "" {
			b.reply(ctx, target, "这条语音没有识别出文字，麻烦重发一次或直接打字。")
			return
		}
		b.logf("info", "语音转写：%s", oneLine(text, 120))
		content = text
		display = "🎤 " + text
		kind = msglog.KindVoice
	}

	// 图片/文件：先落到会话工作目录的 .inbox，再把路径写进指令交给 agent
	if files := nonVoiceAttachments(msg.Attachments); len(files) > 0 {
		sess, ok := currentSession(b.cfg.Get())
		if !ok {
			b.reply(ctx, target, "还没有可用会话，先发送 `/new <目录>` 新建一个。")
			return
		}
		saved, err := saveAttachments(ctx, sess.Dir, files, b.logf)
		if err != nil {
			b.reply(ctx, target, "附件下载失败："+err.Error())
			return
		}
		if len(saved) > 0 {
			rawText = content
			attached = len(saved)
			kind = msglog.KindAttachment
			content = withAttachments(content, saved)
			if display == "" {
				display = fmt.Sprintf("📎 附件 %d 个", len(saved))
			}
			b.logf("info", "附件已保存到 %s：%v", inboxDir, saved)
		}
	}

	if content == "" {
		return
	}

	logged := content
	if kind == msglog.KindAttachment {
		logged = strings.TrimSpace(rawText)
		if logged == "" {
			logged = display
		}
	}
	b.recordMessage(target, nick, msglog.StatusAccepted, kind, logged, attached, msg.ID)

	scene := "单聊"
	if target.isGroup() {
		scene = "群聊"
	}
	b.logf("info", "收到%s消息：%s", scene, oneLine(content, 120))
	if strings.HasPrefix(content, "/") {
		b.handleCommand(ctx, target, content)
		return
	}
	b.startTask(ctx, target, content, display)
}

// isDuplicate 按 msg_id 去重，平台可能重复推送同一事件。
func (b *Bridge) isDuplicate(id string) bool {
	if id == "" {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	for k, t := range b.seen {
		if now.Sub(t) > 10*time.Minute {
			delete(b.seen, k)
		}
	}
	if _, ok := b.seen[id]; ok {
		return true
	}
	b.seen[id] = now
	return false
}

// chatTarget 是一条消息的回复目标：单聊看 userID，群聊看 groupID。
type chatTarget struct {
	from    string // 发消息的人，用于白名单判断
	userID  string // 单聊对端 openid
	groupID string // 群 openid
	msgID   string
}

// isGroup 表示这条消息来自群聊。
func (t chatTarget) isGroup() bool { return t.groupID != "" }

// replier 按场景创建回复器。
func (t chatTarget) replier(api *gateway.API, max int, markdown bool) *Replier {
	if t.isGroup() {
		return NewGroupReplier(api, t.groupID, t.msgID, max, markdown)
	}
	return NewReplier(api, t.userID, t.msgID, max, markdown)
}

// reply 用一次性回复器发一条文本。
func (b *Bridge) reply(ctx context.Context, t chatTarget, text string) {
	r := t.replier(b.api, 1, b.cfg.Get().Push.Markdown)
	r.SetLogger(b.logf)
	r.Text(ctx, text)
}
