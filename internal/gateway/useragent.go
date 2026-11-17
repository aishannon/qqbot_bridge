package gateway

import (
	"fmt"
	"runtime"
	"strings"
	"sync/atomic"
)

// userAgent 是所有对 QQ 平台请求（取 token、OpenAPI、网关握手）统一使用的 User-Agent。
var userAgent atomic.Value

// BuildUserAgent 拼出默认（CodeBuddy）的 User-Agent：
//
//	qqbot_bridge/<桥接版本> (codebuddy/<CodeBuddy 版本>; Go/<Go 版本>; <系统>)
//
// CodeBuddy 版本探测不到时写 unknown，不省略，便于在平台侧区分。
func BuildUserAgent(appVersion, codebuddyVersion string) string {
	return BuildUserAgentFor(appVersion, "codebuddy", codebuddyVersion)
}

// BuildUserAgentFor 按驱动名拼出 User-Agent：
//
//	qqbot_bridge/<桥接版本> (<驱动>/<agent 版本>; Go/<Go 版本>; <系统>)
//
// 驱动名留空时按 codebuddy 处理，保证老调用方的 UA 字节级不变。
func BuildUserAgentFor(appVersion, driverName, agentVersion string) string {
	return fmt.Sprintf("qqbot_bridge/%s (%s/%s; Go/%s; %s)",
		orDefault(appVersion, "dev"),
		orDefault(driverName, "codebuddy"),
		orDefault(agentVersion, "unknown"),
		strings.TrimPrefix(runtime.Version(), "go"),
		runtime.GOOS)
}

// SetUserAgent 设置全局 User-Agent，应在建立连接前调用。
func SetUserAgent(ua string) {
	if strings.TrimSpace(ua) != "" {
		userAgent.Store(ua)
	}
}

// UserAgent 返回当前 User-Agent，未设置时用不含 CodeBuddy 版本的默认值。
func UserAgent() string {
	if v, ok := userAgent.Load().(string); ok && v != "" {
		return v
	}
	return BuildUserAgent("", "")
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return strings.TrimSpace(s)
}
