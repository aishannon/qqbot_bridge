package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"qqbot_bridge/internal/config"
	"qqbot_bridge/internal/gateway"
)

// newButtonBridge 起一个能记录互动回应和消息的假 QQ 接口。
func newButtonBridge(t *testing.T) (*Bridge, *config.Store, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var calls []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		switch {
		case r.URL.Path == "/token":
			_, _ = w.Write([]byte(`{"access_token":"t","expires_in":7200}`))
		case strings.HasPrefix(r.URL.Path, "/interactions/"):
			_, _ = w.Write([]byte(`{}`))
		case r.URL.Path == "/v2/users/u1/messages":
			_, _ = w.Write([]byte(`{"id":"m1"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	store, err := config.OpenAt(filepath.Join(t.TempDir(), "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// 按钮测试默认把 u1 当成绑定者，白名单逻辑才有意义
	if err := store.Update(func(c *config.Config) error {
		c.Bind.UserOpenID = "u1"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	api := gateway.NewAPI(gateway.Credentials{AppID: "1", AppSecret: "s"})
	api.SetEndpoints(srv.URL, srv.URL+"/token")
	return New(store, api, nil), store, &calls
}

func TestButtonClickSwitchesSession(t *testing.T) {
	b, store, calls := newButtonBridge(t)
	current := store.Get().Sessions[0].ID

	// 先切到另一个会话，再用按钮切回来
	b.newSession(filepath.Join(t.TempDir(), "demo"), "演示")
	if store.Get().Current == current {
		t.Fatal("新建后当前会话应切换")
	}

	payload := `{"id":"it-1","type":11,"scene":"c2c","chat_type":2,
		"user_openid":"u1","data":{"type":11,"resolved":{"button_data":"switch:` + current + `"}}}`
	b.HandleEvent(context.Background(), gateway.Event{
		Type: gateway.EventInteractionCreate,
		Data: []byte(payload),
	})

	if store.Get().Current != current {
		t.Fatalf("点击按钮后应切回 %s，实际 %s", current, store.Get().Current)
	}

	joined := strings.Join(*calls, "\n")
	if !strings.Contains(joined, "PUT /interactions/it-1") {
		t.Fatalf("应先回应互动事件：\n%s", joined)
	}
	if !strings.Contains(joined, "POST /v2/users/u1/messages") {
		t.Fatalf("应发出切换结果消息：\n%s", joined)
	}
}

func TestButtonClickStatusAndStop(t *testing.T) {
	b, _, calls := newButtonBridge(t)

	for _, action := range []string{"status", "stop"} {
		payload := `{"id":"it-` + action + `","type":11,"user_openid":"u1",
			"data":{"type":11,"resolved":{"button_data":"` + action + `"}}}`
		b.HandleEvent(context.Background(), gateway.Event{
			Type: gateway.EventInteractionCreate,
			Data: []byte(payload),
		})
	}

	joined := strings.Join(*calls, "\n")
	if !strings.Contains(joined, "PUT /interactions/it-status") || !strings.Contains(joined, "PUT /interactions/it-stop") {
		t.Fatalf("每个按钮都要回应：\n%s", joined)
	}
}

// TestButtonFromOtherUserIgnored 验证非绑定用户点按钮不会被执行。
func TestButtonFromOtherUserIgnored(t *testing.T) {
	b, store, calls := newButtonBridge(t)
	if err := store.Update(func(c *config.Config) error {
		c.Bind.UserOpenID = "owner"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	payload := `{"id":"it-x","type":11,"user_openid":"stranger",
		"data":{"type":11,"resolved":{"button_data":"list"}}}`
	b.HandleEvent(context.Background(), gateway.Event{
		Type: gateway.EventInteractionCreate,
		Data: []byte(payload),
	})

	joined := strings.Join(*calls, "\n")
	if !strings.Contains(joined, "PUT /interactions/it-x") {
		t.Fatalf("仍然要回应，避免对方一直转圈：\n%s", joined)
	}
	if strings.Contains(joined, "POST /v2/users/stranger/messages") {
		t.Fatalf("不应给陌生人发消息：\n%s", joined)
	}
}

func TestSendSessionListUsesCommandTags(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_, _ = w.Write([]byte(`{"access_token":"t","expires_in":7200}`))
		case "/v2/users/u1/messages":
			_ = json.NewDecoder(r.Body).Decode(&body)
			_, _ = w.Write([]byte(`{"id":"m1"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	store, err := config.OpenAt(filepath.Join(t.TempDir(), "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(c *config.Config) error {
		c.Discover.Disable = true // 测试不读真实 CodeBuddy 目录
		c.Sessions[0].New = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	api := gateway.NewAPI(gateway.Credentials{AppID: "1", AppSecret: "s"})
	api.SetEndpoints(srv.URL, srv.URL+"/token")
	b := New(store, api, nil)

	b.sendSessionList(context.Background(), chatTarget{from: "u1", userID: "u1", msgID: "msg-1"})

	if body["msg_type"] != float64(2) {
		t.Fatalf("会话列表应走 Markdown：%+v", body)
	}
	if _, ok := body["keyboard"]; ok {
		t.Fatal("改用指令标签后不应再带键盘")
	}
	md, _ := body["markdown"].(map[string]any)
	content, _ := md["content"].(string)
	if !strings.Contains(content, "<qqbot-cmd-input") {
		t.Fatalf("列表应包含指令标签：\n%s", content)
	}
	if !strings.Contains(content, "🔴") {
		t.Fatalf("新增会话应有红色标记：\n%s", content)
	}
	if store.Get().Sessions[0].New {
		t.Fatal("列表发出后应清除新增标记")
	}
}

func TestSendSessionListFallsBackToPlainText(t *testing.T) {
	var plainBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_, _ = w.Write([]byte(`{"access_token":"t","expires_in":7200}`))
		case "/v2/users/u1/messages":
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req["msg_type"] == float64(2) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"code":40034127,"message":"无markdown权限"}`))
				return
			}
			plainBody = req
			_, _ = w.Write([]byte(`{"id":"m1"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	store, err := config.OpenAt(filepath.Join(t.TempDir(), "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(c *config.Config) error {
		c.Discover.Disable = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	api := gateway.NewAPI(gateway.Credentials{AppID: "1", AppSecret: "s"})
	api.SetEndpoints(srv.URL, srv.URL+"/token")
	b := New(store, api, nil)

	b.sendSessionList(context.Background(), chatTarget{from: "u1", userID: "u1", msgID: "msg-1"})

	content, _ := plainBody["content"].(string)
	if content == "" {
		t.Fatalf("Markdown 失败后应发纯文本：%+v", plainBody)
	}
	if strings.Contains(content, "qqbot-cmd") {
		t.Fatalf("纯文本里不能出现标签源码：\n%s", content)
	}
	if !strings.Contains(content, "/switch") {
		t.Fatalf("纯文本应保留 /switch 用法：\n%s", content)
	}
}

func TestSetupMenuSendsItems(t *testing.T) {
	var raw map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			_, _ = w.Write([]byte(`{"access_token":"t","expires_in":7200}`))
			return
		}
		if r.URL.Path == "/v2/menu" {
			_ = json.NewDecoder(r.Body).Decode(&raw)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	store, err := config.OpenAt(filepath.Join(t.TempDir(), "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	api := gateway.NewAPI(gateway.Credentials{AppID: "1", AppSecret: "s"})
	api.SetEndpoints(srv.URL, srv.URL+"/token")
	b := New(store, api, nil)

	if err := b.SetupMenu(context.Background()); err != nil {
		t.Fatal(err)
	}
	menu := raw["menu"].(map[string]any)
	items := menu["items"].([]any)
	if len(items) < 3 {
		t.Fatalf("菜单项太少：%+v", items)
	}
	first := items[0].(map[string]any)
	if first["send_message"] != "/list" {
		t.Fatalf("第一个菜单项应是 /list：%+v", first)
	}
}
