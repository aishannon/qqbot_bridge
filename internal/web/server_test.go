package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"qqbot_bridge/internal/bridge"
	"qqbot_bridge/internal/config"
	"qqbot_bridge/internal/gateway"
	"qqbot_bridge/internal/logbuf"
	"qqbot_bridge/internal/msglog"
)

const testPassword = "correct-horse-battery"

// newTestServer 返回已设置密码的服务；密码文件放在临时目录，不碰真实配置。
func newTestServer(t *testing.T) (*Server, *config.Store) {
	t.Helper()
	dir := t.TempDir()
	store, err := config.OpenAt(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	logs := logbuf.New(50)
	api := gateway.NewAPI(gateway.Credentials{AppID: "a", AppSecret: "s"})
	br := bridge.New(store, api, logs.Logf())
	msgs := msglog.New("", 100)
	srv := New(store, br, logs, msgs, func() (string, string) { return "connected", "已连接" })
	srv.auth = newAuthState(filepath.Join(dir, "web-auth.json"))
	if err := SetPassword(srv.auth.path, testPassword); err != nil {
		t.Fatal(err)
	}
	return srv, store
}

// loginCookie 走真实登录接口拿到会话 cookie。
func loginCookie(t *testing.T, srv *Server) *http.Cookie {
	t.Helper()
	rec := doJSON(srv, http.MethodPost, "/api/login", `{"password":"`+testPassword+`"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("登录失败：%d %s", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieName {
			return c
		}
	}
	t.Fatal("登录成功但没有下发会话 cookie")
	return nil
}

func doJSON(srv *Server, method, path, body string, c *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "10.0.0.1:5555"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if c != nil {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	return rec
}

// get 以已登录身份请求。
func get(t *testing.T, srv *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	return doJSON(srv, http.MethodGet, path, "", loginCookie(t, srv))
}

func TestStateEndpoint(t *testing.T) {
	srv, store := newTestServer(t)
	if err := store.Update(func(c *config.Config) error {
		c.Bind.AppID = "app1"
		c.Bind.Secret = "sec"
		c.Bind.UserOpenID = "u1"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	srv.logs.Add("info", "启动完成")

	rec := get(t, srv, "/api/state")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码异常：%d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type 不对：%q", ct)
	}

	var got stateView
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("响应不是合法 JSON：%v", err)
	}
	if !got.Bound || got.AppID != "app1" || got.UserOpenID != "u1" {
		t.Fatalf("绑定信息不对：%+v", got)
	}
	if got.Gateway != "connected" {
		t.Fatalf("网关状态未透出：%q", got.Gateway)
	}
	if len(got.Sessions) != 1 || !got.Sessions[0].Current {
		t.Fatalf("会话列表不对：%+v", got.Sessions)
	}
	if len(got.Logs) != 1 || !strings.Contains(got.Logs[0].Message, "启动完成") {
		t.Fatalf("日志未透出：%+v", got.Logs)
	}
}

func TestIndexServesHTML(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := get(t, srv, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("首页状态码异常：%d", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("首页 Content-Type 不对：%q", rec.Header().Get("Content-Type"))
	}
	if rec.Body.Len() == 0 {
		t.Fatal("首页内容为空")
	}
}

func TestUnknownPathNotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := get(t, srv, "/nope")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未知路径应 404，实际 %d", rec.Code)
	}
}

func TestQRWithoutSessionIs404(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := get(t, srv, "/api/qr.png")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("没有二维码时应 404，实际 %d", rec.Code)
	}
}

func TestQRSVGWithoutSessionIs404(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := get(t, srv, "/api/qr.svg")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("没有二维码时应 404，实际 %d", rec.Code)
	}
}

func TestQRSVGServesVector(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.mu.Lock()
	srv.last.URL = "https://q.qq.com/qqbot/openclaw/connect.html?task_id=abc"
	srv.mu.Unlock()
	rec := get(t, srv, "/api/qr.svg")
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "image/svg+xml") {
		t.Fatalf("应返回 SVG：%d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Body.String(), "<svg ") {
		t.Fatalf("响应不是 SVG：%.80s", rec.Body.String())
	}
}

func TestBindRejectsGET(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := get(t, srv, "/api/bind")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET 应返回 405，实际 %d", rec.Code)
	}
}
