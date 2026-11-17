package bridge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"qqbot_bridge/internal/gateway"
)

// Replier 管一次消息的回复预算。
//
// QQ 单聊里同一个 msg_id 最多被动回复 4 次。前 max 条走被动回复（带
// msg_id/msg_seq，不额外占用主动消息额度）；预算用尽、或者被动发送失败
// （消息过期、被限频）时，退化为主动消息（不带 msg_id），保证结果和截图
// 也还能送到用户手机上。
type Replier struct {
	api      *gateway.API
	userID   string
	groupID  string // 非空表示群聊，消息发到群里而不是用户单聊
	msgID    string
	max      int
	markdown bool
	// footer 非空时，每条文本消息末尾附一行「Agent · 模型」标签，
	// 用来区分这个会话是哪个 agent、哪个模型在跑
	footer string
	logf   func(level, format string, args ...any)

	mu       sync.Mutex
	seq      int
	mdBroken bool // 机器人没有 Markdown 权限时，本次任务后续都退回纯文本
	// passiveBroken 表示被动回复通道已经不可用，后续直接发主动消息，
	// 避免每条都先撞一次失败再重试。
	passiveBroken bool
}

// NewReplier 创建一次任务的回复器，markdown 为 true 时优先用 Markdown 消息。
func NewReplier(api *gateway.API, userID, msgID string, max int, markdown bool) *Replier {
	if max <= 0 {
		max = 4
	}
	return &Replier{api: api, userID: userID, msgID: msgID, max: max, markdown: markdown}
}

// NewGroupReplier 创建群聊的回复器，其余行为和单聊一致。
func NewGroupReplier(api *gateway.API, groupID, msgID string, max int, markdown bool) *Replier {
	if max <= 0 {
		max = 4
	}
	return &Replier{api: api, groupID: groupID, msgID: msgID, max: max, markdown: markdown}
}

// send 按单聊/群聊选择发送通道。
func (r *Replier) send(ctx context.Context, msg gateway.Message) error {
	if r.groupID != "" {
		_, err := r.api.SendGroup(ctx, r.groupID, msg)
		return err
	}
	_, err := r.api.SendC2C(ctx, r.userID, msg)
	return err
}

// SetFooter 设置在每条文本消息末尾附带的「Agent · 模型」标签；传空串表示不显示。
func (r *Replier) SetFooter(footer string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.footer = footer
}

// SetLogger 设置日志回调，用于记录 Markdown 降级、被动改主动这类隐形行为。
func (r *Replier) SetLogger(logf func(level, format string, args ...any)) {
	r.logf = logf
}

// Left 返回被动回复还剩几条预算。
func (r *Replier) Left() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.max - r.seq
}

// Text 发一条文本，返回是否全部发出去了。
//
// 超过 chunkRunes（5000 字）才切成多条；如果平台仍回「消息长度超限」，
// 会把这条对半切开重发，直到能发出去为止。中途某一段发不出去时返回
// false，并记一条带段号的错误日志——否则用户只看到半截结果，这边却
// 什么痕迹都没有。
func (r *Replier) Text(ctx context.Context, text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.footer != "" {
		// 跟在最后一段后面，分段发送时也只在结尾出现一次
		text += "\n\n" + r.footer
	}

	chunks := capChunks(splitText(text, chunkRunes))
	for i, chunk := range chunks {
		if r.sendText(ctx, chunk, r.markdown) {
			continue
		}
		if r.logf != nil {
			r.logf("error", "消息发送中断：第 %d/%d 段失败，用户只收到前 %d 段", i+1, len(chunks), i)
		}
		return false
	}
	return len(chunks) > 0
}

