<!-- tags: architecture, overview, api, storage, distributed, cors, config -->
# Tiny Object Storage - Architecture Design

## 1. Overview

A minimal S3-compatible object storage built with **Go 1.26 + net/http**,
using the **Linux filesystem** as the storage backend. Zero external dependencies.

### Design Philosophy
- **Filesystem IS the metadata store** (MinIO-style `.meta` sidecar files)
- **Incremental complexity**: start minimal, add features later
- **Distributed by default**: multi-node deployment with consistent hashing, Gossip, Quorum

---

## 2. Core Components

```
┌──────────────────────────────────────────────────────┐
│                HTTP Server (net/http)                 │
│            Go 1.22+ enhanced ServeMux                 │
├──────────────────────────────────────────────────────┤
│  s3Middleware │ logMiddleware (slog) │ CORSMiddleware      │
│  authMiddleware (AWS Sig V4 + V2) │ Metrics (/_metrics)   │
├──────────────────────────────────────────────────────┤
│  Router │ BucketManager │ ObjectManager │ MultipartManager │
│  BucketLocks (per-bucket mutex)                      │
├──────────────────────────────────────────────────────┤
│           StorageBackend (interface)                  │
│  ┌─ LocalBackend ─────────────────────────────────┐ │
│  │  PathMapper │ service (WriteFile/WriteMeta)     │ │
│  │  MultipartStorage (.uploads/ 临时目录)           │ │
│  │  Linux Filesystem (落盘)                        │ │
│  │  {root}/{bucket}/{key}     ← data file          │ │
│  │  {root}/{bucket}/{key}.meta ← metadata JSON     │ │
│  └─────────────────────────────────────────────────┘ │
│  ┌─ ECBackend (Phase 5) ───────────────────────────┐ │
│  │  Reed-Solomon 编解码 │ 多磁盘分布                │ │
│  └─────────────────────────────────────────────────┘ │
│  ┌─ DistributedBackend (Phase 6) ─────────────────┐ │
│  │  ConsistentHash │ GossipMembership              │ │
│  │  Quorum R/W (N=3, W=2, R=2)                    │ │
│  │  HTTP RPC (/_cluster/*) │ Transport              │ │
│  └─────────────────────────────────────────────────┘ │
│  ┌─ VersionedBackend (Phase 15) ──────────────────┐ │
│  │  装饰器：为任意 StorageBackend 添加版本控制    │ │
│  │  .versions/ 归档 │ .bucket-meta 配置          │ │
│  └─────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────┘
```

### Component Responsibilities

