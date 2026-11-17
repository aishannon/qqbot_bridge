package agent

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// fakeDriver 只用来验证注册表，不做任何真实调用。
type fakeDriver struct{}

func (fakeDriver) Name() string                                         { return "fake" }
func (fakeDriver) Start(context.Context, Options, string) (Task, error) { return nil, nil }
func (fakeDriver) Version(context.Context) string                       { return "" }

func TestRegisterAndOpen(t *testing.T) {
	Register("fake", func(Settings) Driver { return fakeDriver{} })

	d, err := Open("fake", Settings{})
	if err != nil {
		t.Fatalf("Open 失败：%v", err)
	}
	if d.Name() != "fake" {
		t.Fatalf("驱动名不对：%q", d.Name())
	}
	if !slices.Contains(Drivers(), "fake") {
		t.Fatalf("登记后应出现在列表里：%v", Drivers())
	}
}

// 配置里写错驱动名时，错误信息要带上可选值，用户才能照着改。
func TestOpenUnknownDriver(t *testing.T) {
	_, err := Open("no-such-driver", Settings{})
	if err == nil {
		t.Fatal("未知驱动应返回错误")
	}
	if !strings.Contains(err.Error(), "no-such-driver") || !strings.Contains(err.Error(), "可选值") {
		t.Fatalf("错误信息不够自解释：%v", err)
	}
}

func TestParseTeammateMessagesMultiple(t *testing.T) {
	text := `<teammate-message teammate_id="a" summary="s1">第一段</teammate-message>` +
		"\n" + `<teammate-message teammate_id="b">第二段</teammate-message>`
	got := ParseTeammateMessages(text)
	if len(got) != 2 {
		t.Fatalf("应解析出 2 条，实际 %d 条", len(got))
	}
	if got[1].Name != "b" || got[1].Summary != "" || got[1].Text != "第二段" {
		t.Fatalf("第二条解析不对：%+v", got[1])
	}
}
