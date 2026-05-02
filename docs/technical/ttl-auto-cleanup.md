<!-- tags: multipart, ttl, cleanup, background-goroutine -->
# Multipart Upload TTL 自动清理

## 概述

Multipart Upload 允许客户端分多次上传大文件。如果客户端在上传过程中断开连接或崩溃，未完成的 upload 会残留在磁盘上。`TTLCleaner` 通过后台 goroutine 定期扫描并清理超时的 multipart upload，释放存储空间。

## 1. 问题场景

```
客户端发起 InitiateUpload → 上传 Part 1, Part 2 → 客户端崩溃
                                                    ↓
                                          .uploads/{uploadId}/
                                          ├── info.json     ← 残留
                                          ├── part-0001.bin ← 残留
                                          └── part-0002.bin ← 残留
```

如果不清理，这些残留数据会无限积累，占用磁盘空间。

## 2. 数据结构

```go
type TTLCleaner struct {
    backend   StorageBackend  // 用于 ListBuckets
    mpStorage MultipartStorage // 用于 ListUploads + AbortUpload
    ttl       time.Duration    // 上传超时阈值（默认 24h）
    interval  time.Duration    // 扫描间隔（默认 1h）
    onCleanup func(count int64) // 清理回调（通知 metrics）
}
```

**接口检测**：`NewTTLCleaner` 通过 type assertion 检查后端是否实现 `MultipartStorage` 接口。不支持 multipart 的后端返回 `nil`。

```go
func NewTTLCleaner(backend StorageBackend, ttl, interval time.Duration, onCleanup func(int64)) *TTLCleaner {
    ms, ok := backend.(MultipartStorage)
    if !ok {
        return nil  // 后端不支持 multipart，无需清理
    }
    return &TTLCleaner{backend: backend, mpStorage: ms, ...}
}
```

## 3. 清理流程

```
TTLCleaner.sweep()（每次 tick 触发）
  │
  ├─ 计算 cutoff = now - ttl（如 now - 24h）
  │
  ├─ ListBuckets()
  │
  └─ 对每个 bucket:
       │
       └─ cleanupBucket(bucket, cutoff)
            │
            ├─ ListUploads(bucket, keyMarker, maxUploads=1000)
            │    ↓ 分页遍历
            │
            └─ 对每个 upload:
                 │
                 ├─ upload.Initiated.Before(cutoff)?
                 │    ├─ Yes → AbortUpload(bucket, key, uploadId)
                 │    └─ No  → 跳过
                 │
                 └─ keyMarker = nextKeyMarker（分页续传）
```

## 4. 核心实现

```go
func (c *TTLCleaner) sweep() {
    cutoff := time.Now().UTC().Add(-c.ttl)
    totalAborted := 0

    buckets, _ := c.backend.ListBuckets()
    for _, bucket := range buckets {
        totalAborted += c.cleanupBucket(bucket.Name, cutoff)
    }

    if totalAborted > 0 && c.onCleanup != nil {
        c.onCleanup(int64(totalAborted))
    }
}

func (c *TTLCleaner) cleanupBucket(bucket string, cutoff time.Time) int {
    keyMarker := ""
    for {
        uploads, nextKeyMarker, truncated, _ :=
            c.mpStorage.ListUploads(bucket, "", keyMarker, 1000)

        for _, upload := range uploads {
            if upload.Initiated.Before(cutoff) {
                c.mpStorage.AbortUpload(bucket, upload.Key, upload.UploadId)
                aborted++
            }
        }

        if !truncated { break }
        keyMarker = nextKeyMarker
    }
}
```

**设计要点**：
- 使用 UTC 时间比较，避免时区问题
- 分页遍历（每页 1000 条），避免大 bucket 内存溢出
- `AbortUpload` 不仅删除 `.uploads/` 目录，还清理所有已上传的 part 数据和元数据

## 5. 调度与生命周期

```go
func (c *TTLCleaner) run(ctx context.Context) {
    ticker := time.NewTicker(c.interval)
    c.sweep()  // 启动时立即执行一次

    for {
        select {
        case <-ctx.Done(): return  // 优雅关闭
        case <-ticker.C:  c.sweep()
        }
    }
}
```

在 `main.go` 中通过 `context.WithCancel` 控制，收到 SIGINT/SIGTERM 时取消 context 停止清理。

## 6. 配置参数

| 参数 | 配置键 | 默认值 | 说明 |
|------|--------|--------|------|
| TTL | `multipart_ttl_seconds` | 86400（24h） | 上传超时阈值 |
| 扫描间隔 | `cleanup_interval_sec` | 3600（1h） | 两次扫描之间的间隔 |

## 7. Metrics 集成

```go
// main.go
cleaner := storage.NewTTLCleaner(backend, ttl, interval, func(count int64) {
    m.MultipartCleanups.Add(count)  // 原子计数器
})

// metrics endpoint 输出：
// multipart_cleanups: 5
```

## 对应实现

| 文件 | 说明 |
|------|------|
| `src/storage/cleanup.go` | TTLCleaner 完整实现 |
| `src/storage/backend.go` | `MultipartStorage` 接口定义 |
| `src/metrics/metrics.go` | `MultipartCleanups` 计数器 |
