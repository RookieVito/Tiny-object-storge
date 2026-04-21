# Phase 3 完成总结

## 1. 完成状态：全部完成

Phase 3 新增 3 个文件（locks.go、metrics.go、test/phase3.go），修改 7 个文件（config.go、errors.go、bucket.go、object.go、router.go、main.go、test/helper.go），
新增 12 个自动化测试全部通过。

---

## 2. Phase 3 实现内容

### 2.1 请求体大小限制（config.go + errors.go + object.go）

**配置：**
- `Config.MaxBodySize int64` — 默认 `10 << 20`（10MB），可通过 `config.json` 的 `max_body_size` 字段配置
- CLI 暂无对应的 flag（通过 config.json 或代码默认值控制）

**实现：**
- `PutObject` 中使用 `http.MaxBytesReader(w, r.Body, om.maxBodySize)` 包装请求体
- 读取 body 后通过 `errors.As(err, &http.MaxBytesError)` 检测超限
- 超限返回 S3 标准错误 `RequestEntityTooLarge`（HTTP 413）
- `authMiddleware` 不读取 body（仅解析 headers），因此 MaxBytesReader 不影响签名验证

**错误码：**
- `ErrRequestEntityTooLarge` — `"Your proposed upload exceeds the maximum allowed size."` (413)

### 2.2 Per-Bucket 互斥锁（locks.go + bucket.go + object.go）

**BucketLocks 类型（cmd/server/locks.go）：**
- 外层 `sync.Mutex` 保护内部的 `map[string]*sync.Mutex`
- 每次加锁时按需创建 bucket 对应的 mutex（懒初始化）
- `Lock(bucket)` 和 `Unlock(bucket)` 方法

**加锁策略：**
- **写操作加锁：** CreateBucket、DeleteBucket、PutObject、DeleteObject
- **读操作不加锁：** GetObject、HeadObject、ListObjects、ListBuckets、HeadBucket
- **理由：** Linux 并发 `read()`/`write()` 是安全的，`os.Rename` 保证原子性。读操作要么看到旧数据要么看到新数据，不会看到部分状态

**共享锁：** BucketManager 和 ObjectManager 持有同一个 `*BucketLocks` 实例，确保同一 bucket 的跨类型操作（如 PutObject 和 DeleteBucket）也被序列化。

**锁获取时机：** 在 PathMapper 验证（纯计算）之后、文件系统操作之前获取锁。

### 2.3 结构化日志中间件（main.go + router.go）

**slog 初始化（main.go）：**
```go
slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
    Level: slog.LevelInfo,
})))
```
- JSON 格式输出到 stdout
- 日志级别 Info

**logMiddleware（router.go）：**
- 记录字段：method、path、status、duration_ms、remote_addr
- 使用 `statusWriter` 包装 `http.ResponseWriter` 捕获响应状态码
- 同时更新 Metrics 计数器

**中间件链（从外到内）：**
```
s3Middleware（Server/Date 头、panic 恢复）
  → logMiddleware（slog 日志 + metrics 计数）
    → topMux
      → GET/HEAD /_metrics → metrics handler
      → / fallback → inner mux → authWrap → handler
```

**日志示例：**
```json
{"time":"2026-04-20T12:00:00Z","level":"INFO","msg":"request","method":"PUT","path":"/my-bucket/hello.txt","status":200,"duration_ms":1,"remote_addr":"127.0.0.1:54321"}
```

### 2.4 Metrics 端点（metrics.go + router.go）

**Metrics 类型（cmd/server/metrics.go）：**
- `TotalRequests` — `sync/atomic.Int64`，每个请求 +1
- `TotalErrors` — `sync/atomic.Int64`，状态码 >= 400 时 +1
- `BucketCount` — 按需扫描文件系统统计 bucket 目录数
- `StorageBytes` — 按需扫描文件系统统计数据文件总大小（不含 .meta 文件）

**端点：** `GET /_metrics`（无需认证）

**JSON 响应：**
```json
{
  "total_requests": 100,
  "total_errors": 5,
  "bucket_count": 3,
  "storage_bytes": 65536
}
```

**设计决策：**
- 请求计数用 atomic（高频、增量式），避免锁开销
- bucket 数和存储字节数用按需扫描（低频、需要精确反映文件系统状态）
- 非 GET 请求返回 405 Method Not Allowed

