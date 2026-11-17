package cursor

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"qqbot_bridge/internal/agent"
)

// runFake 用伪装 CLI 跑一轮任务，返回任务、收到的事件和 CLI 实际收到的 argv。
func runFake(t *testing.T, opts agent.Options, prompt string) (agent.Task, []agent.Event, []string) {
	t.Helper()
	d := fakeDriver(t)
	argsFile := filepath.Join(t.TempDir(), "args.json")
	t.Setenv(fakeArgsOut, argsFile)

	var mu sync.Mutex
	var events []agent.Event
	opts.OnEvent = func(e agent.Event) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	}
	if opts.Dir == "" {
		opts.Dir = t.TempDir()
	}
	task, err := d.Start(t.Context(), opts, prompt)
	if err != nil {
		t.Fatalf("启动失败：%v", err)
	}
	waitDone(t, task, 10*time.Second)

	var args []string
	if data, err := os.ReadFile(argsFile); err == nil {
		_ = json.Unmarshal(data, &args)
	}
	mu.Lock()
	defer mu.Unlock()
	return task, slices.Clone(events), args
}

func waitDone(t *testing.T, task agent.Task, within time.Duration) {
	t.Helper()
	select {
	case <-task.Done():
	case <-time.After(within):
		task.Stop()
		t.Fatalf("任务 %s 内没有结束", within)
	}
}

func kinds(events []agent.Event) map[string]int {
	out := make(map[string]int)
	for _, e := range events {
		out[e.Kind]++
	}
	return out
}

// 回放真实的成功流：会话 ID、工具摘要、最终回答、thinking 都要解析对。
func TestTaskSuccessStream(t *testing.T) {
	t.Setenv(fakeStream, testdata(t, "stream_success.jsonl"))
	task, events, args := runFake(t, agent.Options{AppendSystemPrompt: "约束"}, "读取 a.txt")

	if err := task.Wait(); err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	if task.IsError() {
		t.Fatalf("不应标记错误：%s", task.Result())
	}
	if got := task.SessionID(); got != "dfb20190-0df9-4185-b1cd-ff3cdfc034dd" {
		t.Fatalf("会话 ID 不对：%q", got)
	}
	wantTools := []string{"Read: /tmp/cursor-probe/ws/a.txt", "Shell: echo hi > b.txt"}
	if got := task.Tools(); !slices.Equal(got, wantTools) {
		t.Fatalf("工具摘要不对（completed 不应重复计数）：%q", got)
	}
	// result 只取最后一次工具调用之后的回答，而不是 CLI 那段首尾相连的全文
	if got := task.Result(); got != "已读取 `a.txt`（内容为 hello world 和 line2），并已执行命令将 `hi` 写入 `b.txt`。" {
		t.Fatalf("结果不对：%q", got)
	}
	if got := task.Texts(); len(got) != 2 {
		t.Fatalf("应有 2 段 assistant 文本：%q", got)
	}
	k := kinds(events)
	if k["system"] != 1 || k["tool_use"] != 2 || k["result"] != 1 || k["text"] != 2 || k["thinking"] != 2 {
		t.Fatalf("事件统计不对：%v", k)
	}
	last := events[len(events)-1]
	if last.Kind != "result" || last.Duration != 5953*time.Millisecond {
		t.Fatalf("result 事件不对：%+v", last)
	}

	// 新会话：系统说明拼在 prompt 前，prompt 放在 -- 之后
	if n := len(args); n < 2 || args[n-2] != "--" || !strings.HasPrefix(args[n-1], "<system_instructions>\n约束\n") {
		t.Fatalf("argv 不对：%q", args)
	}
}

// 续聊：带 --resume，不重复注入系统说明；工具调用覆盖 Edit/Grep/Glob。
func TestTaskResumeStream(t *testing.T) {
	t.Setenv(fakeStream, testdata(t, "stream_resume.jsonl"))
	task, _, args := runFake(t, agent.Options{
		Resume:             "dfb20190-0df9-4185-b1cd-ff3cdfc034dd",
		AppendSystemPrompt: "约束",
		Model:              "auto",
	}, "继续")

	if !slices.Contains(args, "--resume") || args[len(args)-1] != "继续" {
		t.Fatalf("续聊 argv 不对：%q", args)
	}
	want := []string{"Edit: /tmp/cursor-probe/ws/a.txt", "Grep: hello", "Glob: *.txt"}
	if got := task.Tools(); !slices.Equal(got, want) {
		t.Fatalf("工具摘要不对：%q", got)
	}
	if got := task.Result(); !strings.HasPrefix(got, "刚才让你写的文件是 `b.txt`。") {
		t.Fatalf("结果应是最终回答：%q", got)
	}
}

