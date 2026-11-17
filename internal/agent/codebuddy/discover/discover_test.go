package discover

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func writeSession(t *testing.T, root, name, cwd, kind string, startedAt int64) {
	t.Helper()
	body := fmt.Sprintf(`{"cwd":%q,"kind":%q,"status":"idle","startedAt":%d}`, cwd, kind, startedAt)
	if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSessionsDedupesAndSorts(t *testing.T) {
	root := t.TempDir()
	work := t.TempDir()
	other := t.TempDir()

	writeSession(t, root, "1.json", work, "bg", 1000)
	writeSession(t, root, "2.json", work, "bg", 2000) // 同目录，保留更新的
	writeSession(t, root, "3.json", other, "daemon", 3000)
	writeSession(t, root, "4.json", other, "prewarm", 9000)
	writeSession(t, root, "5.json", filepath.Join(work, "not-exist"), "bg", 9000)
	if err := os.WriteFile(filepath.Join(root, "6.json"), []byte(`not json`), 0o600); err != nil {
		t.Fatal(err)
	}

	dirs, err := Dirs(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 2 {
		t.Fatalf("期望 2 个目录，实际 %v", dirs)
	}
	if dirs[0] != other || dirs[1] != work {
		t.Fatalf("应按启动时间倒序：%v", dirs)
	}
}

func TestSessionsMissingRootIsEmpty(t *testing.T) {
	dirs, err := Dirs(filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Fatalf("目录不存在不应报错：%v", err)
	}
	if len(dirs) != 0 {
		t.Fatalf("应返回空列表：%v", dirs)
	}
}
