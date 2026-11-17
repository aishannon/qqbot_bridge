package bridge

import (
	"strings"

	"qqbot_bridge/internal/config"
)

// driverChoice 是 /driver 里列出的可选 agent。
var driverChoice = []string{config.DriverCodeBuddy, config.DriverCursor}

// driverCommand 处理 `/driver`：查看或切换当前会话使用的 agent。
//
// 切换会清空 AgentID 和 Model：CodeBuddy 的 sessionId 和 Cursor 的 chatId 互不
// 通用，模型 ID 也各自一套，留着只会让下一轮启动失败或跑到别的模型上。
func (b *Bridge) driverCommand(arg string) string {
	cfg := b.cfg.Get()
	sess, ok := currentSession(cfg)
	if !ok {
		return "还没有可用会话，先发送 `/new <目录>` 新建一个。"
	}
	current := b.driverName(sess)
	arg = strings.ToLower(strings.TrimSpace(arg))

	if arg == "" {
		return b.driverSummary(cfg, sess, current)
	}

	if !config.KnownDriver(arg) {
		return "没有 agent `" + arg + "`，可选：`" + strings.Join(driverChoice, "`、`") + "`。"
	}
	if arg == current {
		return "会话「" + displayName(sess) + "」已经在用 " + agentTitle(current) + "，无需切换。"
	}

	if err := b.cfg.Update(func(c *config.Config) error {
		for i := range c.Sessions {
			if c.Sessions[i].ID != sess.ID {
				continue
			}
			c.Sessions[i].Driver = arg
			// 两个 agent 的会话 ID / 模型 ID 不通用，切过去必须重新开始
			c.Sessions[i].AgentID = ""
			c.Sessions[i].Model = ""
		}
		return nil
	}); err != nil {
		return "**切换失败，agent 未变**：" + err.Error()
	}

	b.logf("info", "会话 %s 切换 agent：%q -> %q", sess.ID, current, arg)
	out := "**已切换 agent**：会话「" + displayName(sess) + "」→ " + agentTitle(arg) +
		"\n原 agent：" + agentTitle(current) +
		"\n这个会话的对话上下文和模型设置已清空，下一条指令会开一段新对话；其它会话不受影响。"
	if arg == config.DriverCursor {
		out += "\n提示：`/models` 现在列出 Cursor 的模型，Cursor 不提供倍率。"
	}
	return out
}

// driverSummary 渲染 /driver 的查看结果。
func (b *Bridge) driverSummary(cfg config.Config, sess config.Session, current string) string {
	var sb strings.Builder
	sb.WriteString("**会话 agent**：" + agentTitle(current) + "（`" + current + "`）")
	if strings.TrimSpace(sess.Driver) == "" {
		sb.WriteString(" · 跟随全局默认")
	}
	sb.WriteString("\n全局默认：" + agentTitle(cfg.Agent.Driver) + "（`" + cfg.Agent.Driver + "`）")

	sb.WriteString("\n可选：")
	for i, name := range driverChoice {
		if i > 0 {
			sb.WriteString("、")
		}
		sb.WriteString(agentTitle(name) + "（`" + name + "`）")
	}
	sb.WriteString("\n切换：")
	for _, name := range driverChoice {
		if name == current {
			continue
		}
		sb.WriteString(cmdInput("/driver "+name, "切到 "+agentTitle(name)) + " ")
	}
	sb.WriteString("\n切换会清空这个会话的对话上下文和模型设置。")
	return sb.String()
}
