// Package termqr 把二维码渲染到终端里，直接用手机扫屏幕即可。
//
// 用上下半块字符（▀ ▄ █）两行并一行，模块接近正方形；在交互式终端里再用
// ANSI 强制「黑码白底」，这样不管终端是深色还是浅色主题都能正常扫。
//
// 二维码是在屏幕上扫的，不会被刮花或遮挡，所以纠错用最低的 L 级：同样的链接
// 版本号更小、模块更粗；白边（静区）终端里取 2 个模块，比库默认的 4 个紧凑。
package termqr

import (
	"fmt"
	"io"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

// ansi 黑字白底：终端主题配色千差万别，固定配色最稳。
const (
	ansiBegin = "\x1b[30;47m"
	ansiReset = "\x1b[0m"
)

const (
	// level 屏幕扫码用最低纠错等级即可
	level = qrcode.Low
	// terminalQuiet 终端里的白边宽度（模块数）
	terminalQuiet = 2
	// svgQuiet 矢量图沿用标准的 4 个模块白边
	svgQuiet = 4
)

// Level 返回统一使用的纠错等级，供其他渲染（如 PNG）保持一致。
func Level() qrcode.RecoveryLevel { return level }

// matrix 返回只含二维码本体的模块矩阵（无白边），true 为黑。
func matrix(content string) ([][]bool, error) {
	q, err := qrcode.New(content, level)
	if err != nil {
		return nil, err
	}
	q.DisableBorder = true
	return q.Bitmap(), nil
}

// pad 在矩阵四周补 n 个模块的白边。
func pad(m [][]bool, n int) [][]bool {
	if len(m) == 0 {
		return m
	}
	w := len(m[0]) + 2*n
	out := make([][]bool, 0, len(m)+2*n)
	for i := 0; i < n; i++ {
		out = append(out, make([]bool, w))
	}
	for _, row := range m {
		r := make([]bool, w)
		copy(r[n:], row)
		out = append(out, r)
	}
	for i := 0; i < n; i++ {
		out = append(out, make([]bool, w))
	}
	return out
}

// SVG 把 content 渲染成矢量二维码：任意缩放都清晰，黑块合并成横向长条以减小体积。
func SVG(content string) (string, error) {
	m, err := matrix(content)
	if err != nil {
		return "", err
	}
	m = pad(m, svgQuiet)
	n := len(m)
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges">`, n, n)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="#fff"/><path fill="#000" d="`, n, n)
	for y, row := range m {
		for x := 0; x < len(row); {
			if !row[x] {
				x++
				continue
			}
			start := x
			for x < len(row) && row[x] {
				x++
			}
			fmt.Fprintf(&b, "M%d %dh%dv1h-%dz", start, y, x-start, x-start)
		}
	}
	b.WriteString(`"/></svg>`)
	return b.String(), nil
}

// Render 把 content 渲染成终端二维码并写入 w。
// color 为 true 时使用 ANSI 黑白配色，false 时只用字符本身（适合重定向到文件）。
func Render(w io.Writer, content string, color bool) error {
	m, err := matrix(content)
	if err != nil {
		return err
	}
	bitmap := pad(m, terminalQuiet)

	var b strings.Builder
	if color {
		b.WriteString(ansiBegin)
	}
	for y := 0; y < len(bitmap); y += 2 {
		for x := 0; x < len(bitmap[y]); x++ {
			top := bitmap[y][x]
			bottom := false
			if y+1 < len(bitmap) {
				bottom = bitmap[y+1][x]
			}
			switch {
			case top && bottom:
				b.WriteString("█")
			case top:
				b.WriteString("▀")
			case bottom:
				b.WriteString("▄")
			default:
				b.WriteString(" ")
			}
		}
		b.WriteString("\n")
	}
	if color {
		b.WriteString(ansiReset)
	}
	_, err = io.WriteString(w, b.String())
	return err
}
