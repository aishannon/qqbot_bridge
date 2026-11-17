package codebuddy

import (
	"context"
	"testing"
)

func TestParseVersion(t *testing.T) {
	cases := map[string]string{
		"2.181.0\n":               "2.181.0",
		"CodeBuddy Code 2.181.0 ": "2.181.0",
		"codebuddy v3.0.1-beta.2": "3.0.1-beta.2",
		"":                        "",
		"no version here":         "",
	}
	for in, want := range cases {
		if got := parseVersion(in); got != want {
			t.Errorf("parseVersion(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestVersionMissingCommand(t *testing.T) {
	d := &driver{command: "qqbot-no-such-command"}
	if v := d.Version(context.Background()); v != "" {
		t.Fatalf("命令不存在时应返回空串，得到 %q", v)
	}
}
