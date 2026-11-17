package bridge

import (
	"context"
	"strings"
	"testing"

	"qqbot_bridge/internal/agent"
	"qqbot_bridge/internal/config"
)

// cursorTestCatalog 模拟 Cursor 的模型目录：与 CodeBuddy 完全不重叠。
func cursorTestCatalog() agent.ModelCatalog {
	return agent.ModelCatalog{NoRates: true, Models: []agent.ModelInfo{
		{ID: "auto", Name: "Auto (default)"},
		{ID: "gpt-5", Name: "GPT-5"},
	}}
}

// newDriverAwareBridge 让 openDriver 按驱动名返回不同的目录，并记录被请求的驱动名。
func newDriverAwareBridge(t *testing.T) (*Bridge, *config.Store, *[]string) {
	t.Helper()
	b, store := newTestBridge(t)
	seen := new([]string)
	b.openDriver = func(_ config.Config, name string) (agent.Driver, error) {
		*seen = append(*seen, name)
		if name == config.DriverCursor {
			return fakeModelDriver{catalog: cursorTestCatalog()}, nil
		}
		return fakeModelDriver{catalog: testCatalog()}, nil
	}
	return b, store, seen
}

// /driver 切换要清空 AgentID 与 Model（两个 agent 的会话 ID / 模型 ID 不通用），
// 并且不能影响其它会话。
func TestDriverCommandSwitch(t *testing.T) {
	b, store, seen := newDriverAwareBridge(t)
	if err := store.Update(func(c *config.Config) error {
		c.Sessions = append(c.Sessions, config.Session{ID: "other", Dir: "/tmp/other", AgentID: "keep-me"})
		c.Sessions[0].AgentID = "chat-old"
		c.Sessions[0].Model = "flash"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	current := store.Get().Current

	out := b.driverCommand("cursor")
	if !strings.Contains(out, "已切换 agent") || !strings.Contains(out, "Cursor") {
		t.Fatalf("切换提示不对：%s", out)
	}
	cfg := store.Get()
	for _, sess := range cfg.Sessions {
		switch sess.ID {
		case current:
			if sess.Driver != config.DriverCursor || sess.AgentID != "" || sess.Model != "" {
				t.Fatalf("当前会话应切到 cursor 且清空上下文：%+v", sess)
			}
		case "other":
			if sess.Driver != "" || sess.AgentID != "keep-me" {
				t.Fatalf("其它会话不应受影响：%+v", sess)
			}
		}
	}
	// 切换本身只改配置（不启进程）；下一次解析驱动时才按会话的驱动名去构造
	if got := b.driverName(store.Get().Sessions[0]); got != config.DriverCursor {
		t.Fatalf("会话驱动名不对：%q", got)
	}
	if _, err := b.driverForSession(store.Get().Sessions[0]); err != nil {
		t.Fatal(err)
	}
	if len(*seen) == 0 || (*seen)[len(*seen)-1] != config.DriverCursor {
		t.Fatalf("应按会话的驱动名解析：%v", *seen)
	}

	if got := b.driverCommand("cursor"); !strings.Contains(got, "无需切换") {
		t.Fatalf("重复切换应提示：%s", got)
	}
	if got := b.driverCommand("gpt"); !strings.Contains(got, "可选") || !strings.Contains(got, "cursor") {
		t.Fatalf("未知 agent 应列出可选值：%s", got)
	}
}

func TestDriverCommandSummary(t *testing.T) {
	b, store, _ := newDriverAwareBridge(t)
	if err := store.Update(func(c *config.Config) error {
		c.Agent.Driver = config.DriverCodeBuddy
		c.Sessions[0].Driver = config.DriverCursor
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	out := b.driverCommand("")
	for _, want := range []string{"会话 agent", "Cursor", "全局默认", "CodeBuddy", "可选", "/driver%20codebuddy"} {
		if !strings.Contains(out, want) {
			t.Fatalf("查看结果缺少 %q：\n%s", want, out)
		}
	}
	if strings.Contains(out, "/driver%20cursor") {
		t.Fatalf("当前 agent 不需要切换标签：\n%s", out)
	}
}

// /model 必须按会话所属 agent 的目录校验：CodeBuddy 的模型不能切到 Cursor 会话上。
func TestSwitchModelUsesSessionDriverCatalog(t *testing.T) {
	b, store, _ := newDriverAwareBridge(t)
	if err := store.Update(func(c *config.Config) error {
		c.Sessions[0].Driver = config.DriverCursor
		c.Sessions[0].Model = ""
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if got := b.switchModel(ctx, "opus-5"); !strings.Contains(got, "模型未变") {
		t.Fatalf("CodeBuddy 的模型不应在 Cursor 会话里可用：%s", got)
	}
	if got := b.switchModel(ctx, "gpt-5"); !strings.Contains(got, "已切换") {
		t.Fatalf("Cursor 的模型应能切换：%s", got)
	}
	if got := store.Get().Sessions[0].Model; got != "gpt-5" {
		t.Fatalf("应写进会话：%q", got)
	}

	catalog, err := b.modelCatalog(ctx, config.DriverCursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Models) != 2 || !catalog.NoRates {
		t.Fatalf("应按驱动返回各自的目录：%+v", catalog)
	}
}
