package codebuddy

import (
	"os"
	"path/filepath"
	"strings"

	"qqbot_bridge/internal/agent"
	"qqbot_bridge/internal/agent/codebuddy/genie"
)

// TranscriptPath 返回 CodeBuddy 会话 jsonl 的路径。
//
// 这是 CodeBuddy CLI 的落盘约定：~/.codebuddy/projects/<压缩后的 cwd>/<会话>.jsonl。
// 文件可能还不存在（新会话），所以由调用方自己 stat。
func (d *driver) TranscriptPath(cwd, sessionID string) (string, bool) {
	return TranscriptPath(cwd, sessionID), true
}

// TranscriptPath 返回 cwd 下 sessionID 对话的 transcript 路径。
func TranscriptPath(cwd, sessionID string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codebuddy", "projects", projectKey(cwd), sessionID+".jsonl")
}

// Mirror 把会话镜像进 CodeBuddy IDE 的历史库。
func (d *driver) Mirror(cwd, sessionID, transcript string) (agent.MirrorResult, error) {
	res, err := genie.Mirror(cwd, sessionID, transcript)
	if err != nil {
		return agent.MirrorResult{}, err
	}
	return agent.MirrorResult{
		ConversationID: res.ConversationID,
		Name:           res.Name,
		Messages:       res.Messages,
		Store:          res.Store,
	}, nil
}

// projectKey 把工作目录压成 CodeBuddy projects 下的目录名。
func projectKey(cwd string) string {
	p := filepath.Clean(cwd)
	p = strings.TrimPrefix(p, string(filepath.Separator))
	return strings.ReplaceAll(p, string(filepath.Separator), "-")
}
