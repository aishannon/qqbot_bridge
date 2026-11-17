package bridge

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFindNewImages(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old.png")
	if err := os.WriteFile(old, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}

	fresh := filepath.Join(dir, "shots", "new.png")
	if err := os.MkdirAll(filepath.Dir(fresh), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fresh, []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}

	skipped := filepath.Join(dir, "node_modules", "dep.png")
	if err := os.MkdirAll(filepath.Dir(skipped), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skipped, []byte("z"), 0o644); err != nil {
		t.Fatal(err)
	}

	notImage := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(notImage, []byte("t"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := findNewImages(dir, time.Now().Add(-time.Minute), 5)
	if len(got) != 1 || got[0] != fresh {
		t.Fatalf("期望只返回新截图 %s，实际 %v", fresh, got)
	}
}

func TestFindNewImagesLimit(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.png", "b.png", "c.png"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		// 让修改时间有明确先后，避免同秒排序不稳
		ts := time.Now().Add(-time.Duration(len(name)) * time.Second)
		_ = os.Chtimes(path, ts, ts)
	}
	if got := findNewImages(dir, time.Now().Add(-time.Minute), 2); len(got) != 2 {
		t.Fatalf("期望 2 张，实际 %d：%v", len(got), got)
	}
}

func TestFindNewImagesIgnoresEmptyAndLarge(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.png")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := findNewImages(dir, time.Now().Add(-time.Minute), 3); len(got) != 0 {
		t.Fatalf("空文件不应回传，实际 %v", got)
	}
}

// TestFindNewImagesScansShotsDir 验证 .shots 这类点目录不会被误跳过：
// 系统提示词就是让 agent 把截图存到 .shots 的。
func TestFindNewImagesScansShotsDir(t *testing.T) {
	dir := t.TempDir()
	shot := filepath.Join(dir, ".shots", "home.png")
	if err := os.MkdirAll(filepath.Dir(shot), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shot, []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := findNewImages(dir, time.Now().Add(-time.Minute), 3)
	if len(got) != 1 || got[0] != shot {
		t.Fatalf(".shots 下的截图应被回传，实际 %v", got)
	}
}

// TestFindNewImagesSkipsDeepDirs 验证超过深度限制的目录不再遍历。
func TestFindNewImagesSkipsDeepDirs(t *testing.T) {
	dir := t.TempDir()
	deep := filepath.Join(dir, "a", "b", "c", "d", "deep.png")
	if err := os.MkdirAll(filepath.Dir(deep), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(deep, []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := findNewImages(dir, time.Now().Add(-time.Minute), 3); len(got) != 0 {
		t.Fatalf("超过 %d 层的图片不应回传，实际 %v", maxScanDepth, got)
	}
}

// TestFindNewImagesSkipsOtherDotDirs 验证 .git 之外的隐藏目录也被跳过。
func TestFindNewImagesSkipsOtherDotDirs(t *testing.T) {
	dir := t.TempDir()
	hidden := filepath.Join(dir, ".cache", "x.png")
	if err := os.MkdirAll(filepath.Dir(hidden), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hidden, []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := findNewImages(dir, time.Now().Add(-time.Minute), 3); len(got) != 0 {
		t.Fatalf("普通隐藏目录不应扫描，实际 %v", got)
	}
}
