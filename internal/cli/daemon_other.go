//go:build !unix

package cli

import "errors"

// daemonEnv 在非 unix 平台上不起作用，只为和 unix 实现保持同名。
const daemonEnv = "QQBOT_BRIDGE_DAEMON"

func startDaemon([]string) error {
	return errors.New("当前平台不支持后台运行，请用 -f 前台启动")
}

func stopDaemon() error { return errors.New("当前平台不支持后台运行") }

func readPid() (int, error) { return 0, errors.New("当前平台不支持后台运行") }

func processAlive(int) bool { return false }

func writePid(int) error { return nil }

func removePid() {}
