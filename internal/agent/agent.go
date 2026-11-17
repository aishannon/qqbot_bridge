// Package agent 定义驱动本机 CLI agent 的通用抽象。
//
// 桥接本身不关心 agent 是 CodeBuddy 还是 Cursor：每轮任务由一个 Driver 起
// 进程，按统一的事件模型（assistant 文本、thinking、tool_use、result）回传。
// 具体实现放在子包里，用 init() 注册到本包的注册表：
//
//	internal/agent/codebuddy   CodeBuddy CLI（stream-json 协议）
//	internal/agent/cursor      Cursor CLI（占位，尚未实现）
//
// 只有所有驱动都具备的能力才放进 Driver；模型目录、会话索引、transcript、
// IDE 历史库这类某一家独有的能力用可选接口（见 capability.go）表达，
// 调用方用类型断言探测，不具备时走各自的兜底提示。
package agent

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"
)

// Options 描述一次任务怎么跑。
type Options struct {
	// Dir 是工作目录，agent 的所有相对路径都基于它。
	Dir string
	// Model 为空则用 CLI 默认模型。
	Model string
	// PermissionMode 默认 bypassPermissions，让 QQ 指令能直接落地。
	PermissionMode string
	// MaxTurns 为 0 表示不限制。
	MaxTurns int
	// Resume 是上一轮的会话 ID，空表示新会话。
	Resume string
	// AppendSystemPrompt 追加到系统提示词的说明。
	AppendSystemPrompt string
	// OnEvent 事件回调，在读取 goroutine 中同步调用。
	OnEvent func(Event)
}

// Event 是一次任务中的单条事件。
type Event struct {
	Kind      string // system / thinking / text / tool_use / tool_result / result / teammate
	Text      string
	Tool      string
	Input     string
	SessionID string
	Duration  time.Duration
	Turns     int
	IsError   bool
	// Name / Summary 只在 Kind 为 teammate 时有值：团队成员的名称和消息摘要。
	Name    string
	Summary string
}

// Task 是一轮正在执行的 agent 任务。
type Task interface {
	// SessionID 返回 agent 侧会话 ID（通常在 init 事件后才有值）。
	SessionID() string
	// Result 返回最终结果文本。
	Result() string
	// Texts 返回本轮 assistant 输出的所有文本块。
	Texts() []string
	// Tools 返回本轮调用过的工具名（按调用顺序，可能重复）。
	Tools() []string
	// IsError 表示本轮是否以错误结束。
	IsError() bool
	// Wait 等待任务结束（进程退出或 result 事件后关闭输入）。
	Wait() error
	// Done 返回结束信号。
	Done() <-chan struct{}
	// Stop 中断任务。
	Stop()
}

// Driver 是一个本机 CLI agent 的驱动实现。
//
// 实现应当无状态：每轮任务都是一次新的进程调用，Driver 自身只保存
// 可执行文件路径等静态信息，因此可以按需构造，不需要池化。
type Driver interface {
	// Name 是驱动名，与配置里的 agent.driver 取值一致。
	Name() string
	// Start 启动一轮任务，把 prompt 交给 agent。
	Start(ctx context.Context, opts Options, prompt string) (Task, error)
	// Version 返回本机 CLI 的版本号，探测失败返回空串。
	Version(ctx context.Context) string
}

// ErrNotImplemented 表示驱动只是登记了名字，还不能真正执行任务。
var ErrNotImplemented = errors.New("agent 驱动尚未实现")

// teammateMsgRe 匹配团队成员回流到主会话的消息块。
var (
	teammateMsgRe  = regexp.MustCompile(`(?s)<teammate-message\s+([^>]*)>(.*?)</teammate-message>`)
	teammateAttrRe = regexp.MustCompile(`([\w-]+)="([^"]*)"`)
)

// ParseTeammateMessages 从一段文本里解析出所有团队成员消息。
// 返回的 Event 的 Kind 为 teammate，Name 是成员名，Summary 是摘要。
//
// 团队成员的正文只会写进 agent 的 transcript（以及以 user 事件回流到主进程），
// 格式是各驱动共享的约定，所以解析放在通用层。
func ParseTeammateMessages(text string) []Event {
	matches := teammateMsgRe.FindAllStringSubmatch(text, -1)
	out := make([]Event, 0, len(matches))
	for _, m := range matches {
		attrs := make(map[string]string, 4)
		for _, a := range teammateAttrRe.FindAllStringSubmatch(m[1], -1) {
			attrs[a[1]] = a[2]
		}
		name := attrs["teammate_id"]
		if name == "" {
			name = attrs["name"]
		}
		body := strings.TrimSpace(m[2])
		if name == "" && body == "" {
			continue
		}
		out = append(out, Event{
			Kind:    "teammate",
			Name:    name,
			Summary: attrs["summary"],
			Text:    body,
		})
	}
	return out
}
