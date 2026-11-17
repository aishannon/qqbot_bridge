package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// State 是网关连接状态。
type State string

const (
	StateConnecting   State = "connecting"
	StateConnected    State = "connected"
	StateDisconnected State = "disconnected"
	StateError        State = "error"
)

// Options 是网关客户端配置。
type Options struct {
	Credentials Credentials
	// Intents 订阅事件位，为 0 时默认订阅群聊 + 单聊
	Intents int
	// OnEvent 收到事件回调，会在独立 goroutine 中按顺序调用
	OnEvent func(Event)
	// OnState 状态变化回调
	OnState func(State, string)
	// OnLog 日志回调
	OnLog func(level, format string, args ...any)
}

// Client 是一个机器人连接，彼此完全独立，可以同时运行多个。
type Client struct {
	api  *API
	opts Options

	mu        sync.RWMutex
	state     State
	intents   int
	sessionID string
	lastSeq   int64
	self      User
	conn      *websocket.Conn
	writeMu   sync.Mutex
}

// New 创建网关客户端。
func New(opts Options) *Client {
	intents := opts.Intents
	if intents == 0 {
		intents = IntentsGroup
	}
	return &Client{
		api:     NewAPI(opts.Credentials),
		opts:    opts,
		state:   StateDisconnected,
		intents: intents,
	}
}

// API 返回底层 HTTP 客户端，便于发送消息。
func (c *Client) API() *API { return c.api }

// State 返回当前状态。
func (c *Client) State() (State, string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state, ""
}

// Self 返回机器人自身信息（READY 之后才有值）。
func (c *Client) Self() User {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.self
}

// SessionID 返回当前会话 ID。
func (c *Client) SessionID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sessionID
}

// Run 启动连接循环，直到 ctx 取消或遇到不可恢复错误。
func (c *Client) Run(ctx context.Context) error {
	backoff := 2 * time.Second
	for {
		if ctx.Err() != nil {
			c.setState(StateDisconnected, "已断开")
			return nil
		}

		ready, err := c.runAttempt(ctx)
		if ctx.Err() != nil {
			c.setState(StateDisconnected, "已断开")
			return nil
		}

		// 订阅意图不被接受时降级重试一次，这是群机器人的常见情况
		if code, ok := closeCode(err); ok && (code == 4013 || code == 4014) {
			if c.dropGuildIntent() {
				c.log("warn", "订阅意图被拒绝（%d），已降级为仅群聊/单聊重连", code)
				continue
			}
			c.setState(StateError, "机器人未开通对应能力（错误码 "+fmt.Sprint(code)+"）")
			return fmt.Errorf("网关拒绝了订阅意图（%d），请检查机器人是否开通群聊能力", code)
		}

		if ready {
			backoff = 2 * time.Second
		}
		reason := "连接已断开"
		if err != nil {
			reason = err.Error()
		}
		c.setState(StateConnecting, "连接中断，正在重连："+trimErr(reason))
		c.log("warn", "网关断开：%s，%s 后重连", trimErr(reason), backoff)

		select {
		case <-ctx.Done():
			c.setState(StateDisconnected, "已断开")
			return nil
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > 60*time.Second {
			backoff = 60 * time.Second
		}
	}
}

// runAttempt 建立一次连接并阻塞读取，直到断开；ready 表示这次是否成功登录。
func (c *Client) runAttempt(ctx context.Context) (ready bool, err error) {
	c.setState(StateConnecting, "正在连接 QQ 网关…")

	gwURL, err := c.api.Gateway(ctx)
	if err != nil {
		return false, err
	}

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		Proxy:            http.ProxyFromEnvironment,
	}
	conn, resp, err := dialer.DialContext(ctx, gwURL, http.Header{"User-Agent": {UserAgent()}})
	if err != nil {
		if resp != nil {
			return false, fmt.Errorf("连接网关失败（HTTP %d）: %w", resp.StatusCode, err)
		}
		return false, fmt.Errorf("连接网关失败: %w", err)
	}
	defer conn.Close()

	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if c.conn == conn {
			c.conn = nil
		}
		c.mu.Unlock()
	}()

	// ctx 取消时主动断开，让 ReadMessage 立即返回
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()

	// 第一条必须是 Hello
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		return false, fmt.Errorf("等待 Hello 失败: %w", err)
	}
	var hello Payload
	if err := json.Unmarshal(raw, &hello); err != nil {
		return false, fmt.Errorf("解析 Hello 失败: %w", err)
	}
	interval := 30 * time.Second
	if hello.Op == OpHello {
		var hd helloData
		if err := json.Unmarshal(hello.Data, &hd); err == nil && hd.HeartbeatInterval > 0 {
			interval = time.Duration(hd.HeartbeatInterval) * time.Millisecond
		}
	}

	// 有会话就 resume，否则 identify
	c.mu.RLock()
	hasSession := c.sessionID != ""
	c.mu.RUnlock()
	if hasSession {
		if err := c.sendResume(ctx); err != nil {
			return false, err
		}
	} else {
		if err := c.sendIdentify(ctx); err != nil {
			return false, err
		}
	}

	// 心跳
	hbCtx, hbCancel := context.WithCancel(ctx)
	defer hbCancel()
	go c.heartbeat(hbCtx, conn, interval)

	_ = conn.SetReadDeadline(time.Now().Add(interval*2 + 10*time.Second))
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return ready, err
		}
		_ = conn.SetReadDeadline(time.Now().Add(interval*2 + 10*time.Second))

		var p Payload
		if err := json.Unmarshal(raw, &p); err != nil {
			continue
		}
		p.Raw = raw

		switch p.Op {
		case OpHello:
			// 服务端重复下发 Hello，忽略
		case OpHeartbeatAck:
			// 心跳确认
		case OpReconnect:
			return ready, errors.New("服务端要求重连")
		case OpInvalidSess:
			c.mu.Lock()
			c.sessionID = ""
			c.lastSeq = 0
			c.mu.Unlock()
			c.log("warn", "会话失效，重新鉴权")
			return ready, errors.New("会话失效")
		case OpDispatch:
			c.mu.Lock()
			if p.Seq > c.lastSeq {
				c.lastSeq = p.Seq
			}
			c.mu.Unlock()

			switch p.Type {
			case EventReady:
				var rd readyData
				if err := json.Unmarshal(p.Data, &rd); err == nil {
					c.mu.Lock()
					c.sessionID = rd.SessionID
					c.self = rd.User
					c.mu.Unlock()
					ready = true
					c.setState(StateConnected, "已连接"+displayUser(rd.User))
				}
			case EventResumed:
				ready = true
				c.setState(StateConnected, "已恢复连接")
			}

			if c.opts.OnEvent != nil {
				c.opts.OnEvent(Event{
					Type:      p.Type,
					Seq:       p.Seq,
					Data:      p.Data,
					Received:  time.Now(),
					SessionID: c.SessionID(),
				})
			}
		}
	}
}

