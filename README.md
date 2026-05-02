# Tiny Object Storage

一个兼容 S3 协议的轻量对象存储服务，纯 Go 标准库实现，零外部依赖。支持本地存储、纠删码、分布式三种后端模式，适合学习和理解对象存储的核心原理。

## 功能概览

**S3 API 兼容**

- Bucket / Object CRUD、ListObjectsV2（分页、前缀过滤、delimiter 分组）
- Multipart Upload（分片上传，支持断点续传）
- Range 请求（206 Partial Content）
- 对象版本控制（Versioning + Delete Marker）
- Presigned URL（预签名 GET/PUT）
- AWS Signature V2 + V4 双认证

**存储后端**

- **Local** — 基于文件系统，PathMapper 路径映射 + 原子写入
- **EC** — Reed-Solomon 纠删码（K 数据 + M 校验分片），降级读 + 磁盘健康监控 + 自动 Rebalance
- **Distributed** — 一致性哈希 + Gossip 成员管理 + Quorum R/W
- **EC Distributed** — 融合 EC 编码与分布式存储，分片分布到不同节点（非完整复制）

**客户端工具**

- Web UI（React + TypeScript SPA，拖拽上传、进度条、嵌入到 Go 二进制）
- CLI 客户端（ls / cp / mb / rb / rm / cat / stat / presign）

**运维特性**

- 结构化 JSON 日志（slog）
- Metrics 端点（`GET /_metrics`）
- CORS 跨域支持
- Graceful Shutdown
- Multipart Upload TTL 自动清理

## Quick Start

```bash
# 编译
go build -o tiny-storage ./cmd/server/

# 启动（默认端口 9000，凭证 minioadmin/minioadmin）
./tiny-storage

# Web UI — 浏览器打开
open http://localhost:9000/_ui/

# CLI 客户端
go run ./cmd/client/ config --endpoint http://localhost:9000 --access-key minioadmin --secret-key minioadmin
go run ./cmd/client/ mb my-bucket
go run ./cmd/client/ cp README.md s3://my-bucket/README.md
go run ./cmd/client/ ls my-bucket
```

## 配置

服务器默认无需配置文件即可启动。通过 `--config` 指定或使用 CLI 参数覆盖：

```bash
./tiny-storage --port 9000 --root ./data
./tiny-storage --config ./config.json
```

### 后端模式

| 模式 | `backend_type` | 说明 |
|------|---------------|------|
| Local | `local` | 默认，基于文件系统，支持原子写入 |
| EC | `ec` | Reed-Solomon 纠删码，配置 `disks`、`data_shards`、`parity_shards` |
| Distributed | `distributed` | 一致性哈希 + Gossip，配置 `node_id`、`seed_nodes`、Quorum 参数 |
| EC Distributed | `ec_distributed` | EC 编码 + 分布式，分片分布到不同节点 |

完整配置示例见 [`example/`](example/) 目录。

## 测试

```bash
# 全量测试（自动编译、启停服务器、清理数据）
./test/scripts/run.sh

# 按模式单独运行
./test/scripts/run.sh local
./test/scripts/run.sh ec
./test/scripts/run.sh distributed
./test/scripts/run.sh unit

# 单个 Phase 测试（需先启动对应模式的服务器）
go run ./test/ phase1
go run ./test/ phase8

# 单元测试（不需要服务器）
go test ./src/ec/...
go test ./src/hash/...
go test ./src/cluster/...
```

## 架构

```
客户端（CLI / Web UI / curl / SDK）
         │
         ▼
    HTTP Server（net/http）
    ┌─────────────────────────────────────────────────┐
    │  s3Middleware → logMiddleware → CORS → auth      │
    │  AWS Sig V4 + V2 认证 ｜ Presigned URL 验证     │
    ├─────────────────────────────────────────────────┤
    │  Router │ BucketManager │ ObjectManager │       │
    │  MultipartManager │ VersioningManager │ Metrics  │
    ├─────────────────────────────────────────────────┤
    │  StorageBackend（接口）                          │
    │  ┌─ LocalBackend ────────────────────────────┐  │
    │  ├─ ECBackend（Reed-Solomon 纠删码）        ─┤  │
    │  ├─ DistributedBackend（Gossip + Quorum）  ──┤  │
    │  ├─ ECDistributedBackend（EC + 分布式）   ───┤  │
    │  └─ VersionedBackend（版本控制装饰器）     ───┘  │
    └─────────────────────────────────────────────────┘
```

请求流程：`s3Middleware`（Server/Date 头、panic 恢复）→ `logMiddleware`（slog JSON 日志 + 指标计数）→ `CORSMiddleware`（origin 匹配、preflight）→ `authMiddleware`（AWS Sig V4 + V2）→ `ServeMux`（方法+路径路由）→ Handler → `StorageBackend`

## 项目结构

```
cmd/server/       服务器入口（config 加载、slog 初始化、graceful shutdown、Web UI embed）
cmd/client/       CLI 客户端（Sig V4 签名、子命令、进度条）
web/              React + TypeScript + Vite + Tailwind 前端
src/
  auth/           AWS Sig V4 + V2 认证、Presigned URL
  cluster/        Gossip 成员管理 + HTTP RPC + Leader Election
  config/         配置加载
  cors/           CORS 中间件
  ec/             GF(2^8) 有限域 + Reed-Solomon 编解码器
  handler/        HTTP 处理层（Bucket/Object/Multipart/Versioning/Router）
  hash/           Ketama 一致性哈希环
  locks/          Per-bucket 互斥锁
  metrics/        Atomic 计数 + `/_metrics` 端点
  pathmapper/     (bucket, key) → 文件路径映射，3 层遍历防护
  service/        ObjectMeta 原子读写、Content-Type 检测
  s3error/        S3 错误类型 + XML 序列化
  storage/        StorageBackend 接口 + 5 种后端实现 + TTLCleaner + HealthChecker
test/             集成测试（17 个 Phase）
docs/             架构设计、使用指南、技术设计文档（23 篇）、Phase 总结
example/          各后端模式的配置示例和启动脚本
```

## 文档

| 文档 | 说明 |
|------|------|
| [使用指南](docs/usage.md) | CLI 客户端、Web UI、curl API 等所有访问方式 |
| [架构设计](docs/architecture.md) | 系统架构、请求流程、存储后端、磁盘布局 |
| [技术设计文档](docs/technical/) | 23 篇专题：签名认证、纠删码、一致性哈希、Gossip 协议等 |
| [Phase 总结](docs/phases/) | 17 个 Phase 的实现总结 |
| [CLAUDE.md](CLAUDE.md) | 开发指南（构建、测试、编码规范） |

## 技术栈

- **后端**：Go 1.26 + net/http，零外部依赖
- **前端**：React 18 + TypeScript + Vite + Tailwind CSS（仅开发依赖，生产构建嵌入 Go 二进制）
- **测试**：自定义集成测试框架 + Go 单元测试

## License

MIT
