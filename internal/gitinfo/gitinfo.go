// Package gitinfo 读取一个工作目录的 Git 概况：分支、上游、变更和最近提交。
//
// 只做只读查询（GIT_OPTIONAL_LOCKS=0），不会加锁或修改仓库；所有命令都带
// 超时，避免卡在坏仓库或网络文件系统上。
package gitinfo

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ErrNotRepo 表示目录不在 Git 工作区内。
var ErrNotRepo = errors.New("不是 Git 仓库")

// Info 是一个仓库的当前状态。
type Info struct {
	Root       string // 仓库根目录
	Branch     string // 分支名；detached 时为 short commit
	Detached   bool
	Upstream   string
	Tracked    bool // 有 upstream 且能算出 ahead/behind
	Ahead      int
	Behind     int
	Staged     int
	Modified   int
	Untracked  int
	Conflicted int
	Commit     string // 最近提交的短 hash
	Subject    string // 最近提交标题
	Age        string // 最近提交的相对时间
}

// Read 读取 dir 的 Git 概况。dir 可以是仓库里的任意子目录，整体 2 秒超时。
func Read(ctx context.Context, dir string) (Info, error) {
	if strings.TrimSpace(dir) == "" {
		return Info{}, ErrNotRepo
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	root, err := run(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return Info{}, ErrNotRepo
	}
	info := Info{Root: strings.TrimSpace(root)}
	if info.Root == "" {
		return Info{}, ErrNotRepo
	}

	out, err := run(ctx, dir, "status", "--porcelain=v2", "--branch", "--untracked-files=normal")
	if err != nil {
		return Info{}, err
	}
	parseStatus(&info, out)

	// 最近提交：没提交过的新仓库会失败，忽略即可
	if log, err := run(ctx, dir, "log", "-1", "--format=%h%x00%s%x00%ct"); err == nil {
		parts := strings.Split(strings.TrimRight(log, "\n"), "\x00")
		if len(parts) == 3 {
			info.Commit, info.Subject = parts[0], parts[1]
			if sec, err := strconv.ParseInt(strings.TrimSpace(parts[2]), 10, 64); err == nil {
				info.Age = relativeTime(time.Unix(sec, 0))
			}
		}
	}
	return info, nil
}

// HasChanges 表示工作区是否有任何改动。
func (i Info) HasChanges() bool {
	return i.Staged+i.Modified+i.Untracked+i.Conflicted > 0
}

// BranchText 返回适合展示的分支描述。
func (i Info) BranchText() string {
	if i.Detached {
		return "detached @" + i.Branch
	}
	return i.Branch
}

// parseStatus 解析 `git status --porcelain=v2 --branch` 的输出。
func parseStatus(info *Info, out string) {
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "# branch.head "):
			head := strings.TrimSpace(strings.TrimPrefix(line, "# branch.head "))
			if head == "(detached)" {
				info.Detached = true
			} else {
				info.Branch = head
			}
		case strings.HasPrefix(line, "# branch.oid "):
			if info.Branch == "" {
				oid := strings.TrimSpace(strings.TrimPrefix(line, "# branch.oid "))
				if oid != "(initial)" && len(oid) >= 7 {
					info.Branch = oid[:7]
				}
			}
		case strings.HasPrefix(line, "# branch.upstream "):
			info.Upstream = strings.TrimSpace(strings.TrimPrefix(line, "# branch.upstream "))
		case strings.HasPrefix(line, "# branch.ab "):
			info.Tracked = true
			fields := strings.Fields(strings.TrimPrefix(line, "# branch.ab "))
			for _, f := range fields {
				if len(f) < 2 {
					continue
				}
				n, err := strconv.Atoi(f[1:])
				if err != nil {
					continue
				}
				switch f[0] {
				case '+':
					info.Ahead = n
				case '-':
					info.Behind = n
				}
			}
		case strings.HasPrefix(line, "? "):
			info.Untracked++
		case strings.HasPrefix(line, "u "):
			info.Conflicted++
		case strings.HasPrefix(line, "1 "), strings.HasPrefix(line, "2 "):
			fields := strings.Fields(line)
			if len(fields) < 2 || len(fields[1]) < 2 {
				continue
			}
			if fields[1][0] != '.' {
				info.Staged++
			}
			if fields[1][1] != '.' {
				info.Modified++
			}
		}
	}
}

// run 执行一条只读 git 命令，超时由调用方的 context 控制。
func run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(cmd.Environ(),
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_TERMINAL_PROMPT=0",
	)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s 失败: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// relativeTime 把提交时间转成人话，手机上比绝对时间好读。
func relativeTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时前", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%d 天前", int(d.Hours()/24))
	default:
		return t.Format("2006-01-02")
	}
}
