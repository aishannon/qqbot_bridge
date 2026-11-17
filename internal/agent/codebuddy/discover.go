package codebuddy

import (
	"os"
	"path/filepath"

	"qqbot_bridge/internal/agent"
	"qqbot_bridge/internal/agent/codebuddy/discover"
)

// DefaultDiscoverRoot 返回会话索引目录的默认位置。
func DefaultDiscoverRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codebuddy", "sessions"), nil
}

// DiscoverDirs 读取 CodeBuddy 的本地会话索引，返回正在使用的工作目录。
//
// 没配置 discover.root 时用 ~/.codebuddy/sessions；目录不存在不算错误，
// 只是没有可自动注册的会话。
func (d *driver) DiscoverDirs() ([]string, error) {
	root := d.discoverRoot
	if root == "" {
		var err error
		if root, err = DefaultDiscoverRoot(); err != nil {
			return nil, err
		}
	}
	return discover.Dirs(root)
}

// 确保 *driver 实现可选能力，接口方法签名变化时在编译期就能发现。
var (
	_ agent.ModelProvider      = (*driver)(nil)
	_ agent.ModelNamer         = (*driver)(nil)
	_ agent.SessionDiscoverer  = (*driver)(nil)
	_ agent.TranscriptProvider = (*driver)(nil)
	_ agent.HistoryMirror      = (*driver)(nil)
)
