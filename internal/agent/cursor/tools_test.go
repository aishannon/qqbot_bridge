package cursor

import "testing"

func TestDescribeToolCall(t *testing.T) {
	cases := []struct {
		name, raw, dir, wantName, wantLabel string
	}{
		{"read 相对路径", `{"readToolCall":{"args":{"path":"/w/src/main.go"}},"toolCallId":"x"}`, "/w", "Read", "Read: src/main.go"},
		{"read 目录外保留绝对路径", `{"readToolCall":{"args":{"path":"/etc/hosts"}}}`, "/w", "Read", "Read: /etc/hosts"},
		{"shell 压成一行", `{"shellToolCall":{"args":{"command":"go test \\\n  ./...","description":"run tests"}}}`, "/w", "Shell", "Shell: go test \\ ./..."},
		{"grep 取 pattern", `{"grepToolCall":{"args":{"pattern":"hello","path":"/w"}}}`, "/w", "Grep", "Grep: hello"},
		{"glob 取 globPattern", `{"globToolCall":{"args":{"targetDirectory":"/w","globPattern":"*.txt"}}}`, "/w", "Glob", "Glob: *.txt"},
		{"edit", `{"editToolCall":{"args":{"path":"/w/a.txt","streamContent":"x"}}}`, "/w", "Edit", "Edit: a.txt"},
		{"未知工具首字母大写", `{"webSearchToolCall":{"args":{"query":"go 1.26"}}}`, "", "WebSearch", "WebSearch: go 1.26"},
		{"无参数", `{"todoToolCall":{"args":{}}}`, "", "Todo", "Todo"},
		{"function 形状", `{"function":{"name":"browser_open","arguments":"{\"url\":\"https://x.dev\"}"}}`, "", "browser_open", "browser_open: https://x.dev"},
		{"坏 JSON", `nope`, "", "Tool", "Tool"},
		{"认不出的形状", `{"toolCallId":"x"}`, "", "Tool", "Tool"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := describeToolCall([]byte(c.raw), c.dir)
			if got.Name != c.wantName || got.Label != c.wantLabel {
				t.Fatalf("期望 %q / %q，实际 %q / %q", c.wantName, c.wantLabel, got.Name, got.Label)
			}
		})
	}
}

func TestOneLine(t *testing.T) {
	if got := oneLine("a\n\nb   c", 20); got != "a b c" {
		t.Fatalf("压缩空白失败：%q", got)
	}
	if got := oneLine("一二三四五", 3); got != "一二三…" {
		t.Fatalf("截断失败：%q", got)
	}
}
