package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"qqbot_bridge/internal/gateway"
)

// newTestAPI 起一个假的 QQ OpenAPI，统计消息请求次数。
func newTestAPI(t *testing.T, sent *int32, seqs *[]int32) *gateway.API {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/getAppAccessToken" {
			_, _ = w.Write([]byte(`{"access_token":"t","expires_in":7200}`))
			return
		}
		if r.URL.Path == "/v2/users/u1/messages" {
			var body struct {
				MsgSeq int `json:"msg_seq"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			atomic.AddInt32(sent, 1)
			if seqs != nil {
				*seqs = append(*seqs, int32(body.MsgSeq))
			}
			_, _ = w.Write([]byte(`{"id":"m1"}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	api := gateway.NewAPI(gateway.Credentials{AppID: "1", AppSecret: "s"})
	api.SetEndpoints(srv.URL, srv.URL+"/app/getAppAccessToken")
	return api
}

// TestReplierSwitchesToActiveAfterQuota 验证被动预算用尽后自动改发主动消息：
// 主动消息不带 msg_id/msg_seq，所以不会因为配额耗尽把结果丢掉。
func TestReplierSwitchesToActiveAfterQuota(t *testing.T) {
	var sent int32
	var seqs []int32
	api := newTestAPI(t, &sent, &seqs)

	r := NewReplier(api, "u1", "msg-1", 3, false)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if !r.Text(ctx, "第 "+string(rune('1'+i))+" 条") {
			t.Fatalf("第 %d 条应发送成功", i+1)
		}
	}
	if sent != 5 {
		t.Fatalf("预算用尽后应改走主动消息：期望 5 次，实际 %d 次", sent)
	}
	if r.Left() != 0 {
		t.Fatalf("被动预算应耗尽，实际剩 %d", r.Left())
	}
	for i, seq := range seqs {
		want := int32(0) // 主动消息不带 msg_seq
		if i < 3 {
			want = int32(i + 1)
		}
		if seq != want {
			t.Fatalf("msg_seq 不符：期望 %d 得到 %d（全部 %v）", want, seq, seqs)
		}
	}
}

// newReplyModeAPI 起一个能区分被动回复（带 msg_id）和主动消息（不带）的假接口。
func newReplyModeAPI(t *testing.T, passiveOK bool, passive, active *int32) *gateway.API {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/getAppAccessToken" {
			_, _ = w.Write([]byte(`{"access_token":"t","expires_in":7200}`))
			return
		}
		if r.URL.Path == "/v2/users/u1/files" {
			_, _ = w.Write([]byte(`{"file_info":"fi"}`))
			return
		}
		if r.URL.Path != "/v2/users/u1/messages" {
			http.NotFound(w, r)
			return
		}
		var body struct {
			MsgID string `json:"msg_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.MsgID != "" {
			atomic.AddInt32(passive, 1)
			if !passiveOK {
				// 模拟被动回复窗口过期 / 配额用尽
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"code":40034127,"message":"msg_id 已过期"}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"passive"}`))
			return
		}
		atomic.AddInt32(active, 1)
		_, _ = w.Write([]byte(`{"id":"active"}`))
	}))
	t.Cleanup(srv.Close)

	api := gateway.NewAPI(gateway.Credentials{AppID: "1", AppSecret: "s"})
	api.SetEndpoints(srv.URL, srv.URL+"/app/getAppAccessToken")
	return api
}

// TestReplierFallsBackToActiveWhenPassiveFails 验证被动回复失败时改用主动消息。
func TestReplierFallsBackToActiveWhenPassiveFails(t *testing.T) {
	var passive, active int32
	api := newReplyModeAPI(t, false, &passive, &active)
	r := NewReplier(api, "u1", "msg-1", 4, false)
	ctx := context.Background()

	if !r.Text(ctx, "结果正文") {
		t.Fatal("被动失败后应改用主动消息送达")
	}
	if passive != 1 || active != 1 {
		t.Fatalf("应各尝试一次：passive=%d active=%d", passive, active)
	}
	if r.Left() != 4 {
		t.Fatalf("失败的被动回复不应占用预算，实际剩 %d", r.Left())
	}

	// 已知被动通道不可用，后续直接走主动消息，不再重复撞失败
	if !r.Text(ctx, "第二条") {
		t.Fatal("第二条应发送成功")
	}
	if passive != 1 || active != 2 {
		t.Fatalf("后续应直接发主动消息：passive=%d active=%d", passive, active)
	}
}

