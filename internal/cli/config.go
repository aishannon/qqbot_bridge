package cli

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
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
// 未绑定时直接扫码；已绑定时先交互确认要不要重新绑定。凭据一落盘就连上
// QQ 网关，不再等后面的配置。管理端密码等随后再设：密码下次登录即生效，
// 白名单、agent、监听地址这类配置重启后才进当前连接。
// 脚本里用 -yes 走默认分支（不重新绑定、不设密码）；已绑定且服务没在跑时仍会把网关拉起来。
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
	justBound := false
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
		if err := applyBind(store, cred); err != nil {
			fmt.Fprintf(os.Stderr, "保存凭据失败：%v\n", err)
			return 1
		}
		fmt.Printf("绑定完成：appid=%s\n", cred.AppID)
		justBound = true
		cfg = store.Get()
	}

	// 凭据已经在磁盘上就先连网关。密码还没设也不挡：管理端登录不了，QQ 消息进得来。
	var gatewayErr error
	if cfg.Bind.Bound() {
		gatewayErr = connectGateway(cfg, justBound)
	}

	// 管理端密码：没设置前控制台登录不了。连接已经起来了，这一步放在后面。
	passwordMissing := !hasWebPassword()
	if !passwordMissing {
		fmt.Println("管理端密码：已设置")
	} else if interactive && confirm("管理端还没设置访问密码，现在设置吗？", true) {
		if code := runPasswd(nil); code != 0 {
			return code
		}
	} else if !interactive {
		fmt.Println("管理端密码：未设置（稍后运行 qqbot_bridge passwd，不用为密码重启）")
	}

	fmt.Println()
	if gatewayErr != nil {
		fmt.Fprintf(os.Stderr, "启动 QQ 网关失败：%v\n", gatewayErr)
		fmt.Println("凭据已保存。排除问题后重启：")
		printRestartHint()
		return 1
	}
	if !cfg.Bind.Bound() {
		return 0
	}
	if passwordMissing {
		fmt.Println("管理端密码下次登录即生效，不用为它重启。")
	}
	fmt.Println("白名单、agent、监听地址等其它配置若在连接之后再改，重启后生效：")
	printRestartHint()
	return 0
}

// 扫码之后怎么对待已经在跑的进程。
const (
	gatewayStart = iota
	gatewayRestart
	gatewayAlready
)

// gatewayAction 在凭据已经落盘的前提下决定启动、重启还是不动。
//
// 刚写入的 appid 只有新进程会拿去连网关；旧进程若是绑定前拉起来的，手里没有这条连接。
func gatewayAction(justBound, running bool) int {
	if !running {
		return gatewayStart
	}
	if justBound {
		return gatewayRestart
	}
	return gatewayAlready
}

// connectGateway 让已保存的凭据连上 QQ。
//
// systemd 用户单元在的话优先交给它，避免再拉一个抢端口的后台进程。
func connectGateway(cfg config.Config, justBound bool) error {
	unit := userUnit()
	unitOK := systemdUnitUsable(unit)
	active := unitOK && systemdActive(unit)
	pid, daemonOn := daemonPID()
	running := active || daemonOn

	switch gatewayAction(justBound, running) {
	case gatewayAlready:
		if active {
			fmt.Printf("QQ 网关已在运行（systemd：%s）。\n", strings.TrimSuffix(unit, ".service"))
		} else {
			fmt.Printf("QQ 网关已在运行（pid=%d）。\n", pid)
		}
		return nil
	case gatewayRestart:
		fmt.Println("凭据已更新，正在重连 QQ 网关…")
		if active {
			if err := systemctlUser("restart", unit); err != nil {
				return err
			}
		} else {
			if err := stopDaemon(); err != nil {
				return err
			}
			if err := startDaemon(nil); err != nil {
				return err
			}
		}
	default:
		fmt.Println("正在启动 QQ 网关…")
		if unitOK && !daemonOn {
			if err := systemctlUser("start", unit); err != nil {
				return err
			}
		} else if err := startDaemon(nil); err != nil {
			return err
		}
	}
	fmt.Printf("QQ 网关已连接，可以收消息了。%s\n", webAddrText(cfg))
	return nil
}

// userUnit 按数据目录猜 systemd 用户单元名。
//
// 默认目录是 qqbot_bridge.service；~/.qqbot_bridge-<实例名> 对应模板实例。
// 目录对不上安装脚本的约定时返回空，调用方改走自建后台进程。
func userUnit() string {
	dir := config.Dir()
	if dir == config.DefaultDir() {
		return "qqbot_bridge.service"
	}
	prefix := filepath.Base(config.DefaultDir()) + "-"
	name, ok := strings.CutPrefix(filepath.Base(dir), prefix)
	if !ok || name == "" {
		return ""
	}
	return "qqbot_bridge@" + name + ".service"
}

// systemdUnitUsable 判断本机有 systemctl，且这个用户单元文件存在。
func systemdUnitUsable(unit string) bool {
	if unit == "" {
		return false
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}
	cmd := exec.Command("systemctl", "--user", "cat", unit)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run() == nil
}

func systemdActive(unit string) bool {
	cmd := exec.Command("systemctl", "--user", "is-active", "--quiet", unit)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run() == nil
}

func systemctlUser(args ...string) error {
	cmd := exec.Command("systemctl", append([]string{"--user"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return err
		}
		return fmt.Errorf("%v：%s", err, msg)
	}
	return nil
}

// printRestartHint 打印让「连接起来之后才改的配置」生效的两条命令。
func printRestartHint() {
	unit := "qqbot_bridge"
	if u := userUnit(); u != "" {
		unit = strings.TrimSuffix(u, ".service")
	}
	fmt.Println("  systemctl --user restart " + unit + "   # 用 systemd 托管时")
	fmt.Println("  qqbot_bridge" + instanceArgs() + " -stop && qqbot_bridge" + instanceArgs() + "   # 自建后台进程时")
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
