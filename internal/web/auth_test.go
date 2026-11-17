package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSetPasswordRules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := SetPassword(path, "short"); err == nil {
		t.Fatal("过短的密码应被拒绝")
	}
	if err := SetPassword(path, "long-enough-pw"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "long-enough-pw") {
		t.Fatal("密码文件里不能出现明文")
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("密码文件权限应为 0600：%v", fi.Mode().Perm())
	}
	a := loadAuth(path)
	if a == nil || !a.verify("long-enough-pw") || a.verify("long-enough-px") {
		t.Fatal("密码校验结果不对")
	}
}

func TestEverythingRequiresLogin(t *testing.T) {
	srv, _ := newTestServer(t)

	// 页面：跳转到登录页
	rec := doJSON(srv, http.MethodGet, "/", "", nil)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
		t.Fatalf("未登录访问首页应跳转登录：%d %q", rec.Code, rec.Header().Get("Location"))
	}
	// 接口：401，且不泄漏任何数据
	for _, p := range []string{"/api/state", "/api/messages", "/api/whitelist", "/api/sessions", "/api/sessions/main", "/api/sessions/main/log", "/api/qr.svg", "/api/qr.png"} {
		rec := doJSON(srv, http.MethodGet, p, "", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s 未登录应返回 401，实际 %d", p, rec.Code)
		}
	}
	for _, p := range []string{"/api/bind", "/api/whitelist", "/api/logout"} {
		rec := doJSON(srv, http.MethodPost, p, `{}`, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("POST %s 未登录应返回 401，实际 %d", p, rec.Code)
		}
	}
}

func TestPublicAssetsAvailableWithoutLogin(t *testing.T) {
	srv, _ := newTestServer(t)
	for _, p := range []string{"/login", "/favicon.svg", "/favicon.ico", "/static/style.css", "/static/login.js", "/api/auth"} {
		if rec := doJSON(srv, http.MethodGet, p, "", nil); rec.Code != http.StatusOK {
			t.Errorf("%s 应无需登录即可访问，实际 %d", p, rec.Code)
		}
	}
	rec := doJSON(srv, http.MethodGet, "/favicon.svg", "", nil)
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "image/svg+xml") || !strings.Contains(rec.Body.String(), "<svg") {
		t.Fatalf("图标应是 SVG：%q", rec.Header().Get("Content-Type"))
	}
	page := doJSON(srv, http.MethodGet, "/login", "", nil).Body.String()
	if !strings.Contains(page, `rel="icon"`) {
		t.Fatal("登录页应声明图标")
	}
}

func TestLoginWrongAndRightPassword(t *testing.T) {
	srv, _ := newTestServer(t)
	if rec := doJSON(srv, http.MethodPost, "/api/login", `{"password":"wrong-password"}`, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("错误密码应 401：%d", rec.Code)
	}
	c := loginCookie(t, srv)
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
		t.Fatalf("会话 cookie 应为 HttpOnly + SameSite=Strict：%+v", c)
	}
	if rec := doJSON(srv, http.MethodGet, "/api/state", "", c); rec.Code != http.StatusOK {
		t.Fatalf("登录后应能访问：%d", rec.Code)
	}
	rec := doJSON(srv, http.MethodGet, "/api/auth", "", c)
	if !strings.Contains(rec.Body.String(), `"authenticated":true`) {
		t.Fatalf("auth 状态应为已登录：%s", rec.Body.String())
	}
}

func TestLogoutInvalidatesSession(t *testing.T) {
	srv, _ := newTestServer(t)
	c := loginCookie(t, srv)
	if rec := doJSON(srv, http.MethodPost, "/api/logout", `{}`, c); rec.Code != http.StatusOK {
		t.Fatalf("退出失败：%d", rec.Code)
	}
	if rec := doJSON(srv, http.MethodGet, "/api/state", "", c); rec.Code != http.StatusUnauthorized {
		t.Fatalf("退出后旧 cookie 应失效：%d", rec.Code)
	}
}

