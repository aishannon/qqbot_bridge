// Package qrconnect 实现 QQ 机器人的扫码绑定。
//
// 协议参考 tencent-connect/dsh-qqbot 所用的 @tencent-connect/qqbot-connector：
//  1. 本地生成 32 字节随机 key（base64），POST /lite/create_bind_task 拿到 task_id
//  2. 二维码内容为 https://q.qq.com/qqbot/openclaw/connect.html?task_id=...&source=...
//  3. 每 2 秒 POST /lite/poll_bind_result 查询状态（0 未开始 / 1 等待 / 2 完成 / 3 过期）
//  4. 完成后用本地 key 对 bot_encrypt_secret 做 AES-256-GCM 解密，得到 AppSecret
//
// 整个流程只在本地进程内进行，AppSecret 不出进程。
package qrconnect

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// 绑定状态。
const (
	StatusNone      = 0
	StatusPending   = 1
	StatusCompleted = 2
	StatusExpired   = 3
)

// 对外展示的相位。
const (
	PhaseStarting = "starting"
	PhaseWaiting  = "waiting"
	PhaseSaving   = "saving"
	PhaseSuccess  = "success"
	PhaseFailed   = "failed"
)

// 服务地址。
const (
	hostProduction = "q.qq.com"
	hostTest       = "test.q.qq.com"
	// connectPage 是扫码落地页，固定走正式域名
	connectPage = "https://q.qq.com/qqbot/openclaw/connect.html"
)

// pollInterval 是轮询间隔，与官方 connector 一致。
const pollInterval = 2 * time.Second

// requestTimeout 是单次 HTTP 请求超时。
const requestTimeout = 10 * time.Second

// State 是扫码流程对外暴露的状态（不含密钥）。
type State struct {
	Phase    string    `json:"phase"`
	URL      string    `json:"url"`
	Message  string    `json:"message"`
	IssuedAt time.Time `json:"issuedAt"`
	AppID    string    `json:"appId"`
	// Expires 是当前二维码的过期时间（本地估算，仅用于界面提示）
	Expires time.Time `json:"expires"`
}

// Credential 是扫码成功后拿到的机器人凭据。
type Credential struct {
	AppID      string
	AppSecret  string
	UserOpenID string
}

// Options 配置扫码流程。
type Options struct {
	// Source 会拼进二维码 URL，用于标识接入方
	Source string
	// Test 为 true 时使用 test.q.qq.com（仅调试用）
	Test bool
	// Endpoint 覆盖服务地址，仅用于测试
	Endpoint string
	// UserAgent 随扫码请求一起上报，为空则用 Go 默认值
	UserAgent string
	// OnState 状态变化回调
	OnState func(State)
	// OnSuccess 绑定成功回调，凭据只在这里返回
	OnSuccess func(Credential)
}

// Session 是一次扫码绑定会话。
type Session struct {
	opts   Options
	client *http.Client

	mu     sync.RWMutex
	state  State
	cancel context.CancelFunc
	done   chan struct{}
	// refresh 用于要求重建二维码任务
	refresh chan struct{}
}

// Start 启动扫码流程（异步）。
func Start(ctx context.Context, opts Options) *Session {
	ctx, cancel := context.WithCancel(ctx)
	s := &Session{
		opts:    opts,
		client:  &http.Client{Timeout: requestTimeout},
		state:   State{Phase: PhaseStarting, Message: "正在生成二维码…"},
		cancel:  cancel,
		done:    make(chan struct{}),
		refresh: make(chan struct{}, 1),
	}
	go s.run(ctx)
	return s
}

// State 返回当前状态。
func (s *Session) State() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

// Refresh 放弃当前二维码，重新申请一个。
func (s *Session) Refresh() {
	select {
	case s.refresh <- struct{}{}:
	default:
	}
}

// Cancel 终止流程。
func (s *Session) Cancel() {
	s.cancel()
	<-s.done
}

func (s *Session) run(ctx context.Context) {
	defer close(s.done)
	for {
		if ctx.Err() != nil {
			return
		}

		task, key, err := s.createTask(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			s.set(State{Phase: PhaseFailed, Message: "获取绑定二维码失败：" + err.Error()})
			return
		}

		qrURL := buildConnectURL(task, s.opts.Source)
		s.set(State{
			Phase:    PhaseWaiting,
			URL:      qrURL,
			Message:  "请用手机 QQ 扫码，确认绑定机器人",
			IssuedAt: time.Now(),
			Expires:  time.Now().Add(5 * time.Minute),
		})

		restart, cred, err := s.poll(ctx, task, key)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			s.set(State{Phase: PhaseFailed, Message: err.Error()})
			return
		}
		if restart {
			continue
		}
		if cred != nil {
			s.set(State{Phase: PhaseSaving, Message: "扫码成功，正在保存凭据…", AppID: cred.AppID})
			if s.opts.OnSuccess != nil {
				s.opts.OnSuccess(*cred)
			}
			s.set(State{Phase: PhaseSuccess, Message: "绑定成功", AppID: cred.AppID})
			return
		}
	}
}

