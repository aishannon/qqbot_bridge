// Package codebuddy 把 CodeBuddy CLI 当成一个可编程的 agent 来驱动。
//
// 每轮任务起一个 codebuddy 进程，走 --input-format stream-json /
// --output-format stream-json 双向流：写一条 user 消息进去，逐行读回事件
// （assistant 文本、thinking、tool_use、result），处理完关闭 stdin 收尾。
//
// 除了通用的任务执行，这个包还实现 CodeBuddy 独有的能力：模型目录探测
// （capability 里的 ModelProvider）、会话索引发现（SessionDiscoverer）、
// transcript 定位（TranscriptProvider）和 IDE 历史库镜像（HistoryMirror）。
package codebuddy

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"qqbot_bridge/internal/agent"
)

const (
	// Name 是配置里 agent.driver 的取值。
	Name = "codebuddy"
	// DefaultCommand 是 CodeBuddy CLI 的默认可执行文件名。
	DefaultCommand = "codebuddy"
)

func init() { agent.Register(Name, newDriver) }

// newDriver 按配置构造驱动；没配可执行文件时用 codebuddy。
func newDriver(s agent.Settings) agent.Driver {
	command := strings.TrimSpace(s.Command)
	if command == "" {
		command = DefaultCommand
	}
	return &driver{command: command, discoverRoot: strings.TrimSpace(s.DiscoverRoot)}
}

// driver 是 CodeBuddy CLI 的驱动实现。
type driver struct {
	command string
	// discoverRoot 是会话索引目录，空表示用 ~/.codebuddy/sessions。
	discoverRoot string
}

// Name 返回驱动名。
func (d *driver) Name() string { return Name }

// Start 启动一轮任务，把 prompt 写进 stdin。
func (d *driver) Start(ctx context.Context, opts agent.Options, prompt string) (agent.Task, error) {
	if opts.PermissionMode == "" {
		opts.PermissionMode = "bypassPermissions"
	}

	args := []string{
		"-p",
		// 用普通 CLI agent 启动：首行会写带 meta 的 session-meta，
		// CodeBuddy IDE 的历史面板才会把这段对话当成正常会话列出来。
		"--agent", "cli",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--permission-mode", opts.PermissionMode,
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.Resume != "" {
		args = append(args, "--resume", opts.Resume)
	}
	if opts.MaxTurns > 0 {
		args = append(args, "--max-turns", fmt.Sprint(opts.MaxTurns))
	}
	if opts.AppendSystemPrompt != "" {
		args = append(args, "--append-system-prompt", opts.AppendSystemPrompt)
	}

	runCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(runCtx, d.command, args...)
	cmd.Dir = opts.Dir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	var stderr strings.Builder
	cmd.Stderr = &lockedWriter{w: &stderr}

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("启动 %s 失败: %w", d.command, err)
	}

	t := &task{cmd: cmd, stdin: stdin, cancel: cancel, done: make(chan struct{})}

	payload := map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": []map[string]string{{"type": "text", "text": prompt}},
		},
	}
	data, _ := json.Marshal(payload)
	if _, err := stdin.Write(append(data, '\n')); err != nil {
		t.Stop()
		return nil, fmt.Errorf("写入 prompt 失败: %w", err)
	}

	go t.read(stdout, opts.OnEvent, &stderr)
	return t, nil
}

// task 是一轮正在执行的 CodeBuddy 任务。
type task struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	cancel context.CancelFunc

	mu        sync.Mutex
	sessionID string
	result    string
	isError   bool
	texts     []string
	tools     []string
	done      chan struct{}
	waitErr   error
}

// SessionID 返回 codebuddy 会话 ID（init 事件后才有值）。
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

// Tools 返回本轮调用过的工具名（按调用顺序，可能重复）。
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

// Wait 等待任务结束（进程退出或 result 事件后关闭 stdin）。
func (t *task) Wait() error {
	<-t.done
	return t.waitErr
}

// Done 返回结束信号。
func (t *task) Done() <-chan struct{} { return t.done }

// Stop 中断任务。
func (t *task) Stop() {
	t.cancel()
	_ = t.stdin.Close()
}

// read 逐行解析 stream-json 事件。
func (t *task) read(stdout io.Reader, onEvent func(agent.Event), stderr *strings.Builder) {
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

	// 事件读完后收尾：关闭 stdin 让进程自然退出
	_ = t.stdin.Close()
	waitErr := t.cmd.Wait()
	if waitErr != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			t.mu.Lock()
			if t.result == "" {
				t.result = msg
				t.isError = true
			}
			t.mu.Unlock()
		}
		t.waitErr = waitErr
	}
}