// sendText 发送一条文本：超长自动对半切，Markdown 失败转纯文本。
// 调用方需持有锁。
func (r *Replier) sendText(ctx context.Context, text string, markdown bool) bool {
	if markdown && r.mdBroken {
		markdown = false
	}

	err := r.sendOne(ctx, text, markdown)
	if err == nil {
		return true
	}
	if isTooLong(err) {
		// 平台按自己的上限拒收：先对半切开，再逐段走同样的发送逻辑
		if r.sendSplit(ctx, text, markdown) {
			return true
		}
		if r.logf != nil {
			r.logf("error", "消息超长且无法继续切分：%v", err)
		}
		return false
	}
	if markdown {
		r.mdBroken = true
		if r.logf != nil {
			r.logf("warn", "Markdown 发送失败，本次任务改用纯文本：%v", err)
		}
		// 纯文本重试；被动序号还没被占用，仍然可以走被动回复
		return r.sendText(ctx, text, false)
	}
	if r.logf != nil {
		r.logf("error", "消息发送失败：%v", err)
	}
	return false
}

// sendSplit 把超长文本对半切开逐段重发。调用方需持有锁。
func (r *Replier) sendSplit(ctx context.Context, text string, markdown bool) bool {
	parts := splitForRetry(text)
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if !r.sendText(ctx, part, markdown) {
			return false
		}
	}
	return true
}

// sendOne 发一条：先走被动回复，被动不可用时改主动消息。
//
// 超长和 Markdown 被拒的错误会原样返回，并且不占用被动预算，
// 让上层有机会切分或改用纯文本重发。
func (r *Replier) sendOne(ctx context.Context, text string, markdown bool) error {
	if r.msgID == "" || r.passiveBroken || r.seq >= r.max {
		return r.sendActive(ctx, text, markdown)
	}

	seq := r.seq + 1
	err := r.sendPassive(ctx, text, seq, markdown)
	if err == nil {
		r.seq = seq
		return nil
	}
	if isTooLong(err) || markdown {
		return err
	}

	// 被动回复真的不可用了：改用主动消息，主动消息不限量
	r.passiveBroken = true
	if r.logf != nil {
		r.logf("warn", "被动回复失败，改用主动消息：%v", err)
	}
	return r.sendActive(ctx, text, markdown)
}

// sendPassive 发一条被动回复。
func (r *Replier) sendPassive(ctx context.Context, text string, seq int, markdown bool) error {
	msg := gateway.Message{MsgID: r.msgID, MsgSeq: seq}
	if markdown {
		msg.Markdown = text
	} else {
		msg.Content = text
	}
	return r.send(ctx, msg)
}

// sendActive 发一条主动消息：不带 msg_id/msg_seq，不受被动回复条数限制。
func (r *Replier) sendActive(ctx context.Context, text string, markdown bool) error {
	msg := gateway.Message{}
	if markdown {
		msg.Markdown = text
	} else {
		msg.Content = text
	}
	return r.send(ctx, msg)
}

// Rich 发送一条带 <qqbot-cmd-input> 指令标签的 Markdown 消息。
//
// 标签只在 Markdown 消息里生效，所以 Markdown 被拒（没有权限/发送失败）时
// 退回 plain——plain 由调用方提供，不能含标签源码。
func (r *Replier) Rich(ctx context.Context, markdown, plain string) bool {
	markdown = strings.TrimSpace(markdown)
	plain = strings.TrimSpace(plain)

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.markdown && !r.mdBroken && markdown != "" {
		if r.sendMarkdown(ctx, markdown) {
			return true
		}
		r.mdBroken = true
		if r.logf != nil {
			r.logf("warn", "Markdown 指令消息发送失败，改用纯文本")
		}
	}
	if plain == "" {
		plain = stripCmdTags(markdown)
	}
	return r.sendPlain(ctx, plain)
}

// sendMarkdown 只按 Markdown 发送，失败不自行降级（由 Rich 换成 plain 正文）。
func (r *Replier) sendMarkdown(ctx context.Context, text string) bool {
	sent := false
	for _, chunk := range capChunks(splitText(text, chunkRunes)) {
		if !r.sendMarkdownOne(ctx, chunk) {
			return false
		}
		sent = true
	}
	return sent
}