// 失败时 Cursor 不输出 result，只有 stderr + 非零退出码，要翻译成中文提示。
func TestTaskFailures(t *testing.T) {
	cases := []struct {
		name, stderr string
		want         []string
	}{
		{"模型不可用", "stderr_bad_model.txt", []string{"no-such-model-xyz", "/models"}},
		{"未登录", "stderr_unauthenticated.txt", []string{"cursor-agent login", "CURSOR_API_KEY"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(fakeStream, "")
			t.Setenv(fakeStderr, testdata(t, c.stderr))
			t.Setenv(fakeExit, "1")
			task, _, _ := runFake(t, agent.Options{}, "hi")
			if task.Wait() == nil {
				t.Fatal("非零退出应返回错误")
			}
			if !task.IsError() {
				t.Fatal("应标记为错误")
			}
			for _, w := range c.want {
				if !strings.Contains(task.Result(), w) {
					t.Fatalf("提示里应包含 %q：%s", w, task.Result())
				}
			}
		})
	}
}

func TestExplainFailure(t *testing.T) {
	cases := map[string]string{
		"\x1b[31mError: Run Everything is disabled by your team admin\x1b[0m": "agent.permissionMode",
		"Headless mode is disabled for this team":                             "headless",
		"something odd happened":                                              "something odd happened",
	}
	for in, want := range cases {
		got := explainFailure(in, nil)
		if !strings.Contains(got, want) || strings.Contains(got, "\x1b[") {
			t.Errorf("%q → %q，应包含 %q 且不带颜色码", in, got, want)
		}
	}
	if got := explainFailure(strings.Repeat("长", 2000), nil); len([]rune(got)) > maxFailureText+1 {
		t.Fatalf("应截断：%d 字", len([]rune(got)))
	}
	if got := explainFailure("", &exec.ExitError{}); !strings.HasPrefix(got, "cursor-agent 异常退出") {
		t.Fatalf("没有 stderr 时给通用提示：%q", got)
	}
}

// /stop：中断后进程要尽快退出，结果明确是「已中断」。
func TestTaskStop(t *testing.T) {
	stream := writeTemp(t, "init.jsonl",
		`{"type":"system","subtype":"init","session_id":"s-stop","cwd":"/w","model":"Auto","permissionMode":"default"}`+"\n")
	t.Setenv(fakeStream, stream)
	t.Setenv(fakeHang, "1")

	d := fakeDriver(t)
	task, err := d.Start(t.Context(), agent.Options{Dir: t.TempDir()}, "sleep")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for task.SessionID() == "" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if task.SessionID() != "s-stop" {
		t.Fatalf("应先读到 init：%q", task.SessionID())
	}
	start := time.Now()
	task.Stop()
	waitDone(t, task, stopGrace+5*time.Second)
	if took := time.Since(start); took > stopGrace+time.Second {
		t.Fatalf("中断太慢：%s", took)
	}
	if !task.IsError() || task.Result() != stoppedText {
		t.Fatalf("中断后结果不对：%v %q", task.IsError(), task.Result())
	}
}

// 超长 prompt 不进 argv，而是落盘后让 agent 去读。
func TestTaskLongPromptSpilled(t *testing.T) {
	t.Setenv(fakeStream, testdata(t, "stream_success.jsonl"))
	dir := t.TempDir()
	_, _, args := runFake(t, agent.Options{Dir: dir}, strings.Repeat("a", maxArgPrompt+1))
	last := args[len(args)-1]
	if len(last) > 1024 || !strings.Contains(last, filepath.Join(dir, inboxDir)) {
		t.Fatalf("argv 里应是读文件的短指令：%.200q", last)
	}
}

// handleLine 直接喂各种边角事件，不应 panic，也不应误计数。
func TestHandleLineEdgeCases(t *testing.T) {
	tk := newTask(func() {}, "/w")
	var got []agent.Event
	collect := func(e agent.Event) { got = append(got, e) }
	for _, line := range []string{
		``,
		`not json`,
		`{"type":"unknown_future_event","x":1}`,
		`{"type":"assistant"}`,
		`{"type":"assistant","message":{"content":"纯字符串内容"}}`,
		`{"type":"tool_call","subtype":"completed","call_id":"c1","tool_call":{"function":{"name":"mcp_search","arguments":"{\"query\":\"golang\"}"}}}`,
		`{"type":"tool_call","subtype":"completed","call_id":"c1","tool_call":{"function":{"name":"mcp_search","arguments":"{}"}}}`,
		`{"type":"result","subtype":"error","is_error":true,"result":"boom","session_id":"s1"}`,
	} {
		tk.handleLine([]byte(line), collect)
	}
	if got := tk.Tools(); !slices.Equal(got, []string{"mcp_search: golang"}) {
		t.Fatalf("漏掉 started 时 completed 应补记一次：%q", got)
	}
	if !tk.IsError() || tk.Result() != "boom" || tk.SessionID() != "s1" {
		t.Fatalf("错误 result 解析不对：%v %q %q", tk.IsError(), tk.Result(), tk.SessionID())
	}
	if texts := tk.Texts(); !slices.Equal(texts, []string{"纯字符串内容"}) {
		t.Fatalf("字符串形状的 content 也要兼容：%q", texts)
	}
}
