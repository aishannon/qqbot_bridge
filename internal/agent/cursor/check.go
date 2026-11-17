package cursor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"time"
)

// checkTimeout 是登录预检的超时。
const checkTimeout = 10 * time.Second

// Check 检查 Cursor CLI 是否可用且已登录（`cursor-agent status --format json`）。
//
// 设置了 CURSOR_API_KEY 时 status 不一定反映 key 的有效性，这里只在确定
// 未登录且没有 key 时报错，其余情况放行，交给第一轮任务暴露真实错误。
func (d *driver) Check(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, d.command, "status", "--format", "json")
	cmd.Dir = os.TempDir()
	out, err := cmd.Output()
	if err != nil {
		// 命令名查 PATH 失败是 *exec.Error，绝对路径不存在是 fs.ErrNotExist
		if _, ok := errors.AsType[*exec.Error](err); ok || errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("找不到 Cursor CLI（%s）：先安装 `curl https://cursor.com/install -fsS | bash`，"+
				"或在配置里把 agent.command 指向 cursor-agent 的绝对路径", d.command)
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("检查 Cursor 登录状态超时（%s）", checkTimeout)
		}
		return fmt.Errorf("检查 Cursor 登录状态失败：%v", err)
	}
	loggedIn, ok := parseStatus(out)
	if !ok || loggedIn {
		return nil
	}
	if strings.TrimSpace(os.Getenv("CURSOR_API_KEY")) != "" || strings.TrimSpace(os.Getenv("CURSOR_AUTH_TOKEN")) != "" {
		return nil
	}
	return fmt.Errorf("%w：%s", ErrNotLoggedIn, loginHint)
}

// parseStatus 宽松解析 status 的 JSON 输出；ok 为 false 表示看不懂，放行。
func parseStatus(out []byte) (loggedIn, ok bool) {
	var st struct {
		Status          string `json:"status"`
		IsAuthenticated *bool  `json:"isAuthenticated"`
	}
	if json.Unmarshal(out, &st) != nil {
		return false, false
	}
	if st.IsAuthenticated != nil {
		return *st.IsAuthenticated, true
	}
	switch strings.ToLower(st.Status) {
	case "authenticated":
		return true, true
	case "unauthenticated":
		return false, true
	}
	return false, false
}
