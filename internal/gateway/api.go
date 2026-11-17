// Package gateway 是 QQ 机器人接入层：官方 HTTP OpenAPI + WebSocket 网关。
//
// 本项目只用到单聊（C2C）能力：收 C2C_MESSAGE_CREATE、发文本/图片、上传富媒体。
// 连接、心跳、断线重连、Resume 全部自己维护，行为可观测、可取消。
package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// TokenURL 是换取 AppAccessToken 的地址。
	TokenURL = "https://api.bot.qq.com/app/getAppAccessToken"
	// APIBaseURL 是正式环境的 OpenAPI 地址。
	APIBaseURL = "https://api.bot.qq.com"
)

// Credentials 是机器人凭据。
type Credentials struct {
	AppID     string
	AppSecret string
}

// APIError 是 OpenAPI 返回的业务错误。
type APIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	ErrCode int    `json:"err_code"`
	TraceID string `json:"trace_id"`
}

func (e *APIError) Error() string {
	if e.TraceID != "" {
		return fmt.Sprintf("QQ 接口错误 %d: %s (trace_id=%s)", e.Code, e.Message, e.TraceID)
	}
	return fmt.Sprintf("QQ 接口错误 %d: %s", e.Code, e.Message)
}

// API 是机器人的 HTTP 客户端。
type API struct {
	creds Credentials
	http  *http.Client
	token *tokenManager
	// baseURL / tokenURL 仅用于测试注入
	baseURL  string
	tokenURL string
}

// NewAPI 创建 HTTP 客户端。
func NewAPI(creds Credentials) *API {
	tm := &tokenManager{creds: creds, http: &http.Client{Timeout: 15 * time.Second}, url: TokenURL}
	return &API{
		creds: creds,
		http:  &http.Client{Timeout: 30 * time.Second},
		token: tm,
	}
}

// SetEndpoints 覆盖 API 与 token 地址，仅用于测试。
func (a *API) SetEndpoints(apiBase, tokenURL string) {
	if apiBase != "" {
		a.baseURL = strings.TrimRight(apiBase, "/")
	}
	if tokenURL != "" {
		a.tokenURL = tokenURL
		a.token.setURL(tokenURL)
	}
}

// AccessToken 返回可用的 access token，必要时自动刷新。
func (a *API) AccessToken(ctx context.Context) (string, error) {
	return a.token.Get(ctx)
}

// InvalidateToken 让下一次调用重新换取 token。
func (a *API) InvalidateToken() { a.token.Invalidate() }

// BaseURL 返回 API 根地址。
func (a *API) BaseURL() string {
	if a.baseURL != "" {
		return a.baseURL
	}
	return APIBaseURL
}

// Gateway 获取 WebSocket 网关地址。
func (a *API) Gateway(ctx context.Context) (string, error) {
	var out struct {
		URL string `json:"url"`
	}
	if err := a.do(ctx, http.MethodGet, "/gateway", nil, &out); err != nil {
		return "", err
	}
	if strings.TrimSpace(out.URL) == "" {
		return "", errors.New("网关地址为空")
	}
	return out.URL, nil
}

// Me 是机器人自身资料。
type Me struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Avatar   string `json:"avatar"`
	ShareURL string `json:"share_url"`
}

// GetMe 拉取机器人自身资料。
func (a *API) GetMe(ctx context.Context) (Me, error) {
	var out Me
	err := a.do(ctx, http.MethodGet, "/users/@me", nil, &out)
	return out, err
}

// Message 是要发送的文本消息。
type Message struct {
	Content string
	// Markdown 非空时以 Markdown 消息（msg_type=2）发送
	Markdown string
	// Keyboard 非空时在消息底部挂按钮（此时只能用 msg_type=0）
	Keyboard *Keyboard
	// MsgID 非空表示被动回复
	MsgID string
	// EventID 非空表示回应互动事件（逻辑同 MsgID）
	EventID string
	// MsgSeq 同一 MsgID 下多条回复的序号，从 1 开始
	MsgSeq int
	// Media 非空时发富媒体消息（msg_type=7），值是上传拿到的 file_info，
	// Content 作为图片下方的文案一起发出去。
	Media string
}

// SendC2C 发送单聊文本消息，返回消息 ID。
// SendC2C 发送单聊消息。
func (a *API) SendC2C(ctx context.Context, userID string, msg Message) (string, error) {
	body, err := messageBody(msg)
	if err != nil {
		return "", err
	}
	return a.sendMessage(ctx, a.c2cPath(userID, "messages"), body)
}

// SendGroup 发送群聊消息。群聊被动回复同样要带 msg_id/msg_seq。
func (a *API) SendGroup(ctx context.Context, groupOpenID string, msg Message) (string, error) {
	body, err := messageBody(msg)
	if err != nil {
		return "", err
	}
	return a.sendMessage(ctx, "/v2/groups/"+url.PathEscape(groupOpenID)+"/messages", body)
}