func (t *task) handleLine(line []byte, onEvent func(agent.Event)) {
	line = trimSpaceBytes(line)
	if len(line) == 0 || line[0] != '{' {
		return
	}
	var raw struct {
		Type       string `json:"type"`
		Subtype    string `json:"subtype"`
		SessionID  string `json:"session_id"`
		Result     string `json:"result"`
		IsError    bool   `json:"is_error"`
		DurationMS int    `json:"duration_ms"`
		NumTurns   int    `json:"num_turns"`
		Message    *struct {
			// content 可能是块数组，也可能直接是一段字符串，用 RawMessage
			// 接住再按形状解析，免得一种形状解析失败把整行事件丢掉。
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &raw); err != nil {
		return
	}

	if raw.SessionID != "" {
		t.mu.Lock()
		if t.sessionID == "" {
			t.sessionID = raw.SessionID
		}
		t.mu.Unlock()
	}

	emit := func(e agent.Event) {
		if onEvent != nil {
			onEvent(e)
		}
	}

	switch raw.Type {
	case "system":
		if raw.Subtype == "init" {
			emit(agent.Event{Kind: "system", SessionID: raw.SessionID, Text: "会话就绪"})
		}
	case "assistant":
		if raw.Message == nil {
			return
		}
		for _, block := range contentBlocks(raw.Message.Content) {
			switch block.Type {
			case "text":
				if strings.TrimSpace(block.Text) == "" {
					continue
				}
				t.mu.Lock()
				t.texts = append(t.texts, block.Text)
				t.mu.Unlock()
				emit(agent.Event{Kind: "text", Text: block.Text})
			case "thinking":
				emit(agent.Event{Kind: "thinking", Text: block.Thinking})
			case "tool_use":
				label := describeTool(block.Name, block.Input)
				t.mu.Lock()
				t.tools = append(t.tools, label)
				t.mu.Unlock()
				emit(agent.Event{Kind: "tool_use", Tool: block.Name, Input: string(block.Input)})
			}
		}
	case "user":
		// 团队模式下成员发来的消息会以 user 事件回流到主进程，正文包在
		// <teammate-message teammate_id="..." summary="..."> 里。
		// 这些内容默认只留在主会话上下文里，QQ 端看不到，这里拆出来单独上报。
		if raw.Message == nil {
			return
		}
		for _, block := range contentBlocks(raw.Message.Content) {
			if block.Type != "text" && block.Type != "input_text" {
				continue
			}
			for _, e := range agent.ParseTeammateMessages(block.Text) {
				emit(e)
			}
		}
	case "result":
		t.mu.Lock()
		t.result = raw.Result
		t.isError = raw.IsError
		t.mu.Unlock()
		emit(agent.Event{
			Kind:      "result",
			Text:      raw.Result,
			SessionID: raw.SessionID,
			Duration:  time.Duration(raw.DurationMS) * time.Millisecond,
			Turns:     raw.NumTurns,
			IsError:   raw.IsError,
		})
		// 一轮结束：关掉 stdin 让进程退出。-p 模式下进程会一直等 stdin 的下一条消息，
		// 不关就会卡在这里读不到 EOF。
		_ = t.stdin.Close()
	}
}

// contentBlock 是 message.content 里的一个内容块。
type contentBlock struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	Thinking string          `json:"thinking"`
	Name     string          `json:"name"`
	Input    json.RawMessage `json:"input"`
}

// contentBlocks 兼容 content 是块数组或纯字符串两种形状。
func contentBlocks(raw json.RawMessage) []contentBlock {
	if len(raw) == 0 {
		return nil
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		return blocks
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []contentBlock{{Type: "text", Text: s}}
	}
	return nil
}

// describeTool 把工具调用压成一行人话，用于过程回传。
func describeTool(name string, input json.RawMessage) string {
	var fields map[string]any
	_ = json.Unmarshal(input, &fields)
	text := func(key string) string {
		if v, ok := fields[key].(string); ok {
			return strings.TrimSpace(v)
		}
		return ""
	}
	switch name {
	case "Bash":
		if cmd := text("command"); cmd != "" {
			return "Bash: " + oneLine(cmd, 80)
		}
	case "Read", "Write", "Edit", "NotebookEdit":
		if p := text("file_path"); p != "" {
			return name + ": " + p
		}
	case "Glob", "Grep":
		if p := text("pattern"); p != "" {
			return name + ": " + oneLine(p, 60)
		}
	case "WebFetch", "WebSearch":
		if u := text("url"); u != "" {
			return name + ": " + oneLine(u, 60)
		}
		if q := text("query"); q != "" {
			return name + ": " + oneLine(q, 60)
		}
	case "ViewImage":
		if p := text("image_path"); p != "" {
			return "ViewImage: " + p
		}
	}
	return name
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

func trimSpaceBytes(b []byte) []byte {
	start, end := 0, len(b)
	for start < end && (b[start] == ' ' || b[start] == '\t' || b[start] == '\r' || b[start] == '\n') {
		start++
	}
	for end > start && (b[end-1] == ' ' || b[end-1] == '\t' || b[end-1] == '\r' || b[end-1] == '\n') {
		end--
	}
	return b[start:end]
}

// lockedWriter 让 stderr 写入不与其他 goroutine 竞争。
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
