package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

var tinyPNG = []byte("\x89PNG\r\n\x1a\nfake-but-fine")

// fakeAvatarHost 起一个假的头像服务，返回其地址和被请求次数、最后一次请求路径。
func fakeAvatarHost(t *testing.T, handler http.HandlerFunc) (string, *int32, *atomic.Value) {
	t.Helper()
	var hits int32
	var last atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		last.Store(r.URL.Path)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &hits, &last
}

func TestAvatarProxiesAndCaches(t *testing.T) {
	base, hits, last := fakeAvatarHost(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(tinyPNG)
	})
	srv, store := newTestServer(t)
	bindOwner(t, store)
	srv.avatars.base = base

	for i := 0; i < 3; i++ {
		rec := get(t, srv, "/api/avatar/FRIEND01")
		if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" || rec.Body.String() != string(tinyPNG) {
			t.Fatalf("应原样返回头像：%d %q", rec.Code, rec.Header().Get("Content-Type"))
		}
		if rec.Header().Get("Cache-Control") == "" {
			t.Fatal("应带缓存头")
		}
	}
	if atomic.LoadInt32(hits) != 1 {
		t.Fatalf("同一个人的头像只应向外请求一次，实际 %d 次", atomic.LoadInt32(hits))
	}
	// 请求路径必须带 appid 和 openid，appid 取自当前绑定
	if p, _ := last.Load().(string); p != "/qqapp/app1/FRIEND01/100" {
		t.Fatalf("请求头像服务的路径不对：%q", p)
	}
}

func TestAvatarFallsBackToLetter(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"服务端 404": func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) },
		"返回的不是图片": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<script>alert(1)</script>"))
		},
		"跳转到别处": func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://evil.example/x.png", http.StatusFound)
		},
	}
	for name, h := range cases {
		base, _, _ := fakeAvatarHost(t, h)
		srv, store := newTestServer(t)
		bindOwner(t, store)
		srv.avatars.base = base

		rec := get(t, srv, "/api/avatar/FRIEND01?n="+"路人")
		ct := rec.Header().Get("Content-Type")
		if rec.Code != 200 || !strings.HasPrefix(ct, "image/svg+xml") {
			t.Errorf("%s：应回退成 SVG 字母头像，实际 %d %q", name, rec.Code, ct)
			continue
		}
		if !strings.Contains(rec.Body.String(), ">路<") || strings.Contains(rec.Body.String(), "script") {
			t.Errorf("%s：字母头像内容不对：%s", name, rec.Body.String())
		}
	}
}

func TestAvatarUnboundUsesLetter(t *testing.T) {
	srv, _ := newTestServer(t) // 没绑定机器人，没有 appid
	rec := get(t, srv, "/api/avatar/FRIEND01")
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "image/svg+xml") {
		t.Fatalf("未绑定时应直接用字母头像：%q", rec.Header().Get("Content-Type"))
	}
}

func TestAvatarRejectsBadIDAndNeedsLogin(t *testing.T) {
	srv, _ := newTestServer(t)
	if rec := get(t, srv, "/api/avatar/a%20b"); rec.Code != 404 {
		t.Fatalf("非法 openid 应 404：%d", rec.Code)
	}
	if rec := get(t, srv, "/api/avatar/..%2Fetc"); rec.Code != 404 {
		t.Fatalf("路径穿越应被拒绝：%d", rec.Code)
	}
	if rec := doJSON(srv, http.MethodGet, "/api/avatar/FRIEND01", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("未登录不应能取头像：%d", rec.Code)
	}
}

func TestLetterAvatarStableAndEscaped(t *testing.T) {
	a := letterAvatar("OPENID1", "小明")
	if a != letterAvatar("OPENID1", "小明") || !strings.Contains(a, ">小<") {
		t.Fatalf("同一个人应始终同样的头像：%s", a)
	}
	if letterAvatar("OPENID1", "") == letterAvatar("OPENID2", "") {
		t.Fatal("不同 openid 应有不同颜色")
	}
	if strings.Contains(letterAvatar("X", "<b>"), "<b>") {
		t.Fatal("昵称里的特殊字符必须转义")
	}
}
