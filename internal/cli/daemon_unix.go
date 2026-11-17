//go:build unix

package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"qqbot_bridge/internal/config"
)

// daemonEnv 标记当前进程已经是后台子进程，避免无限自我重启。
const daemonEnv = "QQBOT_BRIDGE_DAEMON"

func pidPath() string { return filepath.Join(config.Dir(), "qqbot_bridge.pid") }

func daemonLogPath() string { return filepath.Join(config.Dir(), "logs", "daemon.log") }

// startDaemon 用自身可执行文件重启一个脱离终端的子进程，父进程立刻返回。
func startDaemon(args []string) error {
	if pid, err := readPid(); err == nil && processAlive(pid) {
		return fmt.Errorf("已有实例在运行（pid=%d），先执行 -stop 停止", pid)
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logPath := daemonLogPath()
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer logFile.Close()

	cmd := exec.Command(exe, childArgs(args)...)
	cmd.Env = append(os.Environ(), daemonEnv+"=1")
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// Setsid 让子进程脱离当前终端会话，关掉终端也不会被带走
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()

	if err := writePid(pid); err != nil {
		return err
	}
	fmt.Printf("已在后台启动，pid=%d\n日志：%s\n停止：%s -stop\n", pid, logPath, os.Args[0])
	return nil
}

// childArgs 去掉只对父进程有意义的参数，其余原样透传（比如 -addr）。
func childArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if isParentOnlyFlag(a) {
			continue
		}
		out = append(out, a)
	}
	return out
}

func isParentOnlyFlag(arg string) bool {
	for _, name := range []string{"-f", "--f", "-foreground", "--foreground", "-stop", "--stop"} {
		if arg == name {
			return true
		}
		if strings.HasPrefix(arg, name+"=") {
			return true
		}
	}
	return false
}

// stopDaemon 给后台实例发 SIGTERM，并等它自己退出。
func stopDaemon() error {
	pid, err := readPid()
	if err != nil {
		return fmt.Errorf("数据目录 %s 里没有找到在后台运行的实例", config.Dir())
	}
	if !processAlive(pid) {
		removePid()
		return fmt.Errorf("后台实例（pid=%d）已经不在运行，已清理 pid 文件", pid)
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("发送停止信号失败: %w", err)
	}
	// 最多等 5 秒，等它把正在跑的任务收尾
	for i := 0; i < 50; i++ {
		time.Sleep(100 * time.Millisecond)
		if !processAlive(pid) {
			removePid()
			return nil
		}
	}
	return fmt.Errorf("已发送停止信号，但 pid=%d 还没退出", pid)
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	// 信号 0 只做存在性检查，不真的发信号
	return syscall.Kill(pid, syscall.Signal(0)) == nil
}

func writePid(pid int) error {
	if err := os.MkdirAll(config.Dir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(pidPath(), []byte(strconv.Itoa(pid)+"\n"), 0o644)
}

func readPid() (int, error) {
	data, err := os.ReadFile(pidPath())
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, errors.New("pid 文件内容不合法")
	}
	return pid, nil
}

func removePid() {
	_ = os.Remove(pidPath())
}
