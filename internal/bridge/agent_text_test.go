package bridge

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"qqbot_bridge/internal/agent"
	"qqbot_bridge/internal/config"
)

// legacySystemPrompt 是接入 Cursor 之前追加给 CodeBuddy 的原文，
// CodeBuddy 下的系统说明必须逐字不变。
const legacySystemPrompt = "你正在通过 QQ 单聊接收指令，回复会以 Markdown 消息发到用户手机：" +
	"可以用加粗、列表、行内代码和短代码块，但不要用表格，先给结论，正文尽量控制在 300 字以内。" +
	"用户随消息发来的图片/文件已经保存到工作目录的 .inbox/ 下，指令里会给绝对路径。" +
	"需要用户看到界面时，把截图保存到当前工作目录下（例如 .shots/shot.png），" +
	"桥接服务会自动把这一轮新生成的图片发回给用户。" +
	"用户要求组队、团队或多人并行时，必须先调用 TeamCreate 建队，再用带 name 参数的 Agent 派生成员，" +
	"不要用普通子代理顶替；成员的每条消息都会以「@角色名」单独推送到用户手机。"

func TestPromptFor(t *testing.T) {
	if got := promptFor(config.DriverCodeBuddy); got != legacySystemPrompt {
		t.Fatalf("CodeBuddy 的系统说明必须与原文一致：\n%s", got)
	}
	got := promptFor(config.DriverCursor)
	if strings.Contains(got, "TeamCreate") || got != basePrompt {
		t.Fatalf("Cursor 不应收到 CodeBuddy 专属的组队说明：\n%s", got)
	}
}

func TestAgentTitle(t *testing.T) {
	for in, want := range map[string]string{"codebuddy": "CodeBuddy", "": "CodeBuddy", "cursor": "Cursor", "other": "other"} {
		if got := agentTitle(in); got != want {
			t.Errorf("agentTitle(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// CodeBuddy 下帮助文案逐字不变；Cursor 下换名字，并且不列 /sync。
func TestRenderHelpByDriver(t *testing.T) {
	md, plain := renderHelp("CodeBuddy", true)
	for _, want := range []string{
		"同步到 CodeBuddy IDE 历史对话",
		"\n直接发文字或语音 = 交给当前会话的 CodeBuddy 执行，过程、结果和新生成的截图会自动发回来。\n",
	} {
		if !strings.Contains(md, want) || !strings.Contains(plain, want) {
			t.Fatalf("CodeBuddy 帮助缺少原文 %q", want)
		}
	}

	md, plain = renderHelp("Cursor", false)
	if strings.Contains(md, "/sync") || strings.Contains(plain, "/sync") {
		t.Fatalf("没有 IDE 历史库时不应列出 /sync：\n%s", plain)
	}
	if !strings.Contains(plain, "交给当前会话的 Cursor 执行") || strings.Contains(plain, "同步到 CodeBuddy") {
		t.Fatalf("Cursor 帮助文案不对：\n%s", plain)
	}
}

func cursorCatalog(n int) agent.ModelCatalog {
	c := agent.ModelCatalog{NoRates: true, UpdatedAt: time.Now()}
	c.Models = append(c.Models, agent.ModelInfo{ID: "auto", Name: "Auto (default)"})
	for i := range n - 1 {
		c.Models = append(c.Models, agent.ModelInfo{ID: fmt.Sprintf("claude-opus-%d", i), Name: fmt.Sprintf("Claude Opus %d", i)})
	}
	return c
}

// Cursor 没有倍率：不写「倍率未知」，保持 CLI 顺序，太多时截断并提示筛选。
func TestRenderModelListNoRates(t *testing.T) {
	now := time.Now()
	md, plain := renderModelList(cursorCatalog(100), modelListView{title: "Cursor", current: "auto", source: "全局默认"}, now)
	if strings.Contains(md, "倍率") && !strings.Contains(md, "Cursor 不提供倍率") {
		t.Fatalf("NoRates 时只该在标题说明不提供倍率：\n%s", md)
	}
	if strings.Contains(md, "倍率未知") || strings.Contains(plain, "倍率未知") {
		t.Fatalf("NoRates 时不应逐个写倍率未知：\n%s", plain)
	}
	for _, want := range []string{"**Cursor 模型**", "共 100 个模型", "★ **Auto (default)**", "还有 60 个没列出", "/models <关键词>"} {
		if !strings.Contains(md, want) {
			t.Fatalf("Markdown 缺少 %q：\n%s", want, md)
		}
	}
	if !strings.Contains(plain, "Claude Opus 0 · claude-opus-0") {
		t.Fatalf("纯文本要带 ID 方便手打：\n%s", plain)
	}

	md, _ = renderModelList(cursorCatalog(100), modelListView{title: "Cursor", source: "CLI 默认", filter: "OPUS 9"}, now)
	// 编号 0~98 里含 9 的：9、19…89（9 个）+ 90~98（9 个）= 18 个
	if !strings.Contains(md, "筛选「OPUS 9」：18 个") || strings.Contains(md, "还有") {
		t.Fatalf("关键词筛选不对：\n%s", md)
	}
	md, _ = renderModelList(cursorCatalog(5), modelListView{title: "Cursor", source: "CLI 默认", filter: "gpt"}, now)
	if !strings.Contains(md, "没有匹配的模型") {
		t.Fatalf("没有匹配时要提示：\n%s", md)
	}
	if got := describeModel(cursorCatalog(2), "auto"); got != "Auto (default)（`auto`）" {
		t.Fatalf("NoRates 时描述不带倍率：%q", got)
	}
}

// Cursor 会话的 /status：显示「Cursor 会话」，没有倍率时不出现零值时间。
func TestStatusTextCursor(t *testing.T) {
	b, store := newTestBridge(t)
	b.openDriver = func(config.Config, string) (agent.Driver, error) { return fakeModelDriver{}, nil }
	if err := store.Update(func(c *config.Config) error {
		c.Agent.Driver = config.DriverCursor
		for i := range c.Sessions {
			c.Sessions[i].AgentID = "chat-1"
			c.Sessions[i].Model = "auto"
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	out := b.statusText()
	if !strings.Contains(out, "Cursor 会话：`chat-1`") || strings.Contains(out, "CodeBuddy") {
		t.Fatalf("应显示 Cursor 会话：\n%s", out)
	}
	if !strings.Contains(out, "模型：`auto` · 来源：本会话") || strings.Contains(out, "倍率未知") || strings.Contains(out, "01-01") {
		t.Fatalf("没有倍率时不应出现倍率和零值时间：\n%s", out)
	}
}