| Component | File | Responsibility |
|-----------|------|---------------|
| **main.go** | main.go | Config flags, slog initialization, backend factory, server startup, graceful shutdown |
| **s3Middleware** | router.go | Server/Date headers, panic recovery |
| **logMiddleware** | router.go | Structured JSON logging (slog), metrics counter updates |
| **Router** | router.go | Two-layer ServeMux: topMux for /_metrics and /_cluster/*, inner mux for S3 routes |
| **BucketManager** | bucket.go | Create/delete/head/list buckets, per-bucket lock on writes |
| **ObjectManager** | object.go | Put/get/head/delete objects, MaxBytesReader, per-bucket lock on writes |
| **MultipartManager** | multipart.go | Multipart upload 6 个 HTTP 端点，type assertion 检测后端支持 |
| **StorageBackend** | storage/backend.go | 存储后端统一接口（Bucket/Object CRUD + ListObjects） |
| **MultipartStorage** | storage/backend.go | 可选扩展接口，multipart upload 7 个方法（LocalBackend 完整实现） |
| **LocalBackend** | storage/local.go | 基于本地文件系统的 StorageBackend + MultipartStorage 实现 |
| **BucketLocks** | locks.go | Per-bucket mutex for concurrent write safety |
| **Metrics** | metrics.go | Atomic counters + on-demand filesystem scan, GET /_metrics handler |
| **PathMapper** | pathmapper.go | Convert (bucket, key) to filesystem path safely |
| **MetadataStore** | metadata.go | Read/write `.meta` JSON sidecar files |
| **S3 Errors** | errors.go | S3APIError type, XML error responses |
| **ConsistentHash** | hash/consistent.go | Ketama 风格一致性哈希环，双哈希 FNV-1a + 虚拟节点 |
| **GossipMembership** | cluster/member.go | SWIM 简化版 Gossip 协议，Ping/PingReq/Suspect/Dead |
| **Transport** | cluster/transport.go | HTTP RPC 通信层（Ping/Join/Replicate） |
| **DistributedBackend** | storage/distributed.go | Quorum R/W 分布式存储后端，coordinator 模式，replicas() 通过 AliveNodes 过滤 |
| **TTLCleaner** | storage/cleanup.go | 后台 goroutine 定期扫描过期 multipart upload 并 AbortUpload 清理 |
| **DiskHealthChecker** | storage/health.go | EC 磁盘健康检查，os.Stat 定期巡检，状态变更回调 |
| **Rebalancer** | storage/rebalance.go | 磁盘恢复后自动扫描重建缺失分片 |
| **VersionedBackend** | storage/versioning.go | 装饰器：为任意 StorageBackend 添加对象版本控制 |
| **VersioningManager** | handler/versioning.go | Versioning HTTP handler（PutBucketVersioning/GetBucketVersioning/ListObjectVersions） |

---

## 3. Disk Layout

```
{storage_root}/
├── my-bucket/
│   ├── hello.txt                    # data file（当前版本）
│   ├── hello.txt.meta               # metadata (JSON, 含 version_id)
│   ├── .bucket-meta                 # 版本控制配置 JSON
│   ├── .versions/                   # 版本归档（ListObjects 跳过）
│   │   └── dir%2Ffile.txt/
│   │       ├── {uuid-v4}            # 归档版本数据
│   │       ├── {uuid-v4}.meta        # 归档版本元数据
│   │       ├── .dm-{uuid-v4}         # delete marker（零字节）
│   │       └── .dm-{uuid-v4}.meta  # delete marker 元数据
│   ├── .uploads/                    # multipart 临时目录（ListObjects 跳过）
│   │   └── {uploadId}/
│   │       ├── info.json            # UploadInfo 元数据
│   │       ├── part-0001.bin        # Part 数据
│   │       └── part-0001.bin.meta   # PartInfo 元数据
│   └── photos/
│       └── 2024/
│           ├── cat.jpg
│           └── cat.jpg.meta
└── another-bucket/
    ├── report.pdf
    └── report.pdf.meta
```

### Metadata File Format (.meta)

```json
{
  "key": "photos/2024/cat.jpg",
  "size": 102400,
  "etag": "\"d41d8cd98f00b204e9800998ecf8427e\"",
  "content_type": "image/jpeg",
  "last_modified": "2024-01-15T10:30:00Z",
  "version_id": "a1b2c3d4-...-uuid-v4",
  "is_latest": true,
  "is_delete_marker": false,
  "user_metadata": {
    "X-Amz-Meta-Author": "vito"
  }
}
```

---

## 4. S3 API - Required Operations (Phase 1 Done)

### Bucket Operations

| S3 Operation | HTTP Method | Route Pattern | Status |
|-------------|-------------|---------------|--------|
| CreateBucket | PUT | `/{bucket}` | Done |
| DeleteBucket | DELETE | `/{bucket}` | Done |
| HeadBucket | HEAD | `/{bucket}` | Done |
| ListBuckets | GET | `/` | Done |

### Object Operations

| S3 Operation | HTTP Method | Route Pattern | Status |
|-------------|-------------|---------------|--------|
| PutObject | PUT | `/{bucket}/{key...}` | Done |
| GetObject | GET | `/{bucket}/{key...}` | Done |
| HeadObject | HEAD | `/{bucket}/{key...}` | Done |
| DeleteObject | DELETE | `/{bucket}/{key...}` | Done |
| ListObjectsV2 | GET | `/{bucket}` (with query) | Phase 2 |

### Multipart Upload Operations

| S3 Operation | HTTP Method | Route Pattern | Status |
|-------------|-------------|---------------|--------|
| InitiateMultipartUpload | POST | `/{bucket}/{key...}?uploads` | Phase 8 |
| UploadPart | PUT | `/{bucket}/{key...}?partNumber=N&uploadId=X` | Phase 8 |
| CompleteMultipartUpload | POST | `/{bucket}/{key...}?uploadId=X` | Phase 8 |
| AbortMultipartUpload | DELETE | `/{bucket}/{key...}?uploadId=X` | Phase 8 |
| ListParts | GET | `/{bucket}/{key...}?uploadId=X` | Phase 8 |
| ListMultipartUploads | GET | `/{bucket}?uploads` | Phase 8 |
| PutBucketVersioning | PUT | `/{bucket}?versioning` | Phase 15 |
| GetBucketVersioning | GET | `/{bucket}?versioning` | Phase 15 |
| ListObjectVersions | GET | `/{bucket}?versions` | Phase 15 |

### Route Registration (Go ServeMux patterns)

```go
// 内部 mux — S3 路由（含 multipart 分发）
mux.HandleFunc("GET /{$}",              bm.ListBuckets)
mux.HandleFunc("PUT /{bucket}",          authWrap(auth, "bucket", bm.CreateBucket))
mux.HandleFunc("DELETE /{bucket}",       authWrap(auth, "bucket", bm.DeleteBucket))
mux.HandleFunc("HEAD /{bucket}",         authWrap(auth, "bucket", bm.HeadBucket))
mux.HandleFunc("GET /{bucket}",          authWrap(auth, "bucket", func(w, r) {
    if r.URL.Query().Has("uploads") { mm.ListMultipartUploads(w, r) }
    else { bm.ListObjects(w, r) }
}))
mux.HandleFunc("POST /{bucket}/{key...}", authWrap(auth, "object", func(w, r) {
    if r.URL.Query().Has("uploads") { mm.InitiateMultipartUpload(w, r) }
    else if r.URL.Query().Has("uploadId") { mm.CompleteMultipartUpload(w, r) }
}))
mux.HandleFunc("PUT /{bucket}/{key...}", authWrap(auth, "object", func(w, r) {
    if r.URL.Query().Has("uploadId") { mm.UploadPart(w, r) }
    else { om.PutObject(w, r) }
}))
mux.HandleFunc("GET /{bucket}/{key...}", authWrap(auth, "object", func(w, r) {
    if r.URL.Query().Has("uploadId") { mm.ListParts(w, r) }
    else { om.GetObject(w, r) }
}))
mux.HandleFunc("HEAD /{bucket}/{key...}",authWrap(auth, "object", om.HeadObject))
mux.HandleFunc("DELETE /{bucket}/{key...}", authWrap(auth, "object", func(w, r) {
    if r.URL.Query().Has("uploadId") { mm.AbortMultipartUpload(w, r) }
    else { om.DeleteObject(w, r) }
}))

// 顶层 mux — /_metrics + /_cluster/* + fallback 到内部 mux
topMux.Handle("/_metrics",         metrics)       // 所有方法到达 handler，非 GET 返回 405
topMux.Handle("/_cluster/",        clusterHandler) // 分布式模式集群端点
topMux.Handle("/", mux)

// 中间件链
return s3Middleware(logMiddleware(metrics, cors.CORSMiddleware(cfg.CORS, topMux)))
```

---

## 5. Authentication - AWS Signature V4 + V2 (Phase 2, 10)

### V4 认证（Phase 10，推荐）

1. Client includes `Authorization: AWS4-HMAC-SHA256 Credential=AKID/scope, SignedHeaders=headers, Signature=sig` header
2. Signature = `HMAC-SHA256(SigningKey, StringToSign)`
3. Signing Key 派生：`HMAC(HMAC(HMAC(HMAC("AWS4"+SecretKey, DateStamp), Region), "s3"), "aws4_request")`
4. String to Sign = `Algorithm + "\n" + AmzDate + "\n" + Scope + "\n" + SHA256(CanonicalRequest)`
5. Canonical Request = `Method + "\n" + CanonicalURI + "\n" + CanonicalQueryString + "\n" + CanonicalHeaders + "\n" + SignedHeaders + "\n" + PayloadHash`
6. Payload 统一使用 `UNSIGNED-PAYLOAD`
7. 时间偏移检查：`|X-Amz-Date - server time| > 15min` → 403

### V2 认证（Phase 2，向后兼容）

1. Client includes `Authorization: AWS {AccessKey}:{Signature}` header
2. Signature = `HMAC-SHA1(SecretKey, StringToSign)`
3. `StringToSign = HTTP-VERB + "\n" + Content-MD5 + "\n" + Content-Type + "\n" + Date + "\n" + CanonicalizedResource`
4. `CanonicalizedResource = /{bucket}/{key}`

### 认证分发

`Authenticate()` 按 Authorization 头前缀自动分发：
- `AWS4-HMAC-SHA256` → V4 认证
- `AWS ` → V2 认证（fallback）
- 无 Authorization 头 + query 中 `X-Amz-Algorithm=AWS4-HMAC-SHA256` → Presigned URL 认证

### Presigned URL（Phase 11）

通过预签名 URL 实现无需 Authorization 头的临时访问授权：

1. URL query 中嵌入签名参数：`X-Amz-Algorithm`, `X-Amz-Credential`, `X-Amz-Date`, `X-Amz-Expires`, `X-Amz-SignedHeaders`, `X-Amz-Signature`
2. 签名算法与 V4 相同，但 canonical query string 包含所有 X-Amz-* 参数
3. 过期检查：`server_time > X-Amz-Date + X-Amz-Expires` → 403
4. 最大有效期 604800 秒（7 天）

---

## 6. Key Design Decisions

### 6.1 Path Traversal Prevention (3 layers)

1. **Go ServeMux**: Automatically cleans `..` from URL paths before routing
2. **PathMapper reject**: Explicitly rejects keys containing `..` substring
3. **Prefix verification**: After `filepath.Clean` + `filepath.Join`, verifies result starts with bucket directory prefix

### 6.2 Atomic Writes

All file writes use the temp-then-rename pattern:
```
os.CreateTemp(dir) → Write → Sync → Close → os.Rename
```
This ensures no partial/corrupt files on crash.

### 6.3 DeleteObject Cleanup

DeleteObject removes both data and `.meta` files, then cleans up empty parent
directories up to the bucket root. S3-semantic idempotent: returns 204 even if
key doesn't exist.

### 6.4 Concurrency

Go's `net/http` handles concurrency natively (one goroutine per request).
Phase 3 introduces `BucketLocks` (cmd/server/locks.go) — a per-bucket `sync.Mutex`:

- **Lock scope:** CreateBucket, DeleteBucket, PutObject, DeleteObject, CompleteMultipartUpload, AbortMultipartUpload (write operations)
- **No lock:** GetObject, HeadObject, ListObjects, ListBuckets, HeadBucket, InitiateMultipartUpload, UploadPart, ListParts, ListUploads (read / atomic operations)
- **Rationale:** Linux concurrent `read()`/`write()` on the same file is safe; `os.Rename` is atomic.
  Reads either see the old or new data, never partial state. Only writes need serialization to
  prevent races (e.g., two concurrent `CreateBucket` for the same name).
- **Shared locks:** BucketManager, ObjectManager 和 MultipartManager 持有同一个 `*BucketLocks` 实例，
  所以 `PutObject`、`CompleteMultipartUpload` 和 `DeleteBucket` 在同一 bucket 上会被同一个 mutex 串行化。
- **Multipart UploadPart 并发安全：** 不同 partNumber 可并行上传，同一 partNumber 通过原子 rename（`service.WriteFile`）保证覆盖安全。

### 6.5 ETag Generation

- **PutObject**：`ETag = fmt.Sprintf("\"%x\"", md5.Sum(body))` — quoted hex MD5, matching S3 format.
- **Multipart Complete**：`ETag = fmt.Sprintf("\"%x-%d\"", md5.Sum(concatMD5s), partCount)` — S3 标准复合 ETag，`concatMD5s` 为各 part 的 16 字节 MD5 摘要拼接。

---

## 7. Tech Stack

| Component | Choice | Reason |
|-----------|--------|--------|
| Language | Go 1.26 | Fast development, excellent stdlib |
| HTTP | net/http ServeMux | Go 1.22+ enhanced patterns, zero deps |
| JSON | encoding/json | Standard library |
| XML | encoding/xml | S3 error responses |
| MD5 | crypto/md5 | ETag generation |
| Build | go build | No build system needed |
| Testing | curl / aws-cli | Manual testing for MVP |

---

## 8. Project Structure

```
tiny-object-storge/
├── go.mod
├── cmd/server/
│   └── main.go              # 入口（config 加载、slog 初始化、graceful shutdown）
├── src/
│   ├── s3error/
│   │   └── error.go          # S3APIError 类型 + XML 错误序列化（基础层）
│   ├── config/
│   │   └── config.go          # Config + LoadConfig（配置层）
│   ├── locks/
│   │   └── locks.go           # BucketLocks per-bucket 互斥锁（并发层）
│   ├── pathmapper/
│   │   └── pathmapper.go      # (bucket, key) → 文件路径映射（安全层）
│   ├── auth/
│   │   ├── auth.go            # Authenticator Sig V4/V2/Presign 双认证（认证层）
│   │   ├── v4.go              # Sig V4 签名计算与验证
│   │   └── presign.go         # Presigned URL 生成与验证
│   ├── cors/
│   │   └── cors.go            # CORS 中间件：origin 匹配、preflight、Access-Control 头
│   ├── service/
│   │   └── metadata.go        # ObjectMeta、原子读写、Content-Type 检测（业务层）
│   ├── metrics/
│   │   └── metrics.go         # Metrics atomic 计数 + GET /_metrics（可观测性层）
│   ├── hash/
│   │   └── consistent.go      # 一致性哈希环（Ketama 风格，双哈希 + 虚拟节点）
│   ├── cluster/
│   │   ├── node.go            # NodeID/NodeInfo/NodeState 数据结构
│   │   ├── protocol.go        # RPC 消息类型（Ping/Join/Storage 等）
│   │   ├── transport.go       # HTTP RPC 通信层
│   │   ├── member.go          # Gossip 成员管理（SWIM 简化版）
│   │   └── elect.go           # Leader Election（确定性选举）
│   ├── ec/
│   │   ├── galois.go          # GF(2^8) 有限域算术（查找表）
│   │   └── reedsolomon.go     # Cauchy Reed-Solomon 编解码器
│   ├── storage/
│   │   ├── backend.go         # StorageBackend 接口 + MultipartStorage 接口（存储抽象层）
│   │   ├── local.go           # LocalBackend 本地文件系统实现
│   │   ├── multipart.go       # LocalBackend MultipartStorage 实现
│   │   ├── cleanup.go         # TTLCleaner 后台过期 multipart upload 清理
│   │   ├── health.go          # DiskHealthChecker EC 磁盘健康检查
│   │   ├── rebalance.go       # Rebalancer 磁盘恢复后自动重建缺失分片
│   │   ├── ec.go              # ECBackend 纠删码实现（含 RepairObject、自修复）
│   │   ├── versioning.go      # VersionedBackend 对象版本控制装饰器
│   │   ├── distributed.go     # DistributedBackend Quorum R/W 分布式实现
│   │   └── ec_distributed.go  # ECDistributedBackend 分布式纠删码实现
│   └── handler/
│       ├── router.go          # 路由注册 + middleware 链（HTTP 层）
│       ├── bucket.go          # BucketManager（HTTP 层）
│       ├── object.go          # ObjectManager（HTTP 层）
│       ├── multipart.go       # MultipartManager（HTTP 层）
│       └── helpers.go         # 辅助函数
├── test/
│   ├── main.go                # Test runner
│   ├── helper.go              # Test utilities (Do, Do2, Do3, Sig)
│   ├── phase1.go              # Phase 1 tests (16)
│   ├── phase2.go              # Phase 2 tests (29)
│   ├── phase3.go              # Phase 3 tests (12)
│   ├── phase4.go              # Phase 4 tests (34)
│   ├── phase5.go              # Phase 5 tests (17)
│   ├── phase6.go              # Phase 6 分布式集成测试 (20)
│   ├── phase7.go              # Phase 7 客户端工具集成测试 (19)
│   └── phase8.go              # Phase 8 Multipart Upload 集成测试 (32)
│   ├── phase9.go              # Phase 9 Range 请求测试 (60)
│   ├── phase10.go             # Phase 10 Sig V4 认证测试 (15)
│   ├── phase11.go             # Phase 11 Presigned URL 测试 (20)
│   ├── phase12.go             # Phase 12 CORS 配置测试 (12)
│   ├── phase13.go             # Phase 13 EC/Distributed Multipart 测试
│   ├── phase14.go             # Phase 14 TTL 自动清理测试 (16)
│   ├── phase15.go             # Phase 15 对象版本控制测试 (47)
│   ├── phase16.go             # Phase 16 磁盘健康监控和 Rebalance 测试 (23)
│   └── phase17.go             # Phase 17 分布式纠删码测试
├── docs/
│   ├── architecture.md
│   ├── phase1-summary.md
│   ├── phase2-summary.md
│   ├── phase3-summary.md
│   ├── phase4-summary.md
│   ├── phase5-summary.md
│   ├── phase6-summary.md
│   ├── phase7-summary.md
│   ├── phase8-summary.md
│   ├── phase9-summary.md
│   └── phase10-summary.md
│   └── phase11-summary.md
│   └── phase12-summary.md
│   └── technical/
└── TODO.md
```

### Package Dependencies (Acyclic)

```
s3error/        ← 无依赖
config/         ← 无依赖
locks/          ← 无依赖
hash/           ← 无依赖（纯数据结构）
pathmapper/     ← s3error
auth/           ← s3error
service/        ← s3error
metrics/        ← s3error
cluster/        ← 无外部包依赖（仅 stdlib）
storage/        ← pathmapper, service, ec, s3error, hash, cluster
handler/        ← s3error, auth, locks, storage, metrics, config, cors
cmd/server/     ← handler, config, storage
```

---

## 9. Implementation Phases

### Phase 1: Core MVP ✅
- [x] net/http server with graceful shutdown
- [x] PathMapper with 3-layer traversal prevention
- [x] BucketManager (create/delete/head/list)
- [x] ObjectManager (put/get/head/delete)
- [x] MetadataStore (.meta JSON sidecar, atomic writes)
- [x] Router with Go 1.22+ ServeMux patterns
- [x] S3 XML error responses
- [x] Delete cleanup (empty parent dirs)
- [x] Curl test suite passing

### Phase 2: S3 Compatibility ✅
- [x] ListObjectsV2 with prefix/delimiter/pagination support
- [x] AWS Sig V2 authentication (HMAC-SHA1)
- [x] Content-Type detection (http.DetectContentType)
- [x] Config file (JSON, with CLI flag overrides)

### Phase 3: Robustness ✅
- [x] Request body size limit (`http.MaxBytesReader`, default 10MB, configurable via `max_body_size`)
- [x] Concurrent access safety (`BucketLocks` per-bucket mutex)
- [x] Logging middleware (`log/slog` structured JSON output)
- [x] Metrics endpoint (`GET /_metrics` with atomic counters + filesystem scan)

### Phase 4: Storage Backend Abstraction ✅
- [x] `StorageBackend` interface — 统一的 Bucket/Object CRUD + ListObjects 接口
- [x] `LocalBackend` — 本地文件系统实现，复用 PathMapper + service 原子写入
- [x] Handler 层重构 — ObjectManager/BucketManager 通过 StorageBackend 接口操作存储
- [x] Backend 工厂模式 — `cmd/server/main.go` 根据 `backend_type` 选择后端
- [x] S3 API 零变化，全量回归测试通过

### Phase 5: Erasure Coding ✅
- [x] GF(2^8) 有限域算术（不可约多项式 0x11D，exp/log 查找表）
- [x] Cauchy Reed-Solomon 编解码器（任意 K×K 子矩阵可逆，增广矩阵高斯消元）
- [x] `ECBackend` — K 数据分片 + M 校验分片，N 磁盘分布
- [x] 降级读（可用磁盘 >= K 时自动解码恢复）
- [x] ECObjectMeta 独立 metaStore 存储
- [x] ECConfig 配置（disks、data_shards、parity_shards、meta_root）
- [x] 45 个 EC 单元测试 + 17 个集成测试

### Phase 6: Distributed Storage ✅
- [x] **一致性哈希**（hash/consistent.go）— Ketama 风格，双哈希 FNV-1a + 500 虚拟节点
- [x] **Gossip 成员管理**（cluster/member.go）— SWIM 简化版，Ping → PingReq → Suspect → Dead
- [x] **Leader Election**（cluster/elect.go）— 确定性选举，NodeID 字典序最小者
- [x] **HTTP RPC 通信**（cluster/transport.go）— 集群内部端点 `/_cluster/*`，复用 S3 端口
- [x] **DistributedBackend**（storage/distributed.go）— Quorum R/W（N=3, W=2, R=2），coordinator 模式
- [x] **配置支持**（config.go）— DistributedConfig 结构体，seed_nodes、replication_factor 等
- [x] 9 个一致性哈希单元测试 + 11 个 Gossip 单元测试 + 20 个分布式集成测试

### Phase 7: Client Tools ✅
- [x] **CLI 客户端**（cmd/client/）— ls/cp/mb/rb/rm/cat/stat 子命令，Sig V2 签名，进度条
- [x] **Web UI**（web/）— React 18 + TypeScript SPA，浏览器端 Sig V2 签名
- [x] Bucket/Object 管理、前缀导航、文件拖拽上传（带进度条）、下载、删除
- [x] embed.FS 嵌入到 Go 二进制，`/_ui/` 路径自动服务
- [x] 19 个 CLI 客户端集成测试

### Phase 8: Multipart Upload ✅
- [x] **MultipartStorage 接口**（storage/backend.go）— 独立于 StorageBackend 的可选扩展接口
- [x] **LocalBackend 实现**（storage/multipart.go）— 7 个方法完整实现
- [x] **6 个 S3 multipart 端点** — Initiate、UploadPart、Complete、Abort、ListParts、ListUploads
- [x] **路由分发**（handler/router.go）— 通过 query param 复用现有路由
- [x] **MultipartManager**（handler/multipart.go）— type assertion 检测后端支持
- [x] ETag 标准算法（单 part MD5 + 最终对象 `MD5(concat)-N`）
- [x] 并发安全（UploadPart 无锁可并行，Complete/Abort 加 bucket 锁）
- [x] `.uploads/` 目录隔离（ListObjects/Metrics 自动跳过）
- [x] EC/Distributed 后端 Multipart Upload（Phase 13 完整实现）

### Phase 9: Range 请求 ✅
- [x] **parseRangeHeader**（handler/helpers.go）— 支持 `bytes=start-end`、`bytes=start-`、`bytes=-suffix`
- [x] **GetObject 206** — 单 range 返回 Partial Content + Content-Range + Accept-Ranges
- [x] **HeadObject 206** — 同样支持 Range（仅 headers，无 body）
- [x] **416 InvalidRange** — Range 无法满足时返回 Range Not Satisfiable
- [x] 无效/多 range 回退 200 全量返回
- [x] 60 个集成测试

### Phase 10: AWS Sig V4 认证 ✅
- [x] **Sig V4 签名**（auth/v4.go）— HMAC-SHA256 密钥派生、Canonical Request、String to Sign
- [x] **V4/V2 双认证** — Authenticate() 按前缀分发，V2 作为 fallback
- [x] **时间偏移检查** — `|X-Amz-Date - server time| > 15min` → 403
- [x] **Config Region** — 新增 Region 字段（默认 `us-east-1`）
- [x] **客户端升级** — CLI signer + Web UI signer 升级为 V4
- [x] 15 个集成测试

### Phase 11: Presigned URL ✅
- [x] **PresignV4 生成**（auth/presign.go）— GET/PUT 预签名 URL，query params 嵌入签名
- [x] **Presign 验证** — Authenticate() 检测 query 中 X-Amz-Algorithm 分发
- [x] **过期检查** — `server_time > X-Amz-Date + X-Amz-Expires` → 403，最大 7 天
- [x] **CLI presign 子命令** — `tiny-storage presign <bucket/key>`
- [x] 20 个集成测试

### Phase 12: CORS 配置 ✅
- [x] **CORSConfig**（config.go）— Enabled、AllowedOrigins/Methods/Headers、ExposeHeaders、MaxAge、AllowCredentials
- [x] **CORSMiddleware**（cors/cors.go）— origin 匹配（精确 + 通配符 `*`）、preflight OPTIONS 返回 204
- [x] **中间件链集成** — s3Middleware → logMiddleware → CORSMiddleware → topMux
- [x] 默认启用（`Enabled: true`，`AllowedOrigins: ["*"]`）
- [x] 12 个集成测试

### Phase 13: EC/Distributed Multipart Upload ✅
- [x] **EC multipart**（src/storage/ec.go）— per-part EC 编解码、`.uploads/` 元数据管理
- [x] **Distributed multipart**（src/storage/distributed.go）— coordinator 模式、UploadPart 本地存储、CompleteUpload quorum 写入最终对象
- [x] **Cluster RPC 扩展**（src/cluster/protocol.go）— `PartInfoMsg`、`UploadId`/`PartNumber`/`Parts` 字段
- [x] 替换 EC/Distributed 后端的 ErrNotImplemented stub
- [x] 集成测试 `test/phase13.go`

### Phase 14: TTL 自动清理 ✅
- [x] **TTLCleaner**（src/storage/cleanup.go）— 后台 goroutine 定期扫描过期 multipart upload
- [x] **Config** — `MultipartTTLSeconds`（默认 86400 = 24h）、`CleanupIntervalSec`（默认 3600 = 1h）
- [x] **Metrics** — 新增 `MultipartCleanups` 计数器（`GET /_metrics` 暴露）
- [x] context-based 优雅停止，首次启动立即清理，分页扫描，竞态容错
- [x] 16 个集成测试（过期清理、未过期保留、metrics 计数、CompleteUpload 不受影响）

### Phase 15: 对象版本控制 ✅
- [x] **VersionedBackend 装饰器**（src/storage/versioning.go）— 包装任意 StorageBackend，后端无关版本存储
- [x] **VersionedStorage 接口** — PutBucketVersioning/GetBucketVersioning/GetObjectVersion/HeadObjectVersion/DeleteObjectVersion/ListObjectVersions
- [x] **Delete Marker** — 版本化 bucket 的 DeleteObject 创建 delete marker（零字节 + 哨兵）
- [x] **版本路由** — `?versioning`、`?versions`、`?versionId` 路由分发
- [x] 47 个集成测试

### Phase 16: 磁盘健康监控和 Rebalance ✅
- [x] **DiskHealthChecker**（src/storage/health.go）— 后台 goroutine 定期 os.Stat 巡检磁盘，互斥锁保护并发检查
- [x] **Rebalancer**（src/storage/rebalance.go）— 磁盘恢复后自动扫描所有 EC 对象重建缺失分片，互斥锁防止并发
- [x] **ReedSolomon.Reconstruct**（src/ec/reedsolomon.go）— 恢复所有 N 个分片（含 parity），用于主动修复
- [x] **ECBackend.RepairObject**（src/storage/ec.go）— 主动修复指定对象缺失分片
- [x] **GetObject 自修复升级** — 读取时缺失分片使用 Reconstruct 恢复全部分片（含 parity）
- [x] **ECConfig** — HealthCheckIntervalSec（默认 60）
- [x] **Metrics** — DiskHealthChecks、RebalancedObjects 计数器
- [x] 23 个集成测试（健康检查、降级读、自修复、磁盘故障检测、恢复 Rebalance、多磁盘故障容忍）

### Phase 17: 分布式纠删码存储 ✅
- [x] **ECDistributedBackend**（src/storage/ec_distributed.go）— RS 编码后分片分布到不同节点，非完整复制
- [x] **ECDistMeta** — 记录分片到节点的映射（`ShardNodes`），支持故障后精确定位分片
- [x] **PutObject** — RS.Encode → 一致性哈希选 K+M 节点 → 并发 RPC `ec_put_shard` → quorum 复制元数据
- [x] **GetObject** — 从元数据读取 `ShardNodes` → 并发 RPC `ec_get_shard` → RS.Decode 降级读
- [x] **DeleteObject** — 从元数据读取分片节点 → 并发 RPC `ec_delete_shard` + `ec_delete_meta`
- [x] **MultipartStorage** — coordinator 模式，CompleteUpload 走 EC 分片写入流程
- [x] **配置** — `backend_type: "ec_distributed"`，合并 EC + Distributed 配置
- [x] 集成测试 `test/phase17.go`（6 节点 4+2 EC，节点故障后读写）

### Future Enhancements (post-MVP)
- （磁盘健康监控和自动 rebalance 已在 Phase 16 实现）
