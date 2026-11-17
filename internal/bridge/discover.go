package bridge

import (
	"path/filepath"
	"strings"
	"time"

	"qqbot_bridge/internal/agent"
	"qqbot_bridge/internal/config"
)

// SyncDiscovered 把 agent 正在使用的工作目录注册成会话，返回新增数量。
//
// 只补新目录，不动已有会话和当前选择；用户用 /rm 移除过的目录记在
// discover.ignore 里，之后不再自动加回来。驱动没有会话索引时静默跳过。
func (b *Bridge) SyncDiscovered() int {
	cfg := b.cfg.Get()
	if cfg.Discover.Disable {
		return 0
	}
	// 自动发现只能靠全局驱动的会话索引（目前只有 CodeBuddy 有）；
	// 自动注册出来的会话不写 driver，等于继承全局默认，行为与单驱动时一致。
	driver, err := b.defaultDriver()
	if err != nil {
		b.logf("warn", "解析 agent 驱动失败：%v", err)
		return 0
	}
	discoverer, ok := driver.(agent.SessionDiscoverer)
	if !ok {
		return 0
	}
	dirs, err := discoverer.DiscoverDirs()
	if err != nil {
		b.logf("warn", "读取 %s 会话目录失败：%v", agentTitle(driver.Name()), err)
		return 0
	}

	ignored := make(map[string]bool, len(cfg.Discover.Ignore))
	for _, dir := range cfg.Discover.Ignore {
		ignored[cleanDir(dir)] = true
	}
	known := make(map[string]bool, len(cfg.Sessions))
	for _, s := range cfg.Sessions {
		known[cleanDir(s.Dir)] = true
	}

	var add []string
	for _, dir := range dirs {
		dir = cleanDir(dir)
		if dir == "" || ignored[dir] || known[dir] {
			continue
		}
		add = append(add, dir)
	}
	if len(add) == 0 {
		return 0
	}

	added := 0
	err = b.cfg.Update(func(c *config.Config) error {
		known := make(map[string]bool, len(c.Sessions))
		for _, s := range c.Sessions {
			known[cleanDir(s.Dir)] = true
		}
		skip := make(map[string]bool, len(c.Discover.Ignore))
		for _, dir := range c.Discover.Ignore {
			skip[cleanDir(dir)] = true
		}
		for _, dir := range add {
			if known[dir] || skip[dir] {
				continue
			}
			name := filepath.Base(dir)
			if name == "" || name == "." || name == string(filepath.Separator) {
				name = dir
			}
			c.Sessions = append(c.Sessions, config.Session{
				ID:        uniqueID(c.Sessions, name),
				Name:      name,
				Dir:       dir,
				Auto:      true,
				New:       true,
				CreatedAt: time.Now(),
			})
			known[dir] = true
			added++
		}
		return nil
	})
	if err != nil {
		b.logf("warn", "自动注册 CodeBuddy 目录失败：%v", err)
		return 0
	}
	if added > 0 {
		b.logf("info", "已自动注册 %d 个 CodeBuddy 会话目录", added)
	}
	return added
}

// clearNewFlags 在 /list 成功发出后清掉「新增」标记，避免每次列表都红一片。
func (b *Bridge) clearNewFlags() {
	cfg := b.cfg.Get()
	any := false
	for _, s := range cfg.Sessions {
		if s.New {
			any = true
			break
		}
	}
	if !any {
		return
	}
	_ = b.cfg.Update(func(c *config.Config) error {
		for i := range c.Sessions {
			c.Sessions[i].New = false
		}
		return nil
	})
}

// cleanDir 统一目录写法，避免 /a/b 与 /a/b/ 被当成两个会话。
func cleanDir(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	return filepath.Clean(dir)
}
