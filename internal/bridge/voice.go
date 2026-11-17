package bridge

import (
	"strings"

	"qqbot_bridge/internal/gateway"
)

// pickVoice 从附件里挑出第一条语音。
func pickVoice(atts []gateway.Attachment) (gateway.Attachment, bool) {
	for _, a := range atts {
		ct := strings.ToLower(strings.TrimSpace(a.ContentType))
		if ct == "voice" || strings.HasPrefix(ct, "voice/") ||
			strings.Contains(ct, "silk") || a.VoiceWavURL != "" {
			return a, true
		}
	}
	return gateway.Attachment{}, false
}

// voiceText 取语音消息自带的文字。
//
// QQ 已经把语音转写放在附件里（asr_refer_text），部分场景还会直接放进
// content，所以这里不需要自己下载音频做识别。
func voiceText(att gateway.Attachment, content string) string {
	if text := strings.TrimSpace(content); text != "" {
		return text
	}
	return strings.TrimSpace(att.ASRReferText)
}
