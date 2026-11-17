package cursor

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"qqbot_bridge/internal/agent"
)

// stopGrace 是中断后等 CLI 自己收尾的时间，超时整组强杀。
const stopGrace = 5 * time.Second

// stoppedText 是用户主动中断时的结果文案。
const stoppedText = "任务已中断。"

// task 是一轮正在执行的 Cursor 任务。
type task struct {
	cancel context.CancelFunc
	dir    string        // 工作目录，用于把工具参数里的绝对路径缩短
	exited chan struct{} // 进程 Wait 返回后关闭，供强杀定时器判断
	done   chan struct{} // 事件读完、状态收尾后关闭

	mu         sync.Mutex
	sessionID  string
	result     string
	resultSeen bool
	isError    bool
	stopped    bool
	texts      []string
	tailFrom   int // 最后一次工具调用之后的第一段文本下标，result 只取这之后的
	tools      []string
	seenCalls  map[string]bool
	waitErr    error

	// thinking 只在读取 goroutine 里用，不需要锁。
	thinking strings.Builder
}

func newTask(cancel context.CancelFunc, dir string) *task {
	return &task{
		cancel:    cancel,
		dir:       dir,
		exited:    make(chan struct{}),
		done:      make(chan struct{}),
		seenCalls: make(map[string]bool),
	}
}

// SessionID 返回 Cursor 会话（chat）ID，system/init 事件后才有值。
func (t *task) SessionID() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sessionID
}

// Result 返回最终结果文本。
func (t *task) Result() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.result
}

// Texts 返回本轮 assistant 输出的所有文本块。
func (t *task) Texts() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.texts...)
}

// Tools 返回本轮调用过的工具摘要（按调用顺序，可能重复）。
func (t *task) Tools() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.tools...)
}

// IsError 表示本轮是否以错误结束。
func (t *task) IsError() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.isError
}

// Wait 等待进程退出、事件读完。
func (t *task) Wait() error {
	<-t.done
	return t.waitErr
}

// Done 返回结束信号。
func (t *task) Done() <-chan struct{} { return t.done }

// Stop 中断任务：先给整个进程组发 SIGINT 让 CLI 收尾，超时再强杀。
func (t *task) Stop() {
	t.mu.Lock()
	t.stopped = true
	t.mu.Unlock()
	t.cancel()
}

// run 逐行读 stdout 的 NDJSON，读完等进程退出并收尾。
func (t *task) run(cmd *exec.Cmd, stdout io.Reader, stderr *lockedBuffer, onEvent func(agent.Event)) {
	defer close(t.done)

	reader := bufio.NewReaderSize(stdout, 1<<20)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			t.handleLine(line, onEvent)
		}
		if err != nil {
			break
		}
	}
	waitErr := cmd.Wait()
	close(t.exited)
	t.finish(waitErr, stderr.String())
}

// finish 根据退出状态补齐结果：失败时 Cursor 不输出 result 事件，
// 只有非零退出码和 stderr，这里把它翻译成能照着做的中文提示。
func (t *task) finish(waitErr error, stderr string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.waitErr = waitErr
	switch {
	case t.stopped && !t.resultSeen:
		t.isError = true
		t.result = stoppedText
	case waitErr == nil && !t.resultSeen:
		// 正常退出却没有 result：用最后一段文本兜底，别让用户看到空结果
		t.result = t.tailText()
	case waitErr != nil && (!t.resultSeen || t.result == ""):
		t.isError = true
		t.result = explainFailure(stderr, waitErr)
	}
}

// tailText 返回最后一次工具调用之后的文本，即这一轮的最终回答。调用方持锁。
func (t *task) tailText() string {
	from := min(t.tailFrom, len(t.texts))
	return strings.TrimSpace(strings.Join(t.texts[from:], "\n\n"))
}

