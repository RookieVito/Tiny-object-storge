# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Run

```bash
go build ./cmd/server/...            # compile server
go run ./cmd/server/ --port 9000     # run server
go run ./cmd/server/ --config ./config.json  # run with config file
```

Config file (`config.json`) is optional — defaults are used if missing. CLI flags override config values.
Default credentials: `minioadmin` / `minioadmin`.

No external Go dependencies — the server uses Go standard library only.
Web UI (`web/`) uses React + TypeScript + Vite + Tailwind CSS (dev dependency only).

## CLI Client

```bash
go run ./cmd/client/ config --endpoint http://localhost:9000 --access-key minioadmin --secret-key minioadmin
go run ./cmd/client/ ls                          # list buckets
go run ./cmd/client/ ls mybucket                 # list objects in bucket
go run ./cmd/client/ mb mybucket                 # create bucket
go run ./cmd/client/ rb mybucket                 # remove bucket
go run ./cmd/client/ cp localfile s3://b/key     # upload
go run ./cmd/client/ cp s3://b/key localfile     # download
go run ./cmd/client/ cat s3://bucket/key         # print to stdout
go run ./cmd/client/ stat s3://bucket/key        # show metadata
go run ./cmd/client/ rm s3://bucket/key          # delete object
go run ./cmd/client/ presign bucket/key          # generate presigned URL
```

配置存储在 `~/.tiny-storage/config.json`，支持环境变量 `TOS_ENDPOINT` / `TOS_ACCESS_KEY` / `TOS_SECRET_KEY`。

## Web UI

浏览器访问 `http://localhost:9000/_ui/`。功能：Bucket/Object 管理、前缀导航、文件拖拽上传（带进度条）、下载、删除。
Web UI 通过浏览器端 Web Crypto API 实现 AWS Sig V4 签名，是现有 S3 API 的纯客户端。

```bash
# 开发模式（热重载）
cd web && npm install && npm run dev    # http://localhost:5173，proxy → :9000

# 生产构建（嵌入到 Go 二进制）
cd web && npm run build                 # 输出到 web/dist/，然后复制到 cmd/server/static/dist/
go run ./cmd/server/                    # /_ui/ 路径自动服务嵌入的前端
```

## Testing

```bash
# Server must be running first (local 模式)
go run ./cmd/server/ &

# Run all tests
go run ./test/

# Run specific phase
go run ./test/ phase1
go run ./test/ phase2
go run ./test/ phase3
go run ./test/ phase4

# EC 模式测试（需要先用 EC 配置启动服务器）
go run ./cmd/server/ --config test/ec-config.json &
go run ./test/ phase5

# EC 单元测试（不需要服务器）
go test ./src/ec/...

# 分布式模式测试（自动启动 3 个节点进程）
go run ./test/ phase6

# CLI 客户端集成测试
go run ./test/ phase7

# Multipart upload 集成测试
go run ./test/ phase8

# Presigned URL 集成测试
go run ./test/ phase11

# 一致性哈希 + Gossip 单元测试（不需要服务器）
go test ./src/hash/...
go test ./src/cluster/...

# 一键全量测试脚本（自动编译、启停服务器、清理数据）
./test/scripts/run.sh            # 全量（local + EC + distributed + unit）
./test/scripts/run.sh local      # 仅 local 模式
./test/scripts/run.sh ec         # 仅 EC 模式
./test/scripts/run.sh distributed # 仅分布式
./test/scripts/run.sh unit       # 仅单元测试
```

## Architecture

A minimal S3-compatible object storage. Multi-package Go project under `src/`, no frameworks.

**Request flow:** `s3Middleware` (Server/Date headers, panic recovery) → `logMiddleware` (slog structured JSON log + metrics counters) → `authMiddleware` (AWS Sig V4 + V2) → `ServeMux` (method+path routing) → handler → `StorageBackend`.

