package bridge

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	// 注册 CodeBuddy 驱动：自动注册目录用的就是它读会话索引的能力
	_ "qqbot_bridge/internal/agent/codebuddy"
	"qqbot_bridge/internal/config"
)

func writeIndex(t *testing.T, root, name, dir string, startedAt int64) {
	t.Helper()
	body := fmt.Sprintf(`{"cwd":%q,"kind":"bg","status":"idle","startedAt":%d}`, dir, startedAt)
	if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSyncDiscoveredRegistersAndHonorsIgnore(t *testing.T) {
	b, store := newTestBridge(t)
	root := t.TempDir()
	one := t.TempDir()
	two := t.TempDir()
	writeIndex(t, root, "1.json", one, 1000)
	writeIndex(t, root, "2.json", two, 2000)

	if err := store.Update(func(c *config.Config) error {
		c.Discover.Root = root
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if n := b.SyncDiscovered(); n != 2 {
		t.Fatalf("应注册 2 个目录，实际 %d", n)
	}
	if n := b.SyncDiscovered(); n != 0 {
		t.Fatalf("重复扫描不应重复注册：%d", n)
	}

	var auto config.Session
	for _, s := range store.Get().Sessions {
		if cleanDir(s.Dir) == cleanDir(one) {
			auto = s
		}
	}
	if auto.ID == "" || !auto.Auto {
		t.Fatalf("自动注册的会话标记不对：%+v", auto)
	}
	if !auto.New {
		t.Fatalf("自动注册的会话应标记为新增：%+v", auto)
	}

	// 移除自动会话后应记入忽略列表，不会再被扫回来
	if out := b.removeSession(auto.ID); !strings.Contains(out, "已移除") {
		t.Fatalf("移除失败：%s", out)
	}
	cfg := store.Get()
	if len(cfg.Discover.Ignore) != 1 || cleanDir(cfg.Discover.Ignore[0]) != cleanDir(one) {
		t.Fatalf("忽略列表不对：%v", cfg.Discover.Ignore)
	}
	if n := b.SyncDiscovered(); n != 0 {
		t.Fatalf("被移除的目录不应自动加回：%d", n)
	}

	// 手动 /new 同一个目录：恢复为普通会话，并从忽略列表里清掉
	if out := b.newSession(one, "手动"); !strings.Contains(out, "已新建") {
		t.Fatalf("新建失败：%s", out)
	}
	cfg = store.Get()
	if len(cfg.Discover.Ignore) != 0 {
		t.Fatalf("手动注册后应从忽略列表移除：%v", cfg.Discover.Ignore)
	}
	for _, s := range cfg.Sessions {
		if cleanDir(s.Dir) == cleanDir(one) && s.Auto {
			t.Fatalf("手动注册不应再标记为自动：%+v", s)
		}
	}
}

func TestSyncDiscoveredCanBeDisabled(t *testing.T) {
	b, store := newTestBridge(t)
	root := t.TempDir()
	writeIndex(t, root, "1.json", t.TempDir(), time.Now().UnixMilli())
	if err := store.Update(func(c *config.Config) error {
		c.Discover.Root = root
		c.Discover.Disable = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if n := b.SyncDiscovered(); n != 0 {
		t.Fatalf("关闭自动注册后不应新增：%d", n)
	}
}
