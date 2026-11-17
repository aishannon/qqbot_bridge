package bridge

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"qqbot_bridge/internal/agent"
	"qqbot_bridge/internal/config"
)

func testCatalog() agent.ModelCatalog {
	return agent.ModelCatalog{
		UpdatedAt: time.Date(2026, 11, 7, 11, 50, 0, 0, time.Local),
		Models: []agent.ModelInfo{
			{ID: "flash", Name: "Flash", Rate: 0.11, HasRate: true},
			{ID: "opus-5", Name: "Opus 5", Rate: 2.17, HasRate: true},
		},
	}
}

func TestRenderModelList(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.Local)
	md, plain := renderModelList(testCatalog(), modelListView{title: "CodeBuddy", current: "opus-5", source: "本会话", canReset: true}, now)

	for _, want := range []string{"**CodeBuddy 模型**", "★ **Opus 5** · x2.17", "Flash · x0.11", "/model%20flash", "今天 11:50", "/model%20default", "来源：本会话"} {
		if !strings.Contains(md, want) {
			t.Fatalf("Markdown 缺少 %q：\n%s", want, md)
		}
	}
	if strings.Contains(md, "/model%20opus-5") {
		t.Fatalf("当前模型不需要切换标签：\n%s", md)
	}
	if strings.Contains(plain, "qqbot-cmd") || !strings.Contains(plain, "Flash · x0.11 · flash") {
		t.Fatalf("纯文本不能带标签，且要有 ID：\n%s", plain)
	}

	// 跟随全局时没有「恢复默认」入口；不是今天的数据要带日期；刷新失败要明说
	stale := testCatalog()
	stale.RefreshErr = errors.New("boom")
	md, _ = renderModelList(stale, modelListView{title: "CodeBuddy", current: "flash", source: "全局默认"}, now.AddDate(0, 0, 1))
	if strings.Contains(md, "/model%20default") || !strings.Contains(md, "来源：全局默认") {
		t.Fatalf("跟随全局的展示不对：\n%s", md)
	}
	if !strings.Contains(md, "11-07 11:50") || !strings.Contains(md, "旧数据") {
		t.Fatalf("旧数据要标明日期和失败：\n%s", md)
	}
}

// fakeModelDriver 只实现模型相关能力，桥接的模型指令测试用它注入目录。
type fakeModelDriver struct {
	catalog agent.ModelCatalog
	err     error
}

func (fakeModelDriver) Name() string { return "codebuddy" }

func (fakeModelDriver) Start(context.Context, agent.Options, string) (agent.Task, error) {
	return nil, errors.New("测试不应启动任务")
}

func (fakeModelDriver) Version(context.Context) string { return "" }

func (d fakeModelDriver) Models(context.Context) (agent.ModelCatalog, error) {
	return d.catalog, d.err
}

func (fakeModelDriver) ModelRate(string) (string, time.Time, bool) {
	return "", time.Time{}, false
}

func (d fakeModelDriver) ModelName(id string) (string, bool) {
	if m, ok := d.catalog.Find(id); ok {
		return m.Name, true
	}
	return "", false
}

func newModelBridge(t *testing.T, loadErr error) (*Bridge, *config.Store) {
	t.Helper()
	b, store := newTestBridge(t)
	b.openDriver = func(config.Config, string) (agent.Driver, error) {
		return fakeModelDriver{catalog: testCatalog(), err: loadErr}, nil
	}
	return b, store
}

// 会话模型优先，其次全局默认，都没设就是 CLI 默认。
func TestModelForPriority(t *testing.T) {
	cfg := config.Config{}
	sess := config.Session{}
	if got := modelFor(cfg, sess); got != "" {
		t.Fatalf("都没设时应返回空（CLI 默认）：%q", got)
	}
	if got := modelSource(cfg, sess); got != "CLI 默认" {
		t.Fatalf("来源不对：%s", got)
	}

	cfg.Agent.Model = "flash"
	if got := modelFor(cfg, sess); got != "flash" || modelSource(cfg, sess) != "全局默认" {
		t.Fatalf("应跟随全局默认：%q/%s", got, modelSource(cfg, sess))
	}

	sess.Model = "opus-5"
	if got := modelFor(cfg, sess); got != "opus-5" || modelSource(cfg, sess) != "本会话" {
		t.Fatalf("会话模型应优先：%q/%s", got, modelSource(cfg, sess))
	}
}

// /model 只改当前会话，不动全局配置，也不影响其他会话。
func TestSwitchModel(t *testing.T) {
	b, store := newModelBridge(t, nil)
	ctx := context.Background()

	if got := b.switchModel(ctx, "opus 5"); !strings.Contains(got, "已切换") {
		t.Fatalf("按名称切换失败：%s", got)
	}
	after := store.Get()
	if s, _ := currentSession(after); s.Model != "opus-5" {
		t.Fatalf("应写进会话：%q", s.Model)
	}
	if after.Agent.Model != "" {
		t.Fatalf("不应改全局默认：%q", after.Agent.Model)
	}
	if got := b.switchModel(ctx, "OPUS-5"); !strings.Contains(got, "无需切换") {
		t.Fatalf("重复切换应提示：%s", got)
	}
	if got := b.switchModel(ctx, "nope"); !strings.Contains(got, "模型未变") {
		t.Fatalf("未知模型应拒绝：%s", got)
	}
	if s, _ := currentSession(store.Get()); s.Model != "opus-5" {
		t.Fatalf("拒绝时不应改会话模型：%q", s.Model)
	}
	if got := b.switchModel(ctx, "default"); !strings.Contains(got, "已切换") {
		t.Fatalf("恢复默认失败：%s", got)
	}
	if s, _ := currentSession(store.Get()); s.Model != "" {
		t.Fatalf("恢复默认应清空会话模型：%q", s.Model)
	}
	if got := b.switchModel(ctx, "default"); !strings.Contains(got, "无需切换") {
		t.Fatalf("已经是默认时应提示：%s", got)
	}
}

// 查不到目录时无法校验，不能把模型写进会话，否则之后任务可能启动失败。
func TestSwitchModelKeepsConfigOnLoadError(t *testing.T) {
	b, store := newModelBridge(t, errors.New("no cache"))
	got := b.switchModel(context.Background(), "flash")
	s, _ := currentSession(store.Get())
	if !strings.Contains(got, "模型未变") || s.Model != "" {
		t.Fatalf("查询失败不应改配置：%s / %q", got, s.Model)
	}
}

// 确认消息的尾部标签要同时写明 agent 与模型展示名。
func TestModelFooter(t *testing.T) {
	b, store := newModelBridge(t, nil)
	if err := store.Update(func(c *config.Config) error {
		c.Agent.Model = "opus-5"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sess, _ := currentSession(store.Get())
	if got := b.modelFooter(sess); !strings.Contains(got, "Agent：CodeBuddy") || !strings.Contains(got, "Opus 5") {
		t.Fatalf("应同时带 agent 与模型展示名：%s", got)
	}

	if err := store.Update(func(c *config.Config) error {
		c.Agent.Model = ""
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sess, _ = currentSession(store.Get())
	if got := b.modelFooter(sess); !strings.Contains(got, "模型：CLI 默认") {
		t.Fatalf("没配模型时应写 CLI 默认：%s", got)
	}
}
