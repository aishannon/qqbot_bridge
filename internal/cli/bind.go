package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"qqbot_bridge/internal/agent"
	"qqbot_bridge/internal/agent/codebuddy"
	"qqbot_bridge/internal/config"
	"qqbot_bridge/internal/qrconnect"
	"qqbot_bridge/internal/termqr"
)

// runBind 是 `qqbot_bridge bind` 子命令：完成一次 QQ 机器人扫码绑定，
// 把凭据写进本地配置。返回进程退出码。
//
//	qqbot_bridge bind                  # 终端里出二维码，等手机 QQ 扫码
//	qqbot_bridge bind -out qr.png      # 同时指定二维码图片路径
func runBind(args []string) int {
	fs := flag.NewFlagSet("bind", flag.ContinueOnError)
	out := fs.String("out", filepath.Join(config.Dir(), "qr-bind.png"), "二维码图片输出路径")
	source := fs.String("source", "qqbot_bridge", "扫码来源标识")
	timeout := fs.Duration("timeout", 10*time.Minute, "等待扫码的最长时间")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	cred, err := bindRobot(bindOpts{out: *out, source: *source, timeout: *timeout})
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	if err := saveBind(cred); err != nil {
		fmt.Fprintf(os.Stderr, "保存凭据失败：%v\n", err)
		return 1
	}
	fmt.Printf("BIND_OK appid=%s user_openid=%s\n", cred.AppID, cred.UserOpenID)
	fmt.Println("如果服务已在运行，请重启让新凭据生效：systemctl --user restart qqbot_bridge，或 qqbot_bridge -stop 后重新启动")
	return 0
}

// printQR 同时输出终端可扫的字符二维码、图片文件与原始链接。
func printQR(path, content string) {
	if err := termqr.Render(os.Stdout, content, isTTY(os.Stdout)); err != nil {
		fmt.Fprintf(os.Stderr, "生成终端二维码失败：%v\n", err)
	}
	if dir := filepath.Dir(path); dir != "" {
		_ = os.MkdirAll(dir, 0o700)
	}
	if err := qrcode.WriteFile(content, termqr.Level(), 512, path); err != nil {
		fmt.Fprintf(os.Stderr, "生成二维码图片失败：%v\n", err)
	} else if abs, err := filepath.Abs(path); err == nil {
		fmt.Printf("二维码图片：%s\n", abs)
	}
	fmt.Printf("二维码链接：%s\n", content)
	fmt.Println("请用手机 QQ 扫码确认绑定，等待中…")
}

// isTTY 判断输出是不是交互式终端；重定向到文件时不用 ANSI 配色。
func isTTY(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// codebuddyVersion 探测本机 CodeBuddy CLI 版本，用于扫码请求的 User-Agent。
//
// 扫码发生在还没有配置上下文的时候，所以固定按 CodeBuddy 探测，与服务默认
// 驱动的 UA 形态保持一致。
func codebuddyVersion() string {
	driver, err := agent.Open(codebuddy.Name, agent.Settings{Command: codebuddy.DefaultCommand})
	if err != nil {
		return ""
	}
	return driver.Version(context.Background())
}

func saveBind(cred qrconnect.Credential) error {
	store, err := config.Open()
	if err != nil {
		return err
	}
	return store.Update(func(c *config.Config) error {
		c.Bind = config.Bind{
			AppID:      cred.AppID,
			Secret:     cred.AppSecret,
			UserOpenID: cred.UserOpenID,
			BoundAt:    time.Now(),
		}
		if len(c.Sessions) > 0 && c.Sessions[0].Dir == "" {
			c.Sessions[0].Dir = "."
		}
		return nil
	})
}
