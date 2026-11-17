package cli

import (
	"os"
	"slices"
	"strings"
	"testing"

	"qqbot_bridge/internal/config"
)

func resetHome(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		_ = config.SetDir("")
		_ = os.Unsetenv(config.HomeEnv)
	})
	_ = config.SetDir("")
	_ = os.Unsetenv(config.HomeEnv)
}

// -home 要在子命令分发之前被剥掉并生效，其余参数原样保留。
func TestPreparseHome(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    []string
		wantDir string // 空表示不应改目录
	}{
		{"空格形式", []string{"-home", "/tmp/inst-a", "-f"}, []string{"-f"}, "/tmp/inst-a"},
		{"等号形式", []string{"--home=/tmp/inst-b", "-addr", ":9000"}, []string{"-addr", ":9000"}, "/tmp/inst-b"},
		{"子命令后面", []string{"config", "-home", "/tmp/inst-c", "-yes"}, []string{"config", "-yes"}, "/tmp/inst-c"},
		{"单横线等号", []string{"-home=/tmp/inst-d"}, nil, "/tmp/inst-d"},
		{"没有 -home", []string{"-f"}, []string{"-f"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resetHome(t)
			got, err := preparseHome(c.args)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, c.want) {
				t.Fatalf("剩余参数应 %q，实际 %q", c.want, got)
			}
			if c.wantDir != "" && config.Dir() != c.wantDir {
				t.Fatalf("目录应 %s，实际 %s", c.wantDir, config.Dir())
			}
		})
	}
}

// 后台子进程靠环境变量继承目录，所以 -home 必须落成 $QQBOT_BRIDGE_HOME。
func TestPreparseHomeExportsEnv(t *testing.T) {
	resetHome(t)
	if _, err := preparseHome([]string{"-home", "/tmp/inst-e"}); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv(config.HomeEnv); got != "/tmp/inst-e" {
		t.Fatalf("应导出 %s=/tmp/inst-e，实际 %q", config.HomeEnv, got)
	}
	if got := config.Dir(); got != "/tmp/inst-e" {
		t.Fatalf("目录不对：%s", got)
	}
}

func TestPreparseHomeMissingValue(t *testing.T) {
	resetHome(t)
	_, err := preparseHome([]string{"-f", "-home"})
	if err == nil || !strings.Contains(err.Error(), "数据目录") {
		t.Fatalf("-home 没有值时应报错：%v", err)
	}
}
