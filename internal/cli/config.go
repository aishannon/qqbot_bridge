package cli

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"qqbot_bridge/internal/config"
	"qqbot_bridge/internal/gateway"
	"qqbot_bridge/internal/qrconnect"
	"qqbot_bridge/internal/web"
)

// runConfig 是 `qqbot_bridge config` 子命令：服务初始化向导。
//
// 未绑定时直接扫码；已绑定时先交互确认要不要重新绑定，再检查管理端密码，
// 最后提示启动/重启服务。脚本里用 -yes 走默认分支（不重新绑定、不设密码）。
func runConfig(args []string) int {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	force := fs.Bool("force", false, "已绑定时也重新扫码绑定")
	yes := fs.Bool("yes", false, "不交互，全部使用默认答案")
	out := fs.String("out", filepath.Join(config.Dir(), "qr-bind.png"), "二维码图片输出路径")
	source := fs.String("source", "qqbot_bridge", "扫码来源标识")
	timeout := fs.Duration("timeout", 10*time.Minute, "等待扫码的最长时间")
	addr := fs.String("addr", "", "管理端监听地址，写入配置（多实例时每套一个端口，如 127.0.0.1:8687）")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	store, err := config.Open()
	if err != nil {
		fmt.Fprintf(os.Stderr, "读取配置失败：%v\n", err)
		return 1
	}
	if *addr != "" {
		if err := saveListenAddr(store, *addr); err != nil {
			fmt.Fprintf(os.Stderr, "写入监听地址失败：%v\n", err)
			return 1
		}
	}
	cfg := store.Get()
	interactive := isTTY(os.Stdin) && !*yes

	fmt.Print(configState(cfg))
	fmt.Println()

	// 绑定：没绑过直接扫；绑过就先问，避免手滑把线上凭据覆盖掉
	needBind := !cfg.Bind.Bound()
	if cfg.Bind.Bound() && !*force {
		if interactive {
			needBind = confirm("要重新扫码绑定机器人吗？（会覆盖现有凭据，白名单保留）", false)
		} else {
			fmt.Println("已绑定，跳过扫码（要重绑加 -force）")
		}
	}
	if needBind {
		cred, err := bindRobot(bindOpts{
			out:     *out,
			source:  *source,
			timeout: *timeout,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "绑定失败：%v\n", err)
			return 1
		}
		if err := saveBind(cred); err != nil {
			fmt.Fprintf(os.Stderr, "保存凭据失败：%v\n", err)
			return 1
		}
		fmt.Printf("绑定完成：appid=%s\n", cred.AppID)
	}

	// 管理端密码：没设置前控制台登录不了，初始化时顺手补上
	if hasWebPassword() {
		fmt.Println("管理端密码：已设置")
	} else if interactive && confirm("管理端还没设置访问密码，现在设置吗？", true) {
		if code := runPasswd(nil); code != 0 {
			return code
		}
	} else if !interactive {
		fmt.Println("管理端密码：未设置（稍后运行 qqbot_bridge passwd）")
	}

	fmt.Println()
	if pid, running := daemonPID(); running {
		fmt.Printf("服务正在运行（pid=%d），重启后新配置生效：\n", pid)
		fmt.Println("  systemctl --user restart qqbot_bridge" + serviceSuffix() + "   # 用 systemd 托管时")
		fmt.Println("  qqbot_bridge" + instanceArgs() + " -stop && qqbot_bridge" + instanceArgs() + "   # 自建后台进程时")
		return 0
	}
	if interactive && confirm("现在后台启动服务吗？", true) {
		if err := startDaemon(nil); err != nil {
			fmt.Fprintln(os.Stderr, "后台启动失败："+err.Error())
			return 1
		}
		fmt.Println("已在后台启动，" + webAddrText(cfg))
		return 0
	}
	fmt.Println("下一步：运行 qqbot_bridge" + instanceArgs() + " 启动服务（-f 前台运行）")
	return 0
}

// saveListenAddr 校验并写入管理端监听地址。
//
// 多实例必须各用一个端口，默认的 8686 只能给一套用；这里提前挡掉写错的地址，
// 免得服务起来之后才在监听失败里暴露。
func saveListenAddr(store *config.Store, addr string) error {
	addr = strings.TrimSpace(addr)
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return fmt.Errorf("%q 不是合法的 host:port，例如 127.0.0.1:8687", addr)
	}
	return store.Update(func(c *config.Config) error {
		c.Web.Addr = addr
		return nil
	})
}

