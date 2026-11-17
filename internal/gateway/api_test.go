package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// newTestServer 起一个假 QQ OpenAPI，返回 token 并记录消息请求体。
func newTestServer(t *testing.T, handler http.HandlerFunc) (*API, *int32) {
	t.Helper()
	var tokenCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			atomic.AddInt32(&tokenCalls, 1)
			_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":7200}`))
			return
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	api := NewAPI(Credentials{AppID: "app1", AppSecret: "sec"})
	api.SetEndpoints(srv.URL, srv.URL+"/token")
	return api, &tokenCalls
}

func TestAccessTokenCached(t *testing.T) {
	api, calls := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	ctx := context.Background()
	if _, err := api.AccessToken(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := api.AccessToken(ctx); err != nil {
		t.Fatal(err)
	}
	if *calls != 1 {
		t.Fatalf("token 应缓存，实际换取 %d 次", *calls)
	}
}

func TestSendC2CPassiveReply(t *testing.T) {
	var got struct {
		MsgType int    `json:"msg_type"`
		Content string `json:"content"`
		MsgID   string `json:"msg_id"`
		MsgSeq  int    `json:"msg_seq"`
	}
	var auth string
	api, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"id":"m1"}`))
	})

	id, err := api.SendC2C(context.Background(), "u1", Message{
		Content: "hi",
		MsgID:   "msg-1",
		MsgSeq:  2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if id != "m1" {
		t.Fatalf("返回消息 ID 不对：%q", id)
	}
	if auth != "QQBot tok" {
		t.Fatalf("鉴权头不对：%q", auth)
	}
	if got.MsgID != "msg-1" || got.MsgSeq != 2 || got.Content != "hi" {
		t.Fatalf("被动回复请求体不对：%+v", got)
	}
}

func TestSendC2CActiveOmitsMsgID(t *testing.T) {
	var raw map[string]any
	api, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&raw)
		_, _ = w.Write([]byte(`{"id":"m2"}`))
	})

	if _, err := api.SendC2C(context.Background(), "u1", Message{Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["msg_id"]; ok {
		t.Fatalf("主动消息不应带 msg_id：%+v", raw)
	}
	if _, ok := raw["msg_seq"]; ok {
		t.Fatalf("主动消息不应带 msg_seq：%+v", raw)
	}
}

func TestSendC2CMarkdownBody(t *testing.T) {
	var raw map[string]any
	api, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&raw)
		_, _ = w.Write([]byte(`{"id":"m3"}`))
	})

	if _, err := api.SendC2C(context.Background(), "u1", Message{Markdown: "**b**"}); err != nil {
		t.Fatal(err)
	}
	if raw["msg_type"] != float64(2) {
		t.Fatalf("Markdown 消息 msg_type 应为 2：%+v", raw)
	}
	if _, ok := raw["content"]; ok {
		t.Fatalf("Markdown 消息不应带 content：%+v", raw)
	}
}

func TestSendC2CErrorParsed(t *testing.T) {
	api, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":40034127,"message":"无权限","trace_id":"tr1"}`))
	})

	_, err := api.SendC2C(context.Background(), "u1", Message{Content: "x"})
	if err == nil {
		t.Fatal("应返回错误")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("应为 APIError，实际 %T", err)
	}
	if apiErr.Code != 40034127 || apiErr.Message != "无权限" || apiErr.TraceID != "tr1" {
		t.Fatalf("错误解析不对：%+v", apiErr)
	}
}

// TestUnauthorizedInvalidatesToken 验证 401 之后会重新换 token。
func TestUnauthorizedInvalidatesToken(t *testing.T) {
	var n int32
	var tokenCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			atomic.AddInt32(&tokenCalls, 1)
			_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":7200}`))
			return
		}
		if atomic.AddInt32(&n, 1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":11244,"message":"token invalid"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer srv.Close()

	api := NewAPI(Credentials{AppID: "a", AppSecret: "s"})
	api.SetEndpoints(srv.URL, srv.URL+"/token")
	ctx := context.Background()

	if _, err := api.SendC2C(ctx, "u1", Message{Content: "x"}); err == nil {
		t.Fatal("第一次应失败")
	}
	if _, err := api.SendC2C(ctx, "u1", Message{Content: "x"}); err != nil {
		t.Fatalf("401 后应重新取 token 并成功：%v", err)
	}
	if tokenCalls != 2 {
		t.Fatalf("401 后应重新换取 token，实际 %d 次", tokenCalls)
	}
}

func TestSendC2CTypingHasNoMsgID(t *testing.T) {
	var raw map[string]any
	api, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&raw)
		_, _ = w.Write([]byte(`{}`))
	})

	if err := api.SendC2CTyping(context.Background(), "u1", 60); err != nil {
		t.Fatal(err)
	}
	if raw["msg_type"] != float64(6) {
		t.Fatalf("输入中消息 msg_type 应为 6：%+v", raw)
	}
	if _, ok := raw["msg_id"]; ok {
		t.Fatalf("输入中状态不应带 msg_id：%+v", raw)
	}
}

