package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// 服务配置和会话配置分别落在两个文件里，互不混杂。
func TestSplitIntoTwoYAMLFiles(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenAt(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Update(func(c *Config) error {
		c.Bind.AppID, c.Bind.Secret, c.Bind.UserOpenID = "app1", "TOPSECRET", "owner"
		c.Bind.Whitelist = []string{"friend"}
		c.Agent.TimeoutMinutes = 99
		c.Sessions = []Session{{ID: "s1", Name: "项目甲", Dir: "/work/a", AgentID: "conv-1", CreatedAt: time.Now()}}
		c.Current = "s1"
		c.Discover.Ignore = []string{"/work/ignored"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	svc := readFile(t, filepath.Join(dir, "config.yaml"))
	sess := readFile(t, filepath.Join(dir, "sessions.yaml"))

	for _, want := range []string{"appId: app1", "secret: TOPSECRET", "timeoutMinutes: 99", "whitelist:", "- friend"} {
		if !strings.Contains(svc, want) {
			t.Errorf("config.yaml 缺少 %q：\n%s", want, svc)
		}
	}
	for _, bad := range []string{"项目甲", "/work/a", "conv-1", "current:", "/work/ignored"} {
		if strings.Contains(svc, bad) {
			t.Errorf("config.yaml 里不应出现会话内容 %q", bad)
		}
	}
	for _, want := range []string{"current: s1", "name: 项目甲", "dir: /work/a", "agentId: conv-1", "ignore:", "/work/ignored"} {
		if !strings.Contains(sess, want) {
			t.Errorf("sessions.yaml 缺少 %q：\n%s", want, sess)
		}
	}
	for _, bad := range []string{"TOPSECRET", "appId", "timeoutMinutes"} {
		if strings.Contains(sess, bad) {
			t.Errorf("sessions.yaml 里不应出现服务配置 %q", bad)
		}
	}
	for _, p := range []string{"config.yaml", "sessions.yaml"} {
		if fi, _ := os.Stat(filepath.Join(dir, p)); fi.Mode().Perm() != 0o600 {
			t.Errorf("%s 权限应为 0600：%o", p, fi.Mode().Perm())
		}
	}

	// 重新打开后两部分都能还原
	again, err := OpenAt(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	c := again.Get()
	if c.Bind.Secret != "TOPSECRET" || c.Agent.TimeoutMinutes != 99 || c.Current != "s1" ||
		len(c.Sessions) != 1 || c.Sessions[0].Dir != "/work/a" || len(c.Discover.Ignore) != 1 {
		t.Fatalf("重新读取后内容不对：%+v", c)
	}
}

// 只改会话时，不应该重写含 AppSecret 的 config.yaml。
func TestSessionChangeDoesNotRewriteServiceConfig(t *testing.T) {
	dir := t.TempDir()
	svcPath := filepath.Join(dir, "config.yaml")
	s, _ := OpenAt(svcPath)
	_ = s.Update(func(c *Config) error { c.Bind.AppID, c.Bind.Secret = "app", "sec"; return nil })

	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(svcPath, old, old); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(svcPath)

	_ = s.Update(func(c *Config) error { c.Sessions[0].LastUsed = time.Now(); c.Current = c.Sessions[0].ID; return nil })

	after, _ := os.Stat(svcPath)
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("只改会话时 config.yaml 不应被改写")
	}
	if !strings.Contains(readFile(t, filepath.Join(dir, "sessions.yaml")), "lastUsed:") {
		t.Fatal("会话变化应写进 sessions.yaml")
	}
}

// 旧版单文件 config.json 自动拆分迁移，原文件改名留作备份。
func TestMigrateLegacyJSON(t *testing.T) {
	dir := t.TempDir()
	legacy := `{
  "bind": {"appId": "old-app", "secret": "old-secret", "userOpenId": "owner1", "whitelist": ["w1"], "names": {"owner1": "我"}},
  "agent": {"command": "codebuddy", "timeoutMinutes": 300},
  "sessions": [{"id": "proj", "name": "老项目", "dir": "/old/dir", "agentId": "conv-9", "createdAt": "2026-11-01T00:00:00Z", "lastUsed": "2026-11-02T00:00:00Z"}],
  "current": "proj",
  "push": {"maxMessages": 4, "markdown": true},
  "discover": {"disable": false, "ignore": ["/old/ignored"]},
  "web": {"addr": "127.0.0.1:9999"}
}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := OpenAt(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	c := s.Get()
	if c.Bind.AppID != "old-app" || c.Bind.Secret != "old-secret" || c.Bind.Names["owner1"] != "我" ||
		c.Agent.TimeoutMinutes != 300 || c.Web.Addr != "127.0.0.1:9999" {
		t.Fatalf("服务配置迁移不完整：%+v", c)
	}
	if c.Current != "proj" || len(c.Sessions) != 1 || c.Sessions[0].AgentID != "conv-9" || c.Discover.Ignore[0] != "/old/ignored" {
		t.Fatalf("会话配置迁移不完整：%+v", c)
	}

	if _, err := os.Stat(filepath.Join(dir, "config.json")); !os.IsNotExist(err) {
		t.Fatal("旧 config.json 应被改名")
	}
	if s.MigratedFrom() != filepath.Join(dir, "config.json.migrated") {
		t.Fatalf("应报告备份路径：%q", s.MigratedFrom())
	}
	if got := readFile(t, s.MigratedFrom()); !strings.Contains(got, "old-secret") {
		t.Fatal("备份应保留原内容")
	}
	if !strings.Contains(readFile(t, filepath.Join(dir, "sessions.yaml")), "老项目") ||
		strings.Contains(readFile(t, filepath.Join(dir, "config.yaml")), "老项目") {
		t.Fatal("迁移后的两个文件内容归属不对")
	}

	// 再次打开：直接读 YAML，不再迁移
	again, err := OpenAt(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if again.MigratedFrom() != "" || again.Get().Bind.AppID != "old-app" {
		t.Fatal("第二次打开不应再迁移")
	}
}

// 已经有 YAML 时，旧 config.json 不会再覆盖它。
func TestYAMLWinsOverStaleLegacyJSON(t *testing.T) {
	dir := t.TempDir()
	s, _ := OpenAt(filepath.Join(dir, "config.yaml"))
	_ = s.Update(func(c *Config) error { c.Bind.AppID = "new-app"; return nil })
	_ = os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"bind":{"appId":"stale"}}`), 0o600)

	again, _ := OpenAt(filepath.Join(dir, "config.yaml"))
	if again.Get().Bind.AppID != "new-app" || again.MigratedFrom() != "" {
		t.Fatalf("应以 YAML 为准：%+v", again.Get().Bind)
	}
}

// 配置键名写错要报出来，而不是悄悄用默认值。
func TestUnknownKeyIsRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	_ = os.WriteFile(path, []byte("agent:\n  timeoutMinuts: 10\n"), 0o600)
	_, err := OpenAt(path)
	if err == nil || !strings.Contains(err.Error(), "timeoutMinuts") || !strings.Contains(err.Error(), "config.yaml") {
		t.Fatalf("应指出写错的键和文件：%v", err)
	}
}

// 文件里没写的项沿用默认值，不是零值。
func TestMissingKeysKeepDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	_ = os.WriteFile(path, []byte("bind:\n  appId: a\n  secret: b\n"), 0o600)
	s, err := OpenAt(path)
	if err != nil {
		t.Fatal(err)
	}
	c := s.Get()
	if !c.Push.Markdown || c.Push.MaxMessages != 4 || c.Agent.Command != "codebuddy" || c.Web.Addr != "127.0.0.1:8686" {
		t.Fatalf("没写的项应保持默认：%+v", c)
	}
}

