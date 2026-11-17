// Package msglog 记录机器人收到的每一条 QQ 消息，供 Web 管理端查看。
//
// 内存里留最近 N 条，同时按天追加到 JSONL 文件（权限 0600，内容可能含隐私），
// 重启后会从最近的文件里恢复，超过保留天数的旧文件在启动时清掉。
package msglog

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// 处理结果。
const (
	StatusAccepted  = "accepted"  // 通过白名单，已交给指令或任务
	StatusDenied    = "denied"    // 不在白名单，被拒绝
	StatusDuplicate = "duplicate" // 平台重复推送，已丢弃
)

// 消息形态。
const (
	KindText       = "text"
	KindVoice      = "voice"
	KindAttachment = "attachment"
)

const (
	// maxContent 单条内容最多保留的字符数。
	maxContent = 4000
	// RetentionDays 日志文件保留天数。
	RetentionDays = 30
)

// Entry 是一条收到的消息。
type Entry struct {
	ID          int64     `json:"id"`
	Time        time.Time `json:"time"`
	Group       string    `json:"group,omitempty"` // 群 openid；为空表示单聊
	From        string    `json:"from"`            // 发送者 openid
	Name        string    `json:"name,omitempty"`  // 发送者昵称（当时看到的）
	Status      string    `json:"status"`
	Kind        string    `json:"kind"`
	Content     string    `json:"content"`
	Attachments int       `json:"attachments,omitempty"`
	MsgID       string    `json:"msgId,omitempty"`

	// 这条消息属于哪个对话：收到那一刻的当前会话。被拒绝的消息不属于任何对话，这几项为空。
	// 名称和目录是当时的快照，之后会话改名或删除也不影响这条记录。
	Session      string `json:"session,omitempty"`      // 会话 ID（sessions.yaml 里的 id）
	SessionName  string `json:"sessionName,omitempty"`  // 会话名称
	SessionDir   string `json:"sessionDir,omitempty"`   // 会话工作目录
	Conversation string `json:"conversation,omitempty"` // CodeBuddy 对话 ID；新对话的第一条消息为空
	Command      bool   `json:"command,omitempty"`      // 是 /xxx 机器人指令，而不是交给 CodeBuddy 的任务
}

// Filter 是查询条件。
type Filter struct {
	Status   string // 为空不过滤
	Session  string // 会话 ID，为空不过滤
	Keyword  string // 在内容、openid、昵称里做不区分大小写的子串匹配
	BeforeID int64  // 大于 0 时只返回 ID 更小的（翻页）
	Limit    int    // 默认 100，最多 500
}

// Log 是消息日志。
type Log struct {
	dir string
	max int
	now func() time.Time

	mu     sync.Mutex
	items  []Entry // 按 ID 升序
	nextID int64
	file   *os.File
	day    string
	warned bool
}

// New 创建消息日志。dir 为空时只保留内存记录。
func New(dir string, max int) *Log {
	if max <= 0 {
		max = 1000
	}
	l := &Log{dir: dir, max: max, now: time.Now, nextID: 1}
	if dir != "" {
		_ = os.MkdirAll(dir, 0o700)
		l.cleanup()
		l.load()
	}
	return l
}

// Add 记录一条消息并返回带 ID 的记录。
func (l *Log) Add(e Entry) Entry {
	e.Content = truncate(e.Content, maxContent)
	if e.Time.IsZero() {
		e.Time = l.now()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	e.ID = l.nextID
	l.nextID++
	l.items = append(l.items, e)
	if len(l.items) > l.max {
		l.items = l.items[len(l.items)-l.max:]
	}
	l.writeLocked(e)
	return e
}

// Query 按条件查询，结果按时间倒序（最新在前）。
func (l *Log) Query(f Filter) []Entry {
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	kw := strings.ToLower(strings.TrimSpace(f.Keyword))

	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Entry, 0, limit)
	for i := len(l.items) - 1; i >= 0 && len(out) < limit; i-- {
		e := l.items[i]
		if f.BeforeID > 0 && e.ID >= f.BeforeID {
			continue
		}
		if f.Status != "" && e.Status != f.Status {
			continue
		}
		if f.Session != "" && e.Session != f.Session {
			continue
		}
		if kw != "" && !match(e, kw) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// Close 关闭当前日志文件。
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

func match(e Entry, kw string) bool {
	return strings.Contains(strings.ToLower(e.Content), kw) ||
		strings.Contains(strings.ToLower(e.From), kw) ||
		strings.Contains(strings.ToLower(e.Name), kw) ||
		strings.Contains(strings.ToLower(e.Group), kw) ||
		strings.Contains(strings.ToLower(e.SessionName), kw) ||
		strings.Contains(strings.ToLower(e.SessionDir), kw)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// writeLocked 追加写当天文件。落盘只是尽力而为，失败只提醒一次，不影响消息处理。
func (l *Log) writeLocked(e Entry) {
	if l.dir == "" {
		return
	}
	day := e.Time.Format("2006-01-02")
	if l.file == nil || l.day != day {
		if l.file != nil {
			_ = l.file.Close()
			l.file = nil
		}
		f, err := os.OpenFile(filepath.Join(l.dir, day+".jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			l.warn(err)
			return
		}
		l.file, l.day = f, day
	}
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	if _, err := l.file.Write(append(data, '\n')); err != nil {
		l.warn(err)
	}
}

func (l *Log) warn(err error) {
	if !l.warned {
		l.warned = true
		os.Stderr.WriteString("消息日志落盘失败：" + err.Error() + "\n")
	}
}

// files 返回日志文件，按日期升序。
func (l *Log) files() []string {
	matches, _ := filepath.Glob(filepath.Join(l.dir, "*.jsonl"))
	sort.Strings(matches)
	return matches
}

// load 从最近的文件恢复内存记录，并接上 ID 序号。
func (l *Log) load() {
	files := l.files()
	var all []Entry
	for i := len(files) - 1; i >= 0 && len(all) < l.max; i-- {
		all = append(readFile(files[i]), all...)
	}
	if len(all) > l.max {
		all = all[len(all)-l.max:]
	}
	// 文件里的 ID 可能因为重启而重复，恢复时重新按顺序编号
	for i := range all {
		all[i].ID = int64(i + 1)
	}
	l.items = all
	l.nextID = int64(len(all)) + 1
}

func readFile(path string) []Entry {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.Status != "" {
			out = append(out, e)
		}
	}
	return out
}

// cleanup 删除超过保留天数的日志文件。
func (l *Log) cleanup() {
	cutoff := l.now().AddDate(0, 0, -RetentionDays).Format("2006-01-02")
	for _, p := range l.files() {
		day := strings.TrimSuffix(filepath.Base(p), ".jsonl")
		if day < cutoff {
			_ = os.Remove(p)
		}
	}
}
