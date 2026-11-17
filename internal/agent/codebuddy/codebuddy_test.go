package codebuddy

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"qqbot_bridge/internal/agent"
)

// TestStartAndResult 跑一次真实的 codebuddy，验证进程交互、事件解析与收尾。
// 没有装 codebuddy 或 -short 时跳过。
func TestStartAndResult(t *testing.T) {
	if testing.Short() {
		t.Skip("-short 下跳过真实 agent 调用")
	}
	if _, err := exec.LookPath(DefaultCommand); err != nil {
		t.Skip("未安装 codebuddy，跳过")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var sawResult bool
	task, err := newDriver(agent.Settings{}).Start(ctx, agent.Options{
		Dir:            t.TempDir(),
		PermissionMode: "bypassPermissions",
		MaxTurns:       3,
	}, "只回复两个字：收到")
	if err != nil {
		t.Fatalf("启动失败：%v", err)
	}
	defer task.Stop()

	task.Wait()

	if task.IsError() {
		t.Fatalf("任务报错：%s", task.Result())
	}
	if !strings.Contains(task.Result(), "收到") {
		t.Fatalf("结果不符合预期：%q", task.Result())
	}
	if task.SessionID() == "" {
		t.Fatal("没有解析到 session_id")
	}
	for _, e := range task.Texts() {
		if strings.Contains(e, "收到") {
			sawResult = true
		}
	}
	if !sawResult {
		t.Fatalf("assistant 文本事件缺失：%v", task.Texts())
	}
}

func TestDescribeTool(t *testing.T) {
	cases := []struct{ name, input, want string }{
		{"Bash", `{"command":"go build ./..."}`, "Bash: go build ./..."},
		{"Read", `{"file_path":"/tmp/a.go"}`, "Read: /tmp/a.go"},
		{"Unknown", `{}`, "Unknown"},
	}
	for _, c := range cases {
		got := describeTool(c.name, []byte(c.input))
		if got != c.want {
			t.Fatalf("%s: 期望 %q，实际 %q", c.name, c.want, got)
		}
	}
}

func TestOneLine(t *testing.T) {
	if got := oneLine("a\n\nb   c", 20); got != "a b c" {
		t.Fatalf("压缩空白失败：%q", got)
	}
	if got := oneLine("一二三四五", 3); got != "一二三…" {
		t.Fatalf("截断失败：%q", got)
	}
}

// TestHandleLineEmitsTeammate 验证团队成员的 <teammate-message> 块
// 会被解析成 teammate 事件，普通 user 文本（比如自己的 prompt 回显）不会。
func TestHandleLineEmitsTeammate(t *testing.T) {
	var got []agent.Event
	tk := &task{}
	collect := func(e agent.Event) { got = append(got, e) }

	tk.handleLine([]byte(`{"type":"user","message":{"content":[{"type":"text","text":"普通回显，不应上报"}]}}`), collect)
	if len(got) != 0 {
		t.Fatalf("普通 user 文本不应产生事件：%v", got)
	}

	line := `{"type":"user","message":{"content":[{"type":"text","text":"<teammate-message teammate_id=\"reviewer\" summary=\"审查结论\">\n发现 2 个问题\n</teammate-message>"}]}}`
	tk.handleLine([]byte(line), collect)
	if len(got) != 1 {
		t.Fatalf("应解析出 1 条成员消息，实际 %d 条", len(got))
	}
	e := got[0]
	if e.Kind != "teammate" || e.Name != "reviewer" || e.Summary != "审查结论" || e.Text != "发现 2 个问题" {
		t.Fatalf("解析结果不对：%+v", e)
	}
}
