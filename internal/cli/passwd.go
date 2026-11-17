package cli

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"qqbot_bridge/internal/web"
)

// runPasswd 是 `qqbot_bridge passwd` 子命令：设置 Web 管理端的访问密码。
//
//	qqbot_bridge passwd                      # 交互式输入两次（不回显）
//	echo '新密码' | qqbot_bridge passwd      # 从标准输入读取，适合脚本
//
// 密码不走命令行参数，避免留在 shell 历史和进程列表里。运行中的服务无需重启，
// 下一次登录就会用新密码；改密码后所有已登录的浏览器都需要重新登录。
func runPasswd(args []string) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "-help" || args[0] == "--help") {
		fmt.Println("用法：qqbot_bridge passwd\n  设置 Web 管理端的访问密码（至少 8 位）。\n  交互输入两次；脚本里可 echo '新密码' | qqbot_bridge passwd。\n  密码不接受命令行参数，避免留在 shell 历史和进程列表里。")
		return 0
	}
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "用法：qqbot_bridge passwd（密码通过交互输入或标准输入提供，不接受命令行参数）")
		return 2
	}

	var password string
	if isTTY(os.Stdin) {
		first, err := readSecret("新密码（至少 8 位）：")
		if err != nil {
			fmt.Fprintln(os.Stderr, "读取密码失败："+err.Error())
			return 1
		}
		again, err := readSecret("再输入一次：")
		if err != nil {
			fmt.Fprintln(os.Stderr, "读取密码失败："+err.Error())
			return 1
		}
		if first != again {
			fmt.Fprintln(os.Stderr, "两次输入不一致")
			return 1
		}
		password = first
	} else {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			fmt.Fprintln(os.Stderr, "标准输入里没有读到密码")
			return 1
		}
		password = strings.TrimRight(line, "\r\n")
	}

	if err := web.SetPassword(web.AuthPath(), password); err != nil {
		fmt.Fprintln(os.Stderr, "设置失败："+err.Error())
		return 1
	}
	fmt.Println("访问密码已更新，已登录的浏览器需要重新登录。")
	return 0
}

// readSecret 提示并读取一行，输入时关闭终端回显。
func readSecret(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	off := exec.Command("stty", "-echo")
	off.Stdin = os.Stdin
	echoOff := off.Run() == nil
	if echoOff {
		defer func() {
			on := exec.Command("stty", "echo")
			on.Stdin = os.Stdin
			_ = on.Run()
			fmt.Fprintln(os.Stderr)
		}()
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
