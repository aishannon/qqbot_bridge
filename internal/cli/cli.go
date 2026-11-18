// Package cli 实现 qqbot_bridge 的命令行入口：子命令分发、后台化与服务装配。
//
// 根目录的 main 包只负责把 -ldflags 注入的版本信息转发进来并调用 Main，
// 保证 `go build .` 与 Makefile / install.sh 的构建方式不变。
package cli

import (
	"flag"
	"fmt"
	"net"
	"os"
	"strings"

	"qqbot_bridge/internal/config"
)

// usage 是 -h / help 的输出：先列子命令，再列服务运行参数。
func usage() {
	out := flag.CommandLine.Output()
	fmt.Fprintf(out, `qqbot_bridge %s：把手机 QQ 的单聊/群聊接到本机 CodeBuddy

用法：
  qqbot_bridge [参数]            启动服务（默认后台运行）
  qqbot_bridge <子命令> [参数]

子命令：
  config    服务初始化：扫码绑定后立刻连接 QQ 网关，再设置管理端密码
              -force            已绑定时也重新扫码
              -yes              不交互，全部用默认答案（脚本用）
              -out <文件>       二维码图片输出路径（默认 ~/.qqbot_bridge/qr-bind.png）
              -source <标识>    扫码来源标识（默认 qqbot_bridge）
              -timeout <时长>   等待扫码的最长时间（默认 10m）
  bind      只做扫码绑定（config 的旧名字，行为不变）
  passwd    设置 Web 管理端的访问密码（没设置前管理端拒绝所有访问）
              交互输入两次；脚本里可 echo '新密码' | qqbot_bridge passwd
  version   显示版本号和构建时间（同 -v）
  help      显示本帮助

服务参数：
`, Version)
	flag.PrintDefaults()
	fmt.Fprint(out, `
常用示例：
  qqbot_bridge config            首次使用：扫码绑定并连上 QQ，然后设置密码
  qqbot_bridge passwd            设置管理端密码
  qqbot_bridge                   后台启动；管理端默认 http://127.0.0.1:8686
  qqbot_bridge -f                前台运行，日志打到终端（systemd 托管时用）
  qqbot_bridge -stop             停止后台实例
  qqbot_bridge -v                显示版本号和构建时间

配置与数据目录：~/.qqbot_bridge/（可用 -home 或 $QQBOT_BRIDGE_HOME 换掉，多实例靠它隔离）
  config.yaml    服务配置（机器人凭据、白名单、agent 参数、回传策略、管理端地址）
  sessions.yaml  会话配置（工作目录会话列表、当前会话）
  web-auth.json  管理端密码哈希    messages/  收到的消息记录    logs/  运行日志

一台机器跑多套（每个实例一个 QQ 机器人，appid 不能共用）：
  qqbot_bridge -home ~/.qqbot_bridge-cursor config -addr 127.0.0.1:8687
  qqbot_bridge -home ~/.qqbot_bridge-cursor
  qqbot_bridge -home ~/.qqbot_bridge-cursor -stop
`)
}

// preparseHome 在子命令分发和 flag.Parse 之前处理 -home。
//
// 子命令各自持有独立的 FlagSet，而数据目录必须在读取配置之前就确定
// （config.Open、pid 文件、日志目录都依赖它），所以这里先把 -home 从
// 参数里剥出来，剩下的参数再交给原来的解析流程。
func preparseHome(args []string) ([]string, error) {
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		var dir string
		switch name, val, hasVal := strings.Cut(arg, "="); {
		case arg == "-home" || arg == "--home":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("%s 后面要跟数据目录，例如 -home ~/.qqbot_bridge-cursor", arg)
			}
			i++
			dir = args[i]
		case hasVal && (name == "-home" || name == "--home"):
			dir = val
		default:
			rest = append(rest, arg)
			continue
		}
		if err := config.SetDir(dir); err != nil {
			return nil, err
		}
		// 交给后台子进程：startDaemon 只透传参数、不再带 -home，
		// 靠环境变量保证 daemon 化之后仍然是同一个目录。
		if err := os.Setenv(config.HomeEnv, config.Dir()); err != nil {
			return nil, err
		}
	}
	return rest, nil
}

// Main 解析命令行并执行对应子命令，返回进程退出码。
func Main(args []string) int {
	// -home 必须先于子命令分发和 flag.Parse 生效：配置目录决定了后面读哪个 config.yaml
	rest, err := preparseHome(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "参数错误："+err.Error())
		return 2
	}
	args = rest

	addr := flag.String("addr", "", "控制台监听地址，默认取配置（127.0.0.1:8686）")
	home := flag.String("home", "", "数据目录，默认 ~/.qqbot_bridge；多实例时每个实例一个（同 $QQBOT_BRIDGE_HOME）")
	foreground := flag.Bool("f", false, "前台运行，日志直接打到终端（默认后台运行）")
	stopFlag := flag.Bool("stop", false, "停止在后台运行的实例")
	showVersion := flag.Bool("v", false, "显示版本号和构建时间")
	flag.BoolVar(showVersion, "version", false, "同 -v")
	flag.Usage = usage

	// 子命令：qqbot_bridge bind 扫码绑定机器人（必须在 flag.Parse 之前处理）
	if len(args) > 0 {
		switch args[0] {
		case "config", "setup", "init":
			return runConfig(args[1:])
		case "bind":
			return runBind(args[1:])
		case "passwd":
			return runPasswd(args[1:])
		case "help", "-help", "--help":
			flag.CommandLine.SetOutput(os.Stdout)
			usage()
			return 0
		case "version":
			fmt.Print(versionText())
			return 0
		}
	}

	// flag.CommandLine 是 ExitOnError：解析失败会自己打印用法并退出，行为与旧版一致
	_ = flag.CommandLine.Parse(args)

	// 正常路径上 -home 已被 preparseHome 剥离；这里兜住「参数写在 -- 之后」等漏网情况
	if *home != "" {
		if err := config.SetDir(*home); err != nil {
			fmt.Fprintln(os.Stderr, "参数错误："+err.Error())
			return 2
		}
		_ = os.Setenv(config.HomeEnv, config.Dir())
	}

	if *showVersion {
		fmt.Print(versionText())
		return 0
	}

	if *stopFlag {
		if err := stopDaemon(); err != nil {
			fmt.Fprintln(os.Stderr, "停止失败："+err.Error())
			return 1
		}
		fmt.Println("已停止后台实例")
		return 0
	}

	// 默认后台运行：父进程拉起一个脱离终端的自己就退出，加了 -f 才留在前台。
	// daemonEnv 由子进程继承，用来避免自我重启成死循环。
	if !*foreground && os.Getenv(daemonEnv) == "" {
		if err := startDaemon(args); err != nil {
			fmt.Fprintln(os.Stderr, "后台启动失败："+err.Error())
			return 1
		}
		return 0
	}

	return serve(*addr)
}

// isLoopbackAddr 判断监听地址是否只对本机开放。":8686"、"0.0.0.0:8686" 都是对外开放。
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// displayAddr 把 0.0.0.0:8686 这类监听地址换成可点击的本地地址。
func displayAddr(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "127.0.0.1" + addr
	}
	return strings.Replace(addr, "0.0.0.0", "127.0.0.1", 1)
}