func TestSendC2CWithKeyboard(t *testing.T) {
	var raw map[string]any
	api, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&raw)
		_, _ = w.Write([]byte(`{"id":"k1"}`))
	})

	kb := NewKeyboard([]KeyboardButton{
		CallbackButton("b1", "会话 1", "switch:s1"),
		CallbackButton("b2", "会话 2", "switch:s2"),
	}, 1)
	if _, err := api.SendC2C(context.Background(), "u1", Message{Content: "列表", Keyboard: kb}); err != nil {
		t.Fatal(err)
	}
	if raw["msg_type"] != float64(0) {
		t.Fatalf("带按钮的消息必须是 msg_type=0：%+v", raw)
	}
	if raw["content"] != "列表" {
		t.Fatalf("正文丢了：%+v", raw)
	}
	if _, ok := raw["keyboard"]; !ok {
		t.Fatalf("消息没有带上 keyboard：%+v", raw)
	}
	rows := raw["keyboard"].(map[string]any)["content"].(map[string]any)["rows"].([]any)
	if len(rows) != 2 {
		t.Fatalf("每行一个按钮，应有 2 行：%+v", rows)
	}
}

func TestSendC2CMarkdownWithKeyboardFallsBackToPlain(t *testing.T) {
	var raw map[string]any
	api, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&raw)
		_, _ = w.Write([]byte(`{"id":"k2"}`))
	})
	kb := NewKeyboard([]KeyboardButton{CallbackButton("b1", "x", "y")}, 1)

	if _, err := api.SendC2C(context.Background(), "u1", Message{Markdown: "**粗**", Keyboard: kb}); err != nil {
		t.Fatal(err)
	}
	if raw["msg_type"] != float64(0) {
		t.Fatalf("带按钮时不能用 Markdown 消息：%+v", raw)
	}
	if raw["content"] != "**粗**" {
		t.Fatalf("Markdown 源码应作为正文：%+v", raw)
	}
}

func TestSendC2CReplyToEvent(t *testing.T) {
	var raw map[string]any
	api, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&raw)
		_, _ = w.Write([]byte(`{"id":"e1"}`))
	})

	_, err := api.SendC2C(context.Background(), "u1", Message{Content: "ok", EventID: "ev-1"})
	if err != nil {
		t.Fatal(err)
	}
	if raw["event_id"] != "ev-1" || raw["msg_seq"] != float64(1) {
		t.Fatalf("回应互动消息应带 event_id/msg_seq：%+v", raw)
	}
	if _, ok := raw["msg_id"]; ok {
		t.Fatalf("不应同时带 msg_id：%+v", raw)
	}
}

func TestAckInteraction(t *testing.T) {
	var method, path string
	var body map[string]any
	api, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{}`))
	})

	if err := api.AckInteraction(context.Background(), "it-1", 0); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPut {
		t.Fatalf("回应互动应使用 PUT，实际 %s", method)
	}
	if path != "/interactions/it-1" {
		t.Fatalf("路径不对：%s", path)
	}
	if body["code"] != float64(0) {
		t.Fatalf("回应体不对：%+v", body)
	}
}

func TestSetC2CMenu(t *testing.T) {
	var raw map[string]any
	api, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&raw)
		_, _ = w.Write([]byte(`{}`))
	})

	items := []MenuItem{
		{Name: "会话", Type: MenuTypeSendMessage, SendMessage: "/list"},
		{Name: "状态", Type: MenuTypeSendMessage, SendMessage: "/status"},
	}
	if err := api.SetC2CMenu(context.Background(), items); err != nil {
		t.Fatal(err)
	}
	menu := raw["menu"].(map[string]any)
	got := menu["items"].([]any)
	if len(got) != 2 {
		t.Fatalf("菜单项数量不对：%+v", raw)
	}
	first := got[0].(map[string]any)
	if first["send_message"] != "/list" || first["type"] != "send_message" {
		t.Fatalf("菜单项内容不对：%+v", first)
	}
}

func TestNewKeyboardWrapsRows(t *testing.T) {
	var btns []KeyboardButton
	for i := 0; i < 5; i++ {
		btns = append(btns, CallbackButton("b", "x", "y"))
	}
	kb := NewKeyboard(btns, 2)
	if len(kb.Content.Rows) != 3 {
		t.Fatalf("5 个按钮按每行 2 个应排成 3 行，实际 %d", len(kb.Content.Rows))
	}
	if len(kb.Content.Rows[2].Buttons) != 1 {
		t.Fatalf("最后一行应只有 1 个按钮")
	}
	if NewKeyboard(nil, 3) != nil {
		t.Fatal("没有按钮时应返回 nil")
	}
}
