// Package logbuf 是一个带环形缓冲的日志器：既打到终端，也让控制台能读到最近的日志，
// 可选按天落盘到日志目录，重启后还能翻旧账。
package logbuf

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Entry 是一条日志。
type Entry struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
}

// Buffer 是环形日志缓冲。
type Buffer struct {
	mu    sync.Mutex
	items []Entry
	max   int

	dir    string   // 非空时按天落盘
	file   *os.File // 当前打开的那一天
	day    string   // file 对应的日期
	warned bool     // 落盘失败只提醒一次

	now func() time.Time // 便于测试替换
}

// New 创建缓冲，max 是保留条数。
func New(max int) *Buffer {
	if max <= 0 {
		max = 300
	}
	return &Buffer{max: max, now: time.Now}
}

// EnableFile 开启按天落盘：写入 dir/YYYY-MM-DD.log。
// 目录建不出来时返回错误，但内存缓冲仍然可用。
func (b *Buffer) EnableFile(dir string) error {
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.dir = dir
	return nil
}

// Add 记录一条日志，打印到 stderr、留在缓冲里，并尽量落盘。
func (b *Buffer) Add(level, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	now := time.Now
	if b.now != nil {
		now = b.now
	}
	entry := Entry{Time: now(), Level: level, Message: msg}

	b.mu.Lock()
	b.items = append(b.items, entry)
	if len(b.items) > b.max {
		b.items = b.items[len(b.items)-b.max:]
	}
	b.writeLocked(entry)
	b.mu.Unlock()

	fmt.Fprintf(os.Stderr, "%s [%s] %s\n", entry.Time.Format("15:04:05"), level, msg)
}

// writeLocked 把一条日志写进当天的文件，调用前必须持有 b.mu。
//
// 落盘是尽力而为：失败只提醒一次，绝不能因为磁盘写不了就影响任务本身。
func (b *Buffer) writeLocked(entry Entry) {
	if b.dir == "" {
		return
	}
	day := entry.Time.Format("2006-01-02")
	if b.file == nil || b.day != day {
		if b.file != nil {
			_ = b.file.Close()
			b.file = nil
		}
		path := filepath.Join(b.dir, day+".log")
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			if !b.warned {
				fmt.Fprintf(os.Stderr, "日志落盘失败（%s）：%v\n", path, err)
				b.warned = true
			}
			return
		}
		b.file, b.day = f, day
	}
	line := fmt.Sprintf("%s [%s] %s\n", entry.Time.Format("2006-01-02 15:04:05"), entry.Level, entry.Message)
	if _, err := b.file.WriteString(line); err != nil {
		if !b.warned {
			fmt.Fprintf(os.Stderr, "日志写入失败：%v\n", err)
			b.warned = true
		}
	}
}

// Close 关闭日志文件。
func (b *Buffer) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.file == nil {
		return nil
	}
	err := b.file.Close()
	b.file = nil
	return err
}

// Logf 返回符合 bridge.LogFunc 签名的函数。
func (b *Buffer) Logf() func(level, format string, args ...any) {
	return b.Add
}

// Entries 返回最近的日志副本。
func (b *Buffer) Entries() []Entry {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Entry, len(b.items))
	copy(out, b.items)
	return out
}
