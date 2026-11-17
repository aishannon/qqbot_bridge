package web

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"qqbot_bridge/internal/bridge"
	"qqbot_bridge/internal/config"
	"qqbot_bridge/internal/gitinfo"
	"qqbot_bridge/internal/msglog"
)

var openIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSONStatus(w, status, map[string]string{"error": msg})
}

func queryInt(r *http.Request, key string, def int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// ---------- 收到的消息 ----------

type messageView struct {
	msglog.Entry
	Allowed bool `json:"allowed"` // 发送者现在是否在白名单里
	Owner   bool `json:"owner"`   // 发送者是否是绑定者

	// SessionExists 表示这条消息所属的会话现在还在；在的话 SessionNow 是它现在的名字
	// （可能和收到消息时不同），页面据此决定能不能点进去看。
	SessionExists bool   `json:"sessionExists"`
	SessionNow    string `json:"sessionNow,omitempty"`
}

type messagesResponse struct {
	Messages []messageView `json:"messages"`
	HasMore  bool          `json:"hasMore"`
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 100)
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	status := r.URL.Query().Get("status")
	switch status {
	case "", msglog.StatusAccepted, msglog.StatusDenied, msglog.StatusDuplicate:
	default:
		writeError(w, http.StatusBadRequest, "status 不合法")
		return
	}

	var entries []msglog.Entry
	if s.msgs != nil {
		entries = s.msgs.Query(msglog.Filter{
			Status:   status,
			Session:  r.URL.Query().Get("session"),
			Keyword:  r.URL.Query().Get("q"),
			BeforeID: int64(queryInt(r, "before", 0)),
			Limit:    limit + 1, // 多取一条判断是否还有更早的
		})
	}
	resp := messagesResponse{Messages: []messageView{}}
	if len(entries) > limit {
		resp.HasMore = true
		entries = entries[:limit]
	}
	cfg := s.cfg.Get()
	bind := cfg.Bind
	live := make(map[string]string, len(cfg.Sessions))
	for _, item := range cfg.Sessions {
		name := item.Name
		if name == "" {
			name = item.ID
		}
		live[item.ID] = name
	}
	for _, e := range entries {
		v := messageView{Entry: e, Allowed: bind.Allowed(e.From), Owner: e.From == bind.UserOpenID}
		if e.Session != "" {
			v.SessionNow, v.SessionExists = live[e.Session]
		}
		resp.Messages = append(resp.Messages, v)
	}
	writeJSON(w, resp)
}

// ---------- 白名单 ----------

type personView struct {
	OpenID string `json:"openId"`
	Name   string `json:"name,omitempty"`
}

type seenView struct {
	personView
	Count int       `json:"count"` // 最近被拒绝的次数
	Last  time.Time `json:"last"`
}

type whitelistResponse struct {
	Owner personView   `json:"owner"`
	Users []personView `json:"users"`
	Seen  []seenView   `json:"seen"` // 最近被拒绝、还不在名单里的人，方便一键放行
}

func (s *Server) whitelistSnapshot() whitelistResponse {
	bind := s.cfg.Get().Bind
	resp := whitelistResponse{
		Owner: personView{OpenID: bind.UserOpenID, Name: bind.Names[bind.UserOpenID]},
		Users: []personView{},
		Seen:  []seenView{},
	}
	for _, id := range bind.Whitelist {
		if id == "" || id == bind.UserOpenID {
			continue
		}
		resp.Users = append(resp.Users, personView{OpenID: id, Name: bind.Names[id]})
	}

	if s.msgs != nil {
		seen := map[string]*seenView{}
		for _, e := range s.msgs.Query(msglog.Filter{Status: msglog.StatusDenied, Limit: 500}) {
			if bind.Allowed(e.From) {
				continue
			}
			v := seen[e.From]
			if v == nil {
				v = &seenView{personView: personView{OpenID: e.From, Name: e.Name}, Last: e.Time}
				seen[e.From] = v
			}
			v.Count++
			if v.Name == "" {
				v.Name = e.Name
			}
		}
		for _, v := range seen {
			resp.Seen = append(resp.Seen, *v)
		}
		sort.Slice(resp.Seen, func(i, j int) bool { return resp.Seen[i].Last.After(resp.Seen[j].Last) })
	}
	return resp
}

func (s *Server) handleWhitelistGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.whitelistSnapshot())
}