// TestReplierImageFallsBackToActive 验证截图同样有主动消息兜底：
// 默认 4 条被动预算被确认、过程、结果占满后，截图仍然能发出去。
func TestReplierImageFallsBackToActive(t *testing.T) {
	var passive, active int32
	api := newReplyModeAPI(t, false, &passive, &active)

	r := NewReplier(api, "u1", "msg-1", 4, false)
	ctx := context.Background()
	if !r.Image(ctx, "shot.png", []byte("png")) {
		t.Fatal("被动失败后截图应改用主动消息发出")
	}
	if passive != 1 || active != 1 {
		t.Fatalf("截图应各尝试一次：passive=%d active=%d", passive, active)
	}

	// 预算耗尽的场景：直接把预算设为 1，被文本占满后截图仍要走主动消息
	r2 := NewReplier(api, "u1", "msg-1", 1, false)
	if !r2.Text(ctx, "占满预算") {
		t.Fatal("第一条应发送成功")
	}
	before := active
	if !r2.Image(ctx, "shot2.png", []byte("png")) {
		t.Fatal("预算耗尽后截图应改用主动消息")
	}
	if active != before+1 {
		t.Fatalf("截图应新增 1 条主动消息：%d → %d", before, active)
	}
}

func TestReplierSplitsLongText(t *testing.T) {
	var sent int32
	api := newTestAPI(t, &sent, nil)
	r := NewReplier(api, "u1", "msg-1", 4, false)

	long := make([]rune, chunkRunes*2+50)
	for i := range long {
		long[i] = '啊'
	}
	if !r.Text(context.Background(), string(long)) {
		t.Fatal("长文本应分条发出")
	}
	if sent != 3 {
		t.Fatalf("期望拆成 3 条，实际 %d", sent)
	}
}

func TestReplierCapsChunkCount(t *testing.T) {
	var sent int32
	api := newTestAPI(t, &sent, nil)
	r := NewReplier(api, "u1", "msg-1", 10, false)

	long := make([]rune, chunkRunes*(maxChunks+3))
	for i := range long {
		long[i] = '啊'
	}
	r.Text(context.Background(), string(long))
	if sent != int32(maxChunks) {
		t.Fatalf("最多发 %d 条，实际 %d", maxChunks, sent)
	}
}

func TestReplierSkipsEmpty(t *testing.T) {
	var sent int32
	api := newTestAPI(t, &sent, nil)
	r := NewReplier(api, "u1", "msg-1", 2, false)
	if r.Text(context.Background(), "   ") {
		t.Fatal("空文本不应发送")
	}
	if sent != 0 {
		t.Fatalf("空文本触发了请求：%d", sent)
	}
}

// newMarkdownAPI 起一个能区分 msg_type 的假接口：mdOK 为 false 时 Markdown 消息报错。
func newMarkdownAPI(t *testing.T, mdOK bool, mdCount, plainCount *int32) *gateway.API {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/getAppAccessToken" {
			_, _ = w.Write([]byte(`{"access_token":"t","expires_in":7200}`))
			return
		}
		var body struct {
			MsgType int `json:"msg_type"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.MsgType == 2 {
			atomic.AddInt32(mdCount, 1)
			if !mdOK {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"code":40034127,"message":"无markdown模板权限"}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"md"}`))
			return
		}
		atomic.AddInt32(plainCount, 1)
		_, _ = w.Write([]byte(`{"id":"plain"}`))
	}))
	t.Cleanup(srv.Close)

	api := gateway.NewAPI(gateway.Credentials{AppID: "1", AppSecret: "s"})
	api.SetEndpoints(srv.URL, srv.URL+"/app/getAppAccessToken")
	return api
}

func TestReplierSendsMarkdown(t *testing.T) {
	var md, plain int32
	api := newMarkdownAPI(t, true, &md, &plain)
	r := NewReplier(api, "u1", "msg-1", 3, true)

	if !r.Text(context.Background(), "**加粗** 和 `代码`") {
		t.Fatal("Markdown 消息应发送成功")
	}
	if md != 1 || plain != 0 {
		t.Fatalf("应只发 Markdown：md=%d plain=%d", md, plain)
	}
}

