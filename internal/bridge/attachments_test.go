package bridge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"qqbot_bridge/internal/gateway"
)

// TestSaveAttachmentsDownloads 验证附件会落到工作目录的 .inbox 下。
func TestSaveAttachmentsDownloads(t *testing.T) {
	payload := []byte("fake-png-bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	dir := t.TempDir()
	saved, err := saveAttachments(context.Background(), dir, []gateway.Attachment{
		{URL: srv.URL + "/a.png", Filename: "截图.png", ContentType: "image/png"},
	}, nil)
	if err != nil {
		t.Fatalf("下载附件失败：%v", err)
	}
	if len(saved) != 1 {
		t.Fatalf("期望保存 1 个附件，实际 %d", len(saved))
	}
	if got := filepath.Dir(saved[0]); got != filepath.Join(dir, inboxDir) {
		t.Fatalf("附件应保存在 %s，实际 %s", filepath.Join(dir, inboxDir), got)
	}
	data, err := os.ReadFile(saved[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(payload) {
		t.Fatalf("附件内容不一致：%q", data)
	}
}

// TestSaveAttachmentsRejectsTraversal 验证恶意文件名不会逃出 .inbox。
func TestSaveAttachmentsRejectsTraversal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("x"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	saved, err := saveAttachments(context.Background(), dir, []gateway.Attachment{
		{URL: srv.URL, Filename: "../../evil.png"},
	}, nil)
	if err != nil {
		t.Fatalf("下载失败：%v", err)
	}
	if len(saved) != 1 {
		t.Fatalf("期望保存 1 个，实际 %d", len(saved))
	}
	rel, err := filepath.Rel(dir, saved[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(rel, "..") || strings.Contains(rel, "..") {
		t.Fatalf("落盘路径逃出了工作目录：%s", saved[0])
	}
}

// TestSaveAttachmentsReportsFailure 验证全部下载失败时返回错误。
func TestSaveAttachmentsReportsFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := saveAttachments(context.Background(), t.TempDir(), []gateway.Attachment{
		{URL: srv.URL, Filename: "x.png"},
	}, nil)
	if err == nil {
		t.Fatal("下载失败应返回错误")
	}
}

func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		"a b.png":     "a_b.png",
		"ok-文件_1.md":  "ok-文件_1.md",
		"..":          "",
		"/etc/passwd": "_etc_passwd",
	}
	for in, want := range cases {
		if got := sanitizeName(in); got != want {
			t.Fatalf("sanitizeName(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestNonVoiceAttachmentsAndPrompt(t *testing.T) {
	atts := []gateway.Attachment{
		{ContentType: "voice", ASRReferText: "你好"},
		{ContentType: "image/png", Filename: "a.png"},
	}
	got := nonVoiceAttachments(atts)
	if len(got) != 1 || got[0].Filename != "a.png" {
		t.Fatalf("应只保留非语音附件：%+v", got)
	}

	prompt := withAttachments("看看这个图", []string{"/tmp/a.png"})
	if !strings.Contains(prompt, "看看这个图") || !strings.Contains(prompt, "/tmp/a.png") {
		t.Fatalf("拼装结果不对：%s", prompt)
	}

	// 没有文字时不能以空行开头
	only := withAttachments("", []string{"/tmp/a.png"})
	if strings.HasPrefix(only, "\n") {
		t.Fatalf("纯附件指令不应以空行开头：%q", only)
	}
}
