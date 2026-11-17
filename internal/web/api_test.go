package web

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"qqbot_bridge/internal/agent/codebuddy"
	"qqbot_bridge/internal/config"
	"qqbot_bridge/internal/msglog"
)

func bindOwner(t *testing.T, store *config.Store) {
	t.Helper()
	if err := store.Update(func(c *config.Config) error {
		c.Bind.AppID, c.Bind.Secret, c.Bind.UserOpenID = "app1", "sec", "OWNER1"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestMessagesAPI(t *testing.T) {
	srv, store := newTestServer(t)
	bindOwner(t, store)
	srv.msgs.Add(msglog.Entry{From: "OWNER1", Status: msglog.StatusAccepted, Kind: msglog.KindText, Content: "主人的消息"})
	srv.msgs.Add(msglog.Entry{From: "STRANGER", Name: "路人", Status: msglog.StatusDenied, Kind: msglog.KindText, Content: "我能用吗"})

	var got messagesResponse
	rec := get(t, srv, "/api/messages")
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 2 || got.Messages[0].From != "STRANGER" {
		t.Fatalf("应按时间倒序返回两条：%+v", got.Messages)
	}
	if got.Messages[0].Allowed || !got.Messages[1].Allowed || !got.Messages[1].Owner {
		t.Fatalf("allowed / owner 标记不对：%+v", got.Messages)
	}

	rec = get(t, srv, "/api/messages?status=denied")
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.Messages) != 1 || got.Messages[0].Name != "路人" {
		t.Fatalf("状态过滤不对：%+v", got.Messages)
	}
	if rec := get(t, srv, "/api/messages?status=bogus"); rec.Code != 400 {
		t.Fatalf("非法 status 应 400：%d", rec.Code)
	}
}

func TestMessagesPagingHasMore(t *testing.T) {
	srv, _ := newTestServer(t)
	for i := 0; i < 5; i++ {
		srv.msgs.Add(msglog.Entry{From: "u", Status: msglog.StatusAccepted, Kind: msglog.KindText, Content: fmt.Sprint(i)})
	}
	var got messagesResponse
	_ = json.Unmarshal(get(t, srv, "/api/messages?limit=3").Body.Bytes(), &got)
	if len(got.Messages) != 3 || !got.HasMore {
		t.Fatalf("应返回 3 条且还有更多：%+v", got)
	}
	_ = json.Unmarshal(get(t, srv, fmt.Sprintf("/api/messages?limit=3&before=%d", got.Messages[2].ID)).Body.Bytes(), &got)
	if len(got.Messages) != 2 || got.HasMore {
		t.Fatalf("第二页应只剩 2 条：%+v", got)
	}
}

func TestWhitelistAddRemove(t *testing.T) {
	srv, store := newTestServer(t)
	bindOwner(t, store)
	c := loginCookie(t, srv)

	rec := doJSON(srv, "POST", "/api/whitelist", `{"action":"add","openId":"FRIEND01"}`, c)
	if rec.Code != 200 {
		t.Fatalf("加入白名单失败：%d %s", rec.Code, rec.Body.String())
	}
	if !store.Get().Bind.Allowed("FRIEND01") {
		t.Fatal("加入后应在白名单里")
	}
	var w whitelistResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &w)
	if w.Owner.OpenID != "OWNER1" || len(w.Users) != 1 || w.Users[0].OpenID != "FRIEND01" {
		t.Fatalf("返回的名单不对：%+v", w)
	}

	rec = doJSON(srv, "POST", "/api/whitelist", `{"action":"remove","openId":"FRIEND01"}`, c)
	if rec.Code != 200 || store.Get().Bind.Allowed("FRIEND01") {
		t.Fatalf("移除失败：%d", rec.Code)
	}
}

func TestWhitelistValidation(t *testing.T) {
	srv, store := newTestServer(t)
	bindOwner(t, store)
	c := loginCookie(t, srv)
	cases := map[string]string{
		"绑定者不能被移除":     `{"action":"remove","openId":"OWNER1"}`,
		"绑定者不用加入":      `{"action":"add","openId":"OWNER1"}`,
		"openid 含非法字符": `{"action":"add","openId":"a b;c<d>"}`,
		"openid 为空":    `{"action":"add","openId":""}`,
		"未知动作":         `{"action":"drop","openId":"X1"}`,
		"不是 JSON":      `not json`,
	}
	for name, body := range cases {
		if rec := doJSON(srv, "POST", "/api/whitelist", body, c); rec.Code != 400 {
			t.Errorf("%s：应返回 400，实际 %d", name, rec.Code)
		}
	}
	if len(store.Get().Bind.Whitelist) != 0 {
		t.Fatalf("非法请求不应改动名单：%v", store.Get().Bind.Whitelist)
	}
}

func TestWhitelistSeenFromDenied(t *testing.T) {
	srv, store := newTestServer(t)
	bindOwner(t, store)
	srv.msgs.Add(msglog.Entry{From: "STRANGER", Name: "路人", Status: msglog.StatusDenied, Kind: msglog.KindText, Content: "a"})
	srv.msgs.Add(msglog.Entry{From: "STRANGER", Status: msglog.StatusDenied, Kind: msglog.KindText, Content: "b"})
	srv.msgs.Add(msglog.Entry{From: "OWNER1", Status: msglog.StatusAccepted, Kind: msglog.KindText, Content: "c"})

	var w whitelistResponse
	_ = json.Unmarshal(get(t, srv, "/api/whitelist").Body.Bytes(), &w)
	if len(w.Seen) != 1 || w.Seen[0].OpenID != "STRANGER" || w.Seen[0].Count != 2 || w.Seen[0].Name != "路人" {
		t.Fatalf("应汇总出被拒绝的用户：%+v", w.Seen)
	}
	// 放行后不再出现在「被拒绝」列表
	_ = store.SetAllowed("STRANGER", true)
	_ = json.Unmarshal(get(t, srv, "/api/whitelist").Body.Bytes(), &w)
	if len(w.Seen) != 0 || len(w.Users) != 1 {
		t.Fatalf("放行后列表不对：%+v", w)
	}
}

// writeTranscript 在临时 HOME 下写一个 CodeBuddy 会话文件。
func writeTranscript(t *testing.T, dir, sid string, lines []string) {
	t.Helper()
	path := codebuddy.TranscriptPath(dir, sid)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func sampleLines(n int) []string {
	lines := []string{
		`{"type":"session-meta","meta":{}}`,
		`{"type":"message","role":"user","timestamp":1000,"content":[{"type":"input_text","text":"你好"}]}`,
		`{"type":"reasoning","timestamp":1001,"rawContent":[{"type":"reasoning_text","text":"想一想"}]}`,
		`{"type":"function_call","timestamp":1002,"name":"Bash","callId":"c1","arguments":"{\"command\":\"ls\"}"}`,
		`{"type":"function_call_result","timestamp":1003,"name":"Bash","callId":"c1","output":{"text":"a.txt"}}`,
		`{"type":"message","role":"assistant","timestamp":1004,"content":[{"type":"output_text","text":"完成"}]}`,
		`{"type":"ai-title","aiTitle":"测试标题"}`,
		`not json at all`,
	}
	for i := 0; i < n; i++ {
		lines = append(lines, fmt.Sprintf(`{"type":"message","role":"user","timestamp":%d,"content":[{"type":"input_text","text":"第%d条"}]}`, 2000+i, i))
	}
	return lines
}

func TestSessionDetailAndLog(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv, store := newTestServer(t)
	dir := t.TempDir()
	if err := store.Update(func(c *config.Config) error {
		c.Sessions = []config.Session{{ID: "s1", Name: "示例", Dir: dir, AgentID: "sid-1", CreatedAt: time.Now()}}
		c.Current = "s1"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	writeTranscript(t, dir, "sid-1", sampleLines(0))

	var d sessionDetail
	rec := get(t, srv, "/api/sessions/s1")
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatalf("%v: %s", err, rec.Body.String())
	}
	if d.Name != "示例" || !d.Current || !d.Transcript.Exists || d.Transcript.Title != "测试标题" {
		t.Fatalf("会话详情不对：%+v", d)
	}
	if d.Transcript.Messages != 2 || d.Transcript.Tools != 1 || d.Transcript.Total != 5 {
		t.Fatalf("统计不对：%+v", d.Transcript)
	}

	var lg logResponse
	_ = json.Unmarshal(get(t, srv, "/api/sessions/s1/log").Body.Bytes(), &lg)
	if len(lg.Items) != 5 || lg.Total != 5 {
		t.Fatalf("日志条数不对：%+v", lg)
	}
	kinds := []string{}
	for _, it := range lg.Items {
		kinds = append(kinds, it.Role+"/"+it.Kind)
	}
	want := "user/text assistant/thinking assistant/tool tool/result assistant/text"
	if strings.Join(kinds, " ") != want {
		t.Fatalf("日志类型顺序不对：%v", kinds)
	}
	if lg.Items[2].Name != "Bash" || lg.Items[3].Text != "a.txt" {
		t.Fatalf("工具信息不对：%+v", lg.Items)
	}

	if rec := get(t, srv, "/api/sessions/nope"); rec.Code != 404 {
		t.Fatalf("不存在的会话应 404：%d", rec.Code)
	}
	if rec := get(t, srv, "/api/sessions/nope/log"); rec.Code != 404 {
		t.Fatalf("不存在的会话日志应 404：%d", rec.Code)
	}
}

func TestSessionLogPaging(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv, store := newTestServer(t)
	dir := t.TempDir()
	_ = store.Update(func(c *config.Config) error {
		c.Sessions = []config.Session{{ID: "s1", Dir: dir, AgentID: "sid-2"}}
		c.Current = "s1"
		return nil
	})
	writeTranscript(t, dir, "sid-2", sampleLines(20)) // 共 25 项，idx 0..24

	var page logResponse
	_ = json.Unmarshal(get(t, srv, "/api/sessions/s1/log?limit=10").Body.Bytes(), &page)
	if len(page.Items) != 10 || page.Items[0].Idx != 15 || !page.HasMore || page.Total != 25 {
		t.Fatalf("默认应返回最后 10 项：%+v", page)
	}
	var older logResponse
	_ = json.Unmarshal(get(t, srv, "/api/sessions/s1/log?before=15&limit=10").Body.Bytes(), &older)
	if len(older.Items) != 10 || older.Items[0].Idx != 5 || older.Items[9].Idx != 14 {
		t.Fatalf("向前翻页不对：%+v", older)
	}
	var fresh logResponse
	_ = json.Unmarshal(get(t, srv, "/api/sessions/s1/log?after=23").Body.Bytes(), &fresh)
	if len(fresh.Items) != 2 || fresh.Items[0].Idx != 23 {
		t.Fatalf("轮询新增不对：%+v", fresh)
	}

	// 文件追加后，缓存应感知变化
	f, _ := os.OpenFile(codebuddy.TranscriptPath(dir, "sid-2"), os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString(`{"type":"message","role":"assistant","timestamp":9999,"content":[{"type":"output_text","text":"新增"}]}` + "\n")
	_ = f.Close()
	_ = json.Unmarshal(get(t, srv, "/api/sessions/s1/log?after=25").Body.Bytes(), &fresh)
	if len(fresh.Items) != 1 || fresh.Items[0].Text != "新增" || fresh.Total != 26 {
		t.Fatalf("追加内容应被读到：%+v", fresh)
	}
}

func TestSessionWithoutTranscript(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv, store := newTestServer(t)
	_ = store.Update(func(c *config.Config) error {
		c.Sessions = []config.Session{{ID: "s1", Dir: t.TempDir()}, {ID: "s2", Dir: t.TempDir(), AgentID: "missing"}}
		c.Current = "s1"
		return nil
	})
	for _, id := range []string{"s1", "s2"} {
		var lg logResponse
		rec := get(t, srv, "/api/sessions/"+id+"/log")
		if rec.Code != 200 {
			t.Fatalf("%s 应返回 200 并带提示：%d", id, rec.Code)
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &lg)
		if lg.Note == "" || len(lg.Items) != 0 {
			t.Fatalf("%s 应给出说明而不是报错：%+v", id, lg)
		}
	}
}

func TestTranscriptTruncatesHugeText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	big := strings.Repeat("字", maxItemText*3)
	line := fmt.Sprintf(`{"type":"function_call_result","name":"Bash","output":{"text":%q}}`, big)
	if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tr, err := parseTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := len([]rune(tr.items[0].Text)); got > maxItemText+40 {
		t.Fatalf("超长输出应被截断，实际 %d 字", got)
	}
}

func TestMessagesShowTheirConversation(t *testing.T) {
	srv, store := newTestServer(t)
	bindOwner(t, store)
	_ = store.Update(func(c *config.Config) error {
		c.Sessions = []config.Session{{ID: "pay", Name: "支付项目（新名字）", Dir: "/work/pay"}}
		c.Current = "pay"
		return nil
	})
	srv.msgs.Add(msglog.Entry{From: "OWNER1", Status: msglog.StatusAccepted, Kind: msglog.KindText, Content: "仍存在的会话",
		Session: "pay", SessionName: "支付项目", SessionDir: "/work/pay", Conversation: "conv-1"})
	srv.msgs.Add(msglog.Entry{From: "OWNER1", Status: msglog.StatusAccepted, Kind: msglog.KindText, Content: "已删除的会话",
		Session: "gone", SessionName: "旧项目", SessionDir: "/work/old"})
	srv.msgs.Add(msglog.Entry{From: "STRANGER", Status: msglog.StatusDenied, Kind: msglog.KindText, Content: "被拒绝"})

	var got messagesResponse
	_ = json.Unmarshal(get(t, srv, "/api/messages").Body.Bytes(), &got)
	if len(got.Messages) != 3 {
		t.Fatalf("应返回 3 条：%+v", got.Messages)
	}
	denied, gone, alive := got.Messages[0], got.Messages[1], got.Messages[2]
	if !alive.SessionExists || alive.SessionNow != "支付项目（新名字）" || alive.SessionName != "支付项目" || alive.Conversation != "conv-1" {
		t.Errorf("在的会话：应同时给出当时名称和现在的名称：%+v", alive)
	}
	if gone.SessionExists || gone.SessionNow != "" || gone.SessionName != "旧项目" {
		t.Errorf("已删除的会话：应标记不存在，但保留当时的名称：%+v", gone)
	}
	if denied.Session != "" || denied.SessionExists {
		t.Errorf("被拒绝的不应有对话：%+v", denied)
	}

	_ = json.Unmarshal(get(t, srv, "/api/messages?session=pay").Body.Bytes(), &got)
	if len(got.Messages) != 1 || got.Messages[0].Content != "仍存在的会话" {
		t.Fatalf("按会话筛选不对：%+v", got.Messages)
	}
}

func TestStateIncludesBotName(t *testing.T) {
	srv, store := newTestServer(t)
	bindOwner(t, store)

	var st stateView
	_ = json.Unmarshal(get(t, srv, "/api/state").Body.Bytes(), &st)
	if st.BotName != "" {
		t.Fatalf("没有设置昵称来源时应为空：%q", st.BotName)
	}

	name := ""
	srv.SetBotName(func() string { return name })
	_ = json.Unmarshal(get(t, srv, "/api/state").Body.Bytes(), &st)
	if st.BotName != "" || st.AppID != "app1" {
		t.Fatalf("网关还没登录成功时昵称为空，其他字段不受影响：%+v", st)
	}

	name = "  codebuddy-bridge \n"
	_ = json.Unmarshal(get(t, srv, "/api/state").Body.Bytes(), &st)
	if st.BotName != "codebuddy-bridge" {
		t.Fatalf("应带上去掉空白的昵称：%q", st.BotName)
	}
}