// streamEvent 是 stream-json 的一行。字段按需取，未知字段忽略。
type streamEvent struct {
	Type       string          `json:"type"`
	Subtype    string          `json:"subtype"`
	SessionID  string          `json:"session_id"`
	Text       string          `json:"text"`
	Result     string          `json:"result"`
	IsError    bool            `json:"is_error"`
	DurationMS int64           `json:"duration_ms"`
	CallID     string          `json:"call_id"`
	ToolCall   json.RawMessage `json:"tool_call"`
	Message    *struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// handleLine 解析一行事件，更新任务状态并回调。
func (t *task) handleLine(line []byte, onEvent func(agent.Event)) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || line[0] != '{' {
		return
	}
	var ev streamEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		return
	}
	if ev.SessionID != "" {
		t.mu.Lock()
		if t.sessionID == "" {
			t.sessionID = ev.SessionID
		}
		t.mu.Unlock()
	}

	emit := func(e agent.Event) {
		if onEvent != nil {
			onEvent(e)
		}
	}

	switch ev.Type {
	case "system":
		if ev.Subtype == "init" {
			emit(agent.Event{Kind: "system", SessionID: ev.SessionID, Text: "会话就绪"})
		}

	case "thinking":
		// 推理按 delta 流式给出，completed 时整段上报一次
		switch ev.Subtype {
		case "delta":
			t.thinking.WriteString(ev.Text)
		case "completed":
			if text := strings.TrimSpace(t.thinking.String()); text != "" {
				emit(agent.Event{Kind: "thinking", Text: text})
			}
			t.thinking.Reset()
		}

	case "assistant":
		if ev.Message == nil {
			return
		}
		for _, text := range textBlocks(ev.Message.Content) {
			if strings.TrimSpace(text) == "" {
				continue
			}
			t.mu.Lock()
			t.texts = append(t.texts, text)
			t.mu.Unlock()
			emit(agent.Event{Kind: "text", Text: text})
		}

	case "tool_call":
		// started/completed 用 call_id 配对，只在第一次见到时记一步；
		// 万一漏了 started，completed 也能补上。
		if ev.Subtype != "started" && ev.Subtype != "completed" {
			return
		}
		t.mu.Lock()
		if ev.CallID != "" && t.seenCalls[ev.CallID] {
			t.mu.Unlock()
			return
		}
		if ev.CallID != "" {
			t.seenCalls[ev.CallID] = true
		}
		call := describeToolCall(ev.ToolCall, t.dir)
		t.tools = append(t.tools, call.Label)
		t.tailFrom = len(t.texts)
		t.mu.Unlock()
		emit(agent.Event{Kind: "tool_use", Tool: call.Name, Input: string(call.Args)})

	case "result":
		t.mu.Lock()
		t.resultSeen = true
		t.isError = ev.IsError
		// Cursor 的 result 是全部 assistant 文本首尾直接相连（中间没有分隔），
		// 发到手机上会糊成一段；和 CodeBuddy 对齐，只取最终回答。
		t.result = cmp.Or(t.tailText(), strings.TrimSpace(ev.Result))
		if ev.IsError {
			t.result = cmp.Or(strings.TrimSpace(ev.Result), t.result)
		}
		result := t.result
		t.mu.Unlock()
		emit(agent.Event{
			Kind:      "result",
			Text:      result,
			SessionID: ev.SessionID,
			Duration:  time.Duration(ev.DurationMS) * time.Millisecond,
			IsError:   ev.IsError,
		})
	}
}

// textBlocks 取 message.content 里的文本，兼容块数组和纯字符串两种形状。
func textBlocks(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		out := make([]string, 0, len(blocks))
		for _, b := range blocks {
			if b.Type == "text" || b.Type == "" {
				out = append(out, b.Text)
			}
		}
		return out
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []string{s}
	}
	return nil
}

var (
	ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	// badModelRe 取「Cannot use this model: xxx.」这一句，后面那串可选模型太长不要。
	badModelRe = regexp.MustCompile(`(?i)cannot use this model:\s*([^\s.,]+)`)
)

// maxFailureText 是原样透传 stderr 时的最大字数，避免把一整屏堆栈发到手机上。
const maxFailureText = 800

// explainFailure 把 cursor-agent 失败时的 stderr 翻译成中文提示。
//
// 能识别的几类（未登录、模型不可用、团队禁用 headless / Run Everything）
// 给出具体怎么改；其他情况原样透传 stderr（去掉颜色码、截断）。
func explainFailure(stderr string, waitErr error) string {
	msg := strings.TrimSpace(ansiRe.ReplaceAllString(stderr, ""))
	lower := strings.ToLower(msg)
	switch {
	case isAuthError(msg):
		return "Cursor CLI 还没有登录：" + loginHint + "，然后重发指令。"
	case badModelRe.MatchString(msg):
		model := badModelRe.FindStringSubmatch(msg)[1]
		return "模型 `" + model + "` 在当前 Cursor 账号下不可用。发送 `/models` 查看可选模型，或 `/model default` 恢复默认。"
	case strings.Contains(lower, "run everything"):
		return "团队禁用了 Cursor 的 Run Everything，`--force` 用不了：把配置里的 `agent.permissionMode` 改成 `default` 后重试" +
			"（之后 shell / MCP 只能执行 allowlist 里放行的）。\n原始错误：" + truncateRunes(msg, 300)
	case strings.Contains(lower, "headless"):
		return "Cursor 团队管理员禁用了 CLI 的 headless 模式（Team Settings › Security & automation），桥接没法驱动 Cursor。" +
			"\n原始错误：" + truncateRunes(msg, 300)
	case msg != "":
		return truncateRunes(msg, maxFailureText)
	case waitErr != nil:
		return "cursor-agent 异常退出：" + waitErr.Error()
	default:
		return "cursor-agent 异常退出"
	}
}

// ErrNotLoggedIn 表示本机 Cursor CLI 没有登录。
var ErrNotLoggedIn = errors.New("Cursor CLI 未登录")

// isAuthError 判断 CLI 的错误输出是不是「需要登录」。
func isAuthError(out string) bool {
	lower := strings.ToLower(out)
	return strings.Contains(lower, "authentication required") || strings.Contains(lower, "not logged in")
}

// loginHint 是未登录时的统一指引。
const loginHint = "在运行桥接的机器上执行 `cursor-agent login`，或给桥接服务设置环境变量 `CURSOR_API_KEY`"

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// lockedBuffer 收集 stderr，读写不与其他 goroutine 竞争。
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
