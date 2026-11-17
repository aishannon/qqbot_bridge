// Package web 是本地管理端：需要密码登录，可以看绑定状态、收到的消息、
// 管理白名单、查看会话信息与对话日志。
package web

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"qqbot_bridge/internal/bridge"
	"qqbot_bridge/internal/config"
	"qqbot_bridge/internal/gateway"
	"qqbot_bridge/internal/logbuf"
	"qqbot_bridge/internal/msglog"
	"qqbot_bridge/internal/qrconnect"
	"qqbot_bridge/internal/termqr"
)

//go:embed pages static
var assets embed.FS

// Server 是本地管理端。
type Server struct {
	cfg     *config.Store
	br      *bridge.Bridge
	logs    *logbuf.Buffer
	msgs    *msglog.Log
	gwStat  func() (string, string)
	botName func() string // 机器人昵称，来自网关 READY 事件；未连接成功过时为空

	auth        *authState
	avatars     *avatarCache
	transcripts transcriptCache

	mu     sync.Mutex
	bind   *qrconnect.Session
	last   qrconnect.State
	cancel context.CancelFunc
}

// New 创建管理端。msgs 是收到消息的记录，可为 nil；gwStat 返回网关状态与说明。
// 访问密码保存在 AuthPath()，没设置过密码时所有页面都拒绝访问。
func New(cfg *config.Store, br *bridge.Bridge, logs *logbuf.Buffer, msgs *msglog.Log, gwStat func() (string, string)) *Server {
	return &Server{cfg: cfg, br: br, logs: logs, msgs: msgs, gwStat: gwStat, auth: newAuthState(AuthPath()), avatars: newAvatarCache()}
}

// SetBotName 设置获取机器人昵称的函数。昵称由网关登录成功后才知道，所以用函数而不是固定值。
func (s *Server) SetBotName(fn func() string) { s.botName = fn }

// Routes 注册路由。
func (s *Server) Routes() http.Handler {
	staticFS, _ := fs.Sub(assets, "static")
	static := http.StripPrefix("/static/", http.FileServerFS(staticFS))

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/login", s.handleLoginPage)
	mux.HandleFunc("/favicon.svg", s.handleIcon)
	mux.HandleFunc("/favicon.ico", s.handleIcon)
	mux.Handle("/static/", static)

	mux.HandleFunc("GET /api/auth", s.handleAuthStatus)
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)

	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/api/bind", s.handleBind)
	mux.HandleFunc("/api/qr.png", s.handleQR)
	mux.HandleFunc("/api/qr.svg", s.handleQRSVG)
	mux.HandleFunc("GET /api/avatar/{id}", s.handleAvatar)
	mux.HandleFunc("GET /api/messages", s.handleMessages)
	mux.HandleFunc("GET /api/whitelist", s.handleWhitelistGet)
	mux.HandleFunc("POST /api/whitelist", s.handleWhitelistPost)
	mux.HandleFunc("GET /api/sessions", s.handleSessions)
	mux.HandleFunc("GET /api/sessions/{id}", s.handleSessionDetail)
	mux.HandleFunc("GET /api/sessions/{id}/log", s.handleSessionLog)
	return s.secure(mux)
}

// isPublic 是不需要登录就能访问的路径：登录页、图标、静态资源和登录接口本身。
func isPublic(path string) bool {
	switch path {
	case "/login", "/favicon.svg", "/favicon.ico", "/api/auth", "/api/login":
		return true
	}
	return strings.HasPrefix(path, "/static/")
}

// secure 给所有请求加安全响应头，并要求除公开路径外都已登录。
func (s *Server) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")

		if !isPublic(r.URL.Path) {
			if !s.auth.valid(r) {
				if strings.HasPrefix(r.URL.Path, "/api/") {
					writeJSONStatus(w, http.StatusUnauthorized, map[string]any{
						"error": "unauthorized", "configured": s.auth.configured(),
					})
				} else {
					http.Redirect(w, r, "/login", http.StatusFound)
				}
				return
			}
			if r.Method != http.MethodGet && r.Method != http.MethodHead && !sameOrigin(r) {
				writeError(w, http.StatusForbidden, "来源不合法")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"configured": s.auth.configured(), "authenticated": s.auth.valid(r)})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "来源不合法")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式不对")
		return
	}
	token, locked, err := s.auth.login(clientIP(r), req.Password)
	switch {
	case err == errNotConfigured:
		writeError(w, http.StatusServiceUnavailable, "尚未设置访问密码，请在服务器上运行 qqbot_bridge passwd")
	case err != nil:
		writeError(w, http.StatusInternalServerError, "登录失败")
	case locked:
		s.logs.Add("warn", "Web 管理端：%s 多次密码错误，已临时锁定", clientIP(r))
		writeError(w, http.StatusTooManyRequests, "密码错误次数过多，请 5 分钟后再试")
	case token == "":
		s.logs.Add("warn", "Web 管理端：%s 登录失败（密码错误）", clientIP(r))
		writeError(w, http.StatusUnauthorized, "密码错误")
	default:
		setSessionCookie(w, r, token)
		writeJSON(w, map[string]bool{"ok": true})
	}
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.auth.logout(r)
	clearSessionCookie(w)
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	servePage(w, "pages/login.html")
}

func (s *Server) handleIcon(w http.ResponseWriter, r *http.Request) {
	data, err := assets.ReadFile("static/icon.svg")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(data)
}

