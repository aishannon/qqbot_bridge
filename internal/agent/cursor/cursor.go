// Package cursor 把 Cursor CLI（cursor-agent）当成一个可编程的 agent 来驱动。
//
// 每轮任务起一个 cursor-agent 进程，走 headless 模式：
//
//	cursor-agent -p --output-format stream-json --trust --workspace <dir> \
//	    [--force --approve-mcps] [--model <id>] [--resume <chatId>] -- <prompt>
//
// prompt 只能放在 argv 里（Cursor 没有 --input-format），stdout 逐行是 NDJSON
// 事件（system/init、assistant、thinking、tool_call、result）。和 CodeBuddy
// 一样每轮一个进程、驱动本身无状态，所以直接套进 agent.Driver 模型。
//
// 选它而不是 `cursor-agent acp`：ACP 是常驻进程 + JSON-RPC，要自己维护会话、
// 及时回复权限/提问请求，否则会卡死，和「每轮一个进程」的桥接模型不匹配。
//
// 除了任务执行，这个包还实现 Cursor 具备的可选能力：模型列表（ModelProvider）、
// transcript 定位（TranscriptProvider）和登录预检（Preflight）。Cursor 的
// transcript 里没有工作目录、也没有可写入的外部历史库，所以不实现
// SessionDiscoverer 和 HistoryMirror。
package cursor

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"qqbot_bridge/internal/agent"
)

const (
	// Name 是配置里 agent.driver 的取值。
	Name = "cursor"
	// DefaultCommand 是 Cursor CLI 的默认可执行文件名。
	DefaultCommand = "cursor-agent"

	// maxArgPrompt 是直接放进 argv 的 prompt 上限。Linux 单个参数最长
	// 128KB（MAX_ARG_STRLEN），留出余量，超过就落盘让 agent 自己去读。
	maxArgPrompt = 100 << 10
	// inboxDir 是工作目录下存放用户附件和超长指令的目录，与桥接约定一致。
	inboxDir = ".inbox"
)

// 编译期确认实现了桥接会探测的可选能力。
var (
	_ agent.ModelProvider      = (*driver)(nil)
	_ agent.ModelNamer         = (*driver)(nil)
	_ agent.TranscriptProvider = (*driver)(nil)
	_ agent.Preflight          = (*driver)(nil)
)

func init() { agent.Register(Name, newDriver) }

// newDriver 按配置构造驱动；没配可执行文件时用 cursor-agent。
func newDriver(s agent.Settings) agent.Driver {
	return &driver{command: cmp.Or(strings.TrimSpace(s.Command), DefaultCommand)}
}

// driver 是 Cursor CLI 的驱动实现。
type driver struct {
	command string
	// home 覆盖用户主目录，只给测试用；空表示 os.UserHomeDir。
	home string
}

// Name 返回驱动名。
func (d *driver) Name() string { return Name }

// Version 返回 cursor-agent 的版本号（如 2026.11.13-eacade1），探测失败返回空串。
func (d *driver) Version(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, d.command, "--version").Output()
	if err != nil {
		return ""
	}
	return parseVersion(string(out))
}

// parseVersion 取 --version 输出的第一行非空内容。
func parseVersion(out string) string {
	for line := range strings.SplitSeq(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// Start 启动一轮任务：prompt 走 argv，stdout 读 stream-json 事件。
func (d *driver) Start(ctx context.Context, opts agent.Options, prompt string) (agent.Task, error) {
	prompt, err := spillPrompt(opts.Dir, composePrompt(opts, prompt), time.Now())
	if err != nil {
		return nil, err
	}

	runCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(runCtx, d.command, buildArgs(opts, prompt)...)
	cmd.Dir = opts.Dir
	// stdin 留空（/dev/null）：-p 模式下 CLI 不需要输入，也避免它等管道。
	cmd.Stdin = nil

	t := newTask(cancel, opts.Dir)
	configureStop(cmd, t.exited)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stderr := &lockedBuffer{}
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("启动 %s 失败: %w", d.command, err)
	}
	go t.run(cmd, stdout, stderr, opts.OnEvent)
	return t, nil
}

// buildArgs 把通用的任务选项翻译成 cursor-agent 的命令行参数。
//
// prompt 前面加 `--`，以减号开头的指令（比如「-v 是什么意思」）不会被当成选项。
func buildArgs(opts agent.Options, prompt string) []string {
	args := []string{"-p", "--output-format", "stream-json", "--trust"}
	if opts.Dir != "" {
		args = append(args, "--workspace", opts.Dir)
	}
	args = append(args, permissionArgs(opts.PermissionMode)...)
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.Resume != "" {
		args = append(args, "--resume", opts.Resume)
	}
	// MaxTurns：cursor-agent 没有对应参数，只能忽略（默认 0 本来就是不限制）。
	return append(args, "--", prompt)
}

// permissionArgs 把 CodeBuddy 风格的权限模式映射到 Cursor 的参数。
//
//   - bypassPermissions（默认）：--force --approve-mcps，手机上没法逐条确认，
//     不放开的话 shell、MCP、工作区外写入都会被直接拒绝。
//   - plan：--mode plan，只读规划。
//   - 其他（default、acceptEdits…）：不加强制参数，走 CLI 自己的 allowlist，
//     团队禁用了 Run Everything 时用这个。
func permissionArgs(mode string) []string {
	switch mode {
	case "", "bypassPermissions":
		return []string{"--force", "--approve-mcps"}
	case "plan":
		return []string{"--mode", "plan"}
	default:
		return nil
	}
}

// composePrompt 把桥接追加的系统说明拼到新会话的第一条 prompt 前面。
//
// Cursor 没有 --append-system-prompt。续聊时历史里已经有这段说明，再拼一次
// 只会浪费上下文，所以只在新会话注入。不往用户仓库写 .cursor/rules，
// 免得污染用户的代码库。
func composePrompt(opts agent.Options, prompt string) string {
	sp := strings.TrimSpace(opts.AppendSystemPrompt)
	if sp == "" || opts.Resume != "" {
		return prompt
	}
	return "<system_instructions>\n" + sp + "\n</system_instructions>\n\n" + prompt
}

// spillPrompt 在 prompt 超过 argv 上限时把它写进 <dir>/.inbox，
// argv 里换成一句「先读这个文件」。
func spillPrompt(dir, prompt string, now time.Time) (string, error) {
	if len(prompt) <= maxArgPrompt {
		return prompt, nil
	}
	if dir == "" {
		return "", fmt.Errorf("指令太长（%d 字节），又没有工作目录可以落盘", len(prompt))
	}
	inbox := filepath.Join(dir, inboxDir)
	if err := os.MkdirAll(inbox, 0o755); err != nil {
		return "", fmt.Errorf("指令太长，落盘失败：%w", err)
	}
	path := filepath.Join(inbox, "prompt-"+now.Format("20060102-150405.000")+".md")
	if err := os.WriteFile(path, []byte(prompt), 0o644); err != nil {
		return "", fmt.Errorf("指令太长，落盘失败：%w", err)
	}
	return fmt.Sprintf("本轮指令较长（%d 字节），已完整保存到文件 `%s`。请先完整阅读这个文件，然后严格按其中的指令执行。",
		len(prompt), path), nil
}
