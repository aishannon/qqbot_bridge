//go:build unix

package cursor

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// configureStop 让中断能收掉 cursor-agent 起的所有子进程。
//
// cursor-agent 是 Node 进程，shell 工具会再派生子进程（比如长时间的
// sleep / go test）。只杀 Node 主进程的话这些子进程会变成孤儿继续跑，
// 所以放进独立进程组，中断时对整组发信号：先 SIGINT 让 CLI 体面收尾
// （它会打印 Aborting operation 后退出），stopGrace 后还没退就整组 SIGKILL。
func configureStop(cmd *exec.Cmd, exited <-chan struct{}) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		pid := cmd.Process.Pid
		err := syscall.Kill(-pid, syscall.SIGINT)
		time.AfterFunc(stopGrace, func() {
			select {
			case <-exited:
			default:
				_ = syscall.Kill(-pid, syscall.SIGKILL)
			}
		})
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	// 兜底：强杀之后管道还没关（极端情况下孙进程攥着 stdout）也别让 Wait 永远挂着
	cmd.WaitDelay = stopGrace + 3*time.Second
}
