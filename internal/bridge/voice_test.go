package bridge

import (
	"testing"

	"qqbot_bridge/internal/gateway"
)

func TestPickVoice(t *testing.T) {
	cases := []struct {
		name string
		atts []gateway.Attachment
		want bool
	}{
		{"无附件", nil, false},
		{"图片", []gateway.Attachment{{ContentType: "image/png"}}, false},
		{"语音类型", []gateway.Attachment{{ContentType: "voice"}}, true},
		{"语音子类型", []gateway.Attachment{{ContentType: "voice/wav"}}, true},
		{"silk", []gateway.Attachment{{ContentType: "audio/silk"}}, true},
		{"只有 wav 地址", []gateway.Attachment{{VoiceWavURL: "https://x/y.wav"}}, true},
	}
	for _, c := range cases {
		if _, ok := pickVoice(c.atts); ok != c.want {
			t.Fatalf("%s: 期望 %v，实际 %v", c.name, c.want, ok)
		}
	}
}

func TestVoiceTextPrefersContent(t *testing.T) {
	att := gateway.Attachment{ContentType: "voice", ASRReferText: "ASR 结果"}
	if got := voiceText(att, "content 里的文字"); got != "content 里的文字" {
		t.Fatalf("应优先用 content，实际 %q", got)
	}
	if got := voiceText(att, "   "); got != "ASR 结果" {
		t.Fatalf("content 为空时应回落到 asr_refer_text，实际 %q", got)
	}
	if got := voiceText(gateway.Attachment{ContentType: "voice"}, ""); got != "" {
		t.Fatalf("都没有时应返回空，实际 %q", got)
	}
}
