package web

import (
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	// avatarHost 是 QQ 机器人用户头像的固定出处：openid 只在对应 appid 下有效。
	avatarHost = "q.qlogo.cn"
	// avatarMaxBytes 头像最大读取字节数，防止被异常响应撑爆内存。
	avatarMaxBytes = 512 << 10
	avatarOKTTL    = 6 * time.Hour
	avatarFailTTL  = 5 * time.Minute
	avatarMaxItems = 300
)

type avatarEntry struct {
	data    []byte // 为空表示取不到，用字母头像代替
	ctype   string
	expires time.Time
}

// avatarCache 在内存里缓存头像：管理端页面会反复请求同一批人，没必要每次都去外网拉。
type avatarCache struct {
	// base 是头像服务地址，测试时替换。
	base   string
	client *http.Client

	mu    sync.Mutex
	items map[string]avatarEntry
}

func newAvatarCache() *avatarCache {
	return &avatarCache{
		base: "https://" + avatarHost,
		client: &http.Client{
			Timeout: 6 * time.Second,
			// 头像服务只会直接返回图片；任何跳转都不跟，避免被引到别处。
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		items: map[string]avatarEntry{},
	}
}

// get 返回头像字节与类型；取不到时返回 nil。
func (c *avatarCache) get(appID, openID string) ([]byte, string) {
	key := appID + "/" + openID
	now := time.Now()

	c.mu.Lock()
	if e, ok := c.items[key]; ok && now.Before(e.expires) {
		c.mu.Unlock()
		return e.data, e.ctype
	}
	c.mu.Unlock()

	data, ctype := c.fetch(appID, openID)
	ttl := avatarOKTTL
	if data == nil {
		ttl = avatarFailTTL
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.items) >= avatarMaxItems {
		for k, e := range c.items { // 先清过期的，不够再随便丢一个
			if now.After(e.expires) {
				delete(c.items, k)
			}
		}
		for k := range c.items {
			if len(c.items) < avatarMaxItems {
				break
			}
			delete(c.items, k)
		}
	}
	c.items[key] = avatarEntry{data: data, ctype: ctype, expires: now.Add(ttl)}
	return data, ctype
}

func (c *avatarCache) fetch(appID, openID string) ([]byte, string) {
	target := c.base + "/qqapp/" + url.PathEscape(appID) + "/" + url.PathEscape(openID) + "/100"
	resp, err := c.client.Get(target)
	if err != nil {
		return nil, ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, ""
	}
	// 只接受图片，且不信任响应里的 Content-Type 做 HTML 之类的事
	ctype := strings.ToLower(strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0]))
	switch ctype {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return nil, ""
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, avatarMaxBytes+1))
	if err != nil || len(data) == 0 || len(data) > avatarMaxBytes {
		return nil, ""
	}
	return data, ctype
}

// letterAvatar 生成字母头像：取昵称（没有就用 openid）的第一个字，颜色由 openid 决定，同一个人始终同色。
func letterAvatar(openID, name string) string {
	label := strings.TrimSpace(name)
	if label == "" {
		label = openID
	}
	letter := "?"
	if r, _ := utf8.DecodeRuneInString(label); r != utf8.RuneError {
		letter = strings.ToUpper(string(r))
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(openID))
	hue := h.Sum32() % 360
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 40"><rect width="40" height="40" fill="hsl(%d,45%%,38%%)"/>`+
		`<text x="20" y="20" dy=".35em" text-anchor="middle" font-family="sans-serif" font-size="20" fill="#fff">%s</text></svg>`,
		hue, xmlEscape(letter))
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

// handleAvatar 返回用户头像：优先真实头像，取不到就回退成字母头像。
func (s *Server) handleAvatar(w http.ResponseWriter, r *http.Request) {
	openID := r.PathValue("id")
	if !openIDRe.MatchString(openID) {
		http.NotFound(w, r)
		return
	}
	bind := s.cfg.Get().Bind
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")

	if bind.AppID != "" {
		if data, ctype := s.avatars.get(bind.AppID, openID); data != nil {
			h.Set("Content-Type", ctype)
			h.Set("Cache-Control", "private, max-age=3600")
			_, _ = w.Write(data)
			return
		}
	}
	h.Set("Content-Type", "image/svg+xml; charset=utf-8")
	h.Set("Cache-Control", "private, max-age=300")
	_, _ = io.WriteString(w, letterAvatar(openID, r.URL.Query().Get("n")))
}
