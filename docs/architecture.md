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
│  s3Middleware │ logMiddleware (slog)                  │
│  authMiddleware (AWS Sig V2) │ Metrics (/_metrics)   │
├──────────────────────────────────────────────────────┤
│  Router │ BucketManager │ ObjectManager              │
│  BucketLocks (per-bucket mutex)                      │
├──────────────────────────────────────────────────────┤
│           StorageBackend (interface)                  │
│  ┌─ LocalBackend ─────────────────────────────────┐ │
│  │  PathMapper │ service (WriteFile/WriteMeta)     │ │
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
└──────────────────────────────────────────────────────┘
```

### Component Responsibilities

| Component | File | Responsibility |
|-----------|------|---------------|
| **main.go** | main.go | Config flags, slog initialization, backend factory, server startup, graceful shutdown |
| **s3Middleware** | router.go | Server/Date headers, panic recovery |
| **logMiddleware** | router.go | Structured JSON logging (slog), metrics counter updates |
| **Router** | router.go | Two-layer ServeMux: topMux for /_metrics, inner mux for S3 routes |
| **BucketManager** | bucket.go | Create/delete/head/list buckets, per-bucket lock on writes |
| **ObjectManager** | object.go | Put/get/head/delete objects, MaxBytesReader, per-bucket lock on writes |
| **StorageBackend** | storage/backend.go | 存储后端统一接口（Bucket/Object CRUD + ListObjects） |
| **LocalBackend** | storage/local.go | 基于本地文件系统的 StorageBackend 实现 |
| **BucketLocks** | locks.go | Per-bucket mutex for concurrent write safety |
| **Metrics** | metrics.go | Atomic counters + on-demand filesystem scan, GET /_metrics handler |
| **PathMapper** | pathmapper.go | Convert (bucket, key) to filesystem path safely |
| **MetadataStore** | metadata.go | Read/write `.meta` JSON sidecar files |
| **S3 Errors** | errors.go | S3APIError type, XML error responses |
| **ConsistentHash** | hash/consistent.go | Ketama 风格一致性哈希环，双哈希 FNV-1a + 虚拟节点 |
| **GossipMembership** | cluster/member.go | SWIM 简化版 Gossip 协议，Ping/PingReq/Suspect/Dead |
| **Transport** | cluster/transport.go | HTTP RPC 通信层（Ping/Join/Replicate） |
| **DistributedBackend** | storage/distributed.go | Quorum R/W 分布式存储后端，coordinator 模式 |

---

## 3. Disk Layout

```
{storage_root}/
├── my-bucket/
│   ├── hello.txt                    # data file
│   ├── hello.txt.meta               # metadata (JSON)
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

### Route Registration (Go ServeMux patterns)

```go
// 内部 mux — S3 路由
mux.HandleFunc("GET /{$}",              bm.ListBuckets)
mux.HandleFunc("PUT /{bucket}",          authWrap(auth, "bucket", bm.CreateBucket))
mux.HandleFunc("DELETE /{bucket}",       authWrap(auth, "bucket", bm.DeleteBucket))
mux.HandleFunc("HEAD /{bucket}",         authWrap(auth, "bucket", bm.HeadBucket))
mux.HandleFunc("GET /{bucket}",          authWrap(auth, "bucket", bm.ListObjects))
mux.HandleFunc("PUT /{bucket}/{key...}", authWrap(auth, "object", om.PutObject))
mux.HandleFunc("GET /{bucket}/{key...}", authWrap(auth, "object", om.GetObject))
mux.HandleFunc("HEAD /{bucket}/{key...}",authWrap(auth, "object", om.HeadObject))
mux.HandleFunc("DELETE /{bucket}/{key...}", authWrap(auth, "object", om.DeleteObject))

// 顶层 mux — /_metrics + /_cluster/* + fallback 到内部 mux
topMux.Handle("GET /_metrics",  metrics)
topMux.Handle("HEAD /_metrics", metrics)
if clusterHandler != nil {
    topMux.Handle("/_cluster/", clusterHandler) // 分布式模式集群端点
}
topMux.Handle("/", mux)

// 中间件链
return s3Middleware(logMiddleware(metrics, topMux))
```

---

## 5. Authentication - AWS Signature V2 (Phase 2)

### How It Works

1. Client includes `Authorization: AWS {AccessKey}:{Signature}` header
2. Signature = `HMAC-SHA1(SecretKey, StringToSign)`
3. `StringToSign = HTTP-VERB + "\n" + Content-MD5 + "\n" + Content-Type + "\n" + Date + "\n" + CanonicalizedResource`
4. `CanonicalizedResource = /{bucket}/{key}` (no query string for MVP)

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

- **Lock scope:** CreateBucket, DeleteBucket, PutObject, DeleteObject (write operations)
- **No lock:** GetObject, HeadObject, ListObjects, ListBuckets, HeadBucket (read operations)
- **Rationale:** Linux concurrent `read()`/`write()` on the same file is safe; `os.Rename` is atomic.
  Reads either see the old or new data, never partial state. Only writes need serialization to
  prevent races (e.g., two concurrent `CreateBucket` for the same name).
- **Shared locks:** BucketManager and ObjectManager hold the same `*BucketLocks` instance,
  so a `PutObject` and `DeleteBucket` on the same bucket are serialized by the same mutex.

### 6.5 ETag Generation

`ETag = fmt.Sprintf("\"%x\"", md5.Sum(body))` — quoted hex MD5, matching S3 format.

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
│   │   └── auth.go            # Authenticator AWS Sig V2（认证层）
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
│   │   ├── backend.go         # StorageBackend 接口（存储抽象层）
│   │   ├── local.go           # LocalBackend 本地文件系统实现
│   │   ├── ec.go              # ECBackend 纠删码实现
│   │   └── distributed.go     # DistributedBackend Quorum R/W 分布式实现
│   └── handler/
│       ├── router.go          # 路由注册 + middleware 链（HTTP 层）
│       ├── bucket.go          # BucketManager（HTTP 层）
│       ├── object.go          # ObjectManager（HTTP 层）
│       └── helpers.go         # 辅助函数
├── test/
│   ├── main.go                # Test runner
│   ├── helper.go              # Test utilities (Do, Do2, Do3, Sig)
│   ├── phase1.go              # Phase 1 tests (16)
│   ├── phase2.go              # Phase 2 tests (29)
│   ├── phase3.go              # Phase 3 tests (12)
│   ├── phase4.go              # Phase 4 tests (34)
│   ├── phase5.go              # Phase 5 tests (17)
│   └── phase6.go              # Phase 6 分布式集成测试 (20)
├── docs/
│   ├── architecture.md
│   ├── phase1-summary.md
│   ├── phase2-summary.md
│   ├── phase3-summary.md
│   ├── phase4-summary.md
│   ├── phase5-summary.md
│   ├── phase6-summary.md
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
handler/        ← s3error, auth, locks, storage, metrics, config
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

### Future Enhancements (post-MVP)
- Multipart upload
- Object versioning
- AWS Sig V4 authentication
- 磁盘健康监控和自动 rebalance
