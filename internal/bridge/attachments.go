package bridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"qqbot_bridge/internal/gateway"
)

const (
	// inboxDir 是收到附件后的落盘子目录（相对会话工作目录）。
	inboxDir = ".inbox"
	// maxAttachmentBytes 是单个附件的下载上限。
	maxAttachmentBytes = 20 << 20
	// maxAttachments 是单条消息最多落盘几个附件。
	maxAttachments = 3
)

// attachmentClient 用于下载 QQ 附件，超时给得宽一点，手机拍的图可能不小。
var attachmentClient = &http.Client{Timeout: 60 * time.Second}

// saveAttachments 把图片/文件附件下载到工作目录的 .inbox 下，返回落盘路径。
// 部分失败时返回已成功的路径和错误；全部失败才是致命错误。
func saveAttachments(ctx context.Context, dir string, atts []gateway.Attachment, logf LogFunc) ([]string, error) {
	if dir == "" || len(atts) == 0 {
		return nil, nil
	}
	if logf == nil {
		logf = func(string, string, ...any) {}
	}
	target := filepath.Join(dir, inboxDir)
	if err := os.MkdirAll(target, 0o755); err != nil {
		return nil, fmt.Errorf("创建 %s 目录失败: %w", inboxDir, err)
	}

	var saved []string
	var lastErr error
	for _, att := range atts {
		if len(saved) >= maxAttachments {
			break
		}
		data, err := downloadAttachment(ctx, att)
		if err != nil {
			logf("warn", "下载附件失败（%s）：%v", oneLine(att.Filename, 60), err)
			lastErr = err
			continue
		}
		path := filepath.Join(target, attachmentName(att, len(saved)))
		if err := os.WriteFile(path, data, 0o644); err != nil {
			logf("warn", "保存附件失败（%s）：%v", path, err)
			lastErr = err
			continue
		}
		saved = append(saved, path)
	}
	if len(saved) == 0 && lastErr != nil {
		return nil, lastErr
	}
	return saved, nil
}

// downloadAttachment 下载单个附件，超过上限直接报错而不是截断。
func downloadAttachment(ctx context.Context, att gateway.Attachment) ([]byte, error) {
	u := strings.TrimSpace(att.URL)
	if u == "" {
		return nil, errors.New("附件没有下载地址")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := attachmentClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("附件下载返回 HTTP %d", resp.StatusCode)
	}

	// 多读 1 字节用于判断是否超限
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAttachmentBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxAttachmentBytes {
		return nil, fmt.Errorf("附件超过 %d MB", maxAttachmentBytes>>20)
	}
	if len(data) == 0 {
		return nil, errors.New("附件内容为空")
	}
	return data, nil
}

// nonVoiceAttachments 返回除语音以外的附件（图片、文件、视频）。
func nonVoiceAttachments(atts []gateway.Attachment) []gateway.Attachment {
	if len(atts) == 0 {
		return nil
	}
	out := make([]gateway.Attachment, 0, len(atts))
	for _, a := range atts {
		if _, isVoice := pickVoice([]gateway.Attachment{a}); isVoice {
			continue
		}
		out = append(out, a)
	}
	return out
}

// withAttachments 把落盘路径拼进指令，让 agent 知道该看哪些文件。
func withAttachments(content string, paths []string) string {
	var sb strings.Builder
	if content != "" {
		sb.WriteString(content)
		sb.WriteString("\n\n")
	}
	sb.WriteString("（以下是我随消息发来的附件，已经保存到本地）\n")
	for _, p := range paths {
		sb.WriteString("- ")
		sb.WriteString(p)
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// attachmentName 生成落盘文件名：时间前缀 + 清洗后的原名，避免覆盖和路径穿越。
func attachmentName(att gateway.Attachment, idx int) string {
	base := filepath.Base(strings.TrimSpace(att.Filename))
	base = sanitizeName(base)
	if base == "" || base == "." {
		base = fmt.Sprintf("file-%d%s", idx+1, extFromContentType(att.ContentType))
	}
	return time.Now().Format("150405") + "-" + base
}

// sanitizeName 只保留字母、数字、中文和 ._- ，其余替换成下划线。
func sanitizeName(name string) string {
	var sb strings.Builder
	for _, r := range name {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			sb.WriteRune(r)
		case r == '.' || r == '-' || r == '_':
			sb.WriteRune(r)
		default:
			sb.WriteRune('_')
		}
	}
	// 全是点的名字（如 ".."）也当非法处理
	out := strings.Trim(sb.String(), ".")
	if out == "" {
		return ""
	}
	if len(out) > 80 {
		out = out[:80]
	}
	return out
}

// extFromContentType 在原名缺失时按 MIME 猜后缀。
func extFromContentType(ct string) string {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	switch ct {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "application/pdf":
		return ".pdf"
	case "text/plain":
		return ".txt"
	case "application/zip":
		return ".zip"
	}
	return ""
}
