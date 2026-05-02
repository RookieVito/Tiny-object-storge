package metrics

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"

	"tiny-object-storage/src/s3error"
)

// Metrics 追踪服务器运行时统计信息。
// 请求数和错误数通过 atomic 计数器实时累加，
// bucket 数和存储字节数通过按需扫描文件系统获取。
type Metrics struct {
	TotalRequests     atomic.Int64
	TotalErrors       atomic.Int64
	MultipartCleanups atomic.Int64
	DiskHealthChecks  atomic.Int64
	RebalancedObjects atomic.Int64
	root              string
}

// NewMetrics 创建 Metrics 实例。
func NewMetrics(root string) *Metrics {
	return &Metrics{root: root}
}

// metricsResponse 是 GET /_metrics 返回的 JSON 结构。
type metricsResponse struct {
	TotalRequests     int64 `json:"total_requests"`
	TotalErrors       int64 `json:"total_errors"`
	MultipartCleanups int64 `json:"multipart_cleanups"`
	DiskHealthChecks  int64 `json:"disk_health_checks"`
	RebalancedObjects int64 `json:"rebalanced_objects"`
	BucketCount       int   `json:"bucket_count"`
	StorageBytes      int64 `json:"storage_bytes"`
}

// ServeHTTP 处理 GET /_metrics 请求。
func (m *Metrics) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s3error.WriteS3Error(w, "InvalidMethod", "only GET is allowed", http.StatusMethodNotAllowed, r.URL.Path)
		return
	}

	bucketCount, storageBytes := m.scanFilesystem()

	resp := metricsResponse{
		TotalRequests:     m.TotalRequests.Load(),
		TotalErrors:       m.TotalErrors.Load(),
		MultipartCleanups: m.MultipartCleanups.Load(),
		DiskHealthChecks:  m.DiskHealthChecks.Load(),
		RebalancedObjects: m.RebalancedObjects.Load(),
		BucketCount:       bucketCount,
		StorageBytes:      storageBytes,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// scanFilesystem 扫描存储根目录，统计 bucket 数和数据文件总大小。
func (m *Metrics) scanFilesystem() (bucketCount int, storageBytes int64) {
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return 0, 0
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		bucketCount++
		filepath.WalkDir(filepath.Join(m.root, e.Name()), func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil || d.IsDir() {
				if d.IsDir() && d.Name() == ".uploads" {
					return filepath.SkipDir
				}
				return nil
			}
			if filepath.Ext(path) == ".meta" {
				return nil
			}
			if info, err := d.Info(); err == nil {
				storageBytes += info.Size()
			}
			return nil
		})
	}
	return bucketCount, storageBytes
}
