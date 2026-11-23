// Package config 负责持久化桥接服务的配置。
//
// 配置拆成两个 YAML 文件，都在 ~/.qqbot_bridge/ 下，权限 0600：
//
//	config.yaml    服务配置：机器人凭据与白名单、CodeBuddy 启动参数、回传策略、管理端地址
//	sessions.yaml  会话配置：工作目录会话列表、当前会话、被用户移除的自动发现目录
//
// 前者是人会手工编辑的、很少变；后者随着 QQ 里 /new /switch /rm 等操作频繁变化。
// 拆开后，会话的日常变动不会改写含 AppSecret 的服务配置。
// 里面有机器人 AppSecret，只在本地读写，不写日志、不进仓库。
package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Bind 是扫码绑定得到的机器人身份。
type Bind struct {
	AppID      string    `json:"appId" yaml:"appId"`
	Secret     string    `json:"secret" yaml:"secret"`
	UserOpenID string    `json:"userOpenId" yaml:"userOpenId"`
	BoundAt    time.Time `json:"boundAt" yaml:"boundAt,omitempty"`
	// Whitelist 是允许使用机器人的用户 openid，单聊和群聊共用。
	// 为空时只允许绑定者本人，避免升级后老配置突然对所有人开放。
	Whitelist []string `json:"whitelist,omitempty" yaml:"whitelist,omitempty"`
	// Names 记录见过的人 openid -> 昵称，只用于展示，不参与鉴权。
	Names map[string]string `json:"names,omitempty" yaml:"names,omitempty"`
}

// Label 返回「`openid`（昵称）」，没有昵称时只返回 openid。
func (b Bind) Label(openID string) string {
	if openID == "" {
		return ""
	}
	name := strings.TrimSpace(b.Names[openID])
	if name == "" {
		return "`" + openID + "`"
	}
	return "`" + openID + "`（" + name + "）"
}

// Allowed 判断某个用户能否使用机器人：绑定者始终允许，其余看白名单。
func (b Bind) Allowed(openID string) bool {
	if openID == "" {
		return false
	}
	if b.UserOpenID != "" && openID == b.UserOpenID {
		return true
	}
	for _, id := range b.Whitelist {
		if id == openID {
			return true
		}
	}
	return false
}

// Bound 表示是否已经绑定过机器人。
func (b Bind) Bound() bool { return b.AppID != "" && b.Secret != "" }

// Agent 是驱动本机 CLI agent 的启动参数。
type Agent struct {
	// Driver 选择 agent 驱动的实现，默认 codebuddy。
	// 本机找不到 codebuddy 这个命令时，启动会改用 cursor，见 FallbackFromMissingCodeBuddy。
	Driver string `json:"driver" yaml:"driver"`
	// Command 是当前全局驱动的可执行文件；安装脚本会写成绝对路径，避免 systemd/后台进程 PATH 不全。
	Command string `json:"command" yaml:"command"`
	// Commands 按驱动名存可执行文件绝对路径（键为 codebuddy / cursor）。
	// 会话切到非全局驱动时走这里，避免只配了一个 agent.command 时另一边仍依赖 PATH。
	Commands       map[string]string `json:"commands,omitempty" yaml:"commands,omitempty"`
	Model          string            `json:"model" yaml:"model"`                   // 为空则用 CLI 默认模型
	PermissionMode string            `json:"permissionMode" yaml:"permissionMode"` // 默认 bypassPermissions，让 QQ 指令能直接落地
	MaxTurns       int               `json:"maxTurns" yaml:"maxTurns"`             // 单轮最多 agent 轮次，0 表示不限制
	TimeoutMinutes int               `json:"timeoutMinutes" yaml:"timeoutMinutes"` // 单个任务最长执行时间，默认 30 分钟
}

// agent.driver 的可选值。
const (
	DriverCodeBuddy = "codebuddy"
	DriverCursor    = "cursor"
)

// DefaultCommand 返回某个驱动的默认可执行文件名。
//
// 放在这里而不是 internal/agent，是为了让 config 不反向依赖驱动包；
// 键名和默认值的变化都要同步 README 的配置表。
func DefaultCommand(driver string) string {
	switch strings.TrimSpace(driver) {
	case DriverCursor:
		return "cursor-agent"
	default:
		return DriverCodeBuddy
	}
}

