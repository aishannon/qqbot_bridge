// Package genie 把桥接会话镜像到 CodeBuddy IDE 的「历史对话」存储。
//
// IDE 的历史面板读的是 CodeBuddyExtension 的 Genie 库，和 CLI 的
// ~/.codebuddy/projects/*.jsonl 不是同一套。这里按 Genie 的落盘格式
// 生成一条会话：工作区 index.json + recent.json + 每个会话的
// index.json 和 messages/*.json，从而不需要用户手动导入。
package genie

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Result 是一次镜像的结果。
type Result struct {
	ConversationID string
	Name           string
	Messages       int
	Store          string
}

// Mirror 把 cwd 下的 sessionID 会话镜像到 IDE 历史库。
// 找不到 IDE 历史库（用户没在这个目录打开过 IDE）时返回空 Result。
func Mirror(cwd, sessionID, transcript string) (Result, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Result{}, err
	}
	return mirrorAt(home, cwd, sessionID, transcript)
}

func mirrorAt(home, cwd, sessionID, transcript string) (Result, error) {
	root, err := findStore(home, cwd)
	if err != nil {
		return Result{}, err
	}
	if root == "" {
		return Result{}, nil
	}
	conv, err := parseTranscript(transcript, sessionID)
	if err != nil {
		return Result{}, err
	}
	if len(conv.Messages) == 0 {
		return Result{}, nil
	}
	if err := writeStore(root, conv); err != nil {
		return Result{}, err
	}
	return Result{
		ConversationID: conv.ID,
		Name:           conv.Name,
		Messages:       len(conv.Messages),
		Store:          root,
	}, nil
}

// findStore 找到 cwd 对应的 IDE 历史库目录；多个账号时取最近修改的那个。
func findStore(home, cwd string) (string, error) {
	sum := md5.Sum([]byte(filepath.Clean(cwd)))
	hash := hex.EncodeToString(sum[:])
	pattern := filepath.Join(home, ".local", "share", "CodeBuddyExtension", "Data",
		"*", "CodeBuddyIDE", "*", "history", hash, "index.json")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", nil
	}
	sort.Slice(matches, func(i, j int) bool {
		return modTime(matches[i]).After(modTime(matches[j]))
	})
	return filepath.Dir(matches[0]), nil
}

func modTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// ---------- 会话解析 ----------

type convMessage struct {
	Role      string
	Message   string // 内层 JSON 字符串
	ID        string
	Extra     string
	CreatedAt string
}

type convRequest struct {
	ID        string
	Type      string
	Messages  []string
	State     string
	StartedAt int64
}

type conversation struct {
	ID            string
	Name          string
	CreatedAt     time.Time
	LastMessageAt time.Time
	Requests      []convRequest
	Messages      []convMessage
}

type block struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type record struct {
	ID           string  `json:"id"`
	Timestamp    int64   `json:"timestamp"`
	Type         string  `json:"type"`
	Role         string  `json:"role"`
	Content      []block `json:"content"`
	RawContent   []block `json:"rawContent"`
	Name         string  `json:"name"`
	CallID       string  `json:"callId"`
	Arguments    string  `json:"arguments"`
	Status       string  `json:"status"`
	Output       *block  `json:"output"`
	AITitle      string  `json:"aiTitle"`
	ProviderData struct {
		ConversationRequestID string `json:"conversationRequestId"`
		StartsNewUserRequest  bool   `json:"startsNewUserRequest"`
		Model                 string `json:"model"`
		RequestModelName      string `json:"requestModelName"`
	} `json:"providerData"`
}

