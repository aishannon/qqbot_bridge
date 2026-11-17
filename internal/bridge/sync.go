package bridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"qqbot_bridge/internal/agent"
	"qqbot_bridge/internal/config"
)

// ideSyncInterval 是任务执行期间把对话增量同步到 IDE 的间隔。
const ideSyncInterval = 10 * time.Second

// TranscriptPath 返回会话的 transcript 路径（文件和目录由该会话的 agent 决定）。
//
// 驱动没有这种文件时返回空串，调用方按「没有对话记录」处理，不要报错。
func (b *Bridge) TranscriptPath(sess config.Session, sessionID string) string {
	driver, err := b.driverForSession(sess)
	if err != nil {
		return ""
	}
	provider, ok := driver.(agent.TranscriptProvider)
	if !ok {
		return ""
	}
	path, ok := provider.TranscriptPath(sess.Dir, sessionID)
	if !ok {
		return ""
	}
	return path
}

// canMirror 表示这个会话的驱动能不能把对话写进 IDE 历史库（/sync 是否有意义）。
// 驱动解析失败时按「能」处理，保持帮助里原有的 /sync 入口。
func (b *Bridge) canMirror(sess config.Session) bool {
	driver, err := b.driverForSession(sess)
	if err != nil {
		return true
	}
	_, ok := driver.(agent.HistoryMirror)
	return ok
}

// syncCurrentSession 手动把当前会话镜像到 IDE 的历史对话。
func (b *Bridge) syncCurrentSession() string {
	cfg := b.cfg.Get()
	cur, ok := currentSession(cfg)
	if !ok {
		return "当前没有可用会话，发送 `/new <目录>` 新建一个。"
	}
	driver, err := b.driverForSession(cur)
	if err != nil {
		return "**同步失败**：" + err.Error()
	}
	mirror, ok := driver.(agent.HistoryMirror)
	if !ok {
		return fmt.Sprintf("当前会话用的是 %s，没有可写入的 IDE 历史库，不需要同步。", agentTitle(driver.Name()))
	}
	if cur.AgentID == "" {
		return "当前会话还没有 " + agentTitle(driver.Name()) + " 对话记录，先发一条普通指令。"
	}
	res, err := mirror.Mirror(cur.Dir, cur.AgentID, b.TranscriptPath(cur, cur.AgentID))
	if err != nil {
		return "**同步失败**：" + err.Error()
	}
	if res.ConversationID == "" {
		return fmt.Sprintf("没找到这个目录的 IDE 历史库。先用 IDE 打开 `%s`，再发 `/sync`。", cur.Dir)
	}
	return fmt.Sprintf("**已同步到 IDE 历史**\n标题：%s\n消息：%d 条\n在 IDE 里重载窗口后，历史对话里就能看到。",
		res.Name, res.Messages)
}

// mirrorToIDE 把当前会话同步到 IDE 历史库，失败只记日志，不影响 QQ 回复。
// quiet 为 true 时不打 info 日志（任务中的周期同步用）。
func (b *Bridge) mirrorToIDE(sess config.Session, sessionID string, quiet bool) {
	driver, err := b.driverForSession(sess)
	if err != nil {
		if !quiet {
			b.logf("warn", "解析 agent 驱动失败：%v", err)
		}
		return
	}
	mirror, ok := driver.(agent.HistoryMirror)
	if !ok {
		// 该驱动没有 IDE 历史库，周期同步静默跳过
		return
	}

	b.ideMu.Lock()
	defer b.ideMu.Unlock()

	res, err := mirror.Mirror(sess.Dir, sessionID, b.TranscriptPath(sess, sessionID))
	if err != nil {
		if quiet {
			b.logf("debug", "同步到 IDE 历史失败：%v", err)
		} else {
			b.logf("warn", "同步到 IDE 历史失败：%v", err)
		}
		return
	}
	if res.ConversationID != "" && !quiet {
		b.logf("info", "已同步到 IDE 历史：%s（%d 条消息）", res.Name, res.Messages)
	}
}

// syncLoop 在任务执行期间周期性把新消息同步到 IDE 历史库。
//
// 只在 transcript 的 mtime/size 变化时才真正写盘，任务结束后由 runTask
// 做一次最终同步。
func (b *Bridge) syncLoop(stop <-chan struct{}, ctx context.Context, sess config.Session, task agent.Task) {
	// 驱动没有 IDE 历史库（比如 Cursor）就没东西可同步，别每 10 秒白 stat 一次
	driver, err := b.driverForSession(sess)
	if err != nil {
		return
	}
	if _, ok := driver.(agent.HistoryMirror); !ok {
		return
	}

	ticker := time.NewTicker(ideSyncInterval)
	defer ticker.Stop()

	var lastMod time.Time
	var lastSize int64
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		sid := sess.AgentID
		if sid == "" {
			sid = task.SessionID()
		}
		if sid == "" {
			continue
		}
		path := b.TranscriptPath(sess, sid)
		if path == "" {
			return // 当前驱动没有 transcript，别空转
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if info.ModTime().Equal(lastMod) && info.Size() == lastSize {
			continue
		}
		lastMod, lastSize = info.ModTime(), info.Size()
		b.mirrorToIDE(sess, sid, true)
	}
}

