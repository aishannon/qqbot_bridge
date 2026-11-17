package cursor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func resetModelCache(t *testing.T) {
	t.Helper()
	modelCache.mu.Lock()
	clear(modelCache.entries)
	modelCache.mu.Unlock()
	t.Cleanup(func() {
		modelCache.mu.Lock()
		clear(modelCache.entries)
		modelCache.mu.Unlock()
	})
}

// 用真实采样的 `cursor-agent models` 输出验证解析规则。
func TestParseModels(t *testing.T) {
	data, err := os.ReadFile(testdata(t, "models.txt"))
	if err != nil {
		t.Fatal(err)
	}
	models := parseModels(string(data))
	if len(models) != 280 {
		t.Fatalf("应解析出 280 个模型，实际 %d", len(models))
	}
	if models[0].ID != "auto" || models[0].Name != "Auto (default)" {
		t.Fatalf("auto 应排第一：%+v", models[0])
	}
	for _, m := range models {
		if m.HasRate {
			t.Fatalf("Cursor 没有倍率：%+v", m)
		}
		if strings.ContainsAny(m.Name, "\u200b\u200c\u200d\ufeff") || strings.Contains(m.Name, "  ") {
			t.Fatalf("展示名应去掉零宽字符并压缩空白：%q", m.Name)
		}
		if strings.HasPrefix(m.ID, "Tip") || m.ID == "Available" {
			t.Fatalf("标题和提示行不应当成模型：%+v", m)
		}
	}
	// 实测输出里 Grok 4.7 的名字带双空格和零宽字符
	for _, m := range models {
		if m.ID == "grok-4.7-low-fast" && m.Name != "Grok 4.7 Low Fast" {
			t.Fatalf("名字清理不对：%q", m.Name)
		}
	}
}

// 第一次查询走 CLI，TTL 内复用缓存；刷新失败有旧缓存时退回旧缓存。
func TestModelsCacheAndFallback(t *testing.T) {
	resetModelCache(t)
	d := fakeDriver(t)
	count := filepath.Join(t.TempDir(), "count")
	t.Setenv(fakeCountFile, count)
	t.Setenv(fakeModels, testdata(t, "models.txt"))

	c, err := d.Models(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Models) != 280 || !c.NoRates || c.RefreshErr != nil || c.UpdatedAt.IsZero() {
		t.Fatalf("目录不对：%d 个 NoRates=%v err=%v", len(c.Models), c.NoRates, c.RefreshErr)
	}
	if _, err := d.Models(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := fileLen(t, count); n != 1 {
		t.Fatalf("TTL 内应复用缓存，实际调用 CLI %d 次", n)
	}

	// 让缓存过期，再让 CLI 失败：应退回旧缓存并带上 RefreshErr
	modelCache.mu.Lock()
	e := modelCache.entries[d.command]
	e.at = time.Now().Add(-2 * modelCacheTTL)
	modelCache.entries[d.command] = e
	modelCache.mu.Unlock()
	t.Setenv(fakeModels, "")
	t.Setenv(fakeStderr, testdata(t, "stderr_unauthenticated.txt"))
	t.Setenv(fakeExit, "1")

	c, err = d.Models(t.Context())
	if err != nil {
		t.Fatalf("有旧缓存时不应直接报错：%v", err)
	}
	if len(c.Models) != 280 || !errors.Is(c.RefreshErr, ErrNotLoggedIn) {
		t.Fatalf("应退回旧缓存并说明未登录：%d %v", len(c.Models), c.RefreshErr)
	}
}

// 没有缓存时查询失败要直接报错，并带上登录指引。
func TestModelsNotLoggedIn(t *testing.T) {
	resetModelCache(t)
	d := fakeDriver(t)
	t.Setenv(fakeStderr, testdata(t, "stderr_unauthenticated.txt"))
	t.Setenv(fakeExit, "1")
	_, err := d.Models(t.Context())
	if !errors.Is(err, ErrNotLoggedIn) || !strings.Contains(err.Error(), "cursor-agent login") {
		t.Fatalf("应返回未登录错误：%v", err)
	}
	if _, _, ok := d.ModelRate("auto"); ok {
		t.Fatal("Cursor 没有倍率，ModelRate 应恒为 false")
	}
}

func TestCheck(t *testing.T) {
	d := fakeDriver(t)
	t.Setenv("CURSOR_API_KEY", "")
	t.Setenv("CURSOR_AUTH_TOKEN", "")

	t.Setenv(fakeStatus, testdata(t, "status_logged_in.json"))
	if err := d.Check(t.Context()); err != nil {
		t.Fatalf("已登录不应报错：%v", err)
	}

	t.Setenv(fakeStatus, testdata(t, "status_logged_out.json"))
	if err := d.Check(t.Context()); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("未登录应报 ErrNotLoggedIn：%v", err)
	}

	t.Setenv("CURSOR_API_KEY", "key")
	if err := d.Check(t.Context()); err != nil {
		t.Fatalf("配置了 API Key 时应放行：%v", err)
	}

	t.Setenv(fakeStatus, writeTemp(t, "odd.json", "not json"))
	t.Setenv("CURSOR_API_KEY", "")
	if err := d.Check(t.Context()); err != nil {
		t.Fatalf("看不懂的输出应放行：%v", err)
	}

	missing := &driver{command: filepath.Join(t.TempDir(), "no-such-cursor-agent")}
	if err := missing.Check(t.Context()); err == nil || !strings.Contains(err.Error(), "找不到 Cursor CLI") {
		t.Fatalf("没装 CLI 时应提示安装：%v", err)
	}
}

func fileLen(t *testing.T, p string) int {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return len(data)
}