// poll 轮询绑定结果：restart=true 表示二维码过期需要重建。
func (s *Session) poll(ctx context.Context, task, key string) (bool, *Credential, error) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		result, err := s.pollOnce(ctx, task)
		if err != nil {
			// 网络抖动不终止流程，等下一轮继续
			if ctx.Err() != nil {
				return false, nil, nil
			}
		} else {
			switch result.Status {
			case StatusCompleted:
				secret, derr := DecryptSecret(result.EncryptedSecret, key)
				if derr != nil {
					return false, nil, fmt.Errorf("解密机器人密钥失败：%w", derr)
				}
				if result.AppID == "" {
					return false, nil, errors.New("扫码结果缺少 AppID")
				}
				return false, &Credential{
					AppID:      result.AppID,
					AppSecret:  secret,
					UserOpenID: result.UserOpenID,
				}, nil
			case StatusExpired:
				s.set(State{Phase: PhaseWaiting, Message: "二维码已过期，正在刷新…", URL: s.State().URL})
				return true, nil, nil
			}
		}

		select {
		case <-ctx.Done():
			return false, nil, nil
		case <-s.refresh:
			s.set(State{Phase: PhaseStarting, Message: "正在刷新二维码…"})
			return true, nil, nil
		case <-ticker.C:
		}
	}
}

// ---------- 协议实现 ----------

// bindResult 是 poll_bind_result 的 data。
type bindResult struct {
	Status          int    `json:"status"`
	AppID           string `json:"-"`
	EncryptedSecret string `json:"-"`
	UserOpenID      string `json:"-"`
}

// createTask 申请一个绑定任务。
func (s *Session) createTask(ctx context.Context) (string, string, error) {
	key, err := generateKey()
	if err != nil {
		return "", "", err
	}
	var resp struct {
		RetCode int    `json:"retcode"`
		Msg     string `json:"msg"`
		Data    struct {
			TaskID string `json:"task_id"`
		} `json:"data"`
	}
	if err := s.post(ctx, s.host()+"/lite/create_bind_task", map[string]string{"key": key}, &resp); err != nil {
		return "", "", err
	}
	if resp.RetCode != 0 {
		return "", "", fmt.Errorf("%s（retcode=%d）", fallback(resp.Msg, "create_bind_task 失败"), resp.RetCode)
	}
	if resp.Data.TaskID == "" {
		return "", "", errors.New("create_bind_task 未返回 task_id")
	}
	return resp.Data.TaskID, key, nil
}

// pollOnce 查询一次绑定结果。
func (s *Session) pollOnce(ctx context.Context, task string) (*bindResult, error) {
	var resp struct {
		RetCode int    `json:"retcode"`
		Msg     string `json:"msg"`
		Data    struct {
			Status          int    `json:"status"`
			BotAppID        any    `json:"bot_appid"`
			BotEncryptToken string `json:"bot_encrypt_secret"`
			UserOpenID      string `json:"user_openid"`
		} `json:"data"`
	}
	if err := s.post(ctx, s.host()+"/lite/poll_bind_result", map[string]string{"task_id": task}, &resp); err != nil {
		return nil, err
	}
	if resp.RetCode != 0 {
		return nil, fmt.Errorf("%s（retcode=%d）", fallback(resp.Msg, "poll_bind_result 失败"), resp.RetCode)
	}
	return &bindResult{
		Status:          resp.Data.Status,
		AppID:           stringify(resp.Data.BotAppID),
		EncryptedSecret: resp.Data.BotEncryptToken,
		UserOpenID:      resp.Data.UserOpenID,
	}, nil
}

func (s *Session) post(ctx context.Context, target string, payload any, out any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if s.opts.UserAgent != "" {
		req.Header.Set("User-Agent", s.opts.UserAgent)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("接口返回 HTTP %d", resp.StatusCode)
	}
	return json.Unmarshal(body, out)
}

func (s *Session) host() string {
	if s.opts.Endpoint != "" {
		return strings.TrimRight(s.opts.Endpoint, "/")
	}
	if s.opts.Test {
		return "https://" + hostTest
	}
	return "https://" + hostProduction
}

func (s *Session) set(state State) {
	state.Message = fallback(state.Message, "")
	s.mu.Lock()
	// 保留已经拿到的 URL，便于界面在过期时继续展示旧码
	if state.URL == "" {
		state.URL = s.state.URL
	}
	s.state = state
	current := s.state
	s.mu.Unlock()
	if s.opts.OnState != nil {
		s.opts.OnState(current)
	}
}

// generateKey 生成 32 字节随机密钥的 base64。
func generateKey() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf), nil
}

// buildConnectURL 拼出二维码内容。
func buildConnectURL(taskID, source string) string {
	q := url.Values{}
	q.Set("task_id", taskID)
	q.Set("source", source)
	q.Set("_wv", "2")
	return connectPage + "?" + q.Encode()
}

// DecryptSecret 用 AES-256-GCM 解密 bot_encrypt_secret。
//
// 密文格式：IV(12) + ciphertext(N) + AuthTag(16)，key 为 32 字节。
func DecryptSecret(encryptedBase64, keyBase64 string) (string, error) {
	key, err := base64.StdEncoding.DecodeString(keyBase64)
	if err != nil {
		return "", fmt.Errorf("密钥格式错误: %w", err)
	}
	raw, err := base64.StdEncoding.DecodeString(encryptedBase64)
	if err != nil {
		return "", fmt.Errorf("密文格式错误: %w", err)
	}
	if len(key) != 32 {
		return "", fmt.Errorf("密钥长度应为 32 字节，实际 %d", len(key))
	}
	if len(raw) < 12+16 {
		return "", errors.New("密文长度不足")
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := raw[:12]
	tag := raw[len(raw)-16:]
	ciphertext := raw[12 : len(raw)-16]
	plain, err := gcm.Open(nil, nonce, append(ciphertext, tag...), nil)
	if err != nil {
		return "", fmt.Errorf("解密失败: %w", err)
	}
	return string(plain), nil
}

func fallback(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// stringify 兼容 bot_appid 返回数字或字符串。
func stringify(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return fmt.Sprintf("%.0f", x)
	case json.Number:
		return x.String()
	default:
		return fmt.Sprint(x)
	}
}
