package codebuddy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"qqbot_bridge/internal/agent"
)

const (
	// modelCacheTTL 内的模型目录直接用，超过就刷新，保证看到的是当天的倍率。
	modelCacheTTL = 10 * time.Minute
	// modelRefreshTimeout 是刷新模型目录的超时，正常 6~8 秒。
	modelRefreshTimeout = 45 * time.Second
	// echoModelID 是 CLI 的测试模型，只会回显，切过去机器人就废了，不进列表。
	echoModelID = "echo"
)

var (
	// supportedModelsRe 从 `codebuddy --help` 里取当前可选的模型 ID。
	supportedModelsRe = regexp.MustCompile(`Currently supported:\s*\(([^)]*)\)`)
	// modelRateRe 从 "x2.17 credits" 里取倍率数值。
	modelRateRe = regexp.MustCompile(`x\s*([0-9]+(?:\.[0-9]+)?)`)
)

// cachedModel 是 CLI 模型目录缓存里的一条记录。
type cachedModel struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Credits string `json:"credits"`
}

// Models 查询 CodeBuddy 当前可选的模型和倍率。
func (d *driver) Models(ctx context.Context) (agent.ModelCatalog, error) {
	return fetchModels(ctx, d.command)
}

// ModelRate 只读本地缓存里的倍率，不触发刷新，保证 /status 秒回。
func (d *driver) ModelRate(id string) (string, time.Time, bool) {
	return cachedModelRate(id)
}

// ModelName 只读本地缓存里的模型展示名，不触发刷新（确认消息要立刻发出）。
func (d *driver) ModelName(id string) (string, bool) {
	dir, err := modelCacheDir()
	if err != nil {
		return "", false
	}
	models, _, err := readModelCache(dir)
	if err != nil {
		return "", false
	}
	for _, m := range models {
		if m.ID == id && m.Name != "" {
			return m.Name, true
		}
	}
	return "", false
}

// DefaultModel 读 CLI 配置文件里的默认模型（settings.json 的 model 字段），
// 用来在桥接没显式配置模型时展示实际跑的模型。
func (d *driver) DefaultModel() (string, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	raw, err := os.ReadFile(filepath.Join(home, ".codebuddy", "settings.json"))
	if err != nil {
		return "", false
	}
	var doc struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(raw, &doc) != nil || doc.Model == "" {
		return "", false
	}
	return doc.Model, true
}

// modelCacheDir 返回 CLI 缓存云端模型目录的目录。
func modelCacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codebuddy", "local_storage"), nil
}

// readModelCache 从 CLI 的本地缓存读模型目录，只解析模型字段。
//
// CLI 没有提供查询倍率的命令，云端配置被缓存在 local_storage/entry_*.info 里，
// 文件名是哈希，所以按内容识别；CLI 升级改了格式时这里会报错，不会给出错误倍率。
func readModelCache(dir string) ([]cachedModel, time.Time, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "entry_*.info"))
	if err != nil {
		return nil, time.Time{}, err
	}
	var models []cachedModel
	var newest int64
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var rows []struct {
			TS   int64 `json:"ts"`
			Data struct {
				Models []cachedModel `json:"models"`
			} `json:"data"`
		}
		if json.Unmarshal(raw, &rows) != nil {
			continue
		}
		for _, row := range rows {
			if len(row.Data.Models) > 0 && row.TS > newest {
				models, newest = row.Data.Models, row.TS
			}
		}
	}
	if len(models) == 0 {
		return nil, time.Time{}, errors.New("没有找到 CodeBuddy 模型目录缓存")
	}
	return models, time.UnixMilli(newest), nil
}

