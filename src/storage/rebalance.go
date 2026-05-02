package storage

import (
	"log/slog"
	"strings"
	"sync"
)

// Rebalancer 扫描 EC 对象并修复缺失分片。
type Rebalancer struct {
	ecBackend   *ECBackend
	onRebalance func(repairedObjects int64)
	mu          sync.Mutex // 防止并发 Rebalance
}

// NewRebalancer 创建 Rebalancer。
func NewRebalancer(ecBackend *ECBackend, onRebalance func(int64)) *Rebalancer {
	return &Rebalancer{
		ecBackend:   ecBackend,
		onRebalance: onRebalance,
	}
}

// Rebalance 扫描所有 bucket 的 EC 对象，修复缺失分片。
// 如果已有 rebalance 在运行，直接跳过。
func (r *Rebalancer) Rebalance() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.ecBackend.AliveCount() < r.ecBackend.DataShards() {
		slog.Warn("rebalance: skipped, insufficient alive disks",
			"alive", r.ecBackend.AliveCount(),
			"required", r.ecBackend.DataShards(),
		)
		return
	}

	slog.Info("rebalance: starting")
	buckets, err := r.ecBackend.ListBuckets()
	if err != nil {
		slog.Warn("rebalance: failed to list buckets", "err", err)
		return
	}

	totalRepaired := 0
	for _, bucket := range buckets {
		repaired := r.rebalanceBucket(bucket.Name)
		totalRepaired += repaired
	}

	if totalRepaired > 0 && r.onRebalance != nil {
		r.onRebalance(int64(totalRepaired))
	}
	slog.Info("rebalance: complete", "repaired_objects", totalRepaired)
}

func (r *Rebalancer) rebalanceBucket(bucket string) int {
	entries, _, _, _, err := r.ecBackend.metaStore.ListObjects(bucket, "", "", "", 100000)
	if err != nil {
		slog.Warn("rebalance: failed to list objects", "bucket", bucket, "err", err)
		return 0
	}

	repaired := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Key, ".ec-meta") {
			continue
		}
		objKey := strings.TrimSuffix(e.Key, ".ec-meta")

		count, err := r.ecBackend.RepairObject(bucket, objKey)
		if err != nil {
			slog.Warn("rebalance: failed to repair object",
				"bucket", bucket, "key", objKey, "err", err)
			continue
		}
		if count > 0 {
			repaired++
			slog.Info("rebalance: repaired object",
				"bucket", bucket, "key", objKey, "shards", count)
		}
	}

	return repaired
}
