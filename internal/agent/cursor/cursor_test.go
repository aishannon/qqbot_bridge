package cursor

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"qqbot_bridge/internal/agent"
)

// 驱动必须能被注册表找到，否则配置里写 cursor 会变成「未知驱动」。
func TestDriverRegistered(t *testing.T) {
	d, err := agent.Open(Name, agent.Settings{})
	if err != nil {
		t.Fatalf("Open(%q) 失败：%v", Name, err)
	}
	if d.Name() != Name {
		t.Fatalf("驱动名不对：%q", d.Name())
	}
	if got := d.(*driver).command; got != DefaultCommand {
		t.Fatalf("默认命令应为 %s，实际 %q", DefaultCommand, got)
	}
	d, _ = agent.Open(Name, agent.Settings{Command: " /opt/bin/agent "})
	if got := d.(*driver).command; got != "/opt/bin/agent" {
		t.Fatalf("应使用配置的命令：%q", got)
	}
}

// Cursor 具备模型列表、transcript、登录预检；没有会话发现和 IDE 历史库。
func TestCapabilities(t *testing.T) {
	d, _ := agent.Open(Name, agent.Settings{})
	if _, ok := d.(agent.ModelProvider); !ok {
		t.Fatal("应实现 ModelProvider")
	}
	if _, ok := d.(agent.TranscriptProvider); !ok {
		t.Fatal("应实现 TranscriptProvider")
	}
	if _, ok := d.(agent.Preflight); !ok {
		t.Fatal("应实现 Preflight")
	}
	if _, ok := d.(agent.SessionDiscoverer); ok {
		t.Fatal("不应实现 SessionDiscoverer：transcript 里没有工作目录")
	}
	if _, ok := d.(agent.HistoryMirror); ok {
		t.Fatal("不应实现 HistoryMirror：Cursor 没有可写入的外部历史库")
	}
}

func TestBuildArgs(t *testing.T) {
	cases := []struct {
		name string
		opts agent.Options
		want []string
	}{
		{
			name: "默认权限放开 + 新会话",
			opts: agent.Options{Dir: "/w"},
			want: []string{"-p", "--output-format", "stream-json", "--trust", "--workspace", "/w", "--force", "--approve-mcps", "--", "hi"},
		},
		{
			name: "bypass + 模型 + 续聊",
			opts: agent.Options{Dir: "/w", PermissionMode: "bypassPermissions", Model: "claude-opus-4-8[effort=high]", Resume: "chat-1", MaxTurns: 3},
			want: []string{"-p", "--output-format", "stream-json", "--trust", "--workspace", "/w", "--force", "--approve-mcps",
				"--model", "claude-opus-4-8[effort=high]", "--resume", "chat-1", "--", "hi"},
		},
		{
			name: "plan 只读",
			opts: agent.Options{Dir: "/w", PermissionMode: "plan"},
			want: []string{"-p", "--output-format", "stream-json", "--trust", "--workspace", "/w", "--mode", "plan", "--", "hi"},
		},
		{
			name: "default 不加强制参数",
			opts: agent.Options{Dir: "/w", PermissionMode: "default"},
			want: []string{"-p", "--output-format", "stream-json", "--trust", "--workspace", "/w", "--", "hi"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := buildArgs(c.opts, "hi"); !slices.Equal(got, c.want) {
				t.Fatalf("\n期望 %q\n实际 %q", c.want, got)
			}
		})
	}
}

// 系统说明只在新会话注入，续聊不重复。
func TestComposePrompt(t *testing.T) {
	got := composePrompt(agent.Options{AppendSystemPrompt: "手机端约束"}, "做事")
	want := "<system_instructions>\n手机端约束\n</system_instructions>\n\n做事"
	if got != want {
		t.Fatalf("新会话应注入系统说明：%q", got)
	}
	if got := composePrompt(agent.Options{AppendSystemPrompt: "手机端约束", Resume: "c1"}, "做事"); got != "做事" {
		t.Fatalf("续聊不应重复注入：%q", got)
	}
	if got := composePrompt(agent.Options{}, "做事"); got != "做事" {
		t.Fatalf("没有系统说明时应原样返回：%q", got)
	}
}

// 超长 prompt 落盘到 .inbox，argv 里换成读文件的指令。
func TestSpillPrompt(t *testing.T) {
	dir := t.TempDir()
	short, err := spillPrompt(dir, "短指令", time.Now())
	if err != nil || short != "短指令" {
		t.Fatalf("短指令应原样返回：%q %v", short, err)
	}

	long := strings.Repeat("长", maxArgPrompt) // 3 字节一个字，必然超限
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.Local)
	got, err := spillPrompt(dir, long, now)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, inboxDir, "prompt-20261117-120000.000.md")
	if !strings.Contains(got, path) {
		t.Fatalf("应指向落盘文件 %s：%q", path, got)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != long {
		t.Fatalf("落盘内容不对：%v", err)
	}
	if _, err := spillPrompt("", long, now); err == nil {
		t.Fatal("没有工作目录时应报错")
	}
}

func TestVersion(t *testing.T) {
	d := fakeDriver(t)
	if got := d.Version(t.Context()); got != fakeVersion {
		t.Fatalf("版本号不对：%q", got)
	}
	if got := parseVersion("\n  2026.11.11-b912975 \nfoo"); got != "2026.11.11-b912975" {
		t.Fatalf("应取第一行非空：%q", got)
	}
	missing := &driver{command: filepath.Join(t.TempDir(), "nope")}
	if got := missing.Version(t.Context()); got != "" {
		t.Fatalf("探测失败应返回空串：%q", got)
	}
}

func TestTranscriptPath(t *testing.T) {
	d := &driver{home: "/home/u"}
	cases := map[string]string{
		"/data/workspace/cursor/backend/application/openplatform": "data-workspace-cursor-backend-application-openplatform",
		"/tmp/cursor-probe/ws":          "tmp-cursor-probe-ws",
		"/tmp/cursor-probe/Ab_c.d e@f":  "tmp-cursor-probe-Ab-c-d-e-f",
		"/tmp/cursor-probe/x__y..z":     "tmp-cursor-probe-x-y-z",
		"/tmp/cursor-probe/中文dir":       "tmp-cursor-probe-dir",
		"/data/workspace/qqbot_bridge/": "data-workspace-qqbot-bridge",
	}
	for dir, key := range cases {
		got, ok := d.TranscriptPath(dir, "sid")
		want := filepath.Join("/home/u", ".cursor", "projects", key, "agent-transcripts", "sid", "sid.jsonl")
		if !ok || got != want {
			t.Errorf("%s：\n期望 %s\n实际 %s", dir, want, got)
		}
	}
	if _, ok := d.TranscriptPath("/w", ""); ok {
		t.Fatal("没有会话 ID 时不应给路径")
	}
}
