package web

import (
	"encoding/json"
	"strings"
	"testing"

	"qqbot_bridge/internal/config"
)

// 同一实例里会话可以各用各的 agent，页面拿到的是每个会话自己的 driver 和展示名。
func TestStateReportsSessionDriver(t *testing.T) {
	srv, store := newTestServer(t)
	if err := store.Update(func(c *config.Config) error {
		c.Agent.Driver = config.DriverCodeBuddy
		c.Sessions = []config.Session{
			{ID: "cb", Name: "CodeBuddy 会话", Dir: "/tmp/a"},
			{ID: "cu", Name: "Cursor 会话", Dir: "/tmp/b", Driver: config.DriverCursor},
		}
		c.Current = "cu"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	var got stateView
	_ = json.Unmarshal(get(t, srv, "/api/state").Body.Bytes(), &got)
	if len(got.Sessions) != 2 {
		t.Fatalf("应有 2 个会话：%+v", got.Sessions)
	}
	want := map[string][2]string{
		"cb": {config.DriverCodeBuddy, "CodeBuddy"},
		"cu": {config.DriverCursor, "Cursor"},
	}
	for _, s := range got.Sessions {
		if w := want[s.ID]; s.Driver != w[0] || s.Agent != w[1] {
			t.Errorf("%s 的 driver/agent 不对：%+v", s.ID, s)
		}
	}

	// 会话详情（左边栏点进去的面板）同样带会话自己的 agent
	var detail sessionDetail
	_ = json.Unmarshal(get(t, srv, "/api/sessions/cu").Body.Bytes(), &detail)
	if detail.Driver != config.DriverCursor || detail.Agent != "Cursor" {
		t.Fatalf("会话详情缺少 driver：%+v", detail)
	}
}

// 会话还没有对话时，提示里的 agent 名要按会话自己的驱动拼。
func TestSessionLogNoteUsesSessionDriver(t *testing.T) {
	srv, store := newTestServer(t)
	if err := store.Update(func(c *config.Config) error {
		c.Sessions = []config.Session{{ID: "cu", Dir: "/tmp/b", Driver: config.DriverCursor}}
		c.Current = "cu"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var got logResponse
	_ = json.Unmarshal(get(t, srv, "/api/sessions/cu/log").Body.Bytes(), &got)
	if !strings.Contains(got.Note, "Cursor") {
		t.Fatalf("提示应说明是 Cursor 会话：%q", got.Note)
	}
}
