package logbuf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRingKeepsLastN(t *testing.T) {
	b := New(3)
	for _, m := range []string{"a", "b", "c", "d"} {
		b.Add("info", "%s", m)
	}
	got := b.Entries()
	if len(got) != 3 || got[2].Message != "d" {
		t.Fatalf("环形缓冲应只留最后 3 条：%+v", got)
	}
}

func TestEnableFileWritesDailyFile(t *testing.T) {
	dir := t.TempDir()
	b := New(10)
	if err := b.EnableFile(dir); err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	day := time.Date(2026, 3, 5, 10, 0, 0, 0, time.Local)
	b.now = func() time.Time { return day }
	b.Add("info", "第一条 %d", 1)
	b.Add("warn", "第二条")

	data, err := os.ReadFile(filepath.Join(dir, "2026-03-05.log"))
	if err != nil {
		t.Fatalf("日志文件未生成：%v", err)
	}
	text := string(data)
	if !strings.Contains(text, "第一条 1") || !strings.Contains(text, "[warn] 第二条") {
		t.Fatalf("日志内容不对：\n%s", text)
	}
}

func TestEnableFileRotatesByDay(t *testing.T) {
	dir := t.TempDir()
	b := New(10)
	if err := b.EnableFile(dir); err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	d1 := time.Date(2026, 3, 5, 23, 59, 0, 0, time.Local)
	d2 := d1.Add(2 * time.Minute)
	b.now = func() time.Time { return d1 }
	b.Add("info", "昨天")
	b.now = func() time.Time { return d2 }
	b.Add("info", "今天")

	for _, name := range []string{"2026-03-05.log", "2026-03-06.log"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("缺少 %s：%v", name, err)
		}
	}
	data, _ := os.ReadFile(filepath.Join(dir, "2026-03-06.log"))
	if !strings.Contains(string(data), "今天") || strings.Contains(string(data), "昨天") {
		t.Fatalf("跨天没有切分干净：\n%s", data)
	}
}

// TestFileFailureDoesNotBreakBuffer 验证日志目录不可写时内存缓冲照常工作。
func TestFileFailureDoesNotBreakBuffer(t *testing.T) {
	b := New(5)
	b.mu.Lock()
	b.dir = filepath.Join(t.TempDir(), "no-such-dir", "x")
	b.mu.Unlock()

	b.Add("info", "仍然记录")
	if got := b.Entries(); len(got) != 1 {
		t.Fatalf("落盘失败时仍应保留内存日志：%+v", got)
	}
}
