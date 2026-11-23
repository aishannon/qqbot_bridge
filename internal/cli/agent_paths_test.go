package cli

import (
	"os"
	"path/filepath"
	"testing"

	"qqbot_bridge/internal/config"
)

func TestRunSetAgentPathsWritesCommands(t *testing.T) {
	dir := t.TempDir()
	cb := filepath.Join(dir, "codebuddy")
	cu := filepath.Join(dir, "cursor-agent")
	for _, p := range []string{cb, cu} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	home := filepath.Join(dir, "home")
	if err := config.SetDir(home); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = config.SetDir("") })

	if code := runSetAgentPaths([]string{"--codebuddy", cb, "--cursor", cu}); code != 0 {
		t.Fatalf("exit=%d", code)
	}
	store, err := config.Open()
	if err != nil {
		t.Fatal(err)
	}
	cfg := store.Get()
	if cfg.Agent.Commands[config.DriverCodeBuddy] != cb || cfg.Agent.Commands[config.DriverCursor] != cu {
		t.Fatalf("commands 未写入：%v", cfg.Agent.Commands)
	}
	if cfg.Agent.Command != cb {
		t.Fatalf("当前驱动 command 应为 codebuddy 路径：%q", cfg.Agent.Command)
	}
	if _, err := os.Stat(filepath.Join(home, "sessions.yaml")); !os.IsNotExist(err) {
		t.Fatalf("不应创建 sessions.yaml：%v", err)
	}
}

func TestRunSetAgentPathsCursorOnlySwitchesDriver(t *testing.T) {
	dir := t.TempDir()
	cu := filepath.Join(dir, "cursor-agent")
	if err := os.WriteFile(cu, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(dir, "home")
	if err := config.SetDir(home); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = config.SetDir("") })

	if code := runSetAgentPaths([]string{"--cursor", cu}); code != 0 {
		t.Fatalf("exit=%d", code)
	}
	cfg := mustOpen(t).Get()
	if cfg.Agent.Driver != config.DriverCursor || cfg.Agent.Command != cu {
		t.Fatalf("应切到 cursor：driver=%q command=%q", cfg.Agent.Driver, cfg.Agent.Command)
	}
}

func TestRunSetAgentPathsRequiresPath(t *testing.T) {
	if code := runSetAgentPaths(nil); code != 2 {
		t.Fatalf("无参数应退出 2，得到 %d", code)
	}
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope")
	if code := runSetAgentPaths([]string{"--cursor", missing}); code != 1 {
		t.Fatalf("不存在的路径应退出 1，得到 %d", code)
	}
}

func mustOpen(t *testing.T) *config.Store {
	t.Helper()
	store, err := config.Open()
	if err != nil {
		t.Fatal(err)
	}
	return store
}