// messageBody 把消息结构转成 OpenAPI 的请求体。
func messageBody(msg Message) (map[string]any, error) {
	if strings.TrimSpace(msg.Content) == "" && strings.TrimSpace(msg.Markdown) == "" && msg.Media == "" {
		return nil, errors.New("消息内容为空")
	}
	body := map[string]any{"msg_type": 0, "content": msg.Content}
	if msg.Media != "" {
		// 富媒体消息：一张图 + 一段文案，一条消息发完
		body = map[string]any{
			"msg_type": 7,
			"content":  msg.Content,
			"media":    map[string]string{"file_info": msg.Media},
		}
		return withReplyRef(msg, body), nil
	}
	switch {
	case msg.Markdown != "" && msg.Keyboard == nil:
		// 群/单聊支持原生 Markdown（msg_type=2），content 必须为空
		body = map[string]any{"msg_type": 2, "markdown": map[string]string{"content": msg.Markdown}}
	case msg.Markdown != "":
		// 带按钮的消息只能用 msg_type=0，Markdown 源码直接当正文发出去
		if strings.TrimSpace(msg.Content) == "" {
			body["content"] = msg.Markdown
		}
	}
	if msg.Keyboard != nil {
		body["keyboard"] = msg.Keyboard
	}
	return withReplyRef(msg, body), nil
}

// withReplyRef 给请求体补上被动回复的 msg_id/msg_seq。
func withReplyRef(msg Message, body map[string]any) map[string]any {
	if msg.MsgID != "" {
		body["msg_id"] = msg.MsgID
		body["msg_seq"] = clampSeq(msg.MsgSeq)
	} else if msg.EventID != "" {
		body["event_id"] = msg.EventID
		body["msg_seq"] = clampSeq(msg.MsgSeq)
	}
	return body
}

