package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBuildUserAgent(t *testing.T) {
	ua := BuildUserAgent("1.2.3", "2.181.0")
	if !strings.HasPrefix(ua, "qqbot_bridge/1.2.3 (codebuddy/2.181.0; Go/") {
		t.Fatalf("UA 格式不对：%s", ua)
	}
	if ua := BuildUserAgent("", ""); !strings.Contains(ua, "qqbot_bridge/dev") || !strings.Contains(ua, "codebuddy/unknown") {
		t.Fatalf("缺省值不对：%s", ua)
	}
}

// 换驱动时 UA 里的产品名跟着变，默认（codebuddy）形态与历史一致。
func TestBuildUserAgentFor(t *testing.T) {
	ua := BuildUserAgentFor("1.2.3", "cursor", "")
	if !strings.HasPrefix(ua, "qqbot_bridge/1.2.3 (cursor/unknown; Go/") {
		t.Fatalf("驱动名没进 UA：%s", ua)
	}
	if got, want := BuildUserAgentFor("1.2.3", "", "2.181.0"), BuildUserAgent("1.2.3", "2.181.0"); got != want {
		t.Fatalf("驱动名留空时应回退到 codebuddy：\n%s\n%s", got, want)
	}
}

func TestRequestsCarryUserAgent(t *testing.T) {
	SetUserAgent("qqbot_bridge/test (codebuddy/9.9.9; Go/x; linux)")
	defer SetUserAgent(BuildUserAgent("", ""))

	var tokenUA, apiUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			tokenUA = r.Header.Get("User-Agent")
			_, _ = w.Write([]byte(`{"access_token":"t","expires_in":"7200"}`))
			return
		}
		apiUA = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(`{"url":"wss://example/ws"}`))
	}))
	defer srv.Close()

	api := NewAPI(Credentials{AppID: "1", AppSecret: "s"})
	api.SetEndpoints(srv.URL, srv.URL+"/token")
	if _, err := api.Gateway(t.Context()); err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]string{"token": tokenUA, "api": apiUA} {
		if !strings.Contains(got, "codebuddy/9.9.9") {
			t.Errorf("%s 请求的 UA 缺少 CodeBuddy 版本：%q", name, got)
		}
	}
}
