package cursor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// 测试用 helper-process 模式把测试二进制本身伪装成 cursor-agent：
// 设置了 fakeEnv 时，TestMain 不跑测试，而是按环境变量回放 testdata 里的
// 真实采样输出，从而不依赖本机装没装、登没登录 Cursor。
const (
	fakeEnv       = "CURSOR_FAKE_CLI"    // 非空即进入伪装模式
	fakeStream    = "CURSOR_FAKE_STREAM" // -p 时回放到 stdout 的文件
	fakeStderr    = "CURSOR_FAKE_STDERR" // 写到 stderr 的文件
	fakeExit      = "CURSOR_FAKE_EXIT"   // 退出码
	fakeHang      = "CURSOR_FAKE_HANG"   // 非空则输出完后一直挂着，测试 Stop
	fakeArgsOut   = "CURSOR_FAKE_ARGS"   // 把 argv 写成 JSON 到这个文件
	fakeModels    = "CURSOR_FAKE_MODELS" // models 子命令输出的文件
	fakeStatus    = "CURSOR_FAKE_STATUS" // status 子命令输出的文件
	fakeVersion   = "2026.11.13-eacade1"
	fakeCountFile = "CURSOR_FAKE_COUNT" // 每次调用往这个文件追加一个字节，用于统计调用次数
)

func TestMain(m *testing.M) {
	if os.Getenv(fakeEnv) != "" {
		os.Exit(fakeCLI(os.Args[1:]))
	}
	os.Exit(m.Run())
}

// fakeCLI 模拟 cursor-agent 的几个入口。
func fakeCLI(args []string) int {
	if p := os.Getenv(fakeArgsOut); p != "" {
		data, _ := json.Marshal(args)
		_ = os.WriteFile(p, data, 0o644)
	}
	if p := os.Getenv(fakeCountFile); p != "" {
		f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err == nil {
			_, _ = f.Write([]byte{'x'})
			_ = f.Close()
		}
	}
	code, _ := strconv.Atoi(os.Getenv(fakeExit))
	copyFile := func(env string, w *os.File) {
		if p := os.Getenv(env); p != "" {
			data, err := os.ReadFile(p)
			if err == nil {
				_, _ = w.Write(data)
			}
		}
	}

	switch {
	case len(args) > 0 && args[0] == "--version":
		fmt.Println(fakeVersion)
		return 0
	case len(args) > 0 && args[0] == "models":
		copyFile(fakeModels, os.Stdout)
		copyFile(fakeStderr, os.Stderr)
		return code
	case len(args) > 0 && args[0] == "status":
		copyFile(fakeStatus, os.Stdout)
		return code
	}

	copyFile(fakeStream, os.Stdout)
	copyFile(fakeStderr, os.Stderr)
	if os.Getenv(fakeHang) != "" {
		time.Sleep(time.Minute)
	}
	return code
}

// fakeDriver 返回一个指向测试二进制的驱动，并打开伪装模式。
func fakeDriver(t *testing.T) *driver {
	t.Helper()
	t.Setenv(fakeEnv, "1")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return &driver{command: exe, home: t.TempDir()}
}

// testdata 返回 testdata 下文件的绝对路径。
func testdata(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// writeTemp 把内容写进临时文件并返回路径。
func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}