**Package structure:**
```
cmd/server/                  # 服务器入口（config 加载、slog 初始化、graceful shutdown、Web UI embed）
cmd/server/embed.go          # go:embed 声明前端静态资源，SPA fallback handler
cmd/client/                  # CLI 客户端（Sig V4 签名、子命令、进度条）
web/                         # React + TypeScript + Vite + Tailwind 前端
  src/api/                   # S3 HTTP 客户端（Web Crypto API Sig V4 签名、XML 解析）
  src/components/            # UI 组件（LoginScreen、BucketList、ObjectBrowser、UploadDialog）
  src/hooks/                 # Auth 上下文管理
src/
  s3error/                   # 基础层：S3APIError 类型 + XML 错误序列化
  config/                    # 配置层：Config + LoadConfig
  locks/                     # 并发层：BucketLocks (per-bucket mutex)
  hash/                      # 数据分布层：一致性哈希环（Ketama 风格）
  cluster/                   # 集群层：Gossip 成员管理 + HTTP RPC + Leader Election
  pathmapper/                # 安全层：(bucket, key) → 文件路径映射，3 层遍历防护
  auth/                      # 认证层：Authenticator (AWS Sig V4 HMAC-SHA256 + Sig V2 HMAC-SHA1)
  service/                   # 业务层：ObjectMeta、原子读写、Content-Type 检测
  metrics/                   # 可观测性层：Metrics (atomic 计数 + 文件系统扫描)
  ec/                        # 纠删码层：GF256 有限域 + ReedSolomon 编解码器
  storage/                   # 存储抽象层：StorageBackend + MultipartStorage 接口 + LocalBackend + ECBackend + DistributedBackend
  handler/                   # HTTP 层：BucketManager、ObjectManager、MultipartManager、router、middleware
```

**Key types:**
- `Config` (src/config) — port, root, access_key, secret_key, max_body_size (default 10MB), backend_type (default "local"), ECConfig, DistributedConfig
- `StorageBackend` (src/storage) — 存储后端接口，定义 Bucket/Object 的 CRUD 操作
- `LocalBackend` (src/storage) — 基于本地文件系统的 StorageBackend 实现
- `ECBackend` (src/storage) — 基于纠删码的 StorageBackend 实现，K 数据分片 + M 校验分片多磁盘分布
- `DistributedBackend` (src/storage) — 分布式存储后端，一致性哈希 + Gossip + Quorum R/W
- `ConsistentHash` (src/hash) — Ketama 风格一致性哈希环，双哈希 FNV-1a + 虚拟节点
- `GossipMembership` (src/cluster) — SWIM 简化版 Gossip 成员管理
- `ReedSolomon` (src/ec) — Cauchy Reed-Solomon 编解码器（GF(2^8) 有限域算术）
- `GF256` (src/ec) — GF(2^8) 有限域算术（exp/log 查找表）
- `S3APIError` (src/s3error) — S3 error type with code + HTTP status, XML serialization via `WriteS3Err`
- `PathMapper` (src/pathmapper) — converts `(bucket, key)` to filesystem paths, 3-layer traversal defense
- `Authenticator` (src/auth) — validates AWS Sig V4 (`AWS4-HMAC-SHA256`), Sig V2 (`AWS {key}:{sig}`), and Presigned URL (query params), dispatches by Authorization header prefix or X-Amz-Algorithm query param
- `BucketLocks` (src/locks) — per-bucket `sync.Mutex` for concurrent write safety
- `ObjectMeta` (src/service) — metadata struct, atomic write/read (`WriteFile`/`WriteMeta`/`ReadMeta`)
- `BucketManager` (src/handler) — bucket CRUD + ListObjectsV2, write ops protected by per-bucket lock
- `ObjectManager` (src/handler) — object CRUD, MaxBytesReader body limit, per-bucket lock on writes
- `Metrics` (src/metrics) — atomic counters + on-demand filesystem scan, `GET /_metrics` handler
- `MultipartStorage` (src/storage) — 可选接口，定义 multipart upload 的 7 个方法（Initiate、UploadPart、Complete、Abort、ListParts、ListUploads、GetUploadInfo）
- `PartInfo` (src/storage) — 已上传 part 的元数据（PartNumber、Size、ETag、LastModified）
- `UploadInfo` (src/storage) — 进行中 multipart upload 的元数据（UploadId、Bucket、Key、ContentType、UserMetadata、Initiated）
- `MultipartManager` (src/handler) — multipart upload HTTP handler，6 个端点，通过 type assertion 检测后端支持

