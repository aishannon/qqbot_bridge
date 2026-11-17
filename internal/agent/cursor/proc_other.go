//go:build !unix

package cursor

import (
	"os/exec"
	"time"
)

// configureStop 在非 Unix 平台上没有进程组可用，退回默认的 Kill，
// 只额外设置 WaitDelay，保证中断后 Wait 不会无限等管道。
func configureStop(cmd *exec.Cmd, _ <-chan struct{}) {
	cmd.WaitDelay = stopGrace + 3*time.Second
}
