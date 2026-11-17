package cli

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
	"time"
)

// Version 和 BuildTime 由根目录 main 包通过
// -ldflags "-X main.version=... -X main.buildTime=..." 注入后转发进来，
// 链接期只能改 main 包里的变量，所以真正取值放在那里。
// BuildTime 用 UTC 的 RFC3339，例如 2026-11-08T09:40:12Z。
var (
	Version   = "dev"
	BuildTime = ""
)

// buildInfo 汇总版本、构建时间、提交和运行环境，给 -v 和启动日志用。
type buildInfo struct {
	Version  string
	Built    string // 已格式化为本地时间；未知时为空
	BuiltRaw string
	Commit   string
	Go       string
	Platform string
}

func currentBuildInfo() buildInfo {
	bi := buildInfo{
		Version:  Version,
		Go:       runtime.Version(),
		Platform: runtime.GOOS + "/" + runtime.GOARCH,
	}
	raw := BuildTime
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				bi.Commit = s.Value
			case "vcs.time":
				if raw == "" { // 没通过 make 构建时，退回到最近提交的时间
					raw = s.Value
				}
			}
		}
	}
	bi.BuiltRaw = raw
	bi.Built = formatBuildTime(raw)
	return bi
}

// formatBuildTime 把 RFC3339 时间转成本地时区显示；解析不了就原样返回。
func formatBuildTime(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return raw
	}
	return t.Local().Format("2006-01-02 15:04:05 -0700")
}

// versionText 是 `qqbot_bridge -v` 的输出。
func versionText() string {
	bi := currentBuildInfo()
	built := bi.Built
	if built == "" {
		built = "未知（不是通过 make 或 install.sh 构建的）"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "qqbot_bridge %s\n", bi.Version)
	fmt.Fprintf(&sb, "构建时间：%s\n", built)
	if bi.Commit != "" {
		fmt.Fprintf(&sb, "提交：%s\n", bi.Commit)
	}
	fmt.Fprintf(&sb, "运行环境：%s %s\n", bi.Go, bi.Platform)
	return sb.String()
}

// versionLine 是启动日志里的一行摘要。
func versionLine() string {
	bi := currentBuildInfo()
	if bi.Built == "" {
		return bi.Version
	}
	return fmt.Sprintf("%s（构建于 %s）", bi.Version, bi.Built)
}
