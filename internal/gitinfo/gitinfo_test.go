package gitinfo

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v 失败：%v\n%s", args, err, out)
	}
}

func TestReadRepoStatus(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("环境里没有 git")
	}
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "tester")

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "a.txt")
	runGit(t, dir, "commit", "-m", "init")

	// 未暂存改动 1 个、已暂存新增 1 个、未跟踪 1 个
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("v2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "staged.txt"), []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "staged.txt")
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("n"), 0o600); err != nil {
		t.Fatal(err)
	}

	info, err := Read(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Branch != "main" || info.Detached {
		t.Fatalf("分支不对：%+v", info)
	}
	if info.Staged != 1 || info.Modified != 1 || info.Untracked != 1 {
		t.Fatalf("变更统计不对：%+v", info)
	}
	if info.Conflicted != 0 {
		t.Fatalf("不应有冲突：%+v", info)
	}
	if info.Commit == "" || info.Subject != "init" {
		t.Fatalf("最近提交不对：%+v", info)
	}
	if !info.HasChanges() {
		t.Fatal("应识别出有改动")
	}
}

func TestReadSubdirAndCleanRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("环境里没有 git")
	}
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "trunk")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "tester")
	runGit(t, dir, "commit", "--allow-empty", "-m", "init")

	sub := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	info, err := Read(context.Background(), sub)
	if err != nil {
		t.Fatal(err)
	}
	if info.Root != dir {
		t.Fatalf("仓库根目录不对：%q", info.Root)
	}
	if info.Branch != "trunk" {
		t.Fatalf("子目录读不到分支：%+v", info)
	}
	if info.HasChanges() {
		t.Fatalf("干净仓库不应有改动：%+v", info)
	}
}

func TestReadAheadOfUpstream(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("环境里没有 git")
	}
	bare := t.TempDir()
	runGit(t, bare, "init", "--bare", "-b", "main")

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "tester")
	runGit(t, dir, "commit", "--allow-empty", "-m", "init")
	runGit(t, dir, "remote", "add", "origin", bare)
	runGit(t, dir, "push", "-u", "origin", "main")
	runGit(t, dir, "commit", "--allow-empty", "-m", "local")

	info, err := Read(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Tracked || info.Upstream != "origin/main" {
		t.Fatalf("上游信息不对：%+v", info)
	}
	if info.Ahead != 1 || info.Behind != 0 {
		t.Fatalf("领先/落后不对：%+v", info)
	}
}

func TestReadNotRepo(t *testing.T) {
	_, err := Read(context.Background(), t.TempDir())
	if !errors.Is(err, ErrNotRepo) {
		t.Fatalf("非仓库应返回 ErrNotRepo，实际 %v", err)
	}
}
