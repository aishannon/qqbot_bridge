package bridge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"qqbot_bridge/internal/config"
	"qqbot_bridge/internal/gateway"
)

// recordingAPI 起一个假 OpenAPI，把每次发消息的路径和请求体记下来。
func recordingAPI(t *testing.T) (*gateway.API, func() []sentMessage) {
	t.Helper()
	var mu sync.Mutex
	var sent []sentMessage

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/getAppAccessToken" {
			_, _ = w.Write([]byte(`{"access_token":"t","expires_in":7200}`))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		sent = append(sent, sentMessage{Path: r.URL.Path, Body: string(raw)})
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/files") {
			_, _ = w.Write([]byte(`{"file_info":"FI"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"m1"}`))
	}))
	t.Cleanup(srv.Close)

	api := gateway.NewAPI(gateway.Credentials{AppID: "1", AppSecret: "s"})
	api.SetEndpoints(srv.URL, srv.URL+"/app/getAppAccessToken")
	return api, func() []sentMessage {
		mu.Lock()
		defer mu.Unlock()
		out := make([]sentMessage, len(sent))
		copy(out, sent)
		return out
	}
}

type sentMessage struct {
	Path string
	Body string
}

func newWhitelistBridge(t *testing.T) (*Bridge, *config.Store, func() []sentMessage) {
	t.Helper()
	store, err := config.OpenAt(t.TempDir() + "/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(c *config.Config) error {
		c.Bind = config.Bind{AppID: "1", Secret: "s", UserOpenID: "owner", Whitelist: []string{"vip"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	api, sent := recordingAPI(t)
	return New(store, api, nil), store, sent
}

func c2cEvent(t *testing.T, id, openID, content string) gateway.Event {
	t.Helper()
	body := map[string]any{"id": id, "content": content, "author": map[string]string{"user_openid": openID}}
	raw, _ := json.Marshal(body)
	return gateway.Event{Type: gateway.EventC2CMessage, Data: raw}
}

func groupEvent(t *testing.T, id, memberOpenID, content string) gateway.Event {
	t.Helper()
	body := map[string]any{"id": id, "content": content, "group_openid": "g1", "author": map[string]string{"member_openid": memberOpenID}}
	raw, _ := json.Marshal(body)
	return gateway.Event{Type: gateway.EventGroupAtMessage, Data: raw}
}

// 非白名单用户发单聊：只回一句无权限，不进入指令/任务流程。
func TestWhitelistRejectsStranger(t *testing.T) {
	b, _, sent := newWhitelistBridge(t)
	b.HandleEvent(context.Background(), c2cEvent(t, "m1", "stranger", "你好"))

	got := sent()
	if len(got) != 1 {
		t.Fatalf("应只回一条消息，实际 %d 条：%+v", len(got), got)
	}
	if got[0].Path != "/v2/users/stranger/messages" {
		t.Fatalf("应回到发消息的人：%s", got[0].Path)
	}
	if !strings.Contains(got[0].Body, "无权限") {
		t.Fatalf("应提示无权限：%s", got[0].Body)
	}
}

// 非白名单用户发 /whoami：例外放行，方便自助拿 openid 申请加入。
func TestWhitelistAllowsWhoami(t *testing.T) {
	b, _, sent := newWhitelistBridge(t)
	b.HandleEvent(context.Background(), c2cEvent(t, "m2", "stranger", "/whoami"))

	got := sent()
	if len(got) != 1 || !strings.Contains(got[0].Body, "stranger") {
		t.Fatalf("应返回自己的 openid：%+v", got)
	}
}

// 群聊里非白名单成员 @ 机器人：同样只回无权限，且回复发到群里。
func TestWhitelistRejectsStrangerInGroup(t *testing.T) {
	b, _, sent := newWhitelistBridge(t)
	b.HandleEvent(context.Background(), groupEvent(t, "m3", "stranger", "帮我看看"))

	got := sent()
	if len(got) != 1 {
		t.Fatalf("应只回一条消息，实际 %d 条", len(got))
	}
	if got[0].Path != "/v2/groups/g1/messages" {
		t.Fatalf("群聊回复应发到群：%s", got[0].Path)
	}
	if !strings.Contains(got[0].Body, "无权限") {
		t.Fatalf("应提示无权限：%s", got[0].Body)
	}
}

// 白名单成员在群里 @ 机器人发指令：正常处理并恢复发到群里。
func TestWhitelistAllowsGroupCommand(t *testing.T) {
	b, _, sent := newWhitelistBridge(t)
	b.HandleEvent(context.Background(), groupEvent(t, "m4", "vip", "/allow list"))

	got := sent()
	if len(got) == 0 {
		t.Fatal("白名单用户应收到回复")
	}
	if got[len(got)-1].Path != "/v2/groups/g1/messages" {
		t.Fatalf("指令结果应发到群里：%s", got[len(got)-1].Path)
	}
	if !strings.Contains(got[len(got)-1].Body, "白名单") {
		t.Fatalf("应返回名单内容：%s", got[len(got)-1].Body)
	}
}

// /allow 与 /deny 真正改配置，绑定者不能被移除。
func TestWhitelistCommandUpdatesConfig(t *testing.T) {
	b, store, _ := newWhitelistBridge(t)

	if got := b.whitelistCommand(true, "newbie"); !strings.Contains(got, "已加入") {
		t.Fatalf("加入白名单失败：%s", got)
	}
	if cfg := store.Get(); len(cfg.Bind.Whitelist) != 2 {
		t.Fatalf("白名单应有 2 人：%v", cfg.Bind.Whitelist)
	}
	if got := b.whitelistCommand(false, "newbie"); !strings.Contains(got, "已移出") {
		t.Fatalf("移出白名单失败：%s", got)
	}
	if got := b.whitelistCommand(false, "owner"); !strings.Contains(got, "不能移出") {
		t.Fatalf("绑定者不应能被移除：%s", got)
	}
}

// /whoami 应带上昵称，并把昵称记进配置供名单展示。
func TestWhoamiShowsNickname(t *testing.T) {
	b, store, sent := newWhitelistBridge(t)
	body := map[string]any{"id": "m5", "content": "/whoami", "author": map[string]string{"user_openid": "vip", "username": "小张"}}
	raw, _ := json.Marshal(body)
	b.HandleEvent(context.Background(), gateway.Event{Type: gateway.EventC2CMessage, Data: raw})

	if got := store.Get().Bind.Names["vip"]; got != "小张" {
		t.Fatalf("昵称没记住：%q", got)
	}
	// /whoami 只发文本，不再发图片
	got := sent()
	if len(got) != 1 {
		t.Fatalf("应只发一条文本消息：%+v", got)
	}
	if !strings.Contains(got[0].Body, "小张") {
		t.Fatalf("回复应带昵称：%s", got[0].Body)
	}
}

// 群聊的昵称来源是 member.nick。
func TestGroupNicknameFromMember(t *testing.T) {
	b, store, _ := newWhitelistBridge(t)
	body := map[string]any{
		"id": "m6", "content": "/allow list", "group_openid": "g1",
		"author": map[string]string{"member_openid": "vip"},
		"member": map[string]string{"nick": "阿伟"},
	}
	raw, _ := json.Marshal(body)
	b.HandleEvent(context.Background(), gateway.Event{Type: gateway.EventGroupAtMessage, Data: raw})

	if got := store.Get().Bind.Names["vip"]; got != "阿伟" {
		t.Fatalf("群成员昵称没记住：%q", got)
	}
}
