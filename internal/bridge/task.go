package bridge

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"qqbot_bridge/internal/agent"
	"qqbot_bridge/internal/config"
)

// startTask 把一条自然语言指令交给当前会话的 agent。
// display 是回执里展示的指令摘要，为空时用 prompt 本身。
func (b *Bridge) startTask(ctx context.Context, t chatTarget, prompt, display string) {
	cfg := b.cfg.Get()
	sess, ok := currentSession(cfg)
	if !ok {
		b.reply(ctx, t, "还没有可用会话，先发送 `/new <目录>` 新建一个。")
		return
	}

	b.mu.Lock()
	running := b.tasks[sess.ID] != nil || b.starting[sess.ID]
	if !running {
		b.starting[sess.ID] = true
	}
	b.mu.Unlock()
	if running {
		b.reply(ctx, t, fmt.Sprintf("会话「%s」还有任务在执行。可以 `/switch` 到其他目录继续下发，或 `/stop` 中断后重发。", displayName(sess)))
		return
	}

	if display == "" {
		display = prompt
	}
	replier := t.replier(b.api, cfg.Push.MaxMessages, cfg.Push.Markdown)
	replier.SetLogger(b.logf)
	replier.SetFooter(b.modelFooter(sess))
	replier.Text(ctx, fmt.Sprintf("**收到** · 会话 `%s`\n指令：%s\n目录：`%s`",
		displayName(sess), oneLine(display, 60), sess.Dir))

	go b.runTask(t, sess, prompt, replier)
}

