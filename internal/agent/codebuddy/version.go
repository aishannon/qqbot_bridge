package codebuddy

import (
	"context"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

var versionRe = regexp.MustCompile(`\d+(?:\.\d+)+(?:[-+][0-9A-Za-z.\-]+)?`)

// Version 探测本机 codebuddy 的版本号（如 2.181.0）。
//
// 探测失败返回空串，调用方自行决定如何展示（User-Agent 里会写 unknown）。
func (d *driver) Version(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, d.command, "--version").Output()
	if err != nil {
		return ""
	}
	return parseVersion(string(out))
}

// parseVersion 从 --version 的输出里取第一个形如 1.2.3 的版本号。
func parseVersion(out string) string {
	return strings.TrimSpace(versionRe.FindString(out))
}