// parseSupportedModels 解析 `codebuddy --help` 输出里的可选模型列表。
func parseSupportedModels(help string) []string {
	m := supportedModelsRe.FindStringSubmatch(strings.Join(strings.Fields(help), " "))
	if m == nil {
		return nil
	}
	var ids []string
	for _, id := range strings.Split(m[1], ",") {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// buildModelList 只保留 CLI 当前可选的模型，按倍率从低到高排，倍率未知的排最后。
func buildModelList(cached []cachedModel, supported []string) []agent.ModelInfo {
	byID := make(map[string]cachedModel, len(cached))
	for _, c := range cached {
		byID[c.ID] = c
	}
	out := make([]agent.ModelInfo, 0, len(supported))
	for _, id := range supported {
		if id == echoModelID {
			continue
		}
		m := agent.ModelInfo{ID: id, Name: id}
		if c, ok := byID[id]; ok {
			if c.Name != "" {
				m.Name = c.Name
			}
			if r := modelRateRe.FindStringSubmatch(c.Credits); r != nil {
				if v, err := strconv.ParseFloat(r[1], 64); err == nil {
					m.Rate, m.HasRate = v, true
				}
			}
		}
		out = append(out, m)
	}
	slices.SortStableFunc(out, func(a, b agent.ModelInfo) int {
		if a.HasRate != b.HasRate {
			if a.HasRate {
				return -1
			}
			return 1
		}
		if a.Rate != b.Rate {
			if a.Rate < b.Rate {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

// fetchModels 查询模型目录：缓存过期就先刷新，刷新失败时退回旧缓存并带上错误。
func fetchModels(ctx context.Context, command string) (agent.ModelCatalog, error) {
	dir, err := modelCacheDir()
	if err != nil {
		return agent.ModelCatalog{}, err
	}

	cached, updated, err := readModelCache(dir)
	var refreshErr error
	if err != nil || time.Since(updated) > modelCacheTTL {
		if refreshErr = refreshModelCache(ctx, command); refreshErr == nil {
			cached, updated, err = readModelCache(dir)
		}
	}
	if err != nil {
		if refreshErr != nil {
			return agent.ModelCatalog{}, fmt.Errorf("%v；刷新也失败：%v", err, refreshErr)
		}
		return agent.ModelCatalog{}, err
	}

	out, err := exec.CommandContext(ctx, command, "--help").Output()
	if err != nil {
		return agent.ModelCatalog{}, fmt.Errorf("读取可选模型失败：%w", err)
	}
	supported := parseSupportedModels(string(out))
	if len(supported) == 0 {
		return agent.ModelCatalog{}, errors.New("CLI 帮助里没有可选模型列表")
	}
	return agent.ModelCatalog{
		Models:     buildModelList(cached, supported),
		UpdatedAt:  updated,
		RefreshErr: refreshErr,
	}, nil
}

// refreshModelCache 让 CLI 启动一次新会话来拉取云端配置。
//
// 用 echo 模型不消耗额度（实测 total_cost_usd=0）；不落会话记录、不加载 MCP 和工具。
func refreshModelCache(ctx context.Context, command string) error {
	ctx, cancel := context.WithTimeout(ctx, modelRefreshTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, command, "-p",
		"--model", echoModelID,
		"--max-turns", "1",
		"--no-session-persistence",
		"--strict-mcp-config",
		"--tools", "",
		"ping")
	cmd.Dir = os.TempDir()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("刷新模型目录失败：%w", err)
	}
	return nil
}

// cachedModelRate 只读本地缓存里的倍率，不触发刷新。
func cachedModelRate(id string) (string, time.Time, bool) {
	dir, err := modelCacheDir()
	if err != nil {
		return "", time.Time{}, false
	}
	return cachedModelRateIn(dir, id)
}

// cachedModelRateIn 从指定缓存目录取倍率，便于测试。
func cachedModelRateIn(dir, id string) (string, time.Time, bool) {
	models, updated, err := readModelCache(dir)
	if err != nil {
		return "", time.Time{}, false
	}
	for _, m := range models {
		if m.ID != id {
			continue
		}
		info := agent.ModelInfo{ID: m.ID}
		if r := modelRateRe.FindStringSubmatch(m.Credits); r != nil {
			if v, err := strconv.ParseFloat(r[1], 64); err == nil {
				info.Rate, info.HasRate = v, true
			}
		}
		return info.RateText(), updated, true
	}
	return "", updated, false
}
