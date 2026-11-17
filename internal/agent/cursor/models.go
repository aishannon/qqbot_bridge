package cursor

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"qqbot_bridge/internal/agent"
)

const (
	// modelCacheTTL 内直接用缓存：`cursor-agent models` 要走网络，2~5 秒。
	modelCacheTTL = 10 * time.Minute
	// modelTimeout 是单次查询模型列表的超时。
	modelTimeout = 30 * time.Second
)

// modelCache 按可执行文件缓存模型列表。驱动按需构造、不常驻，缓存只能放包级。
var modelCache = struct {
	mu      sync.Mutex
	entries map[string]modelEntry
}{entries: make(map[string]modelEntry)}

type modelEntry struct {
	models []agent.ModelInfo
	at     time.Time
}

// Models 返回当前 Cursor 账号可用的模型。
//
// Cursor 不提供倍率，目录标记 NoRates，顺序保持 CLI 的输出（auto 在最前）。
// 刷新失败时有旧缓存就退回旧缓存并带上 RefreshErr，和 CodeBuddy 语义一致。
func (d *driver) Models(ctx context.Context) (agent.ModelCatalog, error) {
	// 整个查询持锁：并发的 /models 只会触发一次 CLI 调用
	modelCache.mu.Lock()
	defer modelCache.mu.Unlock()

	cached, ok := modelCache.entries[d.command]
	if ok && time.Since(cached.at) < modelCacheTTL {
		return catalogOf(cached, nil), nil
	}
	models, err := d.listModels(ctx)
	if err != nil {
		if ok {
			return catalogOf(cached, err), nil
		}
		return agent.ModelCatalog{}, err
	}
	cached = modelEntry{models: models, at: time.Now()}
	modelCache.entries[d.command] = cached
	return catalogOf(cached, nil), nil
}

// ModelRate 恒返回未知：Cursor 没有倍率，/status 不需要为此起进程。
func (d *driver) ModelRate(string) (string, time.Time, bool) {
	return "", time.Time{}, false
}

// ModelName 只读包级缓存里的展示名，不触发刷新（Cursor 查询要走网络）。
// 还没有缓存过目录时返回 false，调用方退回模型 ID。
func (d *driver) ModelName(id string) (string, bool) {
	modelCache.mu.Lock()
	defer modelCache.mu.Unlock()
	e, ok := modelCache.entries[d.command]
	if !ok {
		return "", false
	}
	for _, m := range e.models {
		if strings.EqualFold(m.ID, id) && m.Name != "" {
			return m.Name, true
		}
	}
	return "", false
}

func catalogOf(e modelEntry, refreshErr error) agent.ModelCatalog {
	return agent.ModelCatalog{
		Models:     append([]agent.ModelInfo(nil), e.models...),
		UpdatedAt:  e.at,
		RefreshErr: refreshErr,
		NoRates:    true,
	}
}

// listModels 执行 `cursor-agent models` 并解析。
func (d *driver) listModels(ctx context.Context) ([]agent.ModelInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, modelTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, d.command, "models")
	cmd.Dir = os.TempDir()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(string(out) + "\n" + stderr.String())
		if isAuthError(msg) {
			return nil, fmt.Errorf("%w：%s", ErrNotLoggedIn, loginHint)
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("查询 Cursor 模型列表超时（%s）", modelTimeout)
		}
		return nil, fmt.Errorf("查询 Cursor 模型列表失败：%v %s", err, truncateRunes(msg, 200))
	}
	models := parseModels(string(out))
	if len(models) == 0 {
		return nil, errors.New("cursor-agent models 没有返回任何模型")
	}
	return models, nil
}

// zeroWidth 是 CLI 输出里偶尔夹带的零宽字符（实测 Grok 系列的名字后面有 U+200B）。
var zeroWidth = strings.NewReplacer("\u200b", "", "\u200c", "", "\u200d", "", "\ufeff", "")

// parseModels 解析 `cursor-agent models` 的输出：
//
//	Available models
//
//	auto - Auto (default)
//	gpt-5.2 - GPT-5.2
//	...
//
//	Tip: use --model <id> ...
//
// 每行「ID - 展示名」，ID 不含空白；其他行（标题、提示）跳过。
func parseModels(out string) []agent.ModelInfo {
	var models []agent.ModelInfo
	seen := make(map[string]bool)
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(zeroWidth.Replace(line))
		id, name, ok := strings.Cut(line, " - ")
		id = strings.TrimSpace(id)
		if !ok || id == "" || strings.ContainsAny(id, " \t") || seen[id] {
			continue
		}
		seen[id] = true
		name = strings.Join(strings.Fields(name), " ")
		models = append(models, agent.ModelInfo{ID: id, Name: cmp.Or(name, id)})
	}
	return models
}
