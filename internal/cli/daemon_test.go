package cli

import (
	"slices"
	"testing"
)

// 后台子进程只继承对服务本身有意义的参数：-f/-stop 是父进程专用，
// 传下去会让子进程再次后台化或直接退出。
func TestIsParentOnlyFlag(t *testing.T) {
	parentOnly := []string{"-f", "-foreground", "-stop", "-f=true", "-stop=true"}
	for _, arg := range parentOnly {
		if !isParentOnlyFlag(arg) {
			t.Errorf("%q 应被识别为父进程专用参数", arg)
		}
	}
	child := []string{"-addr", "127.0.0.1:9000", "-v", "--version", "", "-fx"}
	for _, arg := range child {
		if isParentOnlyFlag(arg) {
			t.Errorf("%q 不应被过滤", arg)
		}
	}
}

func TestChildArgsStripsParentOnlyFlags(t *testing.T) {
	got := childArgs([]string{"-addr", "127.0.0.1:9000", "-f", "-stop"})
	want := []string{"-addr", "127.0.0.1:9000"}
	if !slices.Equal(got, want) {
		t.Fatalf("透传参数不对：%v，期望 %v", got, want)
	}
	if got := childArgs(nil); len(got) != 0 {
		t.Fatalf("空参数应返回空列表：%v", got)
	}
}
