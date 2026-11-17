package config

import (
	"os"
	"path/filepath"
	"testing"
)

// 数据目录优先级：SetDir > $QQBOT_BRIDGE_HOME > ~/.qqbot_bridge。
func TestDirPriority(t *testing.T) {
	t.Cleanup(func() {
		_ = SetDir("")
		_ = os.Unsetenv(HomeEnv)
	})
	_ = SetDir("")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("没有用户主目录，跳过")
	}

	t.Setenv(HomeEnv, "")
	if got, want := Dir(), filepath.Join(home, ".qqbot_bridge"); got != want {
		t.Fatalf("默认目录应为 %s，实际 %s", want, got)
	}

	t.Setenv(HomeEnv, "/tmp/inst-a")
	if got := Dir(); got != "/tmp/inst-a" {
		t.Fatalf("应跟随环境变量：%s", got)
	}

	// 环境变量里写 ~/ 也要展开，否则会解析成字面量目录
	t.Setenv(HomeEnv, "~/inst-b")
	if got, want := Dir(), filepath.Join(home, "inst-b"); got != want {
		t.Fatalf("应展开 ~：%s，期望 %s", got, want)
	}

	if err := SetDir("/tmp/inst-c"); err != nil {
		t.Fatal(err)
	}
	if got := Dir(); got != "/tmp/inst-c" {
		t.Fatalf("-home 应优先于环境变量：%s", got)
	}

	// 相对路径按当前目录转绝对，避免 daemon 化之后 cwd 变化导致目录漂移
	if err := SetDir("rel-dir"); err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(Dir()) || filepath.Base(Dir()) != "rel-dir" {
		t.Fatalf("相对路径应转成绝对路径：%s", Dir())
	}

	if err := SetDir(""); err != nil {
		t.Fatal(err)
	}
	if got := Dir(); got != filepath.Join(home, "inst-b") {
		t.Fatalf("清除覆盖后应回到环境变量：%s", got)
	}
}

// 两套实例的派生路径必须完全分开：pid、密码库、日志、消息记录都靠 Dir() 拼。
func TestDirIsolatesInstancePaths(t *testing.T) {
	t.Cleanup(func() { _ = SetDir("") })
	a, b := t.TempDir(), t.TempDir()

	if err := SetDir(a); err != nil {
		t.Fatal(err)
	}
	pidA, cfgA := filepath.Join(Dir(), "qqbot_bridge.pid"), Path()

	if err := SetDir(b); err != nil {
		t.Fatal(err)
	}
	pidB, cfgB := filepath.Join(Dir(), "qqbot_bridge.pid"), Path()

	if pidA == pidB || cfgA == cfgB {
		t.Fatalf("不同实例不能共享路径：%s / %s", pidA, pidB)
	}
	if filepath.Dir(cfgA) != a || filepath.Dir(cfgB) != b {
		t.Fatalf("配置文件应落在各自目录：%s / %s", cfgA, cfgB)
	}
}
