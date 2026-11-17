package bridge

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// processText 把工具调用列表压成一条过程消息，相同调用合并计数。
func processText(tools []string, elapsed time.Duration) string {
	// 相同调用合并计数，避免把 30 次 Bash 铺满屏幕
	type agg struct {
		label string
		count int
	}
	var order []string
	counts := make(map[string]*agg)
	for _, t := range tools {
		if a, ok := counts[t]; ok {
			a.count++
			continue
		}
		counts[t] = &agg{label: t, count: 1}
		order = append(order, t)
	}
	var lines []string
	for _, k := range order {
		a := counts[k]
		if a.count > 1 {
			lines = append(lines, fmt.Sprintf("- `%s` ×%d", a.label, a.count))
		} else {
			lines = append(lines, "- `"+a.label+"`")
		}
	}
	const maxLines = 12
	more := 0
	if len(lines) > maxLines {
		more = len(lines) - maxLines
		lines = lines[:maxLines]
	}
	text := fmt.Sprintf("**过程** · %s · %d 步\n%s", humanDuration(elapsed), len(tools), strings.Join(lines, "\n"))
	if more > 0 {
		text += fmt.Sprintf("\n…还有 %d 项", more)
	}
	return text
}

// humanDuration 把时长写成「45 秒」「2 分 5 秒」。
func humanDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.0f 秒", d.Seconds())
	}
	return fmt.Sprintf("%d 分 %d 秒", int(d.Minutes()), int(d.Seconds())%60)
}

// oneLine 把多行文本压成一行并截断，用于日志和摘要。
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

// atoi 只接受纯数字，其余返回 0（指令里的 `/2` 靠它识别）。
func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// waitWithin 最多等 wg d 时间，超时返回 false，避免收尾把整轮任务拖住。
func waitWithin(wg *sync.WaitGroup, d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}