func (c *Client) heartbeat(ctx context.Context, conn *websocket.Conn, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.mu.RLock()
			seq := c.lastSeq
			c.mu.RUnlock()
			frame := map[string]any{"op": OpHeartbeat, "d": seq}
			if err := c.writeJSON(conn, frame); err != nil {
				_ = conn.Close()
				return
			}
		}
	}
}

func (c *Client) sendIdentify(ctx context.Context) error {
	token, err := c.api.AccessToken(ctx)
	if err != nil {
		return err
	}
	c.mu.RLock()
	intents := c.intents
	conn := c.conn
	c.mu.RUnlock()
	if conn == nil {
		return errors.New("连接尚未建立")
	}
	frame := map[string]any{
		"op": OpIdentify,
		"d": map[string]any{
			"token":   "QQBot " + token,
			"intents": intents,
			"shard":   []int{0, 1},
			"properties": map[string]string{
				"$os":      "linux",
				"$browser": "qqbot_bridge",
				"$device":  "qqbot_bridge",
			},
		},
	}
	return c.writeJSON(conn, frame)
}

func (c *Client) sendResume(ctx context.Context) error {
	token, err := c.api.AccessToken(ctx)
	if err != nil {
		return err
	}
	c.mu.RLock()
	sessionID := c.sessionID
	seq := c.lastSeq
	conn := c.conn
	c.mu.RUnlock()
	if conn == nil {
		return errors.New("连接尚未建立")
	}
	frame := map[string]any{
		"op": OpResume,
		"d": map[string]any{
			"token":      "QQBot " + token,
			"session_id": sessionID,
			"seq":        seq,
		},
	}
	return c.writeJSON(conn, frame)
}

func (c *Client) writeJSON(conn *websocket.Conn, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return conn.WriteMessage(websocket.TextMessage, data)
}

// Close 主动断开当前连接。
func (c *Client) Close() {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

func (c *Client) setState(state State, msg string) {
	c.mu.Lock()
	changed := c.state != state
	c.state = state
	c.mu.Unlock()
	if c.opts.OnState != nil && (changed || state != StateConnected) {
		c.opts.OnState(state, msg)
	}
	if changed {
		c.log("info", "网关状态：%s %s", state, msg)
	}
}

func (c *Client) log(level, format string, args ...any) {
	if c.opts.OnLog != nil {
		c.opts.OnLog(level, format, args...)
	}
}

// dropGuildIntent 去掉频道订阅位，返回是否有变化。
func (c *Client) dropGuildIntent() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.intents&IntentsGuildAt == 0 {
		return false
	}
	c.intents &^= IntentsGuildAt
	c.sessionID = ""
	c.lastSeq = 0
	return true
}

func closeCode(err error) (int, bool) {
	var ce *websocket.CloseError
	if errors.As(err, &ce) {
		return ce.Code, true
	}
	return 0, false
}

func displayUser(u User) string {
	if u.Username != "" {
		return "（" + u.Username + "）"
	}
	return ""
}

func trimErr(s string) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) > 200 {
		return string([]rune(s)[:200]) + "…"
	}
	return s
}
