package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"qqbot_bridge/internal/config"
	"qqbot_bridge/internal/gateway"
)

// 按钮回传的动作前缀。
const (
	btnSwitchPrefix = "switch:"
	btnActionList   = "list"
	btnActionStatus = "status"
	btnActionStop   = "stop"
	btnActionMenu   = "menu"
)

// maxSessionTags 限制列表里配指令标签的会话数量：再多手机上就得翻好几屏。
const maxSessionTags = 12

// handleInteraction 处理消息按钮和单聊快捷菜单的点击。
//
// 平台要求 3 秒内回应 interaction，所以先应答再干活，否则用户端一直转圈。
func (b *Bridge) handleInteraction(ctx context.Context, ev gateway.Event) {
	var it gateway.Interaction
	if err := json.Unmarshal(ev.Data, &it); err != nil {
		b.logf("warn", "解析互动事件失败：%v", err)
		return
	}
	if err := b.api.AckInteraction(ctx, it.ID, 0); err != nil {
		b.logf("warn", "回应按钮点击失败：%v", err)
	}
	if it.UserOpenID == "" {
		return
	}

	bound := b.cfg.Get().Bind
	if !bound.Allowed(it.UserOpenID) {
		b.logf("warn", "忽略非白名单用户的按钮点击：%s", it.UserOpenID)
		return
	}
	target := chatTarget{from: it.UserOpenID, userID: it.UserOpenID}
	if it.GroupOpen != "" {
		target = chatTarget{from: it.UserOpenID, groupID: it.GroupOpen}
	}

	action := strings.TrimSpace(it.Data.Resolved.ButtonData)
	if action == "" {
		return
	}
	b.logf("info", "按钮点击：%s", oneLine(action, 60))

	// 点击来自互动事件，没有可用的 msg_id，直接走主动消息回复
	switch {
	case strings.HasPrefix(action, btnSwitchPrefix):
		b.reply(ctx, target, b.switchSession(strings.TrimPrefix(action, btnSwitchPrefix)))
	case action == btnActionList:
		b.sendSessionList(ctx, target)
	case action == btnActionStatus:
		b.reply(ctx, target, b.statusText())
	case action == btnActionStop:
		b.reply(ctx, target, b.stopCommand(nil))
	case action == btnActionMenu:
		b.reply(ctx, target, b.setupMenu(ctx))
	default:
		b.logf("warn", "未知按钮动作：%s", action)
	}
}

// sendSessionList 发送会话列表：Markdown 正文里每个会话配一个指令标签，
// 点一下就把 `/switch N` 填进输入框，不用记编号。
//
// 指令标签只在 Markdown 消息里生效；机器人没有 Markdown 权限时退回纯文本列表。
func (b *Bridge) sendSessionList(ctx context.Context, t chatTarget) {
	b.SyncDiscovered()
	cfg := b.cfg.Get()
	markdown, plain := renderSessionList(cfg)
	r := t.replier(b.api, 1, cfg.Push.Markdown)
	r.SetLogger(b.logf)
	if r.Rich(ctx, markdown, plain) {
		// 列表已经送到用户手机上，清掉「新增」标记，下次不再重复标红
		b.clearNewFlags()
	}
}

// renderSessionList 同时渲染 Markdown（带指令标签）和纯文本两份，
// 后者用于没有 Markdown 权限时降级，不能带标签源码。
func renderSessionList(cfg config.Config) (string, string) {
	var md strings.Builder
	var plain strings.Builder

	hasNew := false
	for _, s := range cfg.Sessions {
		if s.New {
			hasNew = true
			break
		}
	}
	header := "（★ 为当前）"
	plainHeader := "（★ 为当前）"
	if hasNew {
		header += " · 🔴 本次新增"
		plainHeader += " · 🔴 本次新增"
	}
	md.WriteString("**会话列表**" + header + "\n")
	plain.WriteString("会话列表" + plainHeader + "\n")

	tagged := 0
	for i, s := range cfg.Sessions {
		mark := "  "
		if s.ID == cfg.Current {
			mark = "★ "
		}
		name := displayName(s)
		auto := ""
		if s.Auto {
			auto = "（自动）"
		}
		newMark := ""
		newMarkPlain := ""
		if s.New {
			newMark = " 🔴 **新增**"
			newMarkPlain = " 🔴 新增"
		}
		fmt.Fprintf(&md, "%s**%d. %s**%s%s\n`%s`\n", mark, i+1, name, auto, newMark, s.Dir)
		fmt.Fprintf(&plain, "%s%d. %s%s%s · %s\n", mark, i+1, name, auto, newMarkPlain, s.Dir)
		if i < maxSessionTags {
			md.WriteString(cmdInput(fmt.Sprintf("/switch %d", i+1), fmt.Sprintf("/switch %d · %s", i+1, name)))
			md.WriteString("\n")
			tagged++
		}
	}
	if tagged < len(cfg.Sessions) {
		fmt.Fprintf(&md, "\n还有 %d 个会话没配标签，用 `/switch <编号>` 切换。\n", len(cfg.Sessions)-tagged)
	}

	md.WriteString("\n**快捷**：")
	for _, item := range []struct{ text, show string }{
		{"/status", "/status"},
		{"/stop", "/stop"},
		{"/menu", "/menu"},
		{"/help", "/help"},
	} {
		md.WriteString(cmdInput(item.text, item.show))
		md.WriteString(" ")
	}
	md.WriteString("\n\n点上面的指令标签会填进输入框，按发送即可。")

	plain.WriteString("\n发送 /switch <编号> 切换，/help 查看全部指令。")
	return md.String(), plain.String()
}

// setupMenu 配置单聊输入框下方的快捷菜单，返回给用户看的说明。
func (b *Bridge) setupMenu(ctx context.Context) string {
	if err := b.SetupMenu(ctx); err != nil {
		return "**配置快捷菜单失败**：" + err.Error()
	}
	return "**快捷菜单已配置**\n点 QQ 输入框下方的菜单按钮，可以直接填入 `/list`、`/status` 这些指令，少打字。"
}

// SetupMenu 配置单聊快捷菜单，启动时也会自动调用一次。
//
// 菜单项点击后只是把文本填进输入框（平台的 send_message 语义），
// 所以放的都是 /list、/status 这类短指令。
func (b *Bridge) SetupMenu(ctx context.Context) error {
	items := []gateway.MenuItem{
		{Name: "会话列表", Type: gateway.MenuTypeSendMessage, SendMessage: "/list"},
		{Name: "状态", Type: gateway.MenuTypeSendMessage, SendMessage: "/status"},
		{Name: "停止", Type: gateway.MenuTypeSendMessage, SendMessage: "/stop"},
		{Name: "重置上下文", Type: gateway.MenuTypeSendMessage, SendMessage: "/reset"},
		{Name: "帮助", Type: gateway.MenuTypeSendMessage, SendMessage: "/help"},
	}
	if err := b.api.SetC2CMenu(ctx, items); err != nil {
		b.logf("error", "配置快捷菜单失败：%v", err)
		return err
	}
	b.logf("info", "已配置单聊快捷菜单")
	return nil
}
