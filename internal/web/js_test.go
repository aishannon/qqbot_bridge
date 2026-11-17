package web

import (
	"net/http"
	"os/exec"
	"strings"
	"testing"
)

// 前端 Markdown 渲染器的单测是 Node 写的，这里随 go test 一起跑；没装 node 就跳过。
func TestMarkdownRendererJS(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("没有 node，跳过前端 Markdown 渲染器测试")
	}
	out, err := exec.Command(node, "--test", "jstest/").CombinedOutput()
	if err != nil {
		t.Fatalf("Markdown 渲染器测试失败：\n%s", out)
	}
}

// 页面要引用渲染器脚本，且脚本能在不登录时加载（登录页之外的页面都要登录，但静态脚本本身是公开的代码）。
func TestIndexLoadsMarkdownRenderer(t *testing.T) {
	srv, _ := newTestServer(t)
	page := get(t, srv, "/").Body.String()
	md := strings.Index(page, `/static/md.js`)
	app := strings.Index(page, `/static/app.js`)
	if md < 0 || app < 0 || md > app {
		t.Fatalf("index.html 应先加载 md.js 再加载 app.js：md=%d app=%d", md, app)
	}
	rec := doJSON(srv, http.MethodGet, "/static/md.js", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "QQMD") {
		t.Fatalf("md.js 应可访问：%d", rec.Code)
	}
	// 渲染器不能依赖 innerHTML：一旦用了，消息里的 HTML 就可能被执行
	if strings.Contains(rec.Body.String(), "innerHTML") && !strings.Contains(rec.Body.String(), "不使用 innerHTML") {
		t.Fatal("md.js 不应使用 innerHTML")
	}
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		code := strings.TrimSpace(line)
		if strings.HasPrefix(code, "*") || strings.HasPrefix(code, "//") || strings.HasPrefix(code, "/*") {
			continue // 注释里提到 innerHTML 没关系
		}
		if strings.Contains(code, ".innerHTML") || strings.Contains(code, "outerHTML") || strings.Contains(code, "insertAdjacentHTML") || strings.Contains(code, "document.write") {
			t.Fatalf("md.js 里出现了会解析 HTML 的 API：%s", code)
		}
	}
}
