package bridge

import (
	"strings"
	"testing"
)

func TestSplitTextShort(t *testing.T) {
	got := splitText("一句话", 100)
	if len(got) != 1 || got[0] != "一句话" {
		t.Fatalf("短文本应原样返回：%v", got)
	}
	if splitText("   ", 100) != nil {
		t.Fatal("空白应返回 nil")
	}
}

func TestSplitTextByParagraph(t *testing.T) {
	a := strings.Repeat("甲", 60)
	b := strings.Repeat("乙", 60)
	got := splitText(a+"\n"+b, 80)
	if len(got) != 2 {
		t.Fatalf("应按行切成 2 段，实际 %d：%v", len(got), got)
	}
	if got[0] != a || got[1] != b {
		t.Fatalf("切分位置不对：%v", got)
	}
}

func TestSplitTextBySentence(t *testing.T) {
	long := strings.Repeat("这是一个句子。", 20) // 140 字，单行
	got := splitText(long, 50)
	if len(got) < 3 {
		t.Fatalf("超长单行应按句切开，实际 %d 段", len(got))
	}
	for _, chunk := range got {
		if len([]rune(chunk)) > 50 {
			t.Fatalf("单段超出上限 %d：%q", len([]rune(chunk)), chunk)
		}
		if !strings.HasSuffix(chunk, "。") {
			t.Fatalf("应在句号处断开：%q", chunk)
		}
	}
}

func TestSplitSentencesKeepsFileName(t *testing.T) {
	got := splitSentences("看一下 README.md 和 v1.2 的说明")
	if len(got) != 1 {
		t.Fatalf("英文点号不应断句：%v", got)
	}
}

func TestSplitSentencesBreaksOnEnglishPeriod(t *testing.T) {
	got := splitSentences("first part. second part. third")
	if len(got) != 3 {
		t.Fatalf("英文句号后跟空格应断句：%v", got)
	}
}

func TestSplitTextHardCut(t *testing.T) {
	long := strings.Repeat("无标点长串", 60) // 300 字无句号
	got := splitText(long, 80)
	for _, chunk := range got {
		if len([]rune(chunk)) > 80 {
			t.Fatalf("硬切后仍超限：%d", len([]rune(chunk)))
		}
	}
	if strings.Join(got, "") != long {
		t.Fatal("硬切不应丢字")
	}
}
