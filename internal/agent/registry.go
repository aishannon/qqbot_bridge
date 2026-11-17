package agent

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// DefaultDriver 是没有配置 agent.driver 时使用的驱动。
const DefaultDriver = "codebuddy"

// Settings 是构造驱动时可用的本机配置。
//
// 驱动只拿自己需要的字段，避免反向依赖 internal/config。
type Settings struct {
	// Command 是可执行文件名，空表示用驱动自己的默认值。
	Command string
	// DiscoverRoot 是会话索引目录，只有具备 SessionDiscoverer 能力的驱动才用得上。
	DiscoverRoot string
}

// Factory 按配置构造一个驱动实例。
type Factory func(Settings) Driver

var registry = struct {
	mu      sync.RWMutex
	drivers map[string]Factory
}{drivers: make(map[string]Factory, 4)}

// Register 登记一个驱动实现，由驱动包的 init() 调用。
//
// 名字重复或为空时直接 panic：这属于编程错误，应该在启动时就暴露，
// 而不是等到用户发指令才发现驱动被悄悄覆盖。
func Register(name string, factory Factory) {
	name = strings.TrimSpace(name)
	if name == "" || factory == nil {
		panic("agent: 注册驱动需要非空名字和工厂函数")
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, dup := registry.drivers[name]; dup {
		panic("agent: 驱动重复注册: " + name)
	}
	registry.drivers[name] = factory
}

// Drivers 返回已登记的驱动名，按字典序排列。
func Drivers() []string {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	out := make([]string, 0, len(registry.drivers))
	for name := range registry.drivers {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Open 按名字构造驱动；名字为空时用 DefaultDriver。
//
// 名字没登记时返回带可选值列表的错误，方便用户对照修改 config.yaml。
func Open(name string, settings Settings) (Driver, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = DefaultDriver
	}
	registry.mu.RLock()
	factory, ok := registry.drivers[name]
	registry.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("未知的 agent.driver %q，可选值：%s", name, strings.Join(Drivers(), "、"))
	}
	return factory(settings), nil
}
