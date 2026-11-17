package bridge

import (
	"context"
	"fmt"
	"strings"
	"time"

	"qqbot_bridge/internal/config"
)

// handleCommand 处理斜杠指令。
func (b *Bridge) handleCommand(ctx context.Context, t chatTarget, content string) {
	fields := strings.Fields(content)
	cmd := strings.ToLower(fields[0])
	args := fields[1:]

	switch cmd {
	case "/help", "/?", "/start":
		// 帮助按当前会话的 agent 渲染：切到 Cursor 的会话不该看到 CodeBuddy 的 /sync
		title, canSync := b.AgentTitle(), true
		if sess, ok := currentSession(b.cfg.Get()); ok {
			title = agentTitle(b.driverName(sess))
			canSync = b.canMirror(sess)
		}
		markdown, plain := renderHelp(title, canSync)
		r := t.replier(b.api, 1, b.cfg.Get().Push.Markdown)
		r.SetLogger(b.logf)
		r.Rich(ctx, markdown, plain)

	case "/ping":
		b.reply(ctx, t, "pong · "+time.Now().Format("15:04:05"))

	case "/whoami":
		bound := b.cfg.Get().Bind
		hint := "\n（单聊 openid）"
		if t.isGroup() {
			hint = "\n（群聊里用的是群成员 openid）"
		}
		b.reply(ctx, t, "你的 openid："+bound.Label(t.from)+hint)

	case "/allow", "/deny":
		if len(args) == 0 {
			b.reply(ctx, t, "**用法**：`/allow <openid>` 加入白名单，`/deny <openid>` 移出，`/allow list` 查看当前名单。")
			return
		}
		b.reply(ctx, t, b.whitelistCommand(strings.EqualFold(cmd, "/allow"), args[0]))

	case "/list", "/ls":
		b.sendSessionList(ctx, t)

	case "/driver", "/agent":
		if len(args) == 0 {
			b.reply(ctx, t, b.driverCommand(""))
			return
		}
		b.reply(ctx, t, b.driverCommand(args[0]))

	case "/models":
		// 查询可能要刷新模型目录，别堵住后面的事件；后面跟的词用来筛选
		filter := strings.Join(args, " ")
		go b.sendModelList(ctx, t, filter)

	case "/model":
		if len(args) == 0 {
			go b.sendModelList(ctx, t, "")
			return
		}
		// 模型展示名可能带空格，比如 Xiaomi MiMo-V2.6-Pro
		arg := strings.Join(args, " ")
		go func() { b.reply(ctx, t, b.switchModel(ctx, arg)) }()

	case "/menu":
		b.reply(ctx, t, b.setupMenu(ctx))

	case "/switch", "/use":
		if len(args) == 0 {
			// 不带参数就直接把可点的列表发过去，省得再记 /list
			b.sendSessionList(ctx, t)
			return
		}
		b.reply(ctx, t, b.switchSession(args[0]))

	case "/new":
		if len(args) == 0 {
			// 不带参数 = 在当前会话的目录里再开一个独立会话
			b.reply(ctx, t, b.newSessionHere())
			return
		}
		b.reply(ctx, t, b.newSession(args[0], strings.Join(args[1:], " ")))

	case "/status":
		b.reply(ctx, t, b.statusText())

	case "/sync":
		b.reply(ctx, t, b.syncCurrentSession())

	case "/reset":
		b.reply(ctx, t, b.resetSession())

	case "/rename":
		if len(args) == 0 {
			b.reply(ctx, t, "**用法**：`/rename <新名称>`\n给当前会话换个名字，只改标签不动目录。")
			return
		}
		b.reply(ctx, t, b.renameSession(strings.Join(args, " ")))

	case "/rm", "/del":
		if len(args) == 0 {
			b.reply(ctx, t, "**用法**：`/rm <编号|会话ID>`\n只移除会话记录，不删目录。发送 `/list` 查看全部。")
			return
		}
		b.reply(ctx, t, b.removeSession(args[0]))

	case "/stop":
		b.reply(ctx, t, b.stopCommand(args))

	default:
		// `/2` 这种纯编号直接当切换，少打字
		if n := atoi(strings.TrimPrefix(cmd, "/")); n > 0 {
			b.reply(ctx, t, b.switchSession(fmt.Sprint(n)))
			return
		}
		b.reply(ctx, t, "未知指令 `"+cmd+"`，发送 `/help` 查看可用指令。")
	}
}

// helpItem 是一条帮助：点击标签会把这个指令填进输入框。
type helpItem struct {
	cmd   string // 点标签填进输入框的内容
	usage string // 展示的用法
	desc  string
}