**路由架构：**
- 使用两层 ServeMux 避免路由冲突：顶层 topMux 注册 `GET /_metrics` 和 `HEAD /_metrics`，fallback `/` 转发到内部 S3 mux
- Go 1.22+ ServeMux 中，`GET /_metrics`（精确匹配）与 `HEAD /{bucket}`（通配符）会冲突，因为 `{bucket}` 可以匹配 `_metrics`。分离到不同 mux 层解决此问题

---

## 3. 更新后的架构

```
Client Request
  │
  ▼
s3Middleware (Server/Date 头, panic 恢复)
  │
  ▼
logMiddleware (slog JSON 日志, metrics 计数器更新)
  │
  ▼
topMux
  ├── GET/HEAD /_metrics → Metrics handler (无认证)
  └── / fallback → inner mux
                    │
                    ▼
              authMiddleware (提取 bucket/key, 验证 HMAC-SHA1 签名)
                    │
                    ▼
              ServeMux (方法 + 路径路由)
                    │
                    ▼
              Handler (BucketManager / ObjectManager)
                    │   ← BucketLocks 保护写操作
                    ▼
              PathMapper (路径验证 + 遍历防护)
                    │
                    ▼
              Filesystem (数据 + .meta 文件)
```

### 完整路由表

| 方法 | 路径 | Handler | 认证 | 加锁 |
|------|------|---------|------|------|
| `GET /{$}` | 精确根 | ListBuckets | 否 | 否 |
| `PUT /{bucket}` | 单段 | CreateBucket | 是 | 是 |
| `DELETE /{bucket}` | 单段 | DeleteBucket | 是 | 是 |
| `HEAD /{bucket}` | 单段 | HeadBucket | 是 | 否 |
| `GET /{bucket}` | 单段 | ListObjects | 是 | 否 |
| `PUT /{bucket}/{key...}` | 多段 | PutObject | 是 | 是 |
| `GET /{bucket}/{key...}` | 多段 | GetObject | 是 | 否 |
| `HEAD /{bucket}/{key...}` | 多段 | HeadObject | 是 | 否 |
| `DELETE /{bucket}/{key...}` | 多段 | DeleteObject | 是 | 是 |
| `GET /_metrics` | 精确 | Metrics.ServeHTTP | 否 | 否 |
| `HEAD /_metrics` | 精确 | Metrics.ServeHTTP | 否 | 否 |

---

## 4. 文件清单（Phase 3 新增/修改）

| 文件 | 操作 | 变更内容 |
|------|------|----------|
| `cmd/server/locks.go` | 新增 | BucketLocks 类型（per-bucket 互斥锁） |
| `cmd/server/metrics.go` | 新增 | Metrics 类型 + GET /_metrics handler |
| `cmd/server/config.go` | 修改 | Config 新增 MaxBodySize 字段（默认 10MB） |
| `cmd/server/errors.go` | 修改 | 新增 ErrRequestEntityTooLarge |
| `cmd/server/bucket.go` | 修改 | BucketManager 持有 BucketLocks，Create/Delete 加锁 |
| `cmd/server/object.go` | 修改 | ObjectManager 持有 BucketLocks + maxBodySize，MaxBytesReader + Put/Delete 加锁 |
| `cmd/server/router.go` | 修改 | 两层 mux、logMiddleware、statusWriter、wire locks/metrics |
| `cmd/server/main.go` | 修改 | slog JSON handler 初始化 |
| `test/phase3.go` | 新增 | 12 个自动化测试 |
| `test/helper.go` | 修改 | 新增 Do3 helper（io.Reader body） |

---

## 5. 自动化测试

`go run ./test/ phase3` 执行 12 个测试，覆盖：

- 请求体大小限制：>10MB → 413 + RequestEntityTooLarge XML
- 请求体大小限制：正常大小 → 200
- 并发安全：20 个并行 Put/Delete 操作无错误
- Metrics 端点：请求计数 delta 验证
- Metrics 端点：错误计数增长验证
- Metrics 端点：bucket_count > 0
- Metrics 端点：storage_bytes 反映实际数据
- Metrics 端点：返回合法 JSON
- Metrics 端点：POST → 405

---

## 6. 后续规划

### 远期
- 分段上传（Multipart Upload）
- 对象版本控制
- AWS Sig V4 认证
- 分布式多节点模式
- 纠删码（Erasure Coding）
