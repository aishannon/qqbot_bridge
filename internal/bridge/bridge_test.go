package bridge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"qqbot_bridge/internal/agent"
	"qqbot_bridge/internal/config"
	"qqbot_bridge/internal/gateway"
)

// fakeTask 只在 tasks 表里占位，用来验证 /status 的并行任务展示。
type fakeTask struct{}

func (fakeTask) SessionID() string     { return "" }
func (fakeTask) Result() string        { return "" }
func (fakeTask) Texts() []string       { return nil }
func (fakeTask) Tools() []string       { return nil }
func (fakeTask) IsError() bool         { return false }
func (fakeTask) Wait() error           { return nil }
func (fakeTask) Done() <-chan struct{} { return nil }
func (fakeTask) Stop()                 {}

func newTestBridge(t *testing.T) (*Bridge, *config.Store) {
	t.Helper()
	store, err := config.OpenAt(filepath.Join(t.TempDir(), "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	api := gateway.NewAPI(gateway.Credentials{AppID: "1", AppSecret: "s"})
	return New(store, api, nil), store
}

func TestRenderSessionListMarksCurrentWithCommandTags(t *testing.T) {
	_, store := newTestBridge(t)
	cfg := store.Get()

	markdown, plain := renderSessionList(cfg)
	if !strings.Contains(markdown, "★ **1.") {
		t.Fatalf("当前会话应有 ★ 标记：\n%s", markdown)
	}
	if !strings.Contains(markdown, `<qqbot-cmd-input text="/switch%201"`) {
		t.Fatalf("列表应带切换指令标签：\n%s", markdown)
	}
	if !strings.Contains(markdown, `reference="false"`) {
		t.Fatalf("指令标签应关闭引用：\n%s", markdown)
	}
	if strings.Contains(plain, "qqbot-cmd") {
		t.Fatalf("纯文本降级不能带标签源码：\n%s", plain)
	}
	if !strings.Contains(plain, "/switch") {
		t.Fatalf("纯文本里应保留 /switch 用法提示：\n%s", plain)
	}
}

func TestRenderSessionListMarksNewSessions(t *testing.T) {
	_, store := newTestBridge(t)
	if err := store.Update(func(c *config.Config) error {
		c.Sessions[0].New = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	markdown, plain := renderSessionList(store.Get())
	if !strings.Contains(markdown, "🔴") || !strings.Contains(markdown, "新增") {
		t.Fatalf("新增会话应有红色标记：\n%s", markdown)
	}
	if !strings.Contains(plain, "🔴") {
		t.Fatalf("纯文本也应保留红色标记：\n%s", plain)
	}
}

func TestClearNewFlags(t *testing.T) {
	b, store := newTestBridge(t)
	if err := store.Update(func(c *config.Config) error {
		c.Sessions[0].New = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	b.clearNewFlags()
	if store.Get().Sessions[0].New {
		t.Fatal("/list 发出后应清掉新增标记")
	}
}

func TestSwitchByIndexAndID(t *testing.T) {
	b, store := newTestBridge(t)
	b.newSession(t.TempDir(), "第二个")
	cfg := store.Get()
	if len(cfg.Sessions) != 2 {
		t.Fatalf("期望 2 个会话，实际 %d", len(cfg.Sessions))
	}
	second := cfg.Sessions[1].ID

	if out := b.switchSession("1"); !strings.Contains(out, "已切换") {
		t.Fatalf("按序号切换失败：%s", out)
	}
	if store.Get().Current != cfg.Sessions[0].ID {
		t.Fatal("当前会话没有切回第一个")
	}
	if out := b.switchSession(second); !strings.Contains(out, "已切换") {
		t.Fatalf("按 ID 切换失败：%s", out)
	}
	if store.Get().Current != second {
		t.Fatal("当前会话没有切到第二个")
	}
	if out := b.switchSession("99"); !strings.Contains(out, "没找到") {
		t.Fatalf("越界切换应报错：%s", out)
	}
}

func TestNewSessionCreatesDirAndDedupesID(t *testing.T) {
	b, store := newTestBridge(t)
	dir := filepath.Join(t.TempDir(), "demo")
	out := b.newSession(dir, "演示")
	if !strings.Contains(out, "已新建") {
		t.Fatalf("新建失败：%s", out)
	}
	first := store.Get().Current

	// 同一个目录再建一次：复用，不新增
	b.newSession(dir, "演示改名")
	if n := len(store.Get().Sessions); n != 2 {
		t.Fatalf("同目录应复用会话，实际 %d 个", n)
	}
	if store.Get().Current != first {
		t.Fatal("复用时应保持同一个会话")
	}

	// 不同目录同名：ID 去重加后缀
	other := filepath.Join(t.TempDir(), "demo")
	b.newSession(other, "另一个演示")
	ids := map[string]bool{}
	for _, s := range store.Get().Sessions {
		if ids[s.ID] {
			t.Fatalf("会话 ID 重复：%s", s.ID)
		}
		ids[s.ID] = true
	}
}

func TestHelpAndStatusText(t *testing.T) {
	b, _ := newTestBridge(t)
	md, _ := renderHelp("CodeBuddy", true)
	if !strings.Contains(md, "/switch") {
		t.Fatal("帮助里应列出 /switch")
	}
	if out := b.statusText(); !strings.Contains(out, "当前会话") {
		t.Fatalf("状态文本异常：%s", out)
	} else if !strings.Contains(out, "Git：") {
		t.Fatalf("状态里应带 Git 信息：%s", out)
	}
	if out := b.stopCommand(nil); !strings.Contains(out, "没有正在执行") {
		t.Fatalf("空闲任务中断提示异常：%s", out)
	}
}

func TestStatusShowsParallelTasks(t *testing.T) {
	b, store := newTestBridge(t)
	b.newSession(t.TempDir(), "第二个")
	cfg := store.Get()

	// 给第一个会话挂一个占位任务，当前会话是第二个
	b.mu.Lock()
	b.tasks[cfg.Sessions[0].ID] = fakeTask{}
	b.last[cfg.Sessions[0].ID] = TaskSnapshot{Running: true, StartedAt: time.Now()}
	b.mu.Unlock()

	out := b.statusText()
	if !strings.Contains(out, "并行任务") || !strings.Contains(out, "默认会话") {
		t.Fatalf("状态里应列出并行任务：%s", out)
	}
}

func TestHumanDuration(t *testing.T) {
	if got := humanDuration(45e9); got != "45 秒" {
		t.Fatalf("秒级格式错误：%s", got)
	}
	if got := humanDuration(125e9); got != "2 分 5 秒" {
		t.Fatalf("分级格式错误：%s", got)
	}
}

func TestResetClearsAgentID(t *testing.T) {
	b, store := newTestBridge(t)
	if err := store.Update(func(c *config.Config) error {
		c.Sessions[0].AgentID = "agent-1"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	out := b.resetSession()
	if !strings.Contains(out, "已重置") {
		t.Fatalf("重置提示异常：%s", out)
	}
	cfg := store.Get()
	if cfg.Sessions[0].AgentID != "" {
		t.Fatalf("AgentID 应被清空：%q", cfg.Sessions[0].AgentID)
	}
	if cfg.Sessions[0].Dir == "" {
		t.Fatal("重置不应动工作目录")
	}
}

func TestRenameSession(t *testing.T) {
	b, store := newTestBridge(t)
	out := b.renameSession("  新名字  ")
	if !strings.Contains(out, "新名字") {
		t.Fatalf("重命名提示异常：%s", out)
	}
	if got := store.Get().Sessions[0].Name; got != "新名字" {
		t.Fatalf("会话名未更新：%q", got)
	}
	if out := b.renameSession("   "); !strings.Contains(out, "不能为空") {
		t.Fatalf("空名字应被拒绝：%s", out)
	}
}

func TestRemoveSession(t *testing.T) {
	b, store := newTestBridge(t)

	// 只剩一个会话时不允许删除
	if out := b.removeSession("1"); !strings.Contains(out, "最后一个") {
		t.Fatalf("应拒绝删除最后一个会话：%s", out)
	}

	b.newSession(t.TempDir(), "第二个")
	cur := store.Get().Current
	if cur == store.Get().Sessions[0].ID {
		t.Fatal("新建后当前会话应切换")
	}
	if out := b.removeSession("99"); !strings.Contains(out, "没找到") {
		t.Fatalf("越界删除应报错：%s", out)
	}

	out := b.removeSession(cur)
	if !strings.Contains(out, "已移除") {
		t.Fatalf("删除提示异常：%s", out)
	}
	cfg := store.Get()
	if len(cfg.Sessions) != 1 {
		t.Fatalf("应剩 1 个会话，实际 %d", len(cfg.Sessions))
	}
	if cfg.Current == cur {
		t.Fatal("删掉当前会话后应自动切换")
	}
}

func TestTeammateText(t *testing.T) {
	md, plain := teammateText(agent.Event{Name: "reviewer", Summary: "审查结论", Text: "发现 2 个问题"})
	if md != "**@reviewer** · 审查结论\n\n发现 2 个问题" {
		t.Fatalf("Markdown 格式不对：%q", md)
	}
	if plain != "@reviewer · 审查结论\n发现 2 个问题" {
		t.Fatalf("纯文本格式不对：%q", plain)
	}
	if md, plain := teammateText(agent.Event{Text: "只有正文"}); md != "**@成员**\n\n只有正文" || plain != "@成员\n只有正文" {
		t.Fatalf("缺角色名时应兜底：%q / %q", md, plain)
	}
	if md, plain := teammateText(agent.Event{Name: "a", Summary: "s"}); md != "**@a** · s" || plain != "@a · s" {
		t.Fatalf("只有摘要时不应多空行：%q / %q", md, plain)
	}
}

func TestTeammateEventsFromTranscript(t *testing.T) {
	line := []byte(`{"type":"message","role":"user","content":[{"type":"input_text","text":"<teammate-message teammate_id=\"reviewer\" summary=\"结论\">\n发现 2 个问题\n</teammate-message>"}]}`)
	got := teammateEventsFromTranscript(line)
	if len(got) != 1 {
		t.Fatalf("应解析出 1 条成员消息，实际 %d 条", len(got))
	}
	if got[0].Name != "reviewer" || got[0].Summary != "结论" || got[0].Text != "发现 2 个问题" {
		t.Fatalf("解析结果不对：%+v", got[0])
	}

	plain := []byte(`{"role":"user","content":[{"type":"input_text","text":"普通消息"}]}`)
	if got := teammateEventsFromTranscript(plain); len(got) != 0 {
		t.Fatalf("普通消息不应解析出成员消息：%v", got)
	}

	assistant := []byte(`{"role":"assistant","content":[{"type":"text","text":"<teammate-message teammate_id=\"a\">x</teammate-message>"}]}`)
	if got := teammateEventsFromTranscript(assistant); len(got) != 0 {
		t.Fatalf("非 user 角色不应上报：%v", got)
	}
}

func TestForwardTeammatesReadsTailLine(t *testing.T) {
	var sent int32
	api := newTestAPI(t, &sent, nil)
	r := NewReplier(api, "u1", "msg-1", 4, false)
	b, _ := newTestBridge(t)

	path := filepath.Join(t.TempDir(), "s.jsonl")
	line := `{"role":"user","content":[{"type":"input_text","text":"<teammate-message teammate_id=\"a\" summary=\"s\">正文</teammate-message>"}]}`
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}

	seen := make(map[string]bool)
	off := b.forwardTeammates(path, 0, seen, r, false)
	if off != 0 || sent != 0 {
		t.Fatalf("未收尾时半行不应发送：offset=%d sent=%d", off, sent)
	}
	off = b.forwardTeammates(path, off, seen, r, true)
	if off == 0 || sent != 1 {
		t.Fatalf("收尾时应补发最后一行：offset=%d sent=%d", off, sent)
	}
	b.forwardTeammates(path, 0, seen, r, true)
	if sent != 1 {
		t.Fatalf("重复读取应去重，实际发送 %d 次", sent)
	}
}

func TestTeammateTranscriptSkipsSystem(t *testing.T) {
	line := []byte(`{"role":"user","content":[{"type":"input_text","text":"<teammate-message teammate_id=\"system\">框架通知</teammate-message>"}]}`)
	if got := teammateEventsFromTranscript(line); len(got) != 0 {
		t.Fatalf("框架通知不应转发：%v", got)
	}
}

// 帮助里的每条指令都要能点：Markdown 版带指令标签，纯文本版不能漏出标签源码。
func TestRenderHelpHasCmdTags(t *testing.T) {
	md, plain := renderHelp("CodeBuddy", true)

	for _, cmd := range []string{
		"/list", "/switch", "/new", "/rename", "/rm", "/reset",
		"/models", "/model", "/model default", "/driver",
		"/status", "/stop", "/sync", "/menu", "/ping", "/whoami", "/allow", "/deny", "/help",
	} {
		tag := `<qqbot-cmd-input text="` + escapeTagValue(cmd) + `"`
		if !strings.Contains(md, tag) {
			t.Fatalf("帮助里 %s 没有可点标签：\n%s", cmd, md)
		}
		if !strings.Contains(plain, cmd) {
			t.Fatalf("纯文本帮助应列出 %s：\n%s", cmd, plain)
		}
	}
	if strings.Contains(plain, "qqbot-cmd") || strings.Contains(plain, "<!--") {
		t.Fatalf("纯文本不能带标签源码：\n%s", plain)
	}
}

// 不带参数的 /new 在当前会话目录里再开一个，同目录允许多个会话。
func TestNewSessionHere(t *testing.T) {
	b, store := newTestBridge(t)
	dir := t.TempDir()
	if err := store.Update(func(c *config.Config) error {
		c.Sessions = []config.Session{{ID: "demo", Name: "demo", Dir: dir}}
		c.Current = "demo"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	out := b.newSessionHere()
	if !strings.Contains(out, "已新建") {
		t.Fatalf("应提示新建：%s", out)
	}
	cfg := store.Get()
	if len(cfg.Sessions) != 2 {
		t.Fatalf("同目录应允许两个会话，实际 %d 个", len(cfg.Sessions))
	}
	second := cfg.Sessions[1]
	if second.Dir != dir || second.ID != "demo-2" || second.Name != "demo-2" {
		t.Fatalf("新会话应带后缀且共用目录：%+v", second)
	}
	if cfg.Current != second.ID {
		t.Fatalf("应切到新会话：%s", cfg.Current)
	}
	// 再开一个，后缀递增
	b.newSessionHere()
	if got := store.Get().Sessions[2].ID; got != "demo-3" {
		t.Fatalf("后缀应递增：%s", got)
	}
}

func TestStatusShowsModel(t *testing.T) {
	b, store := newTestBridge(t)
	if err := store.Update(func(c *config.Config) error {
		c.Agent.Model = "deepseek-v4.1-flash"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if out := b.statusText(); !strings.Contains(out, "Agent：CodeBuddy") || !strings.Contains(out, "模型：`deepseek-v4.1-flash`") {
		t.Fatalf("状态里应显示 agent 与模型：%s", out)
	}
}