func parseTranscript(path, sessionID string) (conversation, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return conversation{}, err
	}

	var records []record
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var r record
		if json.Unmarshal([]byte(line), &r) != nil {
			continue
		}
		records = append(records, r)
	}

	// 先建立 callId -> 结果，便于给 tool-call 补 toolStatus
	results := make(map[string]record)
	for _, r := range records {
		if r.Type == "function_call_result" && r.CallID != "" {
			results[r.CallID] = r
		}
	}

	conv := conversation{ID: conversationID(sessionID)}
	var cur *convRequest
	first := true
	for _, r := range records {
		if r.Type == "ai-title" {
			if r.AITitle != "" {
				conv.Name = r.AITitle
			}
			continue
		}
		if r.Type != "message" && r.Type != "reasoning" && r.Type != "function_call" && r.Type != "function_call_result" {
			continue
		}

		ts := r.Timestamp
		if ts == 0 {
			ts = time.Now().UnixMilli()
		}
		if first {
			conv.CreatedAt = time.UnixMilli(ts).UTC()
			first = false
		}
		conv.LastMessageAt = time.UnixMilli(ts).UTC()

		modelID := r.ProviderData.Model
		if modelID == "" {
			modelID = "deepseek-v4.1-flash-ioa"
		}
		modelName := r.ProviderData.RequestModelName
		if modelName == "" {
			modelName = "DeepSeek-V4.1-Flash"
		}

		var role string
		var blocks []map[string]any
		var toolStatus map[string]any

		switch r.Type {
		case "message":
			if r.Role != "user" && r.Role != "assistant" {
				continue
			}
			text := blocksText(r.Content)
			if strings.TrimSpace(text) == "" {
				continue
			}
			role = r.Role
			blocks = []map[string]any{{"type": "text", "text": text}}
			if role == "user" && r.ProviderData.StartsNewUserRequest {
				cur = &convRequest{
					ID:        requestID(r.ProviderData.ConversationRequestID, sessionID, ts),
					Type:      "craft",
					State:     "complete",
					StartedAt: ts,
				}
				conv.Requests = append(conv.Requests, *cur)
			}
		case "reasoning":
			text := blocksText(r.RawContent)
			if strings.TrimSpace(text) == "" {
				continue
			}
			role = "assistant"
			blocks = []map[string]any{{"type": "reasoning", "text": text}}
		case "function_call":
			role = "assistant"
			args := parseArgs(r.Arguments)
			blocks = []map[string]any{{
				"type":       "tool-call",
				"toolCallId": r.CallID,
				"toolName":   r.Name,
				"args":       args,
			}}
			if res, ok := results[r.CallID]; ok {
				toolStatus = map[string]any{
					r.CallID: map[string]any{
						"ready":  true,
						"status": "executed",
						"args":   args,
						"result": resultObject(res),
					},
				}
			}
		case "function_call_result":
			role = "tool"
			blocks = []map[string]any{{
				"type":       "tool-result",
				"toolCallId": r.CallID,
				"toolName":   r.Name,
				"result":     resultObject(r),
			}}
		}

		if len(blocks) == 0 {
			continue
		}
		if cur == nil {
			cur = &convRequest{
				ID:        requestID(r.ProviderData.ConversationRequestID, sessionID, ts),
				Type:      "craft",
				State:     "complete",
				StartedAt: ts,
			}
			conv.Requests = append(conv.Requests, *cur)
		}

		mid := messageID(r.ID, sessionID, r.Type, ts)
		body, _ := json.Marshal(map[string]any{"role": role, "content": blocks})
		extra := map[string]any{
			"requestId":       cur.ID,
			"modelId":         modelID,
			"modelName":       modelName,
			"isHelperMessage": false,
		}
		if toolStatus != nil {
			extra["toolStatus"] = toolStatus
		}
		extraJSON, _ := json.Marshal(extra)
		conv.Messages = append(conv.Messages, convMessage{
			Role:      role,
			Message:   string(body),
			ID:        mid,
			Extra:     string(extraJSON),
			CreatedAt: isoTime(ts),
		})
		// 追加到当前请求（conv.Requests 里的元素是副本，需要回写）
		req := &conv.Requests[len(conv.Requests)-1]
		req.Messages = append(req.Messages, mid)
		cur = req
	}

	if conv.Name == "" {
		conv.Name = "QQ · " + firstUserText(records)
	}
	return conv, nil
}

