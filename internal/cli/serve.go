package cli

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"qqbot_bridge/internal/agent"
	"qqbot_bridge/internal/bridge"
	"qqbot_bridge/internal/config"
	"qqbot_bridge/internal/gateway"
	"qqbot_bridge/internal/logbuf"
	"qqbot_bridge/internal/msglog"
	"qqbot_bridge/internal/web"
)

// serve 启动网关、任务编排和 Web 管理端，直到收到退出信号。
//
// addr 为空时用配置里的 web.addr；返回进程退出码。
func serve(addr string) int {
	logs := logbuf.New(400)
	defer logs.Close()
	if err := logs.EnableFile(filepath.Join(config.Dir(), "logs")); err != nil {
		logs.Add("warn", "日志落盘已关闭：%v", err)
	}

	// 同一份配置只允许一个实例，避免控制台端口和 QQ 会话互相打架
	if pid, err := readPid(); err == nil && pid != os.Getpid() && processAlive(pid) {
		logs.Add("error", "数据目录 %s 已有实例在运行（pid=%d），先执行 %s -home %s -stop，或用 -home 换一个目录",
			config.Dir(), pid, os.Args[0], config.Dir())
		return 1
	}
	if err := writePid(os.Getpid()); err != nil {
		logs.Add("warn", "写入 pid 文件失败：%v", err)
	}
	defer removePid()

	store, err := config.Open()
	if err != nil {
		logs.Add("error", "读取配置失败：%v", err)
		return 1
	}
	cfg := store.Get()
	if next, switched := cfg.Agent.FallbackFromMissingCodeBuddy(config.CommandAvailable); switched {
		from := strings.TrimSpace(cfg.Agent.Command)
		if from == "" {
			from = "codebuddy"
		}
		logs.Add("warn", "没找到 %s，自动切换到 %s", from, next.Command)
		if err := store.Update(func(c *config.Config) error {
			c.Agent.Driver = next.Driver
			c.Agent.Command = next.Command
			return nil
		}); err != nil {
			logs.Add("warn", "切换结果没能写入配置，本次运行仍使用 %s：%v", next.Command, err)
			cfg.Agent = next
		} else {
			cfg = store.Get()
		}
	}
	logs.Add("info", "qqbot_bridge %s", versionLine())
	// 多实例时一眼能看出这个进程用的是哪套目录和机器人
	logs.Add("info", "数据目录：%s", config.Dir())
	if from := store.MigratedFrom(); from != "" {
		logs.Add("info", "已把旧配置拆分迁移为 %s（服务）和 %s（会话），旧文件备份为 %s",
			store.ServicePath(), store.SessionsPath(), from)
	}

	// 所有对 QQ 平台的请求统一带上桥接版本和本机 agent 版本。
	// 驱动名解析不出来属于配置错误，直接退出，别带着错配置跑起来。
	driver, err := agent.Open(cfg.Agent.Driver, agent.Settings{
		Command:      cfg.CommandFor(cfg.Agent.Driver),
		DiscoverRoot: cfg.Discover.Root,
	})
	if err != nil {
		logs.Add("error", "解析 agent 驱动失败：%v", err)
		return 1
	}
	gateway.SetUserAgent(gateway.BuildUserAgentFor(Version, driver.Name(), driver.Version(context.Background())))
	logs.Add("info", "User-Agent：%s", gateway.UserAgent())
	preflight(logs, cfg)

	api := gateway.NewAPI(gateway.Credentials{
		AppID:     cfg.Bind.AppID,
		AppSecret: cfg.Bind.Secret,
	})
	br := bridge.New(store, api, logs.Logf())
	msgs := msglog.New(filepath.Join(config.Dir(), "messages"), 1000)
	defer msgs.Close()
	br.SetMessageLog(msgs)
	if !cfg.Discover.Disable {
		// 启动时先把已经在跑的 agent 会话目录注册进来
		br.SyncDiscovered()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var client *gateway.Client
	if cfg.Bind.Bound() {
		client = gateway.New(gateway.Options{
			Credentials: gateway.Credentials{AppID: cfg.Bind.AppID, AppSecret: cfg.Bind.Secret},
			// 单聊消息 + 按钮/菜单互动事件
			Intents: gateway.IntentsGroup | gateway.IntentsInteraction,
			OnEvent: func(ev gateway.Event) {
				br.HandleEvent(ctx, ev)
			},
			OnState: func(state gateway.State, msg string) {
				logs.Add("info", "网关：%s %s", state, msg)
			},
			OnLog: logs.Logf(),
		})
		go func() {
			if err := client.Run(ctx); err != nil {
				logs.Add("error", "网关退出：%v", err)
			}
		}()
		if !cfg.Menu.Disable {
			// 菜单是全局配置，会覆盖手工设置，所以留了 menu.disable 开关
			go func() {
				if err := br.SetupMenu(ctx); err != nil {
					logs.Add("warn", "自动配置快捷菜单失败（可在 QQ 发 /menu 重试）：%v", err)
				}
			}()
		}
	} else {
		logs.Add("warn", "尚未绑定机器人：请在控制台里扫码，绑定成功后重启本服务")
	}

	gwStat := func() (string, string) {
		if client == nil {
			return "disconnected", "未绑定"
		}
		state, msg := client.State()
		return string(state), msg
	}

	listenAddr := addr
	if listenAddr == "" {
		listenAddr = cfg.Web.Addr
	}
	webSrv := web.New(store, br, logs, msgs, gwStat)
	webSrv.SetBotName(func() string {
		if client == nil {
			return ""
		}
		return client.Self().Username
	})
	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           webSrv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	if !isLoopbackAddr(listenAddr) {
		logs.Add("warn", "管理端监听在 %s，不是本机地址：服务本身不带 HTTPS，登录密码会明文经过网络，请放在 HTTPS 反向代理之后", listenAddr)
	}
	if _, err := os.Stat(web.AuthPath()); err != nil {
		logs.Add("warn", "还没有设置管理端访问密码，管理端暂时无法登录：请运行 qqbot_bridge passwd")
	}
	go func() {
		logs.Add("info", "控制台：http://%s", displayAddr(listenAddr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logs.Add("error", "控制台启动失败：%v", err)
		}
	}()

	if cfg.Bind.Bound() {
		logs.Add("info", "已连接机器人 appid=%s，等待 QQ 指令", cfg.Bind.AppID)
	}

	<-ctx.Done()
	logs.Add("info", "正在退出…")
	if client != nil {
		client.Close()
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	return 0
}
