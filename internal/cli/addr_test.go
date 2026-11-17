package cli

import (
	"os"
	"path/filepath"
	"testing"

	"qqbot_bridge/internal/config"
)

// 第二套实例靠 -addr 换端口：写进配置，写错要当场拒绝而不是等服务起不来。
func TestSaveListenAddr(t *testing.T) {
	store, err := config.OpenAt(filepath.Join(t.TempDir(), "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := saveListenAddr(store, " 127.0.0.1:8687 "); err != nil {
		t.Fatal(err)
	}
	if got := store.Get().Web.Addr; got != "127.0.0.1:8687" {
		t.Fatalf("应写入配置：%q", got)
	}

	for _, bad := range []string{"8687", "127.0.0.1", "http://127.0.0.1:8687"} {
		if err := saveListenAddr(store, bad); err == nil {
			t.Fatalf("%q 应被拒绝", bad)
		}
	}
	if got := store.Get().Web.Addr; got != "127.0.0.1:8687" {
		t.Fatalf("校验失败不应改动配置：%q", got)
	}
}

func TestWebAddrText(t *testing.T) {
	cases := []struct{ addr, want string }{
		{"", "管理端：http://127.0.0.1:8686"},
		{"127.0.0.1:8687", "管理端：http://127.0.0.1:8687"},
		{":9000", "管理端：http://127.0.0.1:9000"},
		{"0.0.0.0:9001", "管理端：http://127.0.0.1:9001"},
	}
	for _, c := range cases {
		if got := webAddrText(config.Config{Web: config.Web{Addr: c.addr}}); got != c.want {
			t.Fatalf("%q → %q，期望 %q", c.addr, got, c.want)
		}
	}
}

// 默认数据目录下提示里的命令必须逐字不变，加了 -home 就不一样了。
func TestInstanceArgs(t *testing.T) {
	t.Cleanup(func() {
		_ = config.SetDir("")
		_ = os.Unsetenv(config.HomeEnv)
	})
	_ = config.SetDir("")
	t.Setenv(config.HomeEnv, "")
	if got := instanceArgs(); got != "" {
		t.Fatalf("默认目录不应带 -home：%q", got)
	}
	if got := serviceSuffix(); got != "" {
		t.Fatalf("默认目录不应提示实例名：%q", got)
	}

	if err := config.SetDir("/tmp/inst-x"); err != nil {
		t.Fatal(err)
	}
	if got := instanceArgs(); got != " -home /tmp/inst-x" {
		t.Fatalf("-home 片段不对：%q", got)
	}
	if got := serviceSuffix(); got == "" {
		t.Fatal("非默认目录应提示 systemd 实例名")
	}
}
