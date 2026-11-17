package web

import (
	"bufio"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// logItem 是会话日志里的一项：用户/助手文本、推理、工具调用或工具结果。
type logItem struct {
	Idx  int    `json:"idx"`
	Time int64  `json:"time,omitempty"` // 毫秒时间戳
	Role string `json:"role"`           // user / assistant / tool
	Kind string `json:"kind"`           // text / thinking / tool / result
	Name string `json:"name,omitempty"` // 工具名
	Text string `json:"text"`
}

// transcript 是解析后的一个会话文件。
type transcript struct {
	items []logItem
	title string
	size  int64
	mod   time.Time
}

const (
	// maxItemText 单项文本最多保留的字符数，避免超大的工具输出撑爆页面。
	maxItemText = 4000
	// maxItems 单个会话最多保留最近多少项。
	maxItems = 5000
)

// transcriptBlock 是 Cursor（以及 Anthropic 风格）message.content 里的一块内容。
type transcriptBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type transcriptRecord struct {
	Type      string `json:"type"`
	Role      string `json:"role"`
	Timestamp int64  `json:"timestamp"`
	Content   []struct {
		Text string `json:"text"`
	} `json:"content"`
	RawContent []struct {
		Text string `json:"text"`
	} `json:"rawContent"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Output    *struct {
		Text string `json:"text"`
	} `json:"output"`
	AITitle string `json:"aiTitle"`
	// Message 是 Cursor transcript 的形状：没有 type 字段，role + message.content。
	Message *struct {
		Content []transcriptBlock `json:"content"`
	} `json:"message"`
}

var (
	// cursorTimestampRe 是 Cursor 塞在用户消息前面的时间戳包装。
	cursorTimestampRe = regexp.MustCompile(`(?s)<timestamp>.*?</timestamp>`)
	// cursorQueryRe 是桥接拼进去的真实用户指令。
	cursorQueryRe = regexp.MustCompile(`(?s)<user_query>\s*(.*?)\s*</user_query>`)
)

// parseTranscript 读取 jsonl 会话文件，同时兼容两种格式：
//
//   - CodeBuddy：每行是 {"type":"message"|"reasoning"|"function_call"|…}；
//   - Cursor：每行是 {"role":"user"|"assistant","message":{"content":[…]}}，
//     结尾有 {"type":"turn_ended"} 这类控制行。
//
// 解析不了的行直接跳过。
func parseTranscript(path string) (*transcript, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}

	t := &transcript{size: fi.Size(), mod: fi.ModTime()}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 256*1024), 32<<20)
	total := 0
	appendItem := func(it *logItem) {
		it.Text = truncateRunes(it.Text, maxItemText)
		it.Idx = total
		total++
		t.items = append(t.items, *it)
		if len(t.items) > maxItems*2 { // 摊销裁剪，避免每项都搬一遍
			t.items = append([]logItem(nil), t.items[len(t.items)-maxItems:]...)
		}
	}
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec transcriptRecord
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		// Cursor：没有 type，靠 role + message 识别；CodeBuddy 的行都有 type
		if rec.Type == "" && rec.Role != "" && rec.Message != nil {
			for _, it := range cursorItems(&rec, t) {
				appendItem(&it)
			}
			continue
		}
		var it *logItem
		switch rec.Type {
		case "message":
			if rec.Role != "user" && rec.Role != "assistant" {
				continue
			}
			var sb strings.Builder
			for _, c := range rec.Content {
				sb.WriteString(c.Text)
			}
			if strings.TrimSpace(sb.String()) == "" {
				continue
			}
			it = &logItem{Role: rec.Role, Kind: "text", Text: sb.String()}
		case "reasoning":
			var sb strings.Builder
			for _, c := range rec.RawContent {
				sb.WriteString(c.Text)
			}
			if strings.TrimSpace(sb.String()) == "" {
				continue
			}
			it = &logItem{Role: "assistant", Kind: "thinking", Text: sb.String()}
		case "function_call":
			it = &logItem{Role: "assistant", Kind: "tool", Name: rec.Name, Text: rec.Arguments}
		case "function_call_result":
			text := ""
			if rec.Output != nil {
				text = rec.Output.Text
			}
			it = &logItem{Role: "tool", Kind: "result", Name: rec.Name, Text: text}
		case "ai-title":
			if rec.AITitle != "" {
				t.title = rec.AITitle
			}
			continue
		default:
			continue
		}
		it.Time = rec.Timestamp
		appendItem(it)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(t.items) > maxItems {
		t.items = append([]logItem(nil), t.items[len(t.items)-maxItems:]...)
	}
	return t, nil
}

// cursorItems 把一行 Cursor transcript 拆成日志项：
// text 块 → 文本项，tool_use 块 → 工具项（参数原样给 JSON，前端只在折叠区展示）。
//
// 用户消息带 <timestamp> 包装和 <user_query> 外壳，这里剥掉，只留用户真正说的话；
// 第一条用户指令顺带当作会话标题（Cursor 没有 CodeBuddy 那样的 ai-title）。
func cursorItems(rec *transcriptRecord, t *transcript) []logItem {
	role := "assistant"
	switch rec.Role {
	case "user":
		role = "user"
	case "assistant":
	default:
		role = "tool" // 工具结果等，保持可见但归到工具一侧
	}
	var out []logItem
	for _, b := range rec.Message.Content {
		switch b.Type {
		case "text", "input_text", "output_text":
			text := cursorMessageText(b.Text, rec.Role)
			if strings.TrimSpace(text) == "" {
				continue
			}
			if rec.Role == "user" && t.title == "" {
				t.title = shortTitle(text)
			}
			out = append(out, logItem{Role: role, Kind: "text", Text: text})
		case "tool_use":
			out = append(out, logItem{
				Role: "assistant",
				Kind: "tool",
				Name: b.Name,
				Text: strings.TrimSpace(string(b.Input)),
			})
		case "tool_result":
			out = append(out, logItem{Role: "tool", Kind: "result", Text: b.Text})
		}
	}
	return out
}

// cursorMessageText 去掉时间戳包装；用户消息优先取 <user_query> 里的原文。
func cursorMessageText(text, role string) string {
	if text == "" {
		return ""
	}
	if role == "user" {
		if m := cursorQueryRe.FindStringSubmatch(text); m != nil {
			return strings.TrimSpace(m[1])
		}
		// 没有 <user_query>（比如用户在 IDE 里自己发的消息）就至少去掉时间戳
		return strings.TrimSpace(cursorTimestampRe.ReplaceAllString(text, ""))
	}
	return text
}

// shortTitle 把一段文本压成一行短标题。
func shortTitle(s string) string {
	line := strings.Join(strings.Fields(s), " ")
	if r := []rune(line); len(r) > 60 {
		return string(r[:60]) + "…"
	}
	return line
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "\n…（已截断，共 " + itoa(len(r)) + " 字）"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// transcriptCache 按文件大小和修改时间缓存解析结果：页面每几秒轮询一次，
// 文件没变就不必重新解析几十 MB 的 jsonl。
type transcriptCache struct {
	mu      sync.Mutex
	entries map[string]*transcript
}

const maxCachedTranscripts = 8

func (c *transcriptCache) get(path string) (*transcript, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if e, ok := c.entries[path]; ok && e.size == fi.Size() && e.mod.Equal(fi.ModTime()) {
		c.mu.Unlock()
		return e, nil
	}
	c.mu.Unlock()

	t, err := parseTranscript(path)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]*transcript{}
	}
	if len(c.entries) >= maxCachedTranscripts {
		for k := range c.entries { // 随便淘汰一个，容量很小
			delete(c.entries, k)
			break
		}
	}
	c.entries[path] = t
	return t, nil
}

// slice 取日志的一个窗口。
//
//	after >= 0：返回 idx >= after 的项（轮询新增）
//	before >= 0：返回 idx < before 的最后 limit 项（向前翻页）
//	都没给：返回最后 limit 项
func (t *transcript) slice(after, before, limit int) (items []logItem, hasMore bool) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	n := len(t.items)
	if n == 0 {
		return nil, false
	}
	first := t.items[0].Idx
	// items[i].Idx == first + i
	pos := func(idx int) int {
		p := idx - first
		if p < 0 {
			return 0
		}
		if p > n {
			return n
		}
		return p
	}
	var lo, hi int
	switch {
	case after >= 0:
		lo, hi = pos(after), n
		if hi-lo > limit {
			lo = hi - limit
		}
	case before >= 0:
		hi = pos(before)
		lo = hi - limit
		if lo < 0 {
			lo = 0
		}
	default:
		hi = n
		lo = n - limit
		if lo < 0 {
			lo = 0
		}
	}
	return t.items[lo:hi], lo > 0
}

// counts 统计各类项目的数量。
func (t *transcript) counts() (messages, tools int) {
	for _, it := range t.items {
		switch it.Kind {
		case "text":
			messages++
		case "tool":
			tools++
		}
	}
	return
}