func servePage(w http.ResponseWriter, name string) {
	data, err := assets.ReadFile(name)
	if err != nil {
		http.Error(w, "页面缺失", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

// ---------- 数据结构 ----------

type sessionView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Dir       string `json:"dir"`
	AgentID   string `json:"agentId"`
	Current   bool   `json:"current"`
	CreatedAt string `json:"createdAt"`
	// Driver 是这个会话用的 agent（codebuddy / cursor），Agent 是它的展示名。
	// 同一实例里不同会话可能用不同 agent，所以页面按会话渲染而不是只看全局。
	Driver string `json:"driver"`
	Agent  string `json:"agent"`
}

type bindView struct {
	Phase   string    `json:"phase"`
	URL     string    `json:"url"`
	Message string    `json:"message"`
	At      time.Time `json:"at"`
}

type stateView struct {
	Bound      bool                `json:"bound"`
	AppID      string              `json:"appId"`
	UserOpenID string              `json:"userOpenId"`
	BotName    string              `json:"botName"`
	Gateway    string              `json:"gateway"`
	GatewayMsg string              `json:"gatewayMsg"`
	Sessions   []sessionView       `json:"sessions"`
	Current    string              `json:"current"`
	Task       bridge.TaskSnapshot `json:"task"`
	Bind       bindView            `json:"bind"`
	Logs       []logbuf.Entry      `json:"logs"`
	Now        time.Time           `json:"now"`
	// Agent 是当前 agent 的展示名（CodeBuddy、Cursor…），页面文案用它，
	// 新增字段，老页面忽略即可。
	Agent string `json:"agent"`
}

func (s *Server) snapshot() stateView {
	cfg := s.cfg.Get()
	out := stateView{
		Bound:      cfg.Bind.Bound(),
		AppID:      cfg.Bind.AppID,
		UserOpenID: cfg.Bind.UserOpenID,
		Current:    cfg.Current,
		Now:        time.Now(),
		Logs:       s.logs.Entries(),
		Agent:      s.br.AgentTitle(),
	}
	for _, item := range cfg.Sessions {
		driver := cfg.DriverOf(item)
		out.Sessions = append(out.Sessions, sessionView{
			ID:        item.ID,
			Name:      item.Name,
			Dir:       item.Dir,
			AgentID:   item.AgentID,
			Current:   item.ID == cfg.Current,
			CreatedAt: item.CreatedAt.Format("2006-01-02 15:04"),
			Driver:    driver,
			Agent:     bridge.AgentTitleFor(driver),
		})
	}
	if s.gwStat != nil {
		out.Gateway, out.GatewayMsg = s.gwStat()
	}
	if s.botName != nil {
		out.BotName = strings.TrimSpace(s.botName())
	}
	out.Task = s.br.Snapshot(cfg.Current)

	s.mu.Lock()
	out.Bind = bindView{Phase: s.last.Phase, URL: s.last.URL, Message: s.last.Message, At: s.last.IssuedAt}
	s.mu.Unlock()
	return out
}

// ---------- 处理函数 ----------

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.snapshot())
}

func (s *Server) handleBind(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	s.mu.Lock()
	running := s.bind != nil && s.last.Phase != qrconnect.PhaseSuccess && s.last.Phase != qrconnect.PhaseFailed
	s.mu.Unlock()
	if running {
		writeJSON(w, s.snapshot())
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	session := qrconnect.Start(ctx, qrconnect.Options{
		Source:    "qqbot_bridge",
		UserAgent: gateway.UserAgent(),
		OnState: func(st qrconnect.State) {
			s.mu.Lock()
			s.last = st
			s.mu.Unlock()
			if st.Phase != "" {
				s.logs.Add("info", "扫码绑定：%s %s", st.Phase, st.Message)
			}
		},
		OnSuccess: func(c qrconnect.Credential) {
			err := s.cfg.Update(func(cfg *config.Config) error {
				cfg.Bind = config.Bind{
					AppID:      c.AppID,
					Secret:     c.AppSecret,
					UserOpenID: c.UserOpenID,
					BoundAt:    time.Now(),
				}
				return nil
			})
			if err != nil {
				s.logs.Add("error", "保存绑定凭据失败：%v", err)
				return
			}
			s.logs.Add("info", "绑定成功：appid=%s，重启服务后生效", c.AppID)
		},
	})

	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	s.cancel = cancel
	s.bind = session
	s.last = session.State()
	s.mu.Unlock()

	writeJSON(w, s.snapshot())
}

func (s *Server) handleQR(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	url := s.last.URL
	s.mu.Unlock()
	if url == "" {
		http.Error(w, "还没有可用的二维码", http.StatusNotFound)
		return
	}
	png, err := qrcode.Encode(url, termqr.Level(), 512)
	if err != nil {
		http.Error(w, "生成二维码失败", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

// handleQRSVG 返回矢量二维码，网页里放大也不糊。
func (s *Server) handleQRSVG(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	url := s.last.URL
	s.mu.Unlock()
	if url == "" {
		http.Error(w, "还没有可用的二维码", http.StatusNotFound)
		return
	}
	svg, err := termqr.SVG(url)
	if err != nil {
		http.Error(w, "生成二维码失败", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(svg))
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	servePage(w, "pages/index.html")
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}
