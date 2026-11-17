package bridge

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// cmdInputTag 是 QQ Markdown 消息里的「参数指令」标签：点击后把 text 填进输入框，
// 用户自己编辑、发送。仅在 Markdown 消息（msg_type=2）里生效。
const cmdInputTag = `<qqbot-cmd-input text="%s" show="%s" reference="false" />`

// cmdTagRe 用来在降级成纯文本时去掉指令标签，免得把源码直接摊给用户。
var cmdTagRe = regexp.MustCompile(`\<qqbot-cmd-(?:input|enter)\s[^>]*?/\>`)

// cmdInput 生成一个参数指令标签，text/show 都做 URL 编码并裁到平台上限 100 字符。
func cmdInput(text, show string) string {
	text = limitRunes(strings.TrimSpace(text), 100)
	show = limitRunes(strings.TrimSpace(show), 100)
	if show == "" {
		show = text
	}
	return fmt.Sprintf(cmdInputTag, escapeTagValue(text), escapeTagValue(show))
}

// escapeTagValue 按 QQ 要求做 URL 编码；保留 / 让指令本体仍然可读，
// 空格统一成 %20，避免不同客户端对 + 的解析差异。
func escapeTagValue(s string) string {
	s = url.PathEscape(s)
	s = strings.ReplaceAll(s, "%2F", "/")
	s = strings.ReplaceAll(s, "%2f", "/")
	return s
}

// stripCmdTags 去掉指令标签，并清理标签独占一行时留下的空行。
func stripCmdTags(s string) string {
	s = cmdTagRe.ReplaceAllString(s, "")
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, line := range lines {
		line = strings.TrimRight(line, " \t")
		if line == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func limitRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}
