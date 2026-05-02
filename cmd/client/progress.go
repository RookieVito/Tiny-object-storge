package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// ProgressWriter 包装 io.Reader，在读取时更新进度条。
type ProgressWriter struct {
	Total   int64
	Current atomic.Int64
	Start   time.Time
	Done    atomic.Bool
}

// NewProgressWriter 创建进度跟踪器。
func NewProgressWriter(total int64) *ProgressWriter {
	return &ProgressWriter{Total: total, Start: time.Now()}
}

// Write 实现 io.Writer，更新进度并渲染进度条。
func (p *ProgressWriter) Write(b []byte) (int, error) {
	n := len(b)
	p.Current.Add(int64(n))
	p.render()
	return n, nil
}

// Read 实现 io.Reader，从 src 读取并更新进度。
func (p *ProgressWriter) Read(src io.Reader) (int64, error) {
	buf := make([]byte, 32*1024)
	var total int64
	for {
		n, err := src.Read(buf)
		if n > 0 {
			total += int64(n)
			p.Current.Store(total)
			p.render()
		}
		if err != nil {
			return total, err
		}
	}
}

// Finish 完成进度显示，换行。
func (p *ProgressWriter) Finish() {
	if !p.Done.Swap(true) {
		fmt.Println()
	}
}

func (p *ProgressWriter) render() {
	if p.Done.Load() {
		return
	}
	current := p.Current.Load()
	pct := float64(0)
	if p.Total > 0 {
		pct = float64(current) / float64(p.Total) * 100
	}
	elapsed := time.Since(p.Start).Seconds()
	var rate string
	if elapsed > 0 {
		bps := float64(current) / elapsed
		rate = formatRate(bps)
	}

	bar := progressBar(pct, 30)
	fmt.Fprintf(os.Stderr, "\r%s %8s/%8s %6.1f%% %8s/s  ",
		bar,
		formatSize(current),
		formatSize(p.Total),
		pct,
		rate,
	)
}

func progressBar(pct float64, width int) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	filled := int(pct / 100 * float64(width))
	empty := width - filled
	return "[" + strings.Repeat("=", filled) + strings.Repeat(" ", empty) + "]"
}

func formatRate(bps float64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	switch {
	case bps >= GB:
		return fmt.Sprintf("%.1fG", bps/GB)
	case bps >= MB:
		return fmt.Sprintf("%.1fM", bps/MB)
	case bps >= KB:
		return fmt.Sprintf("%.1fK", bps/KB)
	default:
		return fmt.Sprintf("%.0fB", bps)
	}
}
