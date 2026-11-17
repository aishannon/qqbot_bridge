package cli

import (
	"context"
	"time"

	"qqbot_bridge/internal/agent"
	"qqbot_bridge/internal/config"
	"qqbot_bridge/internal/logbuf"
)

// preflightTimeout 是单个驱动预检的超时。
const preflightTimeout = 15 * time.Second

// preflight 对配置里用到的每个驱动各做一次自检（目前只有 Cursor 实现了 Check）。
//
// 同一实例里可能既有 CodeBuddy 会话又有 Cursor 会话，所以按驱动集合逐个检查，
// 而不是只看全局默认。放后台跑：预检要起一次 CLI（Cursor 查登录状态要走网络），
// 失败也只告警——Web 扫码、改配置不依赖 agent，用户也可能稍后才登录。
func preflight(logs *logbuf.Buffer, cfg config.Config) {
	go func() {
		for _, name := range cfg.Drivers() {
			driver, err := agent.Open(name, agent.Settings{
				Command:      cfg.CommandFor(name),
				DiscoverRoot: cfg.Discover.Root,
			})
			if err != nil {
				logs.Add("warn", "agent 预检跳过（%s）：%v", name, err)
				continue
			}
			checker, ok := driver.(agent.Preflight)
			if !ok {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), preflightTimeout)
			err = checker.Check(ctx)
			cancel()
			if err != nil {
				logs.Add("warn", "agent 预检未通过（%s）：%v", name, err)
				continue
			}
			logs.Add("info", "agent 预检通过（%s）", name)
		}
	}()
}
