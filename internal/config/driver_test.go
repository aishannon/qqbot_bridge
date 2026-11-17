package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// 会话没写 driver 时跟随全局；写了就用会话自己的，前后空白要清掉。
func TestDriverOf(t *testing.T) {
	cfg := Default()
	cfg.Agent.Driver = DriverCodeBuddy
	if got := cfg.DriverOf(Session{}); got != DriverCodeBuddy {
		t.Fatalf("空值应跟随全局：%q", got)
	}
	if got := cfg.DriverOf(Session{Driver: "  cursor  "}); got != DriverCursor {
		t.Fatalf("会话指定的 driver 应优先并去空白：%q", got)
	}
}

// 预检要按驱动集合去重，全局默认排在最前面。
func TestDrivers(t *testing.T) {
	cfg := Default()
	cfg.Agent.Driver = DriverCursor
	cfg.Sessions = []Session{
		{ID: "a"},
		{ID: "b", Driver: DriverCodeBuddy},
		{ID: "c", Driver: DriverCursor},
		{ID: "d", Driver: " codebuddy "},
	}
	if got, want := cfg.Drivers(), []string{DriverCursor, DriverCodeBuddy}; !slices.Equal(got, want) {
		t.Fatalf("驱动集合不对：%q，期望 %q", got, want)
	}
}

func TestCommandFor(t *testing.T) {
	cfg := Default()
	cfg.Agent.Driver = DriverCodeBuddy
	cfg.Agent.Command = "/opt/bin/codebuddy"
	if got := cfg.CommandFor(DriverCodeBuddy); got != "/opt/bin/codebuddy" {
		t.Fatalf("全局驱动应使用 agent.command：%q", got)
	}
	// 全局 command 是 codebuddy 的路径，不能传给 cursor-agent
	if got := cfg.CommandFor(DriverCursor); got != "cursor-agent" {
		t.Fatalf("其它驱动应使用自己的默认命令：%q", got)
	}
}

// 会话里写错 driver 要让服务起不来，并指出是哪个会话。
func TestSessionDriverValidation(t *testing.T) {
	dir := t.TempDir()
	servicePath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(servicePath, []byte("agent:\n  driver: codebuddy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sessions := "current: a\nsessions:\n  - id: a\n    dir: /tmp\n  - id: b\n    dir: /tmp\n    driver: gpt\n"
	if err := os.WriteFile(filepath.Join(dir, "sessions.yaml"), []byte(sessions), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := OpenAt(servicePath)
	if err == nil {
		t.Fatal("非法 driver 应导致读取失败")
	}
	if !strings.Contains(err.Error(), `"b"`) || !strings.Contains(err.Error(), "gpt") || !strings.Contains(err.Error(), DriverCursor) {
		t.Fatalf("错误里应指出会话和可选值：%v", err)
	}

	// 合法的写回去就能读
	sessions = strings.Replace(sessions, "driver: gpt", "driver: cursor", 1)
	if err := os.WriteFile(filepath.Join(dir, "sessions.yaml"), []byte(sessions), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenAt(servicePath)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Get().Sessions[1].Driver; got != DriverCursor {
		t.Fatalf("应读到会话 driver：%q", got)
	}
}
