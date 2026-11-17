package bridge

import (
	"context"
	"testing"

	"qqbot_bridge/internal/config"
	"qqbot_bridge/internal/msglog"
)

// 收到的消息要如实记录：被拒绝的、受理的（含群聊）、重复推送的，各有各的状态。
func TestInboundMessagesAreRecorded(t *testing.T) {
	b, _, _ := newWhitelistBridge(t)
	log := msglog.New("", 50)
	b.SetMessageLog(log)
	ctx := context.Background()

	b.HandleEvent(ctx, c2cEvent(t, "m1", "stranger", "我想用一下"))
	b.HandleEvent(ctx, c2cEvent(t, "m2", "vip", "/allow list"))
	b.HandleEvent(ctx, groupEvent(t, "m3", "vip", "/allow list"))
	b.HandleEvent(ctx, c2cEvent(t, "m2", "vip", "/allow list")) // 同一 msg_id 重推

	// 按时间倒序：重复、群聊受理、单聊受理、被拒绝
	got := log.Query(msglog.Filter{})
	if len(got) != 4 {
		t.Fatalf("应记录 4 条，实际 %d：%+v", len(got), got)
	}
	want := []struct{ status, from, group string }{
		{msglog.StatusDuplicate, "vip", ""},
		{msglog.StatusAccepted, "vip", "g1"},
		{msglog.StatusAccepted, "vip", ""},
		{msglog.StatusDenied, "stranger", ""},
	}
	for i, w := range want {
		if got[i].Status != w.status || got[i].From != w.from || got[i].Group != w.group {
			t.Errorf("第 %d 条不对：期望 %+v，实际 %+v", i, w, got[i])
		}
	}
	if got[3].Content != "我想用一下" {
		t.Fatalf("被拒绝的消息也要保留原文：%q", got[3].Content)
	}
}

// 没配置记录器时不能 panic，也不影响正常处理。
func TestInboundWithoutMessageLog(t *testing.T) {
	b, _, sent := newWhitelistBridge(t)
	b.HandleEvent(context.Background(), c2cEvent(t, "m1", "stranger", "你好"))
	if len(sent()) != 1 {
		t.Fatal("没有记录器时仍应正常回复无权限")
	}
}

// 陌生人发 /whoami 也要留痕为「已拒绝」，否则管理端看不到他。
func TestWhoamiFromStrangerIsRecordedAsDenied(t *testing.T) {
	b, _, _ := newWhitelistBridge(t)
	log := msglog.New("", 10)
	b.SetMessageLog(log)
	b.HandleEvent(context.Background(), c2cEvent(t, "w1", "stranger", "/whoami"))
	got := log.Query(msglog.Filter{})
	if len(got) != 1 || got[0].Status != msglog.StatusDenied || got[0].From != "stranger" {
		t.Fatalf("应记录一条已拒绝：%+v", got)
	}
}

// 受理的消息要写明属于哪个对话（收到那一刻的当前会话），被拒绝的不归属任何对话。
func TestRecordedMessagesCarryTheirConversation(t *testing.T) {
	b, store, _ := newWhitelistBridge(t)
	if err := store.Update(func(c *config.Config) error {
		c.Sessions = []config.Session{
			{ID: "pay", Name: "支付项目", Dir: "/work/pay", AgentID: "conv-pay"},
			{ID: "web", Name: "官网", Dir: "/work/web"},
		}
		c.Current = "pay"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	log := msglog.New("", 50)
	b.SetMessageLog(log)
	ctx := context.Background()

	b.HandleEvent(ctx, c2cEvent(t, "a1", "vip", "/status"))     // 在 pay 下的指令
	b.HandleEvent(ctx, c2cEvent(t, "a2", "vip", "/switch web")) // 切走之前收到，仍属于 pay
	b.HandleEvent(ctx, c2cEvent(t, "a3", "vip", "/status"))     // 现在属于 web，web 还没有对话
	b.HandleEvent(ctx, c2cEvent(t, "a4", "stranger", "我想用一下"))  // 被拒绝

	got := log.Query(msglog.Filter{}) // 倒序
	if len(got) != 4 {
		t.Fatalf("应记录 4 条：%+v", got)
	}
	denied, third, second, first := got[0], got[1], got[2], got[3]

	if first.Session != "pay" || first.SessionName != "支付项目" || first.SessionDir != "/work/pay" ||
		first.Conversation != "conv-pay" || !first.Command {
		t.Errorf("第 1 条应属于 pay 对话且是指令：%+v", first)
	}
	if second.Session != "pay" {
		t.Errorf("/switch 本身是在 pay 下发出的，应属于 pay：%+v", second)
	}
	if third.Session != "web" || third.Conversation != "" {
		t.Errorf("切换后应属于 web，且还没有 CodeBuddy 对话：%+v", third)
	}
	if denied.Status != msglog.StatusDenied || denied.Session != "" || denied.SessionName != "" || denied.Command {
		t.Errorf("被拒绝的消息不应归属任何对话：%+v", denied)
	}
}