func (s *Server) handleWhitelistPost(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var req struct {
		Action string `json:"action"`
		OpenID string `json:"openId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式不对")
		return
	}
	req.OpenID = strings.TrimSpace(req.OpenID)
	if !openIDRe.MatchString(req.OpenID) {
		writeError(w, http.StatusBadRequest, "openid 只能包含字母、数字、下划线和短横线，长度 1-128")
		return
	}
	bind := s.cfg.Get().Bind
	switch req.Action {
	case "add":
		if req.OpenID == bind.UserOpenID {
			writeError(w, http.StatusBadRequest, "绑定者始终可用，不需要加入白名单")
			return
		}
	case "remove":
		if req.OpenID == bind.UserOpenID {
			writeError(w, http.StatusBadRequest, "绑定者始终可用，不能移出白名单")
			return
		}
	default:
		writeError(w, http.StatusBadRequest, "action 只能是 add 或 remove")
		return
	}
	if err := s.cfg.SetAllowed(req.OpenID, req.Action == "add"); err != nil {
		writeError(w, http.StatusInternalServerError, "保存失败："+err.Error())
		return
	}
	s.logs.Add("info", "Web 管理端：%s白名单 %s", map[string]string{"add": "加入", "remove": "移出"}[req.Action], req.OpenID)
	writeJSON(w, s.whitelistSnapshot())
}

// ---------- 会话信息与日志 ----------

type gitView struct {
	Available bool   `json:"available"`
	Branch    string `json:"branch,omitempty"`
	Upstream  string `json:"upstream,omitempty"`
	Ahead     int    `json:"ahead,omitempty"`
	Behind    int    `json:"behind,omitempty"`
	Staged    int    `json:"staged,omitempty"`
	Modified  int    `json:"modified,omitempty"`
	Untracked int    `json:"untracked,omitempty"`
	Commit    string `json:"commit,omitempty"`
	Subject   string `json:"subject,omitempty"`
	Age       string `json:"age,omitempty"`
}

type transcriptView struct {
	Path     string    `json:"path"`
	Exists   bool      `json:"exists"`
	Size     int64     `json:"size,omitempty"`
	Modified time.Time `json:"modified,omitempty"`
	Title    string    `json:"title,omitempty"`
	Total    int       `json:"total"`    // 日志项总数（含已被裁剪的早期部分）
	Messages int       `json:"messages"` // 文本消息数
	Tools    int       `json:"tools"`    // 工具调用数
}

type sessionDetail struct {
	sessionView
	Model      string              `json:"model,omitempty"`
	Auto       bool                `json:"auto,omitempty"`
	LastUsed   string              `json:"lastUsed,omitempty"`
	Running    bool                `json:"running"`
	Task       bridge.TaskSnapshot `json:"task"`
	Git        gitView             `json:"git"`
	Transcript transcriptView      `json:"transcript"`
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	out := make([]sessionDetail, 0, len(cfg.Sessions))
	for _, item := range cfg.Sessions {
		out = append(out, s.sessionBrief(cfg.Current, item))
	}
	writeJSON(w, map[string]any{"sessions": out})
}

func (s *Server) handleSessionDetail(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	for _, item := range cfg.Sessions {
		if item.ID != r.PathValue("id") {
			continue
		}
		d := s.sessionBrief(cfg.Current, item)

		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if info, err := gitinfo.Read(ctx, item.Dir); err == nil {
			d.Git = gitView{
				Available: true, Branch: info.Branch, Upstream: info.Upstream,
				Ahead: info.Ahead, Behind: info.Behind, Staged: info.Staged,
				Modified: info.Modified, Untracked: info.Untracked,
				Commit: info.Commit, Subject: info.Subject, Age: info.Age,
			}
		}
		if item.AgentID != "" {
			if t, err := s.transcripts.get(s.br.TranscriptPath(item, item.AgentID)); err == nil {
				d.Transcript.Exists = true
				d.Transcript.Size = t.size
				d.Transcript.Modified = t.mod
				d.Transcript.Title = t.title
				d.Transcript.Messages, d.Transcript.Tools = t.counts()
				if n := len(t.items); n > 0 {
					d.Transcript.Total = t.items[n-1].Idx + 1
				}
			}
		}
		writeJSON(w, d)
		return
	}
	writeError(w, http.StatusNotFound, "会话不存在")
}

func (s *Server) sessionBrief(current string, item config.Session) sessionDetail {
	driver := s.cfg.Get().DriverOf(item)
	d := sessionDetail{
		sessionView: sessionView{
			ID: item.ID, Name: item.Name, Dir: item.Dir, AgentID: item.AgentID,
			Current: item.ID == current, CreatedAt: item.CreatedAt.Format("2006-01-02 15:04"),
			Driver: driver, Agent: bridge.AgentTitleFor(driver),
		},
		Model:   item.Model,
		Auto:    item.Auto,
		Running: s.br.Running(item.ID),
		Task:    s.br.Snapshot(item.ID),
	}
	if !item.LastUsed.IsZero() {
		d.LastUsed = item.LastUsed.Format("2006-01-02 15:04:05")
	}
	if item.AgentID != "" {
		d.Transcript.Path = s.br.TranscriptPath(item, item.AgentID)
	}
	return d
}

type logResponse struct {
	Items   []logItem `json:"items"`
	HasMore bool      `json:"hasMore"` // 前面还有更早的
	Total   int       `json:"total"`   // 下一项的 idx，轮询新增时作为 after
	Note    string    `json:"note,omitempty"`
}

func (s *Server) handleSessionLog(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	for _, item := range cfg.Sessions {
		if item.ID != r.PathValue("id") {
			continue
		}
		resp := logResponse{Items: []logItem{}}
		if item.AgentID == "" {
			resp.Note = "这个会话还没有 " + bridge.AgentTitleFor(cfg.DriverOf(item)) + " 对话记录，先在 QQ 里发一条指令。"
			writeJSON(w, resp)
			return
		}
		t, err := s.transcripts.get(s.br.TranscriptPath(item, item.AgentID))
		if err != nil {
			resp.Note = "读取会话记录失败：" + err.Error()
			writeJSON(w, resp)
			return
		}
		after, before := -1, -1
		if v := r.URL.Query().Get("after"); v != "" {
			after = queryInt(r, "after", -1)
		}
		if v := r.URL.Query().Get("before"); v != "" {
			before = queryInt(r, "before", -1)
		}
		items, more := t.slice(after, before, queryInt(r, "limit", 100))
		if items != nil {
			resp.Items = items
		}
		resp.HasMore = more
		if n := len(t.items); n > 0 {
			resp.Total = t.items[n-1].Idx + 1
		}
		writeJSON(w, resp)
		return
	}
	writeError(w, http.StatusNotFound, "会话不存在")
}