// runTask 真正执行一轮 agent，并在结束后回传过程与结果。
func (b *Bridge) runTask(t chatTarget, sess config.Session, prompt string, replier *Replier) {
	cfg := b.cfg.Get()
	started := time.Now()
	label := displayName(sess)

	// startTask 已经占住 starting 标记，任务结束后释放
	defer func() {
		b.mu.Lock()
		delete(b.starting, sess.ID)
		b.mu.Unlock()
	}()

	// 截图以这一轮的开始时间为界
	since := started.Add(-2 * time.Second)

	timeout := cfg.Agent.Timeout()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// 任务跑起来就一直维持「正在输入」，让手机端能看出还在干活（群聊没有这个状态）
	if !t.isGroup() {
		go b.keepTyping(ctx, t.userID)
	}

	title := agentTitle(b.driverName(sess))
	driver, err := b.driverForSession(sess)
	if err != nil {
		b.logf("error", "解析 agent 驱动失败：%v", err)
		replier.Text(context.Background(), "**启动 "+title+" 失败**："+err.Error())
		return
	}
	task, err := driver.Start(ctx, agent.Options{
		Dir:                sess.Dir,
		Model:              modelFor(cfg, sess),
		PermissionMode:     cfg.Agent.PermissionMode,
		MaxTurns:           cfg.Agent.MaxTurns,
		Resume:             sess.AgentID,
		AppendSystemPrompt: promptFor(driver.Name()),
	}, prompt)
	if err != nil {
		b.logf("error", "启动 agent 失败：%v", err)
		replier.Text(context.Background(), "**启动 "+title+" 失败**："+err.Error())
		return
	}

	b.mu.Lock()
	b.tasks[sess.ID] = task
	b.last[sess.ID] = TaskSnapshot{Running: true, Prompt: prompt, StartedAt: started}
	b.mu.Unlock()

	// 长任务每两分钟推一次进度，别让用户以为石头沉了海
	go b.reportProgress(ctx, task, replier, started, timeout, label)

	// 团队模式下成员的消息不会出现在 stdout 的 stream-json 里，只会写进
	// 会话 transcript，所以这里跟读 transcript 实时转发到 QQ。
	teamStop := make(chan struct{})
	var teamOnce sync.Once
	stopTeam := func() { teamOnce.Do(func() { close(teamStop) }) }
	defer stopTeam()
	var teamWG sync.WaitGroup
	teamWG.Add(1)
	go func() {
		defer teamWG.Done()
		b.tailTeammates(teamStop, sess, task, replier)
	}()

	// 任务执行期间每 10 秒把新消息增量同步到 IDE 历史，避免长任务期间看不到进度
	syncStop := make(chan struct{})
	var syncOnce sync.Once
	stopSync := func() { syncOnce.Do(func() { close(syncStop) }) }
	var syncWG sync.WaitGroup
	syncWG.Add(1)
	go func() {
		defer syncWG.Done()
		b.syncLoop(syncStop, ctx, sess, task)
	}()
	defer func() {
		stopSync()
		syncWG.Wait()
	}()

	defer func() {
		b.mu.Lock()
		delete(b.tasks, sess.ID)
		snap := b.last[sess.ID]
		snap.Running = false
		b.last[sess.ID] = snap
		b.mu.Unlock()
	}()

	waitErr := task.Wait()

	// 先把任务从表里摘掉，再等团队转发收尾：收尾还要发几条消息，
	// 不能让用户在这段时间被「还有任务在执行」挡住，也不能让 /stop 假成功。
	b.mu.Lock()
	delete(b.tasks, sess.ID)
	delete(b.starting, sess.ID)
	b.mu.Unlock()

	stopTeam()
	if !waitWithin(&teamWG, 5*time.Second) {
		b.logf("warn", "团队成员消息转发未在 5 秒内收尾，先继续")
	}
	stopSync()
	syncWG.Wait()
	elapsed := time.Since(started)
	sessionID := task.SessionID()
	result := strings.TrimSpace(task.Result())
	tools := task.Tools()

	// 记住 agent 会话 ID，下一轮用 --resume 续上同一段对话
	if sessionID != "" && sessionID != sess.AgentID {
		_ = b.cfg.Update(func(c *config.Config) error {
			for i := range c.Sessions {
				if c.Sessions[i].ID == sess.ID {
					c.Sessions[i].AgentID = sessionID
					c.Sessions[i].LastUsed = time.Now()
				}
			}
			return nil
		})
	}
	if sessionID != "" {
		// 镜像到 IDE 历史库：跑在后台，不拖慢 QQ 结果回传
		go b.mirrorToIDE(sess, sessionID, false)
	}

	b.mu.Lock()
	snap := b.last[sess.ID]
	snap.SessionID = sessionID
	snap.Tools = tools
	snap.Result = result
	if waitErr != nil {
		snap.Error = waitErr.Error()
	}
	b.last[sess.ID] = snap
	b.mu.Unlock()

	out := context.Background()
	if waitErr != nil && ctx.Err() == context.DeadlineExceeded {
		replier.Text(out, fmt.Sprintf("**任务超时**（会话「%s」，%s）已中断。可以拆成更小的需求重发。", label, humanDuration(timeout)))
		return
	}
	if waitErr != nil && task.IsError() {
		msg := result
		if msg == "" {
			msg = waitErr.Error()
		}
		replier.Text(out, fmt.Sprintf("**执行失败**（会话「%s」）：%s", label, msg))
		return
	}

	// 过程：工具调用摘要。至少给结果留一条被动回复预算，截图有主动消息兜底。
	if cfg.Push.ShowToolCalls && len(tools) > 0 && replier.Left() > 1 {
		replier.Text(out, processText(tools, elapsed))
	}

	if result == "" {
		result = "（本轮没有文本输出）"
	}
	head := fmt.Sprintf("✅ **完成** · 会话「%s」 · %s · %d 步", label, humanDuration(elapsed), len(tools))
	if task.IsError() {
		head = fmt.Sprintf("⚠️ **结束（有错误）** · 会话「%s」 · %s · %d 步", label, humanDuration(elapsed), len(tools))
	}
	replier.Text(out, head+"\n\n"+result)

	// 截图：这一轮新落盘的图片
	if cfg.Push.SendImages && !t.isGroup() {
		b.sendImages(t.userID, sess.Dir, since, cfg.Push.ImageMax, replier)
	}
}

// stopTask 中断指定会话正在执行的任务。
func (b *Bridge) stopTask(sessionID string) string {
	b.mu.Lock()
	task := b.tasks[sessionID]
	starting := b.starting[sessionID]
	b.mu.Unlock()
	if task == nil {
		if starting {
			// driver.Start 还没返回，这时候回「没有任务」会让用户以为中断生效了
			return "任务正在启动，等一两秒再发 /stop。"
		}
		return "当前会话没有正在执行的任务。"
	}
	task.Stop()

	label := sessionID
	for _, s := range b.cfg.Get().Sessions {
		if s.ID == sessionID {
			label = displayName(s)
			break
		}
	}
	b.logf("warn", "用户中断了会话 %s 的任务", sessionID)
	return fmt.Sprintf("**已发送中断信号**：会话「%s」，稍等片刻再发新指令。", label)
}

