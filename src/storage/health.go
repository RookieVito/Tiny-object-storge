package storage

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"time"
)

// DiskHealthChecker 定期检查 EC 磁盘的健康状态。
type DiskHealthChecker struct {
	ecBackend     *ECBackend
	interval      time.Duration
	onCheck       func() // 每次检查完成后回调
	onStateChange func(diskIndex int, alive bool)
	mu            sync.Mutex
	running       bool
}

// NewDiskHealthChecker 创建 DiskHealthChecker。
func NewDiskHealthChecker(ecBackend *ECBackend, interval time.Duration, onCheck func(), onStateChange func(diskIndex int, alive bool)) *DiskHealthChecker {
	return &DiskHealthChecker{
		ecBackend:     ecBackend,
		interval:      interval,
		onCheck:       onCheck,
		onStateChange: onStateChange,
	}
}

// Start 启动后台健康检查 goroutine。
func (h *DiskHealthChecker) Start(ctx context.Context) {
	h.mu.Lock()
	if h.running {
		h.mu.Unlock()
		return
	}
	h.running = true
	h.mu.Unlock()

	go h.run(ctx)
}

func (h *DiskHealthChecker) run(ctx context.Context) {
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()

	h.Check()

	for {
		select {
		case <-ctx.Done():
			slog.Info("disk health checker stopped")
			return
		case <-ticker.C:
			h.Check()
		}
	}
}

// Check 执行一次磁盘健康检查，对比磁盘可访问性与当前状态。
// 通过互斥锁保证同一时刻只有一个检查在执行。
func (h *DiskHealthChecker) Check() {
	h.mu.Lock()
	defer h.mu.Unlock()

	slog.Debug("disk health check: starting")
	n := h.ecBackend.DiskCount()

	for i := 0; i < n; i++ {
		diskPath := h.ecBackend.DiskPath(i)
		_, err := os.Stat(diskPath)
		alive := err == nil

		if alive == h.ecBackend.IsDiskAlive(i) {
			continue
		}

		h.ecBackend.SetDiskState(i, alive)
		slog.Info("disk state changed",
			"disk_index", i,
			"disk_path", diskPath,
			"alive", alive,
		)

		if h.onStateChange != nil {
			h.onStateChange(i, alive)
		}
	}

	slog.Debug("disk health check: complete")

	if h.onCheck != nil {
		h.onCheck()
	}
}