// renderHelp 渲染帮助：Markdown 版每条指令都配可点标签，纯文本版不带标签。
//
// title 是当前 agent 的展示名；canSync 为 false 时（驱动没有 IDE 历史库）
// 不列出 /sync，免得用户点了只得到「不需要同步」。
func renderHelp(title string, canSync bool) (string, string) {
	taskItems := []helpItem{
		{"/status", "/status", "当前会话、并行任务、目录与 Git 状态"},
		{"/stop", "/stop [会话|all]", "中断当前/指定/全部任务"},
	}
	if canSync {
		taskItems = append(taskItems, helpItem{"/sync", "/sync", "同步到 " + title + " IDE 历史对话"})
	}
	groups := []struct {
		title string
		items []helpItem
	}{
		{"会话", []helpItem{
			{"/list", "/list", "查看全部会话"},
			{"/switch", "/switch <编号>", "切换当前会话，不带参数等于 `/list`"},
			{"/new", "/new [目录] [名称]", "新建会话；不带目录就在当前项目再开一个"},
			{"/rename", "/rename <名称>", "重命名当前会话"},
			{"/rm", "/rm <编号>", "移除会话记录，不删目录"},
			{"/reset", "/reset", "清空当前会话的对话上下文"},
		}},
		{"模型", []helpItem{
			{"/models", "/models", "查看可选模型和当前倍率"},
			{"/model", "/model <模型ID>", "只切当前会话的模型"},
			{"/model default", "/model default", "恢复全局默认模型"},
		}},
		{"agent", []helpItem{
			{"/driver", "/driver", "查看当前会话用的 agent（CodeBuddy / Cursor）"},
			{"/driver", "/driver <名字>", "切换当前会话的 agent，会清空该会话上下文"},
		}},
		{"任务", taskItems},
		{"其他", []helpItem{
			{"/menu", "/menu", "配置输入框下方的快捷菜单"},
			{"/ping", "/ping", "检查机器人是否在线"},
			{"/whoami", "/whoami", "查看自己的 openid（加白名单要用）"},
			{"/allow", "/allow <openid>", "加入白名单"},
			{"/deny", "/deny <openid>", "移出白名单"},
			{"/help", "/help", "显示本帮助"},
		}},
	}

	var md, plain strings.Builder
	md.WriteString("**可用指令**\n")
	plain.WriteString("可用指令\n")
	for _, g := range groups {
		md.WriteString("\n**" + g.title + "**\n")
		plain.WriteString("\n" + g.title + "\n")
		for _, it := range g.items {
			// 标签放行尾，命令本身已经在行首展示，标签只显示短指令名
			fmt.Fprintf(&md, "- `%s` %s %s\n", it.usage, it.desc, cmdInput(it.cmd, it.cmd))
			fmt.Fprintf(&plain, "- %s %s\n", it.usage, it.desc)
		}
	}

	tail := "\n直接发文字或语音 = 交给当前会话的 " + title + " 执行，过程、结果和新生成的截图会自动发回来。\n"
	md.WriteString(tail)
	md.WriteString("\n点指令标签会把指令填进输入框，需要参数的补上参数再发送。")
	plain.WriteString(tail)
	plain.WriteString("\n发送 /指令 执行，需要参数的把参数接在后面。")
	return md.String(), plain.String()
}

// rememberName 记住用户的昵称，只用于展示；昵称没变就不写盘。
func (b *Bridge) rememberName(openID, name string) {
	name = strings.TrimSpace(name)
	if openID == "" || name == "" {
		return
	}
	if b.cfg.Get().Bind.Names[openID] == name {
		return
	}
	_ = b.cfg.Update(func(c *config.Config) error {
		if c.Bind.Names == nil {
			c.Bind.Names = make(map[string]string, 4)
		}
		c.Bind.Names[openID] = name
		return nil
	})
}

// whitelistCommand 维护白名单：/allow <openid>、/deny <openid>、/allow list。
func (b *Bridge) whitelistCommand(allow bool, arg string) string {
	arg = strings.TrimSpace(arg)
	if strings.EqualFold(arg, "list") {
		bound := b.cfg.Get().Bind
		lines := []string{"**白名单**", ""}
		if bound.UserOpenID != "" {
			lines = append(lines, "- "+bound.Label(bound.UserOpenID)+" · 绑定者，始终可用")
		}
		n := 0
		for _, id := range bound.Whitelist {
			if id == "" || id == bound.UserOpenID {
				continue
			}
			lines = append(lines, "- "+bound.Label(id))
			n++
		}
		if n == 0 {
			lines = append(lines, "", "暂无其他用户。")
		}
		lines = append(lines, "", "添加：`/allow <openid>`，移除：`/deny <openid>`")
		return strings.Join(lines, "\n")
	}
	if arg == "" {
		return "**用法**：`/allow <openid>`"
	}
	bound := b.cfg.Get().Bind
	if !allow && arg == bound.UserOpenID {
		return "绑定者始终可用，不能移出白名单。"
	}
	err := b.cfg.SetAllowed(arg, allow)
	if err != nil {
		return "更新白名单失败：" + err.Error()
	}
	label := b.cfg.Get().Bind.Label(arg)
	if allow {
		return "已加入白名单：" + label
	}
	return "已移出白名单：" + label
}