func blocksText(blocks []block) string {
	var sb strings.Builder
	for _, b := range blocks {
		if b.Text != "" {
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
}

func firstUserText(records []record) string {
	for _, r := range records {
		if r.Type == "message" && r.Role == "user" {
			text := strings.TrimSpace(blocksText(r.Content))
			if text != "" {
				return truncateRunes(text, 60)
			}
		}
	}
	return "对话"
}

func resultObject(r record) map[string]any {
	text := ""
	if r.Output != nil {
		text = r.Output.Text
	}
	ok := r.Status == "completed" || r.Status == "success"
	status := "success"
	if !ok {
		status = "error"
	}
	return map[string]any{
		"status":  status,
		"success": ok,
		"result":  map[string]any{"type": "text", "text": text},
	}
}

func parseArgs(raw string) map[string]any {
	var args map[string]any
	if json.Unmarshal([]byte(raw), &args) == nil && args != nil {
		return args
	}
	return map[string]any{"raw": raw}
}

func conversationID(sessionID string) string {
	return hashHex("qqbot:" + sessionID)
}

func messageID(recordID, sessionID, typ string, ts int64) string {
	if recordID != "" {
		return hashHex(recordID)
	}
	return hashHex(fmt.Sprintf("%s:%s:%d", sessionID, typ, ts))
}

func requestID(id, sessionID string, ts int64) string {
	if id != "" {
		return id
	}
	return hashHex(fmt.Sprintf("%s:req:%d", sessionID, ts))
}

func hashHex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func isoTime(ms int64) string {
	return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z")
}

func truncateRunes(s string, n int) string {
	r := []rune(strings.Join(strings.Fields(s), " "))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

// ---------- 写入 IDE 历史库 ----------

func writeStore(root string, conv conversation) error {
	lock, err := acquireLock(root)
	if err != nil {
		return err
	}
	defer releaseLock(lock)

	if err := backupOnce(filepath.Join(root, "index.json")); err != nil {
		return err
	}
	if err := backupOnce(filepath.Join(root, "recent.json")); err != nil {
		return err
	}

	entry := map[string]any{
		"id":                conv.ID,
		"type":              "craft",
		"name":              conv.Name,
		"createdAt":         conv.CreatedAt.Format("2006-01-02T15:04:05.000Z"),
		"lastMessageAt":     conv.LastMessageAt.Format("2006-01-02T15:04:05.000Z"),
		"chatMode":          "craft",
		"selectedModelId":   "deepseek-v4.1-flash-ioa",
		"selectionRevision": 1,
	}

	// 工作区 index.json：替换同 ID 条目并放到最前
	var index map[string]any
	if err := readJSON(filepath.Join(root, "index.json"), &index); err != nil {
		return err
	}
	if index == nil {
		index = map[string]any{}
	}
	conversations, _ := index["conversations"].([]any)
	filtered := make([]any, 0, len(conversations)+1)
	filtered = append(filtered, entry)
	for _, item := range conversations {
		m, _ := item.(map[string]any)
		if m != nil && m["id"] == conv.ID {
			continue
		}
		filtered = append(filtered, item)
	}
	index["conversations"] = filtered
	if err := writeJSONAtomic(filepath.Join(root, "index.json"), index); err != nil {
		return err
	}

	// recent.json：会话 ID -> 最后消息时间
	var recent map[string]any
	if err := readJSON(filepath.Join(root, "recent.json"), &recent); err != nil {
		return err
	}
	if recent == nil {
		recent = map[string]any{}
	}
	recent[conv.ID] = conv.LastMessageAt.Format("2006-01-02T15:04:05.000Z")
	if err := writeJSONAtomic(filepath.Join(root, "recent.json"), recent); err != nil {
		return err
	}

	// 会话目录
	dir := filepath.Join(root, conv.ID)
	msgDir := filepath.Join(dir, "messages")
	if err := os.MkdirAll(msgDir, 0o755); err != nil {
		return err
	}
	keep := make(map[string]bool, len(conv.Messages))
	for _, m := range conv.Messages {
		keep[m.ID+".json"] = true
		path := filepath.Join(msgDir, m.ID+".json")
		// 消息是追加的、ID 不变，已落盘的文件不用重复写；
		// 这样长任务里每 10 秒的增量同步只需要写新消息。
		if _, err := os.Stat(path); err == nil {
			continue
		}
		file := map[string]any{
			"role":      m.Role,
			"message":   m.Message,
			"id":        m.ID,
			"extra":     m.Extra,
			"createdAt": m.CreatedAt,
		}
		if err := writeJSONAtomic(path, file); err != nil {
			return err
		}
	}
	if entries, err := os.ReadDir(msgDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") && !keep[e.Name()] {
				_ = os.Remove(filepath.Join(msgDir, e.Name()))
			}
		}
	}

	requests := make([]any, 0, len(conv.Requests))
	for _, req := range conv.Requests {
		messages := make([]any, 0, len(req.Messages))
		for _, id := range req.Messages {
			messages = append(messages, id)
		}
		requests = append(requests, map[string]any{
			"id":        req.ID,
			"type":      req.Type,
			"messages":  messages,
			"state":     req.State,
			"startedAt": req.StartedAt,
		})
	}
	messageMeta := make([]any, 0, len(conv.Messages))
	for _, m := range conv.Messages {
		messageMeta = append(messageMeta, map[string]any{
			"id":         m.ID,
			"type":       "text",
			"role":       m.Role,
			"isComplete": true,
		})
	}
	return writeJSONAtomic(filepath.Join(dir, "index.json"), map[string]any{
		"requests": requests,
		"messages": messageMeta,
	})
}

func readJSON(path string, out any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

func writeJSONAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func backupOnce(path string) error {
	bak := path + ".qqbot-bak"
	if _, err := os.Stat(bak); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	src, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return os.WriteFile(bak, src, 0o644)
}

type fileLock struct {
	path string
}

func acquireLock(root string) (*fileLock, error) {
	path := filepath.Join(root, ".qqbot-sync.lock")
	for i := 0; i < 40; i++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_, _ = fmt.Fprintf(f, "%d", os.Getpid())
			_ = f.Close()
			return &fileLock{path: path}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil, errors.New("同步锁被占用")
}

func releaseLock(l *fileLock) {
	if l != nil {
		_ = os.Remove(l.path)
	}
}
