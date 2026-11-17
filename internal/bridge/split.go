package bridge

import "strings"

const (
	// chunkRunes 是单条消息正文的切分阈值。5000 字以内不主动切，
	// 尽量保持一次回复的完整性；平台仍报超长时由 replier 再对半切。
	chunkRunes = 5000
	// maxChunks 是一次最多拆成几条，避免超长输出把用户刷屏。
	maxChunks = 6
)

// splitText 把长文本切成多条，尽量落在段落 / 句子边界上。
//
// 规则：先按行聚合，单行超长再按句号切，句子仍超长就硬切。
func splitText(text string, limit int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if limit <= 0 {
		limit = chunkRunes
	}
	if len([]rune(text)) <= limit {
		return []string{text}
	}

	var chunks []string
	var cur []rune

	flush := func() {
		if s := strings.TrimSpace(string(cur)); s != "" {
			chunks = append(chunks, s)
		}
		cur = cur[:0]
	}

	for _, line := range strings.Split(text, "\n") {
		for _, seg := range wrapLine(line, limit) {
			runes := []rune(seg)
			if len(cur) > 0 && len(cur)+len(runes)+1 > limit {
				flush()
			}
			if len(cur) > 0 {
				cur = append(cur, '\n')
			}
			cur = append(cur, runes...)
		}
	}
	flush()
	return chunks
}

// splitForRetry 把一条被平台拒收的超长文本对半切开，用于「超长后重发」。
//
// 优先按段落/句子边界切；实在切不开就硬切，保证每次重试的片段更短，
// 不会陷入无限递归。
func splitForRetry(text string) []string {
	runes := []rune(text)
	if len(runes) < 2 {
		return nil
	}
	limit := len(runes) / 2
	if parts := splitText(text, limit); len(parts) >= 2 {
		return parts
	}
	return []string{string(runes[:limit]), string(runes[limit:])}
}

// wrapLine 把超长的一行按句子切开，单句仍超长就硬切。
func wrapLine(line string, limit int) []string {
	if len([]rune(line)) <= limit {
		return []string{line}
	}
	var out []string
	var cur []rune
	for _, seg := range splitSentences(line) {
		runes := []rune(seg)
		if len(cur) > 0 && len(cur)+len(runes) > limit {
			out = append(out, string(cur))
			cur = cur[:0]
		}
		for len(runes) > limit {
			out = append(out, string(runes[:limit]))
			runes = runes[limit:]
		}
		cur = append(cur, runes...)
	}
	if len(cur) > 0 {
		out = append(out, string(cur))
	}
	return out
}

// splitSentences 按句末标点切句。英文标点后面要跟空白或到行尾才算断句，
// 免得把 README.md、v1.2 这类内容切坏。
func splitSentences(line string) []string {
	runes := []rune(line)
	var out []string
	var cur []rune
	for i, r := range runes {
		cur = append(cur, r)
		end := false
		switch r {
		case '。', '！', '？', '；':
			end = true
		case '.', '!', '?', ';':
			if i+1 >= len(runes) || runes[i+1] == ' ' || runes[i+1] == '\t' {
				end = true
			}
		}
		if end {
			out = append(out, string(cur))
			cur = cur[:0]
		}
	}
	if len(cur) > 0 {
		out = append(out, string(cur))
	}
	return out
}
