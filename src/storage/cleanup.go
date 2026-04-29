package storage

import (
	"context"
	"log/slog"
	"time"
)

// TTLCleaner 定期扫描并清理过期的 multipart upload。
type TTLCleaner struct {
	backend   StorageBackend
	mpStorage MultipartStorage
	ttl       time.Duration
	interval  time.Duration
	onCleanup func(count int64)
}

// NewTTLCleaner 创建 TTLCleaner 实例。
// 后端不支持 MultipartStorage 时返回 nil。
func NewTTLCleaner(backend StorageBackend, ttl, interval time.Duration, onCleanup func(int64)) *TTLCleaner {
	ms, ok := backend.(MultipartStorage)
	if !ok {
		return nil
	}
	return &TTLCleaner{
		backend:   backend,
		mpStorage: ms,
		ttl:       ttl,
		interval:  interval,
		onCleanup: onCleanup,
	}
}

// Start 启动后台清理 goroutine，通过 ctx 取消停止。
func (c *TTLCleaner) Start(ctx context.Context) {
	go c.run(ctx)
}

func (c *TTLCleaner) run(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	c.sweep()

	for {
		select {
		case <-ctx.Done():
			slog.Info("TTL cleaner stopped")
			return
		case <-ticker.C:
			c.sweep()
		}
	}
}

func (c *TTLCleaner) sweep() {
	slog.Info("TTL cleaner: starting sweep")
	cutoff := time.Now().UTC().Add(-c.ttl)
	totalAborted := 0

	buckets, err := c.backend.ListBuckets()
	if err != nil {
		slog.Warn("TTL cleaner: failed to list buckets", "err", err)
		return
	}

	for _, bucket := range buckets {
		totalAborted += c.cleanupBucket(bucket.Name, cutoff)
	}

	if totalAborted > 0 && c.onCleanup != nil {
		c.onCleanup(int64(totalAborted))
	}
	slog.Info("TTL cleaner: sweep complete", "aborted", totalAborted)
}

func (c *TTLCleaner) cleanupBucket(bucket string, cutoff time.Time) int {
	var aborted int
	keyMarker := ""

	for {
		uploads, nextKeyMarker, truncated, err := c.mpStorage.ListUploads(bucket, "", keyMarker, 1000)
		if err != nil {
			slog.Warn("TTL cleaner: failed to list uploads", "bucket", bucket, "err", err)
			return aborted
		}

		for _, upload := range uploads {
			if upload.Initiated.Before(cutoff) {
				if err := c.mpStorage.AbortUpload(bucket, upload.Key, upload.UploadId); err != nil {
					slog.Warn("TTL cleaner: failed to abort upload",
						"bucket", bucket, "key", upload.Key,
						"upload_id", upload.UploadId, "err", err)
					continue
				}
				aborted++
			}
		}

		if !truncated {
			break
		}
		keyMarker = nextKeyMarker
	}

	return aborted
}