func TestPasswordChangeInvalidatesSessions(t *testing.T) {
	srv, _ := newTestServer(t)
	c := loginCookie(t, srv)
	if err := SetPassword(srv.auth.path, "another-password-1"); err != nil {
		t.Fatal(err)
	}
	if rec := doJSON(srv, http.MethodGet, "/api/state", "", c); rec.Code != http.StatusUnauthorized {
		t.Fatalf("改密码后旧会话应失效：%d", rec.Code)
	}
	if rec := doJSON(srv, http.MethodPost, "/api/login", `{"password":"`+testPassword+`"}`, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("旧密码应不能再登录：%d", rec.Code)
	}
	if rec := doJSON(srv, http.MethodPost, "/api/login", `{"password":"another-password-1"}`, nil); rec.Code != http.StatusOK {
		t.Fatalf("新密码应能登录：%d", rec.Code)
	}
}

func TestExpiredSessionRejected(t *testing.T) {
	srv, _ := newTestServer(t)
	c := loginCookie(t, srv)
	srv.auth.mu.Lock()
	s := srv.auth.sessions[c.Value]
	s.expires = time.Now().Add(-time.Minute)
	srv.auth.sessions[c.Value] = s
	srv.auth.mu.Unlock()
	if rec := doJSON(srv, http.MethodGet, "/api/state", "", c); rec.Code != http.StatusUnauthorized {
		t.Fatalf("过期会话应失效：%d", rec.Code)
	}
}

func TestNoPasswordConfiguredDeniesEverything(t *testing.T) {
	srv, _ := newTestServer(t)
	_ = os.Remove(srv.auth.path)

	rec := doJSON(srv, http.MethodPost, "/api/login", `{"password":"anything-at-all"}`, nil)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "passwd") {
		t.Fatalf("未设置密码时登录应提示运行 passwd：%d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(srv, http.MethodGet, "/api/state", "", nil)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), `"configured":false`) {
		t.Fatalf("未设置密码时接口应拒绝并说明原因：%d %s", rec.Code, rec.Body.String())
	}
}

func TestLoginLockoutAfterRepeatedFailures(t *testing.T) {
	srv, _ := newTestServer(t)
	for i := 0; i < maxFailures; i++ {
		if rec := doJSON(srv, http.MethodPost, "/api/login", `{"password":"nope-nope-nope"}`, nil); rec.Code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次失败应 401：%d", i+1, rec.Code)
		}
	}
	// 锁定期间，即使密码正确也拒绝
	rec := doJSON(srv, http.MethodPost, "/api/login", `{"password":"`+testPassword+`"}`, nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("连续失败后应被锁定：%d", rec.Code)
	}
	// 其他来源不受影响
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"password":"`+testPassword+`"}`))
	req.RemoteAddr = "10.9.9.9:1"
	other := httptest.NewRecorder()
	srv.Routes().ServeHTTP(other, req)
	if other.Code != http.StatusOK {
		t.Fatalf("其他来源不应被连带锁定：%d", other.Code)
	}
}

func TestCrossOriginWriteRejected(t *testing.T) {
	srv, _ := newTestServer(t)
	c := loginCookie(t, srv)
	req := httptest.NewRequest(http.MethodPost, "/api/whitelist", strings.NewReader(`{"action":"add","openId":"ABC123"}`))
	req.Host = "127.0.0.1:8686"
	req.Header.Set("Origin", "http://evil.example")
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("跨站来源的写操作应被拒绝：%d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/whitelist", strings.NewReader(`{"action":"add","openId":"ABC123"}`))
	req.Host = "127.0.0.1:8686"
	req.Header.Set("Origin", "http://127.0.0.1:8686")
	req.AddCookie(c)
	rec = httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("同源写操作应通过：%d %s", rec.Code, rec.Body.String())
	}
}

func TestSecurityHeaders(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := doJSON(srv, http.MethodGet, "/login", "", nil)
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe-inline") {
		t.Fatalf("CSP 不应允许内联脚本：%q", csp)
	}
	if rec.Header().Get("X-Frame-Options") != "DENY" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("缺少安全响应头")
	}
}
