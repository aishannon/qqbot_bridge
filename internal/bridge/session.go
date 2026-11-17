package bridge

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"qqbot_bridge/internal/config"
)

// switchSession 按编号或 ID 切换当前会话。
func (b *Bridge) switchSession(arg string) string {
	cfg := b.cfg.Get()
	idx := sessionIndex(cfg.Sessions, arg)
	if idx < 0 {
		return "没找到会话 `" + arg + "`，发送 `/list` 查看全部。"
	}
	target := cfg.Sessions[idx]
	err := b.cfg.Update(func(c *config.Config) error {
		c.Current = target.ID
		for i := range c.Sessions {
			if c.Sessions[i].ID == target.ID {
				c.Sessions[i].LastUsed = time.Now()
			}
		}
		return nil
	})
	if err != nil {
		return "切换失败：" + err.Error()
	}
	b.logf("info", "切换到会话 %s（%s）", target.ID, target.Dir)
	name := target.Name
	if name == "" {
		name = target.ID
	}
	return fmt.Sprintf("**已切换** 到「%s」\n目录：`%s`\n之后的指令都会在这个目录下执行。", name, target.Dir)
}

// resetSession 清空当前会话的 agent 会话 ID，下一次指令开一段新对话。
// 目录和会话记录都保留，只是不再 --resume 旧上下文。
func (b *Bridge) resetSession() string {
	cfg := b.cfg.Get()
	cur, ok := currentSession(cfg)
	if !ok {
		return "当前没有可用会话，发送 `/new <目录>` 新建一个。"
	}
	err := b.cfg.Update(func(c *config.Config) error {
		for i := range c.Sessions {
			if c.Sessions[i].ID == cur.ID {
				c.Sessions[i].AgentID = ""
				c.Sessions[i].LastUsed = time.Now()
			}
		}
		return nil
	})
	if err != nil {
		return "**重置失败**：" + err.Error()
	}
	b.logf("info", "会话 %s 的对话上下文已重置", cur.ID)
	return fmt.Sprintf("**已重置** 「%s」的对话上下文。\n目录不变：`%s`\n下一条指令会开一段新对话，之前的上下文不再带入。",
		displayName(cur), cur.Dir)
}

// renameSession 给当前会话改名，只动展示名不动目录。
func (b *Bridge) renameSession(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "**会话名不能为空**。"
	}
	if r := []rune(name); len(r) > 30 {
		name = string(r[:30])
	}
	cfg := b.cfg.Get()
	cur, ok := currentSession(cfg)
	if !ok {
		return "当前没有可用会话，发送 `/new <目录>` 新建一个。"
	}
	old := displayName(cur)
	err := b.cfg.Update(func(c *config.Config) error {
		for i := range c.Sessions {
			if c.Sessions[i].ID == cur.ID {
				c.Sessions[i].Name = name
			}
		}
		return nil
	})
	if err != nil {
		return "**重命名失败**：" + err.Error()
	}
	return fmt.Sprintf("**已重命名**：「%s」→「%s」", old, name)
}

// removeSession 移除一个会话记录。只删配置里的条目，不动工作目录。
func (b *Bridge) removeSession(arg string) string {
	cfg := b.cfg.Get()
	if len(cfg.Sessions) <= 1 {
		return "这是最后一个会话，不能删除。可以先用 `/new <目录>` 建一个新的。"
	}
	idx := sessionIndex(cfg.Sessions, arg)
	if idx < 0 {
		return "没找到会话 `" + arg + "`，发送 `/list` 查看全部。"
	}
	target := cfg.Sessions[idx]
	err := b.cfg.Update(func(c *config.Config) error {
		for i := range c.Sessions {
			if c.Sessions[i].ID != target.ID {
				continue
			}
			c.Sessions = append(c.Sessions[:i], c.Sessions[i+1:]...)
			break
		}
		if c.Current == target.ID && len(c.Sessions) > 0 {
			c.Current = c.Sessions[0].ID
		}
		// 自动发现的目录移除后记下来，否则下次 /list 又会加回来
		if target.Auto {
			c.Discover.Ignore = appendDir(c.Discover.Ignore, target.Dir)
		}
		return nil
	})
	if err != nil {
		return "**删除失败**：" + err.Error()
	}
	b.logf("info", "已移除会话 %s（%s）", target.ID, target.Dir)
	msg := fmt.Sprintf("**已移除**「%s」，目录 `%s` 没有动过。", displayName(target), target.Dir)
	if cfg.Current == target.ID {
		cur, _ := currentSession(b.cfg.Get())
		msg += fmt.Sprintf("\n现在切换到「%s」。", displayName(cur))
	}
	return msg
}

// newSession 新建会话并把当前会话切过去。
func (b *Bridge) newSession(dir, name string) string {
	return b.createSession(dir, name, "", false)
}

