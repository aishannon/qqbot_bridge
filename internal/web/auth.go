package web

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"qqbot_bridge/internal/config"
)

const (
	cookieName     = "qqbot_session"
	sessionTTL     = 12 * time.Hour
	pbkdf2Iter     = 200_000
	minPasswordLen = 8
	maxFailures    = 5
	lockDuration   = 5 * time.Minute
)

// authFile 是密码文件的内容：只存加盐哈希，不存明文。
type authFile struct {
	Salt      string    `json:"salt"`
	Hash      string    `json:"hash"`
	Iter      int       `json:"iter"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// AuthPath 返回密码文件路径。它和 config.json 分开存放：
// 命令行改密码时，运行中的服务下一次登录就能读到，不用重启。
func AuthPath() string { return filepath.Join(config.Dir(), "web-auth.json") }

// SetPassword 设置 Web 管理端访问密码。已有登录会话会随之失效。
func SetPassword(path, password string) error {
	if len([]rune(password)) < minPasswordLen {
		return errors.New("密码至少 8 位")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	hash, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Iter, 32)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(authFile{
		Salt:      base64.StdEncoding.EncodeToString(salt),
		Hash:      base64.StdEncoding.EncodeToString(hash),
		Iter:      pbkdf2Iter,
		UpdatedAt: time.Now(),
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// loadAuth 读取密码文件；没设置过返回 nil。
func loadAuth(path string) *authFile {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var a authFile
	if json.Unmarshal(data, &a) != nil || a.Hash == "" || a.Salt == "" {
		return nil
	}
	return &a
}

// verify 用常量时间比较校验密码。
func (a *authFile) verify(password string) bool {
	salt, err := base64.StdEncoding.DecodeString(a.Salt)
	if err != nil {
		return false
	}
	want, err := base64.StdEncoding.DecodeString(a.Hash)
	if err != nil {
		return false
	}
	iter := a.Iter
	if iter <= 0 {
		iter = pbkdf2Iter
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iter, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

// fingerprint 标识当前密码版本：改密码后，旧会话的指纹对不上就自动失效。
func (a *authFile) fingerprint() string { return a.Hash }

// authState 管理登录会话和防暴力破解。
type authState struct {
	path string

	mu       sync.Mutex
	sessions map[string]sessionInfo
	fails    map[string]*failInfo
}

type sessionInfo struct {
	fp      string
	expires time.Time
}

type failInfo struct {
	count  int
	locked time.Time
}

func newAuthState(path string) *authState {
	return &authState{path: path, sessions: map[string]sessionInfo{}, fails: map[string]*failInfo{}}
}

// configured 表示是否已设置密码。
func (a *authState) configured() bool { return loadAuth(a.path) != nil }

// valid 校验请求里的会话 cookie。
func (a *authState) valid(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return false
	}
	cur := loadAuth(a.path)
	if cur == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.sessions[c.Value]
	if !ok {
		return false
	}
	if time.Now().After(s.expires) || s.fp != cur.fingerprint() {
		delete(a.sessions, c.Value)
		return false
	}
	return true
}

// login 校验密码，成功返回新会话 token。
// locked 为 true 表示该来源因多次失败被临时锁定。
func (a *authState) login(ip, password string) (token string, locked bool, err error) {
	a.mu.Lock()
	f := a.fails[ip]
	if f != nil && time.Now().Before(f.locked) {
		a.mu.Unlock()
		return "", true, nil
	}
	a.mu.Unlock()

	cur := loadAuth(a.path)
	if cur == nil {
		return "", false, errNotConfigured
	}
	ok := cur.verify(password)

	a.mu.Lock()
	defer a.mu.Unlock()
	if !ok {
		if f == nil {
			f = &failInfo{}
			a.fails[ip] = f
		}
		f.count++
		if f.count >= maxFailures {
			f.count = 0
			f.locked = time.Now().Add(lockDuration)
		}
		return "", false, nil
	}
	delete(a.fails, ip)
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", false, err
	}
	token = hex.EncodeToString(buf)
	now := time.Now()
	for k, s := range a.sessions { // 顺手清理过期会话
		if now.After(s.expires) {
			delete(a.sessions, k)
		}
	}
	a.sessions[token] = sessionInfo{fp: cur.fingerprint(), expires: now.Add(sessionTTL)}
	return token, false, nil
}

func (a *authState) logout(r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		a.mu.Lock()
		delete(a.sessions, c.Value)
		a.mu.Unlock()
	}
}

var errNotConfigured = errors.New("尚未设置访问密码")

// clientIP 取连接对端地址。不信任 X-Forwarded-For：它可以被随意伪造来绕过锁定。
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// sameOrigin 对写操作校验 Origin，防止其他网页借浏览器里的登录态发请求。
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	i := strings.Index(origin, "://")
	if i < 0 {
		return false
	}
	return origin[i+3:] == r.Host
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
}
