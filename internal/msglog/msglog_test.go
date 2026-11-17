package msglog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAddAndQueryNewestFirst(t *testing.T) {
	l := New("", 10)
	l.Add(Entry{From: "u1", Status: StatusAccepted, Kind: KindText, Content: "第一条"})
	l.Add(Entry{From: "u2", Name: "小王", Status: StatusDenied, Kind: KindText, Content: "第二条"})
	l.Add(Entry{From: "u1", Status: StatusAccepted, Kind: KindText, Content: "第三条"})

	got := l.Query(Filter{})
	if len(got) != 3 || got[0].Content != "第三条" || got[2].Content != "第一条" {
		t.Fatalf("应按时间倒序：%+v", got)
	}
	if got := l.Query(Filter{Status: StatusDenied}); len(got) != 1 || got[0].From != "u2" {
		t.Fatalf("状态过滤不对：%+v", got)
	}
	if got := l.Query(Filter{Keyword: "小王"}); len(got) != 1 {
		t.Fatalf("昵称搜索不对：%+v", got)
	}
	if got := l.Query(Filter{Keyword: "U1"}); len(got) != 2 {
		t.Fatalf("openid 搜索应不区分大小写：%+v", got)
	}
}

func TestPagingByBeforeID(t *testing.T) {
	l := New("", 10)
	for i := 0; i < 5; i++ {
		l.Add(Entry{From: "u", Status: StatusAccepted, Kind: KindText, Content: strings.Repeat("x", i+1)})
	}
	page1 := l.Query(Filter{Limit: 2})
	if len(page1) != 2 {
		t.Fatalf("第一页条数不对：%d", len(page1))
	}
	page2 := l.Query(Filter{Limit: 2, BeforeID: page1[1].ID})
	if len(page2) != 2 || page2[0].ID >= page1[1].ID {
		t.Fatalf("翻页不对：%+v / %+v", page1, page2)
	}
}

func TestRingBufferKeepsLatest(t *testing.T) {
	l := New("", 3)
	for i := 0; i < 5; i++ {
		l.Add(Entry{From: "u", Status: StatusAccepted, Kind: KindText, Content: string(rune('a' + i))})
	}
	got := l.Query(Filter{})
	if len(got) != 3 || got[0].Content != "e" || got[2].Content != "c" {
		t.Fatalf("环形缓冲保留不对：%+v", got)
	}
}

func TestContentTruncated(t *testing.T) {
	l := New("", 3)
	e := l.Add(Entry{From: "u", Status: StatusAccepted, Kind: KindText, Content: strings.Repeat("中", maxContent+50)})
	if got := len([]rune(e.Content)); got != maxContent+1 {
		t.Fatalf("内容应被截断到 %d 字加省略号，实际 %d", maxContent, got)
	}
}

func TestPersistAndReload(t *testing.T) {
	dir := t.TempDir()
	l := New(dir, 10)
	l.Add(Entry{From: "u1", Status: StatusAccepted, Kind: KindText, Content: "落盘的消息"})
	l.Add(Entry{From: "u2", Status: StatusDenied, Kind: KindText, Content: "被拒的消息"})
	_ = l.Close()

	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if len(files) != 1 {
		t.Fatalf("应有一个日志文件：%v", files)
	}
	if fi, _ := os.Stat(files[0]); fi.Mode().Perm() != 0o600 {
		t.Fatalf("日志文件权限应为 0600：%v", fi.Mode().Perm())
	}

	again := New(dir, 10)
	got := again.Query(Filter{})
	if len(got) != 2 || got[0].Content != "被拒的消息" {
		t.Fatalf("重启后应恢复记录：%+v", got)
	}
	e := again.Add(Entry{From: "u3", Status: StatusAccepted, Kind: KindText, Content: "新消息"})
	if e.ID <= got[0].ID {
		t.Fatalf("恢复后 ID 应继续递增：%d <= %d", e.ID, got[0].ID)
	}
}

func TestCleanupOldFiles(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, time.Now().AddDate(0, 0, -RetentionDays-2).Format("2006-01-02")+".jsonl")
	recent := filepath.Join(dir, time.Now().AddDate(0, 0, -1).Format("2006-01-02")+".jsonl")
	for _, p := range []string{old, recent} {
		if err := os.WriteFile(p, []byte(`{"status":"accepted","content":"x"}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	New(dir, 10)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("超期文件应被清理")
	}
	if _, err := os.Stat(recent); err != nil {
		t.Fatal("未超期文件不应被删除")
	}
}

func TestFilterBySessionAndSearchSessionName(t *testing.T) {
	l := New("", 10)
	l.Add(Entry{From: "u", Status: StatusAccepted, Kind: KindText, Content: "a", Session: "s1", SessionName: "支付项目", SessionDir: "/work/pay"})
	l.Add(Entry{From: "u", Status: StatusAccepted, Kind: KindText, Content: "b", Session: "s2", SessionName: "官网", SessionDir: "/work/web"})
	l.Add(Entry{From: "x", Status: StatusDenied, Kind: KindText, Content: "c"})

	if got := l.Query(Filter{Session: "s1"}); len(got) != 1 || got[0].Content != "a" {
		t.Fatalf("按会话过滤不对：%+v", got)
	}
	if got := l.Query(Filter{Keyword: "支付"}); len(got) != 1 || got[0].Session != "s1" {
		t.Fatalf("应能按会话名搜索：%+v", got)
	}
	if got := l.Query(Filter{Keyword: "/work/web"}); len(got) != 1 || got[0].Session != "s2" {
		t.Fatalf("应能按目录搜索：%+v", got)
	}
}

func TestSessionFieldsPersist(t *testing.T) {
	dir := t.TempDir()
	l := New(dir, 10)
	l.Add(Entry{From: "u", Status: StatusAccepted, Kind: KindText, Content: "hi", Session: "s1",
		SessionName: "支付项目", SessionDir: "/work/pay", Conversation: "conv-1", Command: true})
	_ = l.Close()

	got := New(dir, 10).Query(Filter{})
	if len(got) != 1 || got[0].Session != "s1" || got[0].SessionName != "支付项目" ||
		got[0].SessionDir != "/work/pay" || got[0].Conversation != "conv-1" || !got[0].Command {
		t.Fatalf("对话信息应随记录一起落盘并恢复：%+v", got)
	}
}