// tailTeammates 跟读会话 transcript，把团队成员的 <teammate-message> 转发到 QQ。
//
// 成员消息只写进 transcript（~/.codebuddy/projects/<dir>/<session>.jsonl），
// stdout 的 stream-json 里没有，所以只能从这里取；续聊时会先跳过已有历史。
func (b *Bridge) tailTeammates(stop <-chan struct{}, sess config.Session, task agent.Task, replier *Replier) {
	// 驱动没有 transcript 就没有团队成员消息可跟读，直接不启动轮询
	driver, err := b.driverForSession(sess)
	if err != nil {
		return
	}
	if _, ok := driver.(agent.TranscriptProvider); !ok {
		return
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	seen := make(map[string]bool) // 按「行偏移:序号」去重，同一个人发两条一样的话不会被吞
	var path, sid string
	var offset int64

	forward := func(final bool) {
		cur := sess.AgentID
		if cur == "" {
			cur = task.SessionID()
		}
		if cur == "" {
			return
		}
		if cur != sid {
			// 会话 ID 变了（新建会话第一次拿到 ID 也走这里）就重新定位文件
			sid = cur
			path = b.TranscriptPath(sess, cur)
			offset = 0
			if sess.AgentID != "" {
				// 续聊的文件里已经有历史，直接跳到末尾；新会话的文件本来就是空的
				if fi, err := os.Stat(path); err == nil {
					offset = fi.Size()
				}
			}
		}
		offset = b.forwardTeammates(path, offset, seen, replier, final)
	}

	for {
		select {
		case <-stop:
			forward(true) // 收尾把没有换行的最后一行也读掉
			return
		case <-ticker.C:
			forward(false)
		}
	}
}

// forwardTeammates 从 offset 起读 transcript 的新增行，转发成员消息并返回新 offset。
func (b *Bridge) forwardTeammates(path string, offset int64, seen map[string]bool, replier *Replier, final bool) int64 {
	if path == "" {
		return offset
	}
	f, err := os.Open(path)
	if err != nil {
		return offset
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return offset
	}
	if fi.Size() < offset { // 文件被截断或重建，从头再来
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return offset
	}

	reader := bufio.NewReaderSize(f, 1<<20)
	for {
		start := offset
		line, err := reader.ReadBytes('\n')
		if err != nil {
			// 没有换行的半行平时留到下一轮；任务收尾时才当完整一行处理，
			// 否则最后一条成员消息会永远读不到。
			if final && len(line) > 0 {
				b.forwardTeammateLine(line, start, seen, replier)
				offset += int64(len(line))
			}
			return offset
		}
		offset += int64(len(line))
		b.forwardTeammateLine(line, start, seen, replier)
	}
}

// forwardTeammateLine 把一行 transcript 里的成员消息发出去。
func (b *Bridge) forwardTeammateLine(line []byte, start int64, seen map[string]bool, replier *Replier) {
	for i, e := range teammateEventsFromTranscript(line) {
		key := fmt.Sprintf("%d:%d", start, i)
		if seen[key] {
			continue
		}
		seen[key] = true
		b.logf("info", "转发团队成员消息：%s", e.Name)
		md, plain := teammateText(e)
		replier.Rich(context.Background(), md, plain)
	}
}

// teammateEventsFromTranscript 从一行 transcript 里抽出成员消息。
// 只有 role=user 且正文含 <teammate-message> 的行才可能有内容。
func teammateEventsFromTranscript(line []byte) []agent.Event {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || line[0] != '{' || !bytes.Contains(line, []byte("teammate-message")) {
		return nil
	}
	var rec struct {
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(line, &rec); err != nil {
		return nil
	}
	if rec.Role != "user" {
		return nil
	}
	var out []agent.Event
	for _, c := range rec.Content {
		if c.Type != "text" && c.Type != "input_text" {
			continue
		}
		for _, e := range agent.ParseTeammateMessages(c.Text) {
			if strings.EqualFold(e.Name, "system") {
				continue // 框架自动通知，不是成员说的话，别往手机上推
			}
			out = append(out, e)
		}
	}
	return out
}

// teammateText 把一条团队成员消息拼成 QQ 文本：第一行是「@角色名 · 摘要」，
// 返回 Markdown 和纯文本两个版本，由 Replier.Rich 按机器人能力二选一。
func teammateText(e agent.Event) (markdown, plain string) {
	name := strings.TrimSpace(e.Name)
	if name == "" {
		name = "成员"
	}
	summary := strings.TrimSpace(e.Summary)
	body := strings.TrimSpace(e.Text)

	mdHead := "**@" + name + "**"
	if summary != "" {
		mdHead += " · " + summary
	}
	plainHead := "@" + name
	if summary != "" {
		plainHead += " · " + summary
	}

	if body == "" {
		return mdHead, plainHead
	}
	return mdHead + "\n\n" + body, plainHead + "\n" + body
}