// webAddrText 按配置里的真实地址生成「管理端：http://…」提示。
func webAddrText(cfg config.Config) string {
	addr := cfg.Web.Addr
	if addr == "" {
		addr = "127.0.0.1:8686"
	}
	return "管理端：http://" + displayAddr(addr)
}

// instanceArgs 返回命令里的 -home 片段：默认目录时为空，保证老提示逐字不变。
func instanceArgs() string {
	if config.Dir() == config.DefaultDir() {
		return ""
	}
	return " -home " + config.Dir()
}

// serviceSuffix 返回 systemd 实例后缀：默认实例为空，其它目录按目录名给出提示。
func serviceSuffix() string {
	if config.Dir() == config.DefaultDir() {
		return ""
	}
	return "（或 qqbot_bridge@<实例名>）"
}

// configState 打印当前配置概况，让用户知道这次会改什么。
func configState(cfg config.Config) string {
	var b strings.Builder
	fmt.Fprintf(&b, "配置目录：%s\n", config.Dir())
	fmt.Fprintf(&b, "%s\n", webAddrText(cfg))

	if cfg.Bind.Bound() {
		when := ""
		if !cfg.Bind.BoundAt.IsZero() {
			when = "，" + cfg.Bind.BoundAt.Format("2006-01-02 15:04")
		}
		fmt.Fprintf(&b, "机器人：已绑定 appid=%s%s\n", cfg.Bind.AppID, when)
		if cfg.Bind.UserOpenID != "" {
			fmt.Fprintf(&b, "绑定用户：%s\n", cfg.Bind.UserOpenID)
		}
		fmt.Fprintf(&b, "白名单：%d 人\n", len(cfg.Bind.Whitelist))
	} else {
		b.WriteString("机器人：未绑定\n")
	}

	if hasWebPassword() {
		b.WriteString("管理端密码：已设置\n")
	} else {
		b.WriteString("管理端密码：未设置\n")
	}

	if pid, running := daemonPID(); running {
		fmt.Fprintf(&b, "服务：运行中（pid=%d）\n", pid)
	} else {
		b.WriteString("服务：未运行\n")
	}
	return b.String()
}

// hasWebPassword 判断管理端密码是否已经设置过。
func hasWebPassword() bool {
	_, err := os.Stat(web.AuthPath())
	return err == nil
}

// daemonPID 返回后台实例的 pid 以及是否在运行。
func daemonPID() (int, bool) {
	pid, err := readPid()
	if err != nil || pid <= 0 {
		return 0, false
	}
	if pid == os.Getpid() || !processAlive(pid) {
		return 0, false
	}
	return pid, true
}

// askAnswer 读取一行答案并归一化：空串表示用默认值。
func askAnswer(r *bufio.Reader) string {
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(line))
}

// confirm 交互式确认，默认值由 def 决定（y 或 n）。
func confirm(prompt string, def bool) bool {
	hint := "[y/N]"
	if def {
		hint = "[Y/n]"
	}
	fmt.Printf("%s %s ", prompt, hint)
	switch askAnswer(bufio.NewReader(os.Stdin)) {
	case "y", "yes", "是", "好":
		return true
	case "n", "no", "否", "不":
		return false
	}
	fmt.Println()
	return def
}

// bindOpts 是一次扫码绑定的参数。
type bindOpts struct {
	out     string
	source  string
	timeout time.Duration
}

// bindRobot 走一遍扫码流程，成功后返回凭据（还没落盘）。
func bindRobot(opts bindOpts) (qrconnect.Credential, error) {
	timeout := opts.timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	go func() {
		select {
		case <-sig:
			cancel()
		case <-ctx.Done():
		}
	}()

	done := make(chan qrconnect.Credential, 1)
	errCh := make(chan error, 1)
	printed := false

	session := qrconnect.Start(ctx, qrconnect.Options{
		Source:    opts.source,
		UserAgent: gateway.BuildUserAgent(Version, codebuddyVersion()),
		OnState: func(st qrconnect.State) {
			if st.Phase == qrconnect.PhaseWaiting && !printed && st.URL != "" {
				printed = true
				printQR(opts.out, st.URL)
			}
			if st.Phase == qrconnect.PhaseFailed {
				select {
				case errCh <- fmt.Errorf("%s", st.Message):
				default:
				}
				cancel()
			}
		},
		OnSuccess: func(c qrconnect.Credential) { done <- c },
	})
	defer session.Cancel()

	select {
	case cred := <-done:
		return cred, nil
	case err := <-errCh:
		return qrconnect.Credential{}, err
	case <-ctx.Done():
		return qrconnect.Credential{}, fmt.Errorf("等待扫码超时或被取消（%s），未完成绑定", opts.timeout)
	}
}
