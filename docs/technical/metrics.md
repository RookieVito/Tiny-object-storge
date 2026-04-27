<!-- tags: observability, monitoring -->
# Metrics 端点设计

## 概述

`GET /_metrics` 返回服务器运行时的 JSON 格式指标，无需认证。

## 端点

```
GET /_metrics HTTP/1.1
Host: localhost:9000
```

**响应：**
```json
{
  "total_requests": 100,
  "total_errors": 5,
  "bucket_count": 3,
  "storage_bytes": 65536
}
```

**字段说明：**

| 字段 | 类型 | 来源 | 说明 |
|------|------|------|------|
| `total_requests` | int64 | atomic 计数器 | 服务启动以来的总请求数 |
| `total_errors` | int64 | atomic 计数器 | HTTP 状态码 >= 400 的请求数 |
| `bucket_count` | int | 按需扫描 | 存储根目录下的 bucket 目录数 |
| `storage_bytes` | int64 | 按需扫描 | 数据文件总大小（不含 .meta 文件） |

## 数据采集策略

采用混合策略：高频数据用 atomic 计数器，低频数据用按需扫描。

### Atomic 计数器（实时累加）

- `TotalRequests` — `logMiddleware` 每处理一个请求 +1
- `TotalErrors` — `logMiddleware` 检测 `statusCode >= 400` 时 +1
- 使用 `sync/atomic.Int64`，无锁开销，适合高频更新

### 按需扫描（请求时计算）

- `BucketCount` — `os.ReadDir(root)` 统计目录数
- `StorageBytes` — 遍历所有 bucket 目录，累加数据文件大小（跳过 `.meta` 文件）

**不使用增量追踪的原因：**
- 存储 bytes 需要跟踪 put（新增）和 delete（减少）以及覆盖（差值），逻辑复杂
- Scan 本身的时间复杂度与对象数成正比，对于单节点 MVP 是可接受的
- `/` 端点使用频率低（调试/监控），不需要极致性能

## 路由架构

Go 1.22+ ServeMux 的路由匹配规则导致精确路径和方法通配符之间存在冲突：

```
GET /_metrics  ← 精确路径，只匹配 GET
HEAD /{bucket} ← 通配符，{bucket} 可以匹配 "_metrics"
```

两者在同一层 ServeMux 中会 panic（"pattern conflicts"）。

### 解决方案：两层 ServeMux

```go
// 内部 mux — S3 路由（{bucket} 通配符）
mux := http.NewServeMux()
mux.HandleFunc("GET /{$}", bm.ListBuckets)
mux.HandleFunc("PUT /{bucket}", ...)
// ...

// 顶层 mux — /_metrics 精确匹配 + fallback
topMux := http.NewServeMux()
topMux.Handle("GET /_metrics", metrics)
topMux.Handle("HEAD /_metrics", metrics)
topMux.Handle("/", mux)  // fallback 到内部 mux

// 中间件链
return s3Middleware(logMiddleware(metrics, topMux))
```

**匹配优先级：** 顶层 mux 中，`GET /_metrics` 精确匹配优先于 `/` fallback。不匹配 `/_metrics` 的请求走 `/` fallback 到内部 S3 mux。

## statusWriter

`logMiddleware` 需要知道响应状态码来更新 metrics 计数器，但 `http.ResponseWriter` 接口没有提供读取状态码的方法。

```go
type statusWriter struct {
    http.ResponseWriter
    statusCode int
}

func (sw *statusWriter) WriteHeader(code int) {
    sw.statusCode = code
    sw.ResponseWriter.WriteHeader(code)
}
```

Go 的默认行为是 `WriteHeader` 未调用时状态码为 200，因此 `statusWriter` 的零值为 200，与默认行为一致。

## 与 logMiddleware 的集成

`logMiddleware` 同时负责日志记录和 metrics 更新，避免额外中间件：

```go
func logMiddleware(metrics *Metrics, next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        start := time.Now()
        sw := &statusWriter{ResponseWriter: w}

        next.ServeHTTP(sw, r)

        metrics.TotalRequests.Add(1)
        if sw.statusCode >= 400 {
            metrics.TotalErrors.Add(1)
        }

        slog.Info("request",
            "method", r.Method,
            "path", r.URL.Path,
            "status", sw.statusCode,
            "duration_ms", time.Since(start).Milliseconds(),
            "remote_addr", r.RemoteAddr,
        )
    })
}
```

这确保了 **所有请求**（包括 `/_metrics` 自身）都被计数。

## 对应实现

| 文件 | 说明 |
|------|------|
| `src/metrics/metrics.go` | Metrics 结构、`/_metrics` handler |
| `src/handler/router.go` | `logMiddleware` 中更新计数 |

**关键类型：** `Metrics`
**关键函数：** `NewMetrics()`、`Metrics.ServeHTTP()`、`scanFilesystem()`
