// Package discover 从 CodeBuddy 的本地会话索引里找出正在使用的工作目录。
//
// CodeBuddy 每次启动会话都会在 ~/.codebuddy/sessions 下写一份 <pid>.json，
// 里面带 cwd。桥接服务读这份索引，就能把「已经在跑的 CodeBuddy 对话」
// 自动注册成 QQ 端可切换的会话，不用再手动 /new。
package discover

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Session 是索引里的一条 CodeBuddy 会话。
type Session struct {
	Dir       string
	Kind      string
	Status    string
	StartedAt time.Time
}

// Sessions 读取 root 下的会话索引，按目录去重并按启动时间倒序返回。
//
// root 不存在时返回空列表：没装过 CodeBuddy 或换了数据目录都不算错误。
func Sessions(root string) ([]Session, error) {
	if strings.TrimSpace(root) == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	seen := make(map[string]Session)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			continue
		}
		var raw struct {
			Cwd       string `json:"cwd"`
			Kind      string `json:"kind"`
			Status    string `json:"status"`
			StartedAt int64  `json:"startedAt"`
		}
		if err := json.Unmarshal(data, &raw); err != nil {
			continue
		}
		dir := strings.TrimSpace(raw.Cwd)
		if dir == "" || !filepath.IsAbs(dir) || raw.Kind == "prewarm" {
			// prewarm 是 IDE 的预热进程，不是用户真的在用的对话
			continue
		}
		dir = filepath.Clean(dir)
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			continue
		}
		started := time.UnixMilli(raw.StartedAt)
		if old, ok := seen[dir]; ok && !started.After(old.StartedAt) {
			continue
		}
		seen[dir] = Session{
			Dir:       dir,
			Kind:      raw.Kind,
			Status:    raw.Status,
			StartedAt: started,
		}
	}

	out := make([]Session, 0, len(seen))
	for _, s := range seen {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].Dir < out[j].Dir
		}
		return out[i].StartedAt.After(out[j].StartedAt)
	})
	return out, nil
}

// Dirs 只返回去重后的工作目录，按最近启动排序。
func Dirs(root string) ([]string, error) {
	sessions, err := Sessions(root)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, s.Dir)
	}
	return out, nil
}
