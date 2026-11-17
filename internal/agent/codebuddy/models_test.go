package codebuddy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadModelCachePicksCatalog 文件名是哈希，要按内容认出模型目录，并取最新的一份。
func TestReadModelCachePicksCatalog(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"entry_a.info": `"just a string"`,
		"entry_b.info": `{"productFeatures":{}}`,
		"entry_c.info": `[{"ts":1000,"data":{"models":[{"id":"old","credits":"x9.00 credits"}]}}]`,
		"entry_d.info": `[{"ts":2000,"userId":"u","data":{"models":[{"id":"new","name":"New","credits":"x1.50 credits"}]}}]`,
		"entry_e.info": `not json`,
		"other.info":   `[{"ts":9000,"data":{"models":[{"id":"ignored"}]}}]`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	models, updated, err := readModelCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "new" {
		t.Fatalf("应取最新的模型目录：%+v", models)
	}
	if updated.UnixMilli() != 2000 {
		t.Fatalf("更新时间不对：%v", updated)
	}
	if _, _, err := readModelCache(t.TempDir()); err == nil {
		t.Fatal("没有缓存时应报错，不能返回空列表")
	}
}

func TestParseSupportedModels(t *testing.T) {
	// --help 按终端宽度折行，模型列表可能被拆到多行
	help := "  --model <model>   Model for the session. Currently supported:\n   (claude-opus-5.5, deepseek-v4.1-flash,\n   echo)\n  --other"
	got := parseSupportedModels(help)
	if strings.Join(got, "|") != "claude-opus-5.5|deepseek-v4.1-flash|echo" {
		t.Fatalf("解析不对：%v", got)
	}
	if parseSupportedModels("no list") != nil {
		t.Fatal("没有列表时应返回 nil")
	}
}

func TestBuildModelListFiltersAndSorts(t *testing.T) {
	cached := []cachedModel{
		{ID: "opus", Name: "Opus", Credits: "x2.17 credits"},
		{ID: "flash", Name: "Flash", Credits: "x0.11 credits"},
		{ID: "free", Name: "Free", Credits: "x0.00 credits"},
		{ID: "hidden", Name: "Hidden", Credits: "x0.01 credits"},
		{ID: "echo", Name: "Echo"},
		{ID: "norate", Name: "NoRate"},
	}
	got := buildModelList(cached, []string{"opus", "echo", "norate", "flash", "free", "uncached"})

	var order []string
	for _, m := range got {
		order = append(order, m.ID)
	}
	// hidden 不在可选列表、echo 只会回显，都不能出现；x0.00 是真实倍率，不能当成未知
	if s := strings.Join(order, ","); s != "free,flash,opus,norate,uncached" {
		t.Fatalf("筛选或排序不对：%s", s)
	}
	if got[0].RateText() != "x0.00" {
		t.Fatalf("0 倍率应显示 x0.00：%s", got[0].RateText())
	}
	if got[3].RateText() != "倍率未知" {
		t.Fatalf("没有倍率不能猜：%s", got[3].RateText())
	}
}

// 没显式配置模型时，从 CLI 的 settings.json 读它当前的默认模型。
func TestDefaultModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if _, ok := (&driver{}).DefaultModel(); ok {
		t.Fatal("没有配置文件时不应给出默认模型")
	}
	dir := filepath.Join(home, ".codebuddy")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"model":"deepseek-v4.1-flash"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, ok := (&driver{}).DefaultModel()
	if !ok || got != "deepseek-v4.1-flash" {
		t.Fatalf("应读到默认模型：%q %v", got, ok)
	}
}
