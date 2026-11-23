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

	cfg.Agent.Commands = map[string]string{
		DriverCodeBuddy: "/opt/bin/codebuddy",
		DriverCursor:    "/opt/bin/cursor-agent",
	}
	if got := cfg.CommandFor(DriverCursor); got != "/opt/bin/cursor-agent" {
		t.Fatalf("commands 里的路径应优先：%q", got)
	}
}

func TestSetAgentCommand(t *testing.T) {
	a := Agent{Driver: DriverCodeBuddy, Command: "codebuddy"}
	a.SetAgentCommand(DriverCodeBuddy, "/usr/local/bin/codebuddy")
	a.SetAgentCommand(DriverCursor, "/opt/cursor-agent")
	if a.Command != "/usr/local/bin/codebuddy" {
		t.Fatalf("当前驱动应同步 command：%q", a.Command)
	}
	if a.Commands[DriverCursor] != "/opt/cursor-agent" {
		t.Fatalf("应记下另一驱动的路径：%v", a.Commands)
	}
}

// 本机没有 codebuddy 时改用 cursor-agent；找得到，或本来就不是 codebuddy，则不动。
func TestFallbackFromMissingCodeBuddy(t *testing.T) {
	yes := func(string) bool { return true }
	no := func(string) bool { return false }

	kept := Agent{Driver: DriverCodeBuddy, Command: "codebuddy", Model: "m", TimeoutMinutes: 30}
	if _, ok := kept.FallbackFromMissingCodeBuddy(yes); ok {
		t.Fatal("找得到 codebuddy 时不应切换")
	}
	next, ok := kept.FallbackFromMissingCodeBuddy(no)
	if !ok || next.Driver != DriverCursor || next.Command != "cursor-agent" || next.Model != "m" || next.TimeoutMinutes != 30 {
		t.Fatalf("应切到 cursor-agent 并保留其它字段：%+v ok=%v", next, ok)
	}

	withCmds := Agent{
		Driver: DriverCodeBuddy, Command: "codebuddy",
		Commands: map[string]string{DriverCursor: "/opt/bin/cursor-agent"},
	}
	next, ok = withCmds.FallbackFromMissingCodeBuddy(no)
	if !ok || next.Driver != DriverCursor || next.Command != "/opt/bin/cursor-agent" {
		t.Fatalf("切换时应优先用 commands 里的绝对路径：%+v ok=%v", next, ok)
	}

	custom := Agent{Driver: DriverCodeBuddy, Command: "/opt/bin/codebuddy"}
	if next, ok = custom.FallbackFromMissingCodeBuddy(no); !ok || next.Command != "cursor-agent" {
		t.Fatal("自定义路径不存在时也应切换")
	}
	if _, ok = custom.FallbackFromMissingCodeBuddy(yes); ok {
		t.Fatal("自定义路径存在时不应切换")
	}

	cursor := Agent{Driver: DriverCursor, Command: "cursor-agent"}
	if _, ok = cursor.FallbackFromMissingCodeBuddy(no); ok {
		t.Fatal("已经是 cursor 时不应再改")
	}

	blank := Agent{}
	if next, ok = blank.FallbackFromMissingCodeBuddy(no); !ok || next.Driver != DriverCursor || next.Command != "cursor-agent" {
		t.Fatalf("空驱动应按 codebuddy 处理并在找不到时切换：%+v ok=%v", next, ok)
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