// commandForDriver 返回某个驱动在本 Agent 里配置的可执行文件（未配置则空）。
func (a Agent) commandForDriver(driver string) string {
	driver = strings.TrimSpace(driver)
	if driver == "" {
		return ""
	}
	if path := strings.TrimSpace(a.Commands[driver]); path != "" {
		return path
	}
	if driver == strings.TrimSpace(a.Driver) || (strings.TrimSpace(a.Driver) == "" && driver == DriverCodeBuddy) {
		return strings.TrimSpace(a.Command)
	}
	return ""
}

// FallbackFromMissingCodeBuddy 在要用 codebuddy、但本机找不到对应命令时改用 cursor-agent。
//
// 已经选了别的驱动，或者 codebuddy 的命令确实能启动，就原样返回。
// 只改驱动名和命令，模型、超时这些字段保留。available 用来探测命令，测试里替换掉。
func (a Agent) FallbackFromMissingCodeBuddy(available func(string) bool) (Agent, bool) {
	driver := strings.TrimSpace(a.Driver)
	if driver == "" {
		driver = DriverCodeBuddy
	}
	if driver != DriverCodeBuddy {
		return a, false
	}
	cmd := a.commandForDriver(DriverCodeBuddy)
	if cmd == "" {
		cmd = DefaultCommand(DriverCodeBuddy)
	}
	if available != nil && available(cmd) {
		return a, false
	}
	// 先读 cursor 路径再改 Driver，避免 commandForDriver 把旧的 codebuddy command 当成 cursor 的。
	cursorCmd := strings.TrimSpace(a.Commands[DriverCursor])
	a.Driver = DriverCursor
	if cursorCmd != "" {
		a.Command = cursorCmd
	} else {
		a.Command = DefaultCommand(DriverCursor)
	}
	return a, true
}

// CommandAvailable 判断命令能否在本机启动。带目录的路径直接查文件，否则查 PATH。
func CommandAvailable(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	_, err := exec.LookPath(name)
	return err == nil
}

// CommandFor 返回某个驱动该用的可执行文件。
//
// 优先用 agent.commands 里按驱动存的绝对路径；否则全局驱动用 agent.command，
// 其它驱动回落到各自的默认命令名（避免把 codebuddy 的路径传给 cursor-agent）。
func (c Config) CommandFor(driver string) string {
	driver = strings.TrimSpace(driver)
	if driver == "" {
		driver = c.Agent.Driver
	}
	if path := c.Agent.commandForDriver(driver); path != "" {
		return path
	}
	return DefaultCommand(driver)
}

// SetAgentCommand 把某个驱动的可执行文件路径写入 Commands，并在它是当前全局驱动时同步 Command。
func (a *Agent) SetAgentCommand(driver, path string) {
	driver = strings.TrimSpace(driver)
	path = strings.TrimSpace(path)
	if driver == "" || path == "" {
		return
	}
	if a.Commands == nil {
		a.Commands = map[string]string{}
	}
	a.Commands[driver] = path
	if strings.TrimSpace(a.Driver) == driver || (strings.TrimSpace(a.Driver) == "" && driver == DriverCodeBuddy) {
		a.Command = path
	}
}

// KnownDriver 判断名字是不是已实现的驱动。
//
// 会话级 driver 只能在这个范围里选：写错名字会让服务起不来，而不是静默回落到
// 全局驱动——后者会让用户以为切过去了，实际还在用另一个 agent。
func KnownDriver(name string) bool {
	switch strings.TrimSpace(name) {
	case DriverCodeBuddy, DriverCursor:
		return true
	}
	return false
}

// DriverOf 返回某个会话实际使用的驱动名：会话指定优先，否则跟随全局。
func (c Config) DriverOf(s Session) string {
	if name := strings.TrimSpace(s.Driver); name != "" {
		return name
	}
	return c.Agent.Driver
}