// sendMarkdownOne 发一条 Markdown，超长时对半切开重发。
func (r *Replier) sendMarkdownOne(ctx context.Context, text string) bool {
	err := r.sendOne(ctx, text, true)
	if err == nil {
		return true
	}
	if isTooLong(err) {
		parts := splitForRetry(text)
		if len(parts) < 2 {
			return false
		}
		for _, part := range parts {
			if !r.sendMarkdownOne(ctx, part) {
				return false
			}
		}
		return true
	}
	if r.logf != nil {
		r.logf("warn", "Markdown 发送失败：%v", err)
	}
	return false
}

// sendPlain 只按纯文本发送，超长时同样会切分。
func (r *Replier) sendPlain(ctx context.Context, text string) bool {
	sent := false
	for _, chunk := range capChunks(splitText(text, chunkRunes)) {
		if !r.sendText(ctx, chunk, false) {
			return false
		}
		sent = true
	}
	return sent
}

// capChunks 限制一次最多发几条，剩下的用一行提示收尾。
func capChunks(chunks []string) []string {
	if len(chunks) > maxChunks {
		rest := len(chunks) - maxChunks
		chunks = chunks[:maxChunks]
		chunks[maxChunks-1] += fmt.Sprintf("\n\n…（还有 %d 段未发送）", rest)
	}
	return chunks
}

// isTooLong 判断平台错误是不是「消息长度超限」。
//
// 40054007 / 40054018 是官方文档里的超长错误码；部分环境只回文案，
// 所以再兜一层关键字匹配。
func isTooLong(err error) bool {
	var apiErr *gateway.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.Code {
	case 40054007, 40054018:
		return true
	}
	switch apiErr.ErrCode {
	case 40054007, 40054018:
		return true
	}
	msg := strings.ToLower(apiErr.Message)
	return strings.Contains(msg, "消息长度") ||
		strings.Contains(msg, "消息过长") ||
		strings.Contains(msg, "too long")
}

// ImageWithText 用一条富媒体消息（msg_type=7）同时发图片和文案，
// 文案会显示在图片下方，不会拆成两条。
func (r *Replier) ImageWithText(ctx context.Context, name string, data []byte, text string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	fileInfo, err := r.api.UploadC2CFile(ctx, r.userID, 1, name, data)
	if err != nil {
		if r.logf != nil {
			r.logf("warn", "上传图片失败（%s）：%v", name, err)
		}
		return false
	}

	msg := gateway.Message{Content: text, Media: fileInfo}
	if r.msgID == "" || r.passiveBroken || r.seq >= r.max {
		_, err := r.api.SendC2C(ctx, r.userID, msg)
		return err == nil
	}

	r.seq++
	msg.MsgID = r.msgID
	msg.MsgSeq = r.seq
	if _, err := r.api.SendC2C(ctx, r.userID, msg); err == nil {
		return true
	}

	r.seq--
	r.passiveBroken = true
	if r.logf != nil {
		r.logf("warn", "富媒体被动回复失败，改用主动消息")
	}
	msg.MsgID, msg.MsgSeq = "", 0
	_, err = r.api.SendC2C(ctx, r.userID, msg)
	return err == nil
}

// Image 发一张图片，返回是否发出去了。
//
// 与 Text 一样先走被动回复，失败或预算用尽时改发主动消息。
func (r *Replier) Image(ctx context.Context, name string, data []byte) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	fileInfo, err := r.api.UploadC2CFile(ctx, r.userID, 1, name, data)
	if err != nil {
		if r.logf != nil {
			r.logf("warn", "上传图片失败（%s）：%v", name, err)
		}
		return false
	}

	if r.msgID == "" || r.passiveBroken || r.seq >= r.max {
		_, err := r.api.SendC2CImage(ctx, r.userID, fileInfo, "", 0)
		return err == nil
	}

	r.seq++
	seq := r.seq
	if _, err := r.api.SendC2CImage(ctx, r.userID, fileInfo, r.msgID, seq); err == nil {
		return true
	}

	r.seq--
	r.passiveBroken = true
	if r.logf != nil {
		r.logf("warn", "图片被动回复失败，改用主动消息")
	}
	_, err = r.api.SendC2CImage(ctx, r.userID, fileInfo, "", 0)
	return err == nil
}