// newSessionHere 在当前会话的目录里再开一个会话：同一目录可以有多个会话，上下文互不影响。
func (b *Bridge) newSessionHere() string {
	cur, ok := currentSession(b.cfg.Get())
	if !ok {
		return "当前没有可用会话，先用 `/new <目录>` 新建一个。"
	}
	// 用当前会话的 ID 作为新会话的 ID 前缀，同目录多开时自然变成 demo-2、demo-3
	return b.createSession(cur.Dir, "", sessionBase(cur.ID), true)
}

// sessionBase 去掉 ID 末尾的序号，保证同目录连续多开时序号一路往上加。
func sessionBase(id string) string {
	if i := strings.LastIndex(id, "-"); i > 0 {
		if _, err := strconv.Atoi(id[i+1:]); err == nil {
			return id[:i]
		}
	}
	return id
}

// createSession 新建会话并切换过去。
//
// force 为 false 时，目录已有会话就只切过去并改名（老行为）；
// 为 true 时即使目录相同也新建一个，ID 和名称自动加后缀区分。
func (b *Bridge) createSession(dir, name, idBase string, force bool) string {
	abs, err := expandPath(dir)
	if err != nil {
		return "**目录不合法**：" + err.Error()
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "**创建目录失败**：" + err.Error()
	}
	id := idBase
	if id == "" {
		id = filepath.Base(abs)
	}
	if id == "" || id == "." || id == string(filepath.Separator) {
		id = "session"
	}

	var created string
	switched := false
	err = b.cfg.Update(func(c *config.Config) error {
		if !force {
			for i := range c.Sessions {
				if c.Sessions[i].Dir == abs {
					if name == "" {
						name = c.Sessions[i].Name
					}
					c.Sessions[i].Name = name
					c.Sessions[i].Auto = false
					c.Sessions[i].New = false
					c.Current = c.Sessions[i].ID
					c.Discover.Ignore = removeDir(c.Discover.Ignore, abs)
					created = c.Sessions[i].Name
					switched = true
					return nil
				}
			}
		}
		unique := uniqueID(c.Sessions, id)
		if name == "" {
			// 同目录多开时用 ID 当名字，自动变成「demo-2」这样，方便区分
			name = unique
		}
		c.Sessions = append(c.Sessions, config.Session{
			ID:        unique,
			Name:      name,
			Dir:       abs,
			CreatedAt: time.Now(),
			LastUsed:  time.Now(),
		})
		c.Current = unique
		c.Discover.Ignore = removeDir(c.Discover.Ignore, abs)
		created = name
		return nil
	})
	if err != nil {
		return "**新建会话失败**：" + err.Error()
	}
	if switched {
		return fmt.Sprintf("**已切换**到「%s」\n目录：`%s`\n这个目录已经有会话，没重复创建。", created, abs)
	}
	return fmt.Sprintf("**已新建** 并切换到「%s」\n目录：`%s`\n同一目录的独立会话，上下文互不影响。", created, abs)
}

// sessionIndex 按编号（从 1 开始）或会话 ID 定位，-1 表示没找到。
func sessionIndex(sessions []config.Session, arg string) int {
	if n := atoi(arg); n > 0 && n <= len(sessions) {
		return n - 1
	}
	for i, s := range sessions {
		if s.ID == arg {
			return i
		}
	}
	return -1
}

// currentSession 返回当前会话；当前 ID 失效时退回第一个会话。
func currentSession(cfg config.Config) (config.Session, bool) {
	if len(cfg.Sessions) == 0 {
		return config.Session{}, false
	}
	for _, s := range cfg.Sessions {
		if s.ID == cfg.Current {
			return s, true
		}
	}
	return cfg.Sessions[0], true
}

// displayName 返回会话的展示名，没设名字时用 ID。
func displayName(s config.Session) string {
	if s.Name != "" {
		return s.Name
	}
	return s.ID
}

// uniqueID 生成不与现有会话冲突的 ID。
func uniqueID(sessions []config.Session, base string) string {
	exists := func(id string) bool {
		for _, s := range sessions {
			if s.ID == id {
				return true
			}
		}
		return false
	}
	if !exists(base) {
		return base
	}
	for i := 2; ; i++ {
		id := fmt.Sprintf("%s-%d", base, i)
		if !exists(id) {
			return id
		}
	}
}

// appendDir 把一个目录加进忽略列表，已存在时不重复加。
func appendDir(list []string, dir string) []string {
	dir = cleanDir(dir)
	if dir == "" {
		return list
	}
	for _, item := range list {
		if cleanDir(item) == dir {
			return list
		}
	}
	return append(list, dir)
}

// removeDir 从忽略列表里删掉一个目录。
func removeDir(list []string, dir string) []string {
	dir = cleanDir(dir)
	out := make([]string, 0, len(list))
	for _, item := range list {
		if cleanDir(item) != dir {
			out = append(out, item)
		}
	}
	return out
}

// expandPath 把 ~ 和相对路径展开成绝对路径。
func expandPath(p string) (string, error) {
	if strings.HasPrefix(p, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	if !filepath.IsAbs(p) {
		abs, err := filepath.Abs(p)
		if err != nil {
			return "", err
		}
		p = abs
	}
	return filepath.Clean(p), nil
}
