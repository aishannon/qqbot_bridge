package bridge

import (
	"net/url"
	"regexp"
	"strings"
	"testing"
)

var tagValueRe = regexp.MustCompile(`text="([^"]*)" show="([^"]*)"`)

func TestCmdInputEncodesValues(t *testing.T) {
	got := cmdInput("/switch 1", "切到 1. 默认会话")
	if !strings.HasPrefix(got, "<qqbot-cmd-input ") || !strings.HasSuffix(got, "/>") {
		t.Fatalf("标签格式不对：%s", got)
	}
	if strings.Contains(got, " ") && strings.Contains(got, `text="/switch 1"`) {
		t.Fatalf("text 应做 URL 编码：%s", got)
	}
	m := tagValueRe.FindStringSubmatch(got)
	if m == nil {
		t.Fatalf("解析不到 text/show：%s", got)
	}
	text, err := url.QueryUnescape(m[1])
	if err != nil || text != "/switch 1" {
		t.Fatalf("text 解码失败：%q %v", text, err)
	}
	show, err := url.QueryUnescape(m[2])
	if err != nil || show != "切到 1. 默认会话" {
		t.Fatalf("show 解码失败：%q %v", show, err)
	}
	if !strings.Contains(got, `reference="false"`) {
		t.Fatalf("reference 应为 false：%s", got)
	}
}

func TestCmdInputShowFallsBackToText(t *testing.T) {
	got := cmdInput("/list", "")
	if !strings.Contains(got, `show="/list"`) {
		t.Fatalf("show 为空时应取 text：%s", got)
	}
}

func TestCmdInputLimitsTo100Runes(t *testing.T) {
	long := strings.Repeat("会", 150)
	got := cmdInput(long, long)
	m := tagValueRe.FindStringSubmatch(got)
	if m == nil {
		t.Fatalf("解析不到 text/show：%s", got)
	}
	for i, raw := range m[1:] {
		decoded, err := url.QueryUnescape(raw)
		if err != nil {
			t.Fatal(err)
		}
		if n := len([]rune(decoded)); n != 100 {
			t.Fatalf("第 %d 个值应裁到 100 字，实际 %d", i+1, n)
		}
	}
}

func TestStripCmdTagsRemovesTagLines(t *testing.T) {
	in := "**会话列表**\n1. main\n" +
		cmdInput("/switch 1", "切到 1. main") + "\n\n" +
		cmdInput("/status", "状态") + "\n尾部说明"
	got := stripCmdTags(in)
	if strings.Contains(got, "qqbot-cmd") {
		t.Fatalf("标签没删干净：\n%s", got)
	}
	if !strings.Contains(got, "尾部说明") || !strings.Contains(got, "会话列表") {
		t.Fatalf("正文被误删：\n%s", got)
	}
	if strings.Contains(got, "\n\n\n") {
		t.Fatalf("空行没有收敛：\n%q", got)
	}
}