// agent.driver 是新增键：老配置没有它时按 codebuddy 处理，已有 command 原样保留；
// 选了别的驱动又没写 command 时，默认可执行文件跟着驱动走。
func TestDriverNormalization(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		driver  string
		command string
	}{
		{"老配置没有 driver 键", "agent:\n  command: my-cli\n", DriverCodeBuddy, "my-cli"},
		{"只有 driver", "agent:\n  driver: cursor\n", DriverCursor, "cursor-agent"},
		{"driver 和 command 都写", "agent:\n  driver: cursor\n  command: cc\n", DriverCursor, "cc"},
		{"空文件", "", DriverCodeBuddy, "codebuddy"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if c.yaml != "" {
				if err := os.WriteFile(path, []byte(c.yaml), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			s, err := OpenAt(path)
			if err != nil {
				t.Fatal(err)
			}
			got := s.Get().Agent
			if got.Driver != c.driver || got.Command != c.command {
				t.Fatalf("归一化结果不对：driver=%q command=%q", got.Driver, got.Command)
			}
		})
	}
}

// 另一个进程（如 qqbot_bridge bind）改了配置后，服务下一次写配置不能把它覆盖回旧值。
func TestUpdateKeepsExternalEdits(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	svc, _ := OpenAt(path)
	_ = svc.Update(func(c *Config) error { c.Bind.AppID, c.Bind.Secret = "old-app", "old-sec"; return nil })

	// 模拟 `bind` 进程：另开一个 Store 写入新凭据
	other, _ := OpenAt(path)
	if err := other.Update(func(c *Config) error { c.Bind.AppID, c.Bind.Secret = "new-app", "new-sec"; return nil }); err != nil {
		t.Fatal(err)
	}
	// 让文件签名一定不同（有的文件系统时间精度低）
	future := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(path, future, future)

	// 服务随后记录一个昵称
	if err := svc.Update(func(c *Config) error {
		if c.Bind.Names == nil {
			c.Bind.Names = map[string]string{}
		}
		c.Bind.Names["u1"] = "小王"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	got := svc.Get().Bind
	if got.AppID != "new-app" || got.Secret != "new-sec" || got.Names["u1"] != "小王" {
		t.Fatalf("外部修改被覆盖了：%+v", got)
	}
	if !strings.Contains(readFile(t, path), "appId: new-app") {
		t.Fatal("落盘内容应保留新凭据")
	}
}

// 手工改坏了文件时，Update 要报错而不是把它覆盖掉。
func TestUpdateRefusesToOverwriteBrokenFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	s, _ := OpenAt(path)
	_ = s.Update(func(c *Config) error { c.Bind.AppID = "a"; return nil })

	broken := "bind: [not, a, map\n"
	_ = os.WriteFile(path, []byte(broken), 0o600)
	future := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(path, future, future)

	if err := s.Update(func(c *Config) error { c.Agent.Model = "x"; return nil }); err == nil {
		t.Fatal("文件损坏时 Update 应返回错误")
	}
	if readFile(t, path) != broken {
		t.Fatal("损坏的文件不应被覆盖，留给用户修")
	}
}
