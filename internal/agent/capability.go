package agent

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ModelInfo 是 agent 当前可选的一个模型及其倍率。
type ModelInfo struct {
	ID      string
	Name    string
	Rate    float64
	HasRate bool
}

// RateText 返回倍率文案，没有倍率数据时明确说未知，不猜。
func (m ModelInfo) RateText() string {
	if !m.HasRate {
		return "倍率未知"
	}
	return fmt.Sprintf("x%.2f", m.Rate)
}

// ModelCatalog 是一次查询到的模型目录。
type ModelCatalog struct {
	Models     []ModelInfo
	UpdatedAt  time.Time // 倍率数据的更新时间
	RefreshErr error     // 刷新失败时非空，此时 Models 是旧缓存
	// NoRates 表示这个 CLI 根本不提供倍率（例如 Cursor），展示时不再逐个写
	// 「倍率未知」，列表也保持 CLI 给出的顺序。
	NoRates bool
}

// Find 按 ID 或展示名查模型，不区分大小写。
func (c ModelCatalog) Find(arg string) (ModelInfo, bool) {
	arg = strings.TrimSpace(arg)
	for _, m := range c.Models {
		if strings.EqualFold(m.ID, arg) || strings.EqualFold(m.Name, arg) {
			return m, true
		}
	}
	return ModelInfo{}, false
}

// MirrorResult 是一次把会话镜像进 IDE 历史库的结果。
type MirrorResult struct {
	ConversationID string
	Name           string
	Messages       int
	Store          string
}

// ModelProvider 是「能列出可选模型和倍率」的驱动能力。
//
// 模型是各 CLI 自己的概念：CodeBuddy 从 --help 和本地缓存里读，Cursor 走
// 自己的模型列表。桥接只用它渲染 /models、/model 和 /status。
type ModelProvider interface {
	// Models 查询模型目录，可能触发一次刷新，耗时可达数十秒。
	Models(ctx context.Context) (ModelCatalog, error)
	// ModelRate 只读本地缓存里的倍率，不触发刷新，保证 /status 秒回。
	ModelRate(id string) (text string, at time.Time, ok bool)
}

// ModelNamer 是「不刷新就能拿到模型展示名」的可选能力。
//
// 确认消息要立刻发出，不能为了一个展示名去走网络；驱动只读自己的本地缓存，
// 没有这条模型时返回 false，调用方退回模型 ID。
type ModelNamer interface {
	ModelName(id string) (string, bool)
}

// DefaultModeler 是「能读出 CLI 自己当前默认模型」的可选能力。
//
// 桥接没配模型时，CLI 的默认模型才是真正在跑的那个；确认消息用它展示具体
// 模型名，而不是只说一句「CLI 默认」。
type DefaultModeler interface {
	DefaultModel() (string, bool)
}

// SessionDiscoverer 是「能列出本机正在使用的会话目录」的驱动能力。
//
// 具备这个能力的驱动，桥接启动时会把已有目录自动注册成 QQ 会话。
type SessionDiscoverer interface {
	// DiscoverDirs 返回去重后的工作目录，按最近使用排序。
	DiscoverDirs() ([]string, error)
}

// TranscriptProvider 是「能找到会话 transcript 原始文件」的驱动能力。
//
// transcript 是 JSONL 事件流，桥接用它跟读团队成员消息、给 Web 管理端
// 展示完整对话。
type TranscriptProvider interface {
	// TranscriptPath 返回 dir 下 sessionID 对话的文件路径；ok 为 false
	// 表示这个驱动没有这种文件。
	TranscriptPath(dir, sessionID string) (path string, ok bool)
}

// Preflight 是「启动前自检」的驱动能力，例如检查 CLI 是否已登录。
//
// 服务启动时调用一次，失败只告警、不阻止启动：Web 管理端扫码、改配置
// 这些功能不依赖 agent，用户也可能在服务起来之后才去登录。
type Preflight interface {
	// Check 返回带操作指引的错误，nil 表示可以正常接任务。
	Check(ctx context.Context) error
}

// HistoryMirror 是「能把对话写进 IDE 历史对话」的驱动能力。
//
// 只有带自家 IDE 的 agent 才有这项能力；不具备时 /sync 与周期同步静默跳过。
type HistoryMirror interface {
	// Mirror 把 transcript 镜像进 cwd 对应的 IDE 历史库。
	Mirror(cwd, sessionID, transcript string) (MirrorResult, error)
}