// Drivers 返回配置里用到的所有驱动（去重，全局默认在前）。
//
// 启动预检要逐个检查登录状态，所以需要这份清单。
func (c Config) Drivers() []string {
	out := make([]string, 0, 3)
	seen := make(map[string]bool, 3)
	add := func(name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	add(c.Agent.Driver)
	for _, s := range c.Sessions {
		add(strings.TrimSpace(s.Driver))
	}
	return out
}

// DefaultTimeout 返回单任务超时，未配置时取 30 分钟。
const DefaultTimeoutMinutes = 30

// Timeout 把配置的分钟数转成 duration。
func (a Agent) Timeout() time.Duration {
	m := a.TimeoutMinutes
	if m <= 0 {
		m = DefaultTimeoutMinutes
	}
	return time.Duration(m) * time.Minute
}

// Session 是一个可切换的 agent 会话：一个工作目录 + 一段连续对话。
type Session struct {
	ID        string    `json:"id" yaml:"id"`                             // 短标识，QQ 端 /switch 用
	Name      string    `json:"name" yaml:"name"`                         // 展示名
	Dir       string    `json:"dir" yaml:"dir"`                           // 工作目录
	AgentID   string    `json:"agentId" yaml:"agentId"`                   // 当前 agent 的会话 ID（codebuddy 的 sessionId / cursor 的 chatId），用于续聊
	Driver    string    `json:"driver,omitempty" yaml:"driver,omitempty"` // 这个会话用哪个 agent；空表示跟随全局 agent.driver
	Model     string    `json:"model,omitempty" yaml:"model,omitempty"`   // 这个会话单独指定的模型；空表示跟随全局默认
	Auto      bool      `json:"auto,omitempty" yaml:"auto,omitempty"`
	New       bool      `json:"new,omitempty" yaml:"new,omitempty"` // 自动注册后还没在 /list 里展示过
	CreatedAt time.Time `json:"createdAt" yaml:"createdAt"`
	LastUsed  time.Time `json:"lastUsed" yaml:"lastUsed,omitempty"`
}

// Push 控制把工作过程推回 QQ 的策略。
type Push struct {
	// MaxMessages 是一次任务里最多走几条被动回复。
	// C2C 每个用户消息最多被动回复 4 次，默认 4（确认 + 过程 + 结果 + 截图）；
	// 超出的部分自动改用主动消息，所以这里是预算而不是硬上限。
	MaxMessages int `json:"maxMessages" yaml:"maxMessages"`
	// ShowToolCalls 为 true 时把工具调用摘进过程消息。
	ShowToolCalls bool `json:"showToolCalls" yaml:"showToolCalls"`
	// SendImages 为 true 时把本轮新生成的截图/图片主动发回。
	SendImages bool `json:"sendImages" yaml:"sendImages"`
	// ImageMax 是单次任务最多发几张图。
	ImageMax int `json:"imageMax" yaml:"imageMax"`
	// Markdown 为 true 时消息按 msg_type=2 发送；机器人没有 Markdown 权限会自动退回纯文本。
	Markdown bool `json:"markdown" yaml:"markdown"`
}

// Web 是本地控制台监听参数。
type Web struct {
	Addr string `json:"addr" yaml:"addr"`
}

// Menu 控制单聊输入框下方的快捷菜单。
type Menu struct {
	// Disable 为 true 时不在启动时自动配置菜单（菜单是全局的，会覆盖手工配置）
	Disable bool `json:"disable" yaml:"disable"`
}

// Discover 控制从 CodeBuddy 本地会话索引里自动注册工作目录。
type Discover struct {
	// Disable 为 true 时不再自动注册。
	Disable bool `json:"disable" yaml:"disable"`
	// Root 是 CodeBuddy 的会话索引目录，默认 ~/.codebuddy/sessions。
	Root string `json:"root,omitempty" yaml:"root,omitempty"`
	// Ignore 是用户主动移除过的目录，自动注册时跳过。属于会话状态，存在 sessions.yaml 里。
	Ignore []string `json:"ignore,omitempty" yaml:"-"`
}

// Config 是内存里的全量配置；落盘时按 serviceFile / sessionsFile 拆成两个文件。
type Config struct {
	Bind     Bind      `json:"bind"`
	Agent    Agent     `json:"agent"`
	Sessions []Session `json:"sessions"`
	Current  string    `json:"current"`
	Push     Push      `json:"push"`
	Menu     Menu      `json:"menu"`
	Discover Discover  `json:"discover"`
	Web      Web       `json:"web"`
}

// Default 返回默认配置。默认会话取当前工作目录，避免指向不存在的路径。
func Default() *Config {
	home, _ := os.UserHomeDir()
	dir, err := os.Getwd()
	if err != nil || dir == "" {
		dir = home
	}
	return &Config{
		Agent: Agent{
			Driver:         DriverCodeBuddy,
			Command:        DefaultCommand(DriverCodeBuddy),
			PermissionMode: "bypassPermissions",
			TimeoutMinutes: DefaultTimeoutMinutes,
		},
		Push: Push{
			MaxMessages:   4,
			ShowToolCalls: true,
			SendImages:    true,
			ImageMax:      2,
			Markdown:      true,
		},
		Web: Web{Addr: "127.0.0.1:8686"},
		Discover: Discover{
			Root: filepath.Join(home, ".codebuddy", "sessions"),
		},
		Sessions: []Session{{
			ID:        "main",
			Name:      "默认会话",
			Dir:       dir,
			CreatedAt: time.Now(),
		}},
		Current: "main",
	}
}
