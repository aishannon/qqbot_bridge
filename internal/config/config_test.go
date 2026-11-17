package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultValues(t *testing.T) {
	cfg := Default()
	if cfg.Agent.Driver != DriverCodeBuddy {
		t.Fatalf("默认驱动不对：%q", cfg.Agent.Driver)
	}
	if cfg.Agent.Command != "codebuddy" {
		t.Fatalf("默认命令不对：%q", cfg.Agent.Command)
	}
	if cfg.Agent.TimeoutMinutes != DefaultTimeoutMinutes {
		t.Fatalf("默认超时不对：%d", cfg.Agent.TimeoutMinutes)
	}
	if cfg.Push.MaxMessages != 4 {
		t.Fatalf("默认回复预算应为 4，实际 %d", cfg.Push.MaxMessages)
	}
	if len(cfg.Sessions) != 1 || cfg.Current != cfg.Sessions[0].ID {
		t.Fatalf("默认会话不对：%+v", cfg.Sessions)
	}
	if cfg.Web.Addr == "" {
		t.Fatal("默认控制台地址不应为空")
	}
}

func TestAgentTimeout(t *testing.T) {
	if got := (Agent{TimeoutMinutes: 5}).Timeout(); got != 5*time.Minute {
		t.Fatalf("超时换算不对：%s", got)
	}
	// 没配置时回落到默认值
	if got := (Agent{}).Timeout(); got != DefaultTimeoutMinutes*time.Minute {
		t.Fatalf("零值应回落到默认超时：%s", got)
	}
}

func TestOpenAtMissingFileUsesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	store, err := OpenAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Get().Agent.Command; got != "codebuddy" {
		t.Fatalf("缺省配置应补上默认命令：%q", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("只读打开不应创建配置文件")
	}
}

func TestUpdatePersistsAndNormalizes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	store, err := OpenAt(path)
	if err != nil {
		t.Fatal(err)
	}
	err = store.Update(func(c *Config) error {
		c.Agent.TimeoutMinutes = 10
		c.Web.Addr = ""        // 应被 normalize 补回默认
		c.Push.MaxMessages = 0 // 同上
		c.Sessions[0].Name = "改过的名字"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Update 应写入配置文件：%v", err)
	}

	reopened, err := OpenAt(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := reopened.Get()
	if cfg.Agent.TimeoutMinutes != 10 {
		t.Fatalf("超时未持久化：%d", cfg.Agent.TimeoutMinutes)
	}
	if cfg.Web.Addr == "" {
		t.Fatal("空地址应被 normalize 补回默认")
	}
	if cfg.Push.MaxMessages != 4 {
		t.Fatalf("零预算应被 normalize 补回 4，实际 %d", cfg.Push.MaxMessages)
	}
	if cfg.Sessions[0].Name != "改过的名字" {
		t.Fatalf("会话名未持久化：%q", cfg.Sessions[0].Name)
	}
}

// TestFilePermission 确认配置文件是 0600：里面有 AppSecret。
func TestFilePermission(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	store, err := OpenAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(c *Config) error {
		c.Bind.AppID = "app"
		c.Bind.Secret = "secret"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("配置权限应为 0600，实际 %o", perm)
	}
}

func TestBindAllowed(t *testing.T) {
	b := Bind{UserOpenID: "owner", Whitelist: []string{"vip"}}
	cases := []struct {
		openID string
		want   bool
	}{
		{"owner", true},
		{"vip", true},
		{"stranger", false},
		{"", false},
	}
	for _, c := range cases {
		if got := b.Allowed(c.openID); got != c.want {
			t.Fatalf("Allowed(%q)=%v，期望 %v", c.openID, got, c.want)
		}
	}

	// 没配白名单时，只允许绑定者本人，避免老配置突然对所有人开放
	only := Bind{UserOpenID: "owner"}
	if !only.Allowed("owner") || only.Allowed("vip") {
		t.Fatal("白名单为空时应只允许绑定者")
	}
}

func TestSetAllowed(t *testing.T) {
	s, err := OpenAt(filepath.Join(t.TempDir(), "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Update(func(c *Config) error { c.Bind.UserOpenID = "owner"; return nil })

	_ = s.SetAllowed("  friend ", true)
	_ = s.SetAllowed("friend", true) // 重复添加不产生重复项
	_ = s.SetAllowed("owner", true)  // 绑定者不写进名单
	if got := s.Get().Bind.Whitelist; len(got) != 1 || got[0] != "friend" {
		t.Fatalf("名单不对：%v", got)
	}
	if !s.Get().Bind.Allowed("friend") || !s.Get().Bind.Allowed("owner") {
		t.Fatal("friend 和 owner 都应被允许")
	}

	_ = s.SetAllowed("friend", false)
	if s.Get().Bind.Allowed("friend") || len(s.Get().Bind.Whitelist) != 0 {
		t.Fatal("移除后不应再被允许")
	}
	_ = s.SetAllowed("owner", false)
	if !s.Get().Bind.Allowed("owner") {
		t.Fatal("绑定者不能被移出")
	}
}
