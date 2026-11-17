package bridge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"qqbot_bridge/internal/gitinfo"
)

// statusText 渲染当前会话状态，并列出其他正在并行执行的会话。
func (b *Bridge) statusText() string {
	cfg := b.cfg.Get()
	cur, ok := currentSession(cfg)
	var sb strings.Builder
	if !ok {
		return "当前没有可用会话，发送 `/new <目录>` 新建一个。"
	}
	fmt.Fprintf(&sb, "**当前会话**：%s · `%s`\n目录：`%s`", displayName(cur), cur.ID, cur.Dir)
	if git := b.gitStatusText(cur.Dir); git != "" {
		sb.WriteString("\n" + git)
	}
	if cur.AgentID != "" {
		fmt.Fprintf(&sb, "\n%s 会话：`%s`", agentTitle(b.driverName(cur)), cur.AgentID)
	}
	line := "\n" + b.modelFooter(cur)
	id, fromCLI := b.effectiveModel(b.driverName(cur), cfg, cur)
	if id != "" {
		if !fromCLI {
			line += " · 来源：" + modelSource(cfg, cur)
		}
		// 只读缓存，避免 /status 还要等模型目录刷新
		rate, updated, ok := b.modelRate(b.driverName(cur), id)
		// 既没倍率也没有缓存时间（比如 Cursor 根本不提供倍率），就不写倍率，
		// 免得出现「倍率未知（01-01 00:00）」这种误导人的零值时间
		if ok || !updated.IsZero() {
			if !ok {
				rate = "倍率未知"
			}
			line += " · " + rate
			line += "（" + updatedText(updated, time.Now()) + "）"
		}
	}
	sb.WriteString(line)

	b.mu.Lock()
	task := b.tasks[cur.ID]
	snap := b.last[cur.ID]
	var others []string
	for _, s := range cfg.Sessions {
		if s.ID == cur.ID || b.tasks[s.ID] == nil {
			continue
		}
		elapsed := 0.0
		if other := b.last[s.ID]; !other.StartedAt.IsZero() {
			elapsed = time.Since(other.StartedAt).Seconds()
		}
		others = append(others, fmt.Sprintf("%s（%.0f 秒）", displayName(s), elapsed))
	}
	b.mu.Unlock()

	if task != nil {
		fmt.Fprintf(&sb, "\n**状态**：执行中（%.0f 秒）", time.Since(snap.StartedAt).Seconds())
	} else if !snap.StartedAt.IsZero() {
		fmt.Fprintf(&sb, "\n**状态**：空闲（上次任务 %s，%d 步）",
			snap.StartedAt.Format("15:04"), len(snap.Tools))
	} else {
		sb.WriteString("\n**状态**：空闲")
	}
	if len(others) > 0 {
		fmt.Fprintf(&sb, "\n并行任务：%s", strings.Join(others, "、"))
	}
	return sb.String()
}

// gitStatusText 返回当前工作目录的 Git 概况：分支、上游、变更和最近提交。
// 不是仓库时给一句明确提示，读取失败则静默忽略，不影响 /status 本身。
func (b *Bridge) gitStatusText(dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	info, err := gitinfo.Read(ctx, dir)
	if err != nil {
		if errors.Is(err, gitinfo.ErrNotRepo) {
			return "Git：不是 Git 仓库"
		}
		b.logf("warn", "读取 git 信息失败：%v", err)
		return ""
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Git：`%s`", info.BranchText())
	if info.Upstream != "" && info.Tracked {
		if info.Ahead > 0 || info.Behind > 0 {
			fmt.Fprintf(&sb, " · ↑%d ↓%d", info.Ahead, info.Behind)
		} else {
			sb.WriteString(" · 与上游一致")
		}
	}
	if info.Root != "" && cleanDir(info.Root) != cleanDir(dir) {
		fmt.Fprintf(&sb, "\n仓库：`%s`", info.Root)
	}

	if info.HasChanges() {
		var parts []string
		if info.Staged > 0 {
			parts = append(parts, fmt.Sprintf("暂存 %d", info.Staged))
		}
		if info.Modified > 0 {
			parts = append(parts, fmt.Sprintf("未暂存 %d", info.Modified))
		}
		if info.Untracked > 0 {
			parts = append(parts, fmt.Sprintf("未跟踪 %d", info.Untracked))
		}
		if info.Conflicted > 0 {
			parts = append(parts, fmt.Sprintf("冲突 %d", info.Conflicted))
		}
		fmt.Fprintf(&sb, "\n变更：%s", strings.Join(parts, " · "))
	} else {
		sb.WriteString("\n变更：工作区干净")
	}

	if info.Commit != "" {
		fmt.Fprintf(&sb, "\n最近：`%s` %s", info.Commit, oneLine(info.Subject, 50))
		if info.Age != "" {
			fmt.Fprintf(&sb, "（%s）", info.Age)
		}
	}
	return sb.String()
}
