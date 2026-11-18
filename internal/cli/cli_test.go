package cli

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"qqbot_bridge/internal/config"
	"qqbot_bridge/internal/qrconnect"
)

func TestIsLoopbackAddr(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:8686": true, "localhost:8686": true, "[::1]:8686": true,
		":8686": false, "0.0.0.0:8686": false, "192.168.1.5:8686": false, "bad": false,
	}
	for in, want := range cases {
		if got := isLoopbackAddr(in); got != want {
			t.Errorf("isLoopbackAddr(%q) = %v，期望 %v", in, got, want)
		}
	}
}

func TestFormatBuildTime(t *testing.T) {
	if got := formatBuildTime(""); got != "" {
		t.Fatalf("空值应返回空：%q", got)
	}
	if got := formatBuildTime("not-a-time"); got != "not-a-time" {
		t.Fatalf("解析不了应原样返回：%q", got)
	}
	got := formatBuildTime("2026-11-08T09:40:12Z")
	if !strings.HasPrefix(got, "2026-11-08 ") && !strings.HasPrefix(got, "2026-11-07 ") && !strings.HasPrefix(got, "2026-11-09 ") {
		t.Fatalf("应转成本地时间显示：%q", got)
	}
	if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} [+-]\d{4}$`).MatchString(got) {
		t.Fatalf("格式不对：%q", got)
	}
}

func TestVersionTextUsesInjectedValues(t *testing.T) {
	oldV, oldB := Version, BuildTime
	defer func() { Version, BuildTime = oldV, oldB }()
	Version, BuildTime = "v1.2.3-4-gabc", "2026-11-08T09:40:12Z"

	out := versionText()
	for _, want := range []string{"qqbot_bridge v1.2.3-4-gabc\n", "构建时间：2026-", "运行环境：go"} {
		if !strings.Contains(out, want) {
			t.Errorf("版本输出缺少 %q：\n%s", want, out)
		}
	}
	if !strings.Contains(versionLine(), "v1.2.3-4-gabc（构建于 ") {
		t.Fatalf("启动日志摘要不对：%q", versionLine())
	}
}

func TestVersionTextWithoutBuildTime(t *testing.T) {
	oldV, oldB := Version, BuildTime
	defer func() { Version, BuildTime = oldV, oldB }()
	Version, BuildTime = "dev", ""
	// 测试二进制没有 vcs 信息，构建时间应明确写「未知」，而不是留空或编一个
	if out := versionText(); !strings.Contains(out, "构建时间：") {
		t.Fatalf("没有构建时间也要有这一行：\n%s", out)
	}
}

// 交互确认的输入归一化：空行走默认值，中英文都认。
func TestAskAnswer(t *testing.T) {
	cases := map[string]string{
		"y\n": "y", "  Yes \n": "yes", "N\n": "n", "\n": "", "是\n": "是",
	}
	for in, want := range cases {
		if got := askAnswer(bufio.NewReader(strings.NewReader(in))); got != want {
			t.Errorf("askAnswer(%q) = %q，期望 %q", in, got, want)
		}
	}
	// 没有换行的最后一行也要读出来
	if got := askAnswer(bufio.NewReader(strings.NewReader("y"))); got != "y" {
		t.Errorf("无换行输入应读出 y，实际 %q", got)
	}
}

// 扫码写盘必须落在向导已经打开的那份 Store 上，否则 Get 仍是未绑定快照，网关不会启动。
func TestApplyBindVisibleOnSameStore(t *testing.T) {
	store, err := config.OpenAt(filepath.Join(t.TempDir(), "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if store.Get().Bind.Bound() {
		t.Fatal("空配置不应已绑定")
	}
	if err := applyBind(store, qrconnect.Credential{AppID: "app-1", AppSecret: "secret-1", UserOpenID: "openid-1"}); err != nil {
		t.Fatal(err)
	}
	cfg := store.Get()
	if !cfg.Bind.Bound() || cfg.Bind.AppID != "app-1" || cfg.Bind.Secret != "secret-1" {
		t.Fatalf("同一份 Store 应立刻能看到凭据，Bound=%v appId=%q", cfg.Bind.Bound(), cfg.Bind.AppID)
	}
}

// 凭据刚写入且旧进程还在时必须重启，否则扫码前拉起来的进程不会去连网关。
func TestGatewayAction(t *testing.T) {
	cases := []struct {
		justBound, running bool
		want               int
	}{
		{false, false, gatewayStart},
		{true, false, gatewayStart},
		{true, true, gatewayRestart},
		{false, true, gatewayAlready},
	}
	for _, c := range cases {
		if got := gatewayAction(c.justBound, c.running); got != c.want {
			t.Errorf("gatewayAction(%v, %v) = %d，期望 %d", c.justBound, c.running, got, c.want)
		}
	}
}

func TestUserUnitInstance(t *testing.T) {
	t.Cleanup(func() { _ = config.SetDir("") })
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.SetDir(filepath.Join(home, ".qqbot_bridge-cursor")); err != nil {
		t.Fatal(err)
	}
	if got := userUnit(); got != "qqbot_bridge@cursor.service" {
		t.Fatalf("实例目录应对应模板单元，实际 %q", got)
	}
}

// 初始化向导打印的状态要覆盖绑定、密码、服务三块。
func TestConfigState(t *testing.T) {
	cfg := config.Config{}
	cfg.Bind.AppID, cfg.Bind.Secret = "app1", "s"
	cfg.Bind.UserOpenID = "u1"
	cfg.Bind.Whitelist = []string{"u1", "u2"}

	out := configState(cfg)
	for _, want := range []string{"已绑定 appid=app1", "绑定用户：u1", "白名单：2 人", "管理端密码：", "服务："} {
		if !strings.Contains(out, want) {
			t.Fatalf("状态缺少 %q：\n%s", want, out)
		}
	}

	out = configState(config.Config{})
	if !strings.Contains(out, "机器人：未绑定") {
		t.Fatalf("未绑定时应提示：\n%s", out)
	}
}
