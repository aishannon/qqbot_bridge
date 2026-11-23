package cli

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"qqbot_bridge/internal/config"
)

// runSetAgentPaths 是安装脚本用的子命令：把本机找到的 codebuddy / cursor-agent
// 绝对路径写入 config.yaml 的 agent.command 与 agent.commands，避免服务进程 PATH
// 不全时找不到可执行文件。不创建 sessions.yaml。
func runSetAgentPaths(args []string) int {
	fs := flag.NewFlagSet("set-agent-paths", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	codebuddy := fs.String("codebuddy", "", "codebuddy 可执行文件的绝对路径")
	cursor := fs.String("cursor", "", "cursor-agent 可执行文件的绝对路径")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `用法：qqbot_bridge set-agent-paths [--codebuddy PATH] [--cursor PATH]

把 agent 可执行文件的绝对路径写入配置（安装脚本调用）。
至少提供一个路径。可用 -home 指定数据目录。
`)
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cb := strings.TrimSpace(*codebuddy)
	cu := strings.TrimSpace(*cursor)
	if cb == "" && cu == "" {
		fmt.Fprintln(os.Stderr, "请至少提供 --codebuddy 或 --cursor")
		fs.Usage()
		return 2
	}
	for _, p := range []string{cb, cu} {
		if p == "" {
			continue
		}
		if fi, err := os.Stat(p); err != nil || fi.IsDir() {
			fmt.Fprintf(os.Stderr, "路径不可用：%s\n", p)
			return 1
		}
	}

	store, err := config.Open()
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取配置失败："+err.Error())
		return 1
	}
	if err := store.UpdateService(func(c *config.Config) error {
		if cb != "" {
			c.Agent.SetAgentCommand(config.DriverCodeBuddy, cb)
		}
		if cu != "" {
			c.Agent.SetAgentCommand(config.DriverCursor, cu)
		}
		// 只有 cursor、当前还是默认 codebuddy 时，直接切过去，少一次启动时的回落。
		if cb == "" && cu != "" && strings.TrimSpace(c.Agent.Driver) == config.DriverCodeBuddy {
			c.Agent.Driver = config.DriverCursor
			c.Agent.Command = cu
		}
		return nil
	}); err != nil {
		fmt.Fprintln(os.Stderr, "写入配置失败："+err.Error())
		return 1
	}

	cfg := store.Get()
	fmt.Printf("已写入 %s\n", store.ServicePath())
	fmt.Printf("  driver: %s\n", cfg.Agent.Driver)
	fmt.Printf("  command: %s\n", cfg.Agent.Command)
	for _, name := range []string{config.DriverCodeBuddy, config.DriverCursor} {
		if p := cfg.Agent.Commands[name]; p != "" {
			fmt.Printf("  commands.%s: %s\n", name, p)
		}
	}
	return 0
}