// sendMessage 统一发消息并取回消息 ID。
func (a *API) sendMessage(ctx context.Context, path string, body map[string]any) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	if err := a.do(ctx, http.MethodPost, path, body, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

func clampSeq(seq int) int {
	if seq <= 0 {
		return 1
	}
	return seq
}

// AckInteraction 回应按钮/菜单互动。code=0 表示成功，其他值为错误码。
// 必须在收到 INTERACTION_CREATE 后 3 秒内调用，否则用户端一直 loading。
func (a *API) AckInteraction(ctx context.Context, interactionID string, code int) error {
	body := map[string]any{"code": code}
	return a.do(ctx, http.MethodPut, "/interactions/"+url.PathEscape(interactionID), body, nil)
}

// SetC2CMenu 设置单聊输入框下方的快捷菜单（全局生效，覆盖原有配置）。
//
// 菜单项点击后只把 send_message 的文本填进输入框，不会自动发送，
// 所以这里放的是 /list、/status 这类短指令。
func (a *API) SetC2CMenu(ctx context.Context, items []MenuItem) error {
	if len(items) == 0 {
		return errors.New("菜单项为空")
	}
	if len(items) > 10 {
		items = items[:10]
	}
	body := map[string]any{"menu": map[string]any{"items": items}}
	return a.do(ctx, http.MethodPut, "/v2/menu", body, nil)
}

// typingMaxSeconds 是「正在输入」状态单次可维持的最长时间。
const typingMaxSeconds = 60

// SendC2CTyping 在单聊里展示「正在输入」状态。
//
// 这条消息不带 msg_id / msg_seq：输入中不是一条真正的回复，带上去会占用
// 被动回复的序号，导致随后 seq=1 的第一条回复因「同一 msg_id + msg_seq 重复」失败。
func (a *API) SendC2CTyping(ctx context.Context, userID string, seconds int) error {
	if seconds <= 0 {
		seconds = 30
	}
	if seconds > typingMaxSeconds {
		seconds = typingMaxSeconds
	}
	body := map[string]any{
		"msg_type":     6,
		"input_notify": map[string]int{"input_type": 1, "input_second": seconds},
	}
	return a.do(ctx, http.MethodPost, a.c2cPath(userID, "messages"), body, nil)
}

// UploadC2CFile 上传文件到单聊（file_type 1=图片 2=视频 3=语音 4=文件），返回 file_info。
// 二进制直传上限 5MB，超过时调用方应自己压图。
func (a *API) UploadC2CFile(ctx context.Context, userID string, fileType int, name string, data []byte) (string, error) {
	body := map[string]any{
		"file_type":    fileType,
		"file_name":    name,
		"file_data":    base64.StdEncoding.EncodeToString(data),
		"srv_send_msg": false,
	}
	var out struct {
		FileInfo string `json:"file_info"`
	}
	if err := a.do(ctx, http.MethodPost, a.c2cPath(userID, "files"), body, &out); err != nil {
		return "", err
	}
	if out.FileInfo == "" {
		return "", errors.New("上传文件失败：未返回 file_info")
	}
	return out.FileInfo, nil
}

// SendC2CImage 发送单聊图片消息（msg_type=7），占一次被动回复序号。
func (a *API) SendC2CImage(ctx context.Context, userID, fileInfo, msgID string, seq int) (string, error) {
	body := map[string]any{
		"msg_type": 7,
		"media":    map[string]string{"file_info": fileInfo},
	}
	if msgID != "" {
		body["msg_id"] = msgID
		if seq <= 0 {
			seq = 1
		}
		body["msg_seq"] = seq
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := a.do(ctx, http.MethodPost, a.c2cPath(userID, "messages"), body, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

func (a *API) c2cPath(userID, suffix string) string {
	return "/v2/users/" + url.PathEscape(userID) + "/" + suffix
}

// do 发起带鉴权的请求并解析响应。
func (a *API) do(ctx context.Context, method, path string, payload any, out any) error {
	token, err := a.token.Get(ctx)
	if err != nil {
		return err
	}

	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, a.BaseURL()+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "QQBot "+token)
	req.Header.Set("X-Bot-Appid", a.creds.AppID)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgent())

	resp, err := a.http.Do(req)
	if err != nil {
		return fmt.Errorf("请求 QQ 接口失败: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("读取 QQ 接口响应失败: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		apiErr := &APIError{Code: resp.StatusCode, Message: strings.TrimSpace(string(data))}
		_ = json.Unmarshal(data, apiErr)
		if apiErr.Message == "" {
			apiErr.Message = strings.TrimSpace(string(data))
		}
		if apiErr.TraceID == "" {
			apiErr.TraceID = resp.Header.Get("X-Tps-Trace-Id")
		}
		if resp.StatusCode == http.StatusUnauthorized {
			a.token.Invalidate()
		}
		return apiErr
	}

	if out == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("解析 QQ 接口响应失败: %w", err)
	}
	return nil
}

// ---------- access token ----------

// tokenManager 负责按需换取与缓存 AppAccessToken。
type tokenManager struct {
	mu     sync.Mutex
	creds  Credentials
	http   *http.Client
	token  string
	expiry time.Time
	url    string
}

type tokenResponse struct {
	Code        int             `json:"code"`
	Message     string          `json:"message"`
	AccessToken string          `json:"access_token"`
	ExpiresIn   json.RawMessage `json:"expires_in"`
}

// Get 返回可用 token，过期前 60 秒自动刷新。
func (t *tokenManager) Get(ctx context.Context) (string, error) {
	t.mu.Lock()
	if t.token != "" && time.Now().Before(t.expiry.Add(-60*time.Second)) {
		token := t.token
		t.mu.Unlock()
		return token, nil
	}
	t.mu.Unlock()
	return t.fetch(ctx)
}

func (t *tokenManager) setURL(u string) {
	t.mu.Lock()
	t.url = u
	t.mu.Unlock()
}

// Invalidate 丢弃缓存。
func (t *tokenManager) Invalidate() {
	t.mu.Lock()
	t.token = ""
	t.expiry = time.Time{}
	t.mu.Unlock()
}

func (t *tokenManager) fetch(ctx context.Context) (string, error) {
	if t.creds.AppID == "" || t.creds.AppSecret == "" {
		return "", errors.New("AppID 与 AppSecret 不能为空")
	}

	body, err := json.Marshal(map[string]string{
		"appId":        t.creds.AppID,
		"clientSecret": t.creds.AppSecret,
	})
	if err != nil {
		return "", err
	}
	target := t.url
	if target == "" {
		target = TokenURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgent())

	resp, err := t.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("获取 AccessToken 失败: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}

	var out tokenResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("解析 AccessToken 响应失败: %w", err)
	}
	if out.AccessToken == "" {
		msg := out.Message
		if msg == "" {
			msg = strings.TrimSpace(string(data))
		}
		return "", fmt.Errorf("获取 AccessToken 失败（%s），请检查 AppID 与 AppSecret", msg)
	}

	ttl := parseExpiresIn(out.ExpiresIn)
	if ttl <= 0 {
		ttl = 7200
	}

	t.mu.Lock()
	t.token = out.AccessToken
	t.expiry = time.Now().Add(time.Duration(ttl) * time.Second)
	token := t.token
	t.mu.Unlock()
	return token, nil
}

// parseExpiresIn 兼容数字与字符串两种返回形式。
func parseExpiresIn(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err == nil {
		return n
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil {
			return v
		}
	}
	return 0
}
