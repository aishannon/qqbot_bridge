package bridge

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// imageExts 是会被回传到 QQ 的图片后缀。
var imageExts = map[string]bool{
	".png":  true,
	".jpg":  true,
	".jpeg": true,
	".webp": true,
	".gif":  true,
}

// skipDirs 是扫描时直接跳过的目录，避免在大仓库里翻 node_modules。
var skipDirs = map[string]bool{
	"node_modules": true,
	".git":         true,
	"vendor":       true,
	".venv":        true,
	"__pycache__":  true,
	"dist":         true,
	"build":        true,
	"target":       true,
}

// dotDirs 是允许扫描的点目录：系统提示词就是让 agent 把截图存到 .shots 的，
// 其他隐藏目录（.git、.cache…）一律跳过。
var dotDirs = map[string]bool{
	".shots":       true,
	".screenshots": true,
	".screenshot":  true,
}

const (
	// maxImageBytes 是单张图的上限：直接二进制上传接口限 5MB。
	maxImageBytes = 5 << 20
	// maxScanDepth 是扫描的最大层级，避免在大仓库里做全量遍历。
	maxScanDepth = 3
	// maxScanFiles 是单次扫描的文件数上限，超过直接放弃剩余目录。
	maxScanFiles = 5000
)

// findNewImages 找出 dir 下在 since 之后修改过的图片，按时间倒序最多返回 limit 张。
//
// 截图通常是 agent 跑测试/浏览器时落盘的，所以按 mtime 判断「这一轮新产生的」。
// 只扫前三层、跳过依赖目录，大仓库里也不会每轮都全量遍历。
func findNewImages(dir string, since time.Time, limit int) []string {
	if dir == "" || limit <= 0 {
		return nil
	}
	type entry struct {
		path string
		mod  time.Time
	}
	var found []entry
	scanned := 0

	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		depth := 0
		if rel, rerr := filepath.Rel(dir, path); rerr == nil && rel != "." {
			depth = len(strings.Split(rel, string(filepath.Separator)))
		}
		if d.IsDir() {
			if depth >= maxScanDepth {
				return filepath.SkipDir
			}
			if path == dir {
				return nil
			}
			name := d.Name()
			if skipDirs[name] {
				return filepath.SkipDir
			}
			if strings.HasPrefix(name, ".") && !dotDirs[name] {
				return filepath.SkipDir
			}
			return nil
		}
		if scanned++; scanned > maxScanFiles {
			return filepath.SkipAll
		}
		if !imageExts[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > maxImageBytes || info.Size() == 0 {
			return nil
		}
		if info.ModTime().Before(since) {
			return nil
		}
		found = append(found, entry{path: path, mod: info.ModTime()})
		return nil
	})

	sort.Slice(found, func(i, j int) bool { return found[i].mod.After(found[j].mod) })
	if len(found) > limit {
		found = found[:limit]
	}
	paths := make([]string, 0, len(found))
	for _, e := range found {
		paths = append(paths, e.path)
	}
	return paths
}

// readImage 读入图片内容，超过 5MB 返回 nil。
func readImage(path string) (string, []byte) {
	info, err := os.Stat(path)
	if err != nil || info.Size() > maxImageBytes || info.Size() == 0 {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil
	}
	return filepath.Base(path), data
}