func TestReplierFallsBackToPlainText(t *testing.T) {
	var md, plain int32
	api := newMarkdownAPI(t, false, &md, &plain)
	r := NewReplier(api, "u1", "msg-1", 3, true)
	ctx := context.Background()

	if !r.Text(ctx, "**第一条**") {
		t.Fatal("Markdown 失败后应降级为纯文本发送")
	}
	if md != 1 || plain != 1 {
		t.Fatalf("首次应 md+plain 各一次：md=%d plain=%d", md, plain)
	}

	// 之后不应再重复尝试 Markdown
	if !r.Text(ctx, "**第二条**") {
		t.Fatal("第二条应发送成功")
	}
	if md != 1 {
		t.Fatalf("已知无权限后不应再试 Markdown：md=%d", md)
	}
	if plain != 2 {
		t.Fatalf("纯文本应发 2 次：plain=%d", plain)
	}
}

// newLengthLimitedAPI 起一个按字数拒收的假接口，用来模拟 40054007 消息长度超限。
func newLengthLimitedAPI(t *testing.T, limit int, mu *sync.Mutex, contents *[]string, seqs *[]int) *gateway.API {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/getAppAccessToken" {
			_, _ = w.Write([]byte(`{"access_token":"t","expires_in":7200}`))
			return
		}
		var body struct {
			MsgType  int    `json:"msg_type"`
			Content  string `json:"content"`
			MsgSeq   int    `json:"msg_seq"`
			MsgID    string `json:"msg_id"`
			Markdown *struct {
				Content string `json:"content"`
			} `json:"markdown"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		text := body.Content
		if body.Markdown != nil {
			text = body.Markdown.Content
		}
		if len([]rune(text)) > limit {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":40054007,"message":"消息长度超限"}`))
			return
		}
		mu.Lock()
		*contents = append(*contents, text)
		*seqs = append(*seqs, body.MsgSeq)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"id":"m"}`))
	}))
	t.Cleanup(srv.Close)

	api := gateway.NewAPI(gateway.Credentials{AppID: "1", AppSecret: "s"})
	api.SetEndpoints(srv.URL, srv.URL+"/app/getAppAccessToken")
	return api
}

// TestReplierKeepsMessageUnderThreshold 验证 5000 字以内不主动切分。
func TestReplierKeepsMessageUnderThreshold(t *testing.T) {
	var mu sync.Mutex
	var contents []string
	var seqs []int
	api := newLengthLimitedAPI(t, 10000, &mu, &contents, &seqs)

	r := NewReplier(api, "u1", "msg-1", 4, false)
	text := strings.Repeat("啊", chunkRunes-1)
	if !r.Text(context.Background(), text) {
		t.Fatal("未超阈值的文本应一次发出")
	}
	if len(contents) != 1 || contents[0] != text {
		t.Fatalf("不应被切分：%d 条", len(contents))
	}
	if len(seqs) != 1 || seqs[0] != 1 {
		t.Fatalf("应只占一条被动回复：%v", seqs)
	}
}

// TestReplierSplitsAfterPlatformRejects 验证平台报超长后对半切重发，且不丢内容。
func TestReplierSplitsAfterPlatformRejects(t *testing.T) {
	var mu sync.Mutex
	var contents []string
	var seqs []int
	// 平台限 2600 字：5000 字的首条会被拒绝，对半切成 2500+2500 后才能过
	api := newLengthLimitedAPI(t, 2600, &mu, &contents, &seqs)

	r := NewReplier(api, "u1", "msg-1", 6, false)
	text := strings.Repeat("啊", 6000)
	if !r.Text(context.Background(), text) {
		t.Fatal("超长文本应能切分后发出")
	}
	if got := strings.Join(contents, ""); got != text {
		t.Fatalf("切分后内容不完整：%d 字 vs %d 字", len([]rune(got)), len([]rune(text)))
	}
	if len(contents) != 3 {
		t.Fatalf("期望切成 3 条，实际 %d 条", len(contents))
	}
	for i, seq := range seqs {
		if seq != i+1 {
			t.Fatalf("超长重发不应浪费被动预算：seqs=%v", seqs)
		}
	}
	if r.Left() != 3 {
		t.Fatalf("应消耗 3 条被动预算，剩 %d", r.Left())
	}
}

// TestRichSplitsAfterPlatformRejects 验证带指令标签的 Markdown 也支持超长重发。
func TestRichSplitsAfterPlatformRejects(t *testing.T) {
	var mu sync.Mutex
	var contents []string
	var seqs []int
	api := newLengthLimitedAPI(t, 2600, &mu, &contents, &seqs)

	r := NewReplier(api, "u1", "msg-1", 6, true)
	text := strings.Repeat("啊", 6000)
	if !r.Rich(context.Background(), text, "plain") {
		t.Fatal("超长 Markdown 应能切分后发出")
	}
	if got := strings.Join(contents, ""); got != text {
		t.Fatalf("切分后内容不完整：%d 字 vs %d 字", len([]rune(got)), len([]rune(text)))
	}
	if len(contents) != 3 {
		t.Fatalf("期望切成 3 条，实际 %d 条", len(contents))
	}
}

func TestIsTooLong(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{&gateway.APIError{Code: 40054007}, true},
		{&gateway.APIError{ErrCode: 40054018}, true},
		{&gateway.APIError{Message: "消息过长或异常"}, true},
		{&gateway.APIError{Message: "content too long"}, true},
		{&gateway.APIError{Code: 40054005, Message: "消息被去重"}, false},
		{context.Canceled, false},
	}
	for _, c := range cases {
		if got := isTooLong(c.err); got != c.want {
			t.Fatalf("isTooLong(%+v)=%v，期望 %v", c.err, got, c.want)
		}
	}
}

// TestTextReportsPartialFailure 验证分段发送中途失败时返回 false，
// 并且留下一句能看出断在第几段的错误日志。
func TestTextReportsPartialFailure(t *testing.T) {
	var sent int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/getAppAccessToken" {
			_, _ = w.Write([]byte(`{"access_token":"t","expires_in":7200}`))
			return
		}
		if atomic.AddInt32(&sent, 1) == 1 {
			_, _ = w.Write([]byte(`{"id":"m1"}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":500,"message":"boom"}`))
	}))
	t.Cleanup(srv.Close)

	api := gateway.NewAPI(gateway.Credentials{AppID: "1", AppSecret: "s"})
	api.SetEndpoints(srv.URL, srv.URL+"/app/getAppAccessToken")

	r := NewReplier(api, "u1", "msg-1", 4, false)
	var logs []string
	r.SetLogger(func(level, format string, args ...any) {
		if level == "error" {
			logs = append(logs, fmt.Sprintf(format, args...))
		}
	})

	text := strings.Repeat("甲", chunkRunes) + "\n" + strings.Repeat("乙", chunkRunes)
	if r.Text(context.Background(), text) {
		t.Fatal("中途失败时应返回 false")
	}
	for _, line := range logs {
		if strings.Contains(line, "第 2/2 段失败") {
			return
		}
	}
	t.Fatalf("应记录第 2 段失败的日志，实际：%v", logs)
}

// 任务回复末尾要带上本次会话的 agent 和模型；没设置就不带。
func TestReplierAppendsFooter(t *testing.T) {
	api, sent := recordingAPI(t)
	r := NewReplier(api, "u1", "m1", 2, false)
	r.SetFooter("Agent：CodeBuddy · 模型：Opus 5（`opus-5`）")
	if !r.Text(context.Background(), "结果正文") {
		t.Fatal("发送失败")
	}
	got := sent()
	if len(got) != 1 || !strings.Contains(got[0].Body, "Agent：CodeBuddy") || !strings.Contains(got[0].Body, "模型：Opus 5") {
		t.Fatalf("应附带 agent 与模型名：%+v", got)
	}

	api2, sent2 := recordingAPI(t)
	r2 := NewReplier(api2, "u1", "m1", 2, false)
	r2.Text(context.Background(), "结果正文")
	if body := sent2()[0].Body; strings.Contains(body, "Agent：") {
		t.Fatalf("没设置标签不应附带：%s", body)
	}
}
