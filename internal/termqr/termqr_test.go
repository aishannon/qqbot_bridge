package termqr

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestRenderPlain 校验纯字符渲染：行数、宽度、只包含半块字符与空格。
func TestRenderPlain(t *testing.T) {
	var b bytes.Buffer
	if err := Render(&b, "https://q.qq.com/qqbot/openclaw/connect.html?task_id=test", false); err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	if len(lines) < 15 {
		t.Fatalf("行数太少，二维码可能没生成：%d", len(lines))
	}
	w := len([]rune(lines[0]))
	for i, line := range lines {
		if got := len([]rune(line)); got != w {
			t.Fatalf("第 %d 行宽度不一致：%d vs %d", i, got, w)
		}
		for _, r := range line {
			if !strings.ContainsRune(" ▀▄█", r) {
				t.Fatalf("出现非法字符 %q", r)
			}
		}
	}
	if strings.Count(b.String(), "█") < 10 {
		t.Fatal("黑块太少，二维码可疑")
	}
}

// TestRenderColor 校验着色模式带 ANSI 前后缀。
func TestRenderColor(t *testing.T) {
	var b bytes.Buffer
	if err := Render(&b, "hello", true); err != nil {
		t.Fatal(err)
	}
	s := b.String()
	if !strings.HasPrefix(s, ansiBegin) || !strings.HasSuffix(s, ansiReset) {
		t.Fatal("着色模式应包在 ANSI 序列里")
	}
}

const sampleURL = "https://q.qq.com/qqbot/openclaw/connect.html?task_id=6f0c2b7e-1d3a-4c55-9a77-0123456789ab&source=qqbot_bridge&_wv=2"

// TestRenderCompact 校验 L 级 + 2 模块白边后尺寸与官方插件相当（43 列 × 22 行附近），
// 而不是旧的 M 级 + 4 模块白边（约 53 列）。
func TestRenderCompact(t *testing.T) {
	var b bytes.Buffer
	if err := Render(&b, sampleURL, false); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	w := len([]rune(lines[0]))
	if w > 47 || len(lines) > 24 {
		t.Fatalf("二维码偏大：%d 列 × %d 行", w, len(lines))
	}
}

// TestSVG 校验矢量输出：是合法的 svg 根，包含白底和黑块路径，viewBox 为方形。
func TestSVG(t *testing.T) {
	svg, err := SVG(sampleURL)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<svg ", `viewBox="0 0 `, `fill="#fff"`, `fill="#000"`, "</svg>"} {
		if !strings.Contains(svg, want) {
			t.Fatalf("SVG 缺少 %q：%.120s", want, svg)
		}
	}
	m, _ := matrix(sampleURL)
	n := len(m) + 2*svgQuiet
	if !strings.Contains(svg, `viewBox="0 0 `+strconv.Itoa(n)+" "+strconv.Itoa(n)+`"`) {
		t.Fatalf("viewBox 与模块数不符，期望边长 %d：%.120s", n, svg)
	}
}

// TestSVGMatchesMatrix 把 SVG 路径还原成网格，与矩阵逐格比对，保证矢量图和终端图内容一致。
func TestSVGMatchesMatrix(t *testing.T) {
	svg, err := SVG(sampleURL)
	if err != nil {
		t.Fatal(err)
	}
	m, err := matrix(sampleURL)
	if err != nil {
		t.Fatal(err)
	}
	want := pad(m, svgQuiet)
	n := len(want)

	got := make([][]bool, n)
	for i := range got {
		got[i] = make([]bool, n)
	}
	segs := regexp.MustCompile(`M(\d+) (\d+)h(\d+)v1h-\d+z`).FindAllStringSubmatch(svg, -1)
	if len(segs) == 0 {
		t.Fatal("SVG 里没有黑块路径")
	}
	for _, s := range segs {
		x, _ := strconv.Atoi(s[1])
		y, _ := strconv.Atoi(s[2])
		w, _ := strconv.Atoi(s[3])
		for i := 0; i < w; i++ {
			got[y][x+i] = true
		}
	}
	for y := range want {
		for x := range want[y] {
			if got[y][x] != want[y][x] {
				t.Fatalf("(%d,%d) 不一致：svg=%v 矩阵=%v", x, y, got[y][x], want[y][x])
			}
		}
	}
}

// TestPadAddsQuietZone 白边四周全白。
func TestPadAddsQuietZone(t *testing.T) {
	m := [][]bool{{true}}
	p := pad(m, 2)
	if len(p) != 5 || len(p[0]) != 5 || !p[2][2] {
		t.Fatalf("补白边结果不对：%v", p)
	}
	for y := range p {
		for x := range p[y] {
			if (x != 2 || y != 2) && p[y][x] {
				t.Fatalf("白边里出现黑块 (%d,%d)", x, y)
			}
		}
	}
}