// stopAllTasks 中断所有正在执行的会话。
func (b *Bridge) stopAllTasks() string {
	b.mu.Lock()
	tasks := make([]agent.Task, 0, len(b.tasks))
	for _, task := range b.tasks {
		if task != nil {
			tasks = append(tasks, task)
		}
	}
	b.mu.Unlock()
	if len(tasks) == 0 {
		return "当前没有正在执行的任务。"
	}
	for _, task := range tasks {
		task.Stop()
	}
	b.logf("warn", "用户中断了全部 %d 个任务", len(tasks))
	return fmt.Sprintf("**已发送中断信号**：%d 个任务", len(tasks))
}

// stopCommand 处理 `/stop`：默认当前会话，`/stop all` 全部中断，
// `/stop <编号|会话ID>` 中断指定会话。
func (b *Bridge) stopCommand(args []string) string {
	cfg := b.cfg.Get()
	if len(args) > 0 && strings.EqualFold(args[0], "all") {
		return b.stopAllTasks()
	}
	if len(args) > 0 {
		idx := sessionIndex(cfg.Sessions, args[0])
		if idx < 0 {
			return "没找到会话 `" + args[0] + "`，发送 `/status` 查看当前会话。"
		}
		return b.stopTask(cfg.Sessions[idx].ID)
	}
	cur, ok := currentSession(cfg)
	if !ok {
		return "当前没有可用会话。"
	}
	return b.stopTask(cur.ID)
}

// sendImages 把这一轮新生成的图片发回 QQ。
func (b *Bridge) sendImages(userID, dir string, since time.Time, limit int, replier *Replier) {
	paths := findNewImages(dir, since, limit)
	if len(paths) == 0 {
		return
	}
	for _, p := range paths {
		name, data := readImage(p)
		if data == nil {
			continue
		}
		if replier.Image(context.Background(), name, data) {
			b.logf("info", "已回传截图 %s", p)
		} else {
			b.logf("warn", "截图回传失败 %s", p)
		}
	}
}

// keepTyping 周期性刷新「正在输入」状态，直到任务结束或超时。
//
// 输入状态不占被动回复配额，是长任务期间唯一能让手机端看出「还在跑」的信号。
func (b *Bridge) keepTyping(ctx context.Context, userID string) {
	if err := b.api.SendC2CTyping(ctx, userID, 60); err != nil {
		// 机器人没有输入状态权限时会一直失败，第一次就放弃，避免刷日志
		b.logf("info", "「正在输入」状态不可用，已关闭：%v", err)
		return
	}
	ticker := time.NewTicker(typingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := b.api.SendC2CTyping(ctx, userID, 60); err != nil {
				b.logf("warn", "「正在输入」刷新失败，已停止：%v", err)
				return
			}
		}
	}
}

// reportProgress 每两分钟把执行进度推给用户，直到任务结束或超时。
//
// 进度消息走主动消息兜底，不依赖被动回复窗口，长任务期间也能送到。
func (b *Bridge) reportProgress(ctx context.Context, task agent.Task, replier *Replier, started time.Time, timeout time.Duration, label string) {
	ticker := time.NewTicker(progressInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-task.Done():
			return
		case <-ticker.C:
		}

		tools := task.Tools()
		latest := "正在思考"
		if n := len(tools); n > 0 {
			latest = tools[n-1]
		}
		replier.Text(context.Background(), fmt.Sprintf(
			"⏳ **进度** · 会话「%s」 · 已执行 %s · %d 步\n最近：`%s`\n（上限 %s，发送 `/stop` 可中断）",
			label, humanDuration(time.Since(started)), len(tools), oneLine(latest, 60), humanDuration(timeout)))
	}
}

// Snapshot 返回指定会话最近一次任务状态，供控制台展示。
func (b *Bridge) Snapshot(sessionID string) TaskSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.last[sessionID]
}

// Running 返回指定会话是否有任务在跑。
func (b *Bridge) Running(sessionID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.tasks[sessionID] != nil
}