**Route table:**
- `GET /{$}` — ListBuckets (no auth)
- `PUT/DELETE/HEAD /{bucket}` — bucket ops (auth required)
- `GET /{bucket}` — ListObjects (auth); `?uploads` → ListMultipartUploads
- `POST /{bucket}/{key...}` — `?uploads` → InitiateMultipartUpload; `?uploadId` → CompleteMultipartUpload (auth required)
- `PUT /{bucket}/{key...}` — PutObject; `?uploadId` → UploadPart (auth required)
- `GET /{bucket}/{key...}` — GetObject; `?uploadId` → ListParts (auth required)
- `HEAD /{bucket}/{key...}` — HeadObject (auth required)
- `DELETE /{bucket}/{key...}` — DeleteObject; `?uploadId` → AbortMultipartUpload (auth required)
- `GET/HEAD /_metrics` — metrics endpoint (no auth)
- `GET /_ui/{path...}` — Web UI 静态资源 (no auth, SPA fallback, embed.FS)
- `POST /_cluster/ping|ping-req|join|leave` — Gossip 协议 (no auth, distributed mode)
- `POST /_cluster/replicate` — 存储复制 RPC (no auth, distributed mode)
- `GET /_cluster/members` — 成员列表 (no auth, distributed mode)

**ListObjectsV2 algorithm:** WalkDir → collect keys + read .meta → sort → prefix filter → start-after pagination → delimiter grouping (CommonPrefixes) → max-keys truncation → base64 continuation token.

**Disk layout (local):** `{root}/{bucket}/{key}` for data, `{root}/{bucket}/{key}.meta` for JSON metadata.
**Disk layout (local, multipart):** `{root}/{bucket}/.uploads/{uploadId}/info.json` for upload metadata, `part-NNNN.bin` for part data, `part-NNNN.bin.meta` for part metadata. Cleaned up after Complete/Abort.

**Disk layout (EC):** `disk-{i}/{bucket}/{key}` for each shard (i=0..N-1), `meta-root/{bucket}/{key}.ec-meta` for EC metadata.

**Disk layout (distributed):** Each node uses LocalBackend layout; consistent hash ring determines which nodes store replicas.

## Conventions

- All new S3 errors: `*s3error.S3APIError` vars in `src/s3error/error.go`
- All file writes: atomic pattern (`service.WriteFile`/`service.WriteMeta`)
- All storage operations go through `StorageBackend` interface — handler 层不直接调用 pathmapper/service/os
- Multipart upload 通过可选 `MultipartStorage` 接口扩展，后端未实现时返回 ErrNotImplemented
- New backends implement `storage.StorageBackend` interface, registered via backend factory in `cmd/server/main.go`
- Handlers extract `bucket`/`key` via `r.PathValue()`, validate through backend
- New protected routes must be wrapped with `authWrap(auth, "bucket"|"object", handler)`
- New packages go under `src/`, package dependency must be acyclic

## Rules

- 使用中文编写文档和注释，专业术语保留英文原文（如 Bucket、ETag、delimiter、ServeMux）
- 新增功能必须同步编写测试，写入 `test/` 目录下对应的 phase 文件中
- 每个 Phase 完成时，运行 `./test/scripts/run.sh` 全量回归测试，确认所有测试通过后再标记完成
- 每个 Phase 完成时，必须同步更新 `docs/` 目录下的相关文档（architecture.md、usage.md、phase summary、技术设计文档等），保持文档与代码一致
