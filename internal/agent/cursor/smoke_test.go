package cursor

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"qqbot_bridge/internal/agent"
)

// TestRealCLI 跑一次真实的 cursor-agent，验证参数、事件解析与收尾。
// 没装 cursor-agent 或 -short 时跳过；需要本机已 login（或设了 CURSOR_API_KEY）。
func TestRealCLI(t *testing.T) {
	if testing.Short() {
		t.Skip("-short 下跳过真实 agent 调用")
	}
	if _, err := exec.LookPath(DefaultCommand); err != nil {
		t.Skip("未安装 cursor-agent，跳过")
	}

	d := newDriver(agent.Settings{}).(*driver)
	if err := d.Check(t.Context()); err != nil {
		t.Skipf("Cursor CLI 不可用，跳过：%v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()

	task, err := d.Start(ctx, agent.Options{
		Dir:            t.TempDir(),
		PermissionMode: "bypassPermissions",
		Model:          "auto",
	}, "只回复两个字：收到")
	if err != nil {
		t.Fatalf("启动失败：%v", err)
	}
	defer task.Stop()

	select {
	case <-task.Done():
	case <-time.After(3 * time.Minute):
		task.Stop()
		t.Fatal("真实任务 3 分钟内没有结束")
	}

	if task.IsError() {
		t.Fatalf("任务报错：%s", task.Result())
	}
	if !strings.Contains(task.Result(), "收到") {
		t.Fatalf("结果不符合预期：%q", task.Result())
	}
	if task.SessionID() == "" {
		t.Fatal("没有解析到 session_id")
	}
}
