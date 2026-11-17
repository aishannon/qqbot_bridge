package cursor

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// nonAlnumRe 匹配 Cursor 项目 key 里会被替换掉的字符段。
var nonAlnumRe = regexp.MustCompile(`[^A-Za-z0-9]+`)

// TranscriptPath 返回 Cursor 会话 transcript 的路径。
//
// 这是 cursor-agent 的落盘约定（实测 2026.11）：
// ~/.cursor/projects/<项目 key>/agent-transcripts/<chatId>/<chatId>.jsonl。
// 新会话的文件可能还不存在，由调用方自己 stat。
func (d *driver) TranscriptPath(dir, sessionID string) (string, bool) {
	if dir == "" || sessionID == "" {
		return "", false
	}
	home := d.home
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		home = h
	}
	return filepath.Join(home, ".cursor", "projects", projectKey(dir), "agent-transcripts", sessionID, sessionID+".jsonl"), true
}

// projectKey 把工作目录压成 ~/.cursor/projects 下的目录名。
//
// 规则：连续的非字母数字字符（包括路径分隔符、下划线、点、中文）合成一个
// 「-」，再去掉首尾的「-」。例如 /tmp/Ab_c.d e@f → tmp-Ab-c-d-e-f。
// 这个映射有损、不可逆，所以 Cursor 驱动做不了「从 transcript 反推目录」的会话发现。
func projectKey(dir string) string {
	return strings.Trim(nonAlnumRe.ReplaceAllString(filepath.Clean(dir), "-"), "-")
}
