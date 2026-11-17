package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Cursor 的 jsonl：{"role":…,"message":{"content":[…]}} + turn_ended，
// 用真实采样（两轮对话：读取+写文件、改文件+搜索）验证解析。
func TestParseCursorTranscript(t *testing.T) {
	tr, err := parseTranscript(filepath.Join("testdata", "cursor_transcript.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	messages, tools := tr.counts()
	if messages != 6 || tools != 5 {
		t.Fatalf("应有 6 条文本、5 个工具调用，实际 %d / %d", messages, tools)
	}
	if len(tr.items) != 11 {
		t.Fatalf("共应 11 项（turn_ended 不产生条目），实际 %d", len(tr.items))
	}
	for i, it := range tr.items {
		if it.Idx != i {
			t.Fatalf("idx 应连续：第 %d 项是 %d", i, it.Idx)
		}
		if strings.Contains(it.Text, "<timestamp>") || strings.Contains(it.Text, "<user_query>") ||
			strings.Contains(it.Text, "</user_query>") {
			t.Fatalf("用户消息的包装没剥干净：%q", it.Text)
		}
	}
	// 第一条用户指令当标题，且是 <user_query> 里的原文
	if !strings.HasPrefix(tr.title, "读取 a.txt 的内容") {
		t.Fatalf("标题应取第一条用户指令：%q", tr.title)
	}
	first := tr.items[0]
	if first.Role != "user" || first.Kind != "text" || !strings.Contains(first.Text, "echo hi > b.txt") {
		t.Fatalf("第一条应是用户文本：%+v", first)
	}
	// 工具调用块：名字和参数
	tool := tr.items[2]
	if tool.Kind != "tool" || tool.Name != "Read" || !strings.Contains(tool.Text, "a.txt") {
		t.Fatalf("工具项解析不对：%+v", tool)
	}
	last := tr.items[len(tr.items)-1]
	if last.Kind != "text" || !strings.Contains(last.Text, "b.txt") {
		t.Fatalf("最后一项应是助手回答：%+v", last)
	}
}

// 同一个文件里混着两种格式（比如旧会话被两边都写过）也要各自解析，互不干扰。
func TestParseMixedTranscript(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mixed.jsonl")
	lines := []string{
		`{"type":"message","role":"user","content":[{"text":"CodeBuddy 的第一句"}]}`,
		`{"role":"user","message":{"content":[{"type":"text","text":"<timestamp>Tuesday, Nov 17, 2026, 12:12 PM (UTC+8)</timestamp>\n<user_query>\nCursor 的第一句\n</user_query>"}]}}`,
		`{"type":"function_call","name":"Bash","arguments":"{\"command\":\"ls\"}"}`,
		`{"role":"assistant","message":{"content":[{"type":"text","text":"Cursor 的回答"},{"type":"tool_use","name":"Shell","input":{"command":"ls"}}]}}`,
		`{"type":"turn_ended","status":"success"}`,
		`{"type":"ai-title","aiTitle":"测试标题"}`,
		`not json`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tr, err := parseTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range tr.items {
		got = append(got, it.Kind+":"+it.Role+":"+it.Text)
	}
	want := []string{
		"text:user:CodeBuddy 的第一句",
		"text:user:Cursor 的第一句",
		"tool:assistant:{\"command\":\"ls\"}",
		"text:assistant:Cursor 的回答",
		"tool:assistant:{\"command\":\"ls\"}",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("混合解析结果不对：\n%s", strings.Join(got, "\n"))
	}
	// CodeBuddy 的 ai-title 在 Cursor 行之后出现，仍然优先生效
	if tr.title != "测试标题" {
		t.Fatalf("ai-title 应覆盖用户指令标题：%q", tr.title)
	}
}

func TestShortTitle(t *testing.T) {
	if got := shortTitle("a\n\nb"); got != "a b" {
		t.Fatalf("应压成一行：%q", got)
	}
	if got := shortTitle(strings.Repeat("字", 80)); len([]rune(got)) != 61 {
		t.Fatalf("超长应截断到 60 字 + 省略号：%d", len([]rune(got)))
	}
}
