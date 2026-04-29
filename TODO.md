# TODO

## Phase 1: Core MVP ✅
## Phase 2: S3 Compatibility ✅

## Phase 3: Robustness ✅

- [x] **请求体大小限制** — `http.MaxBytesReader` 防止大文件耗尽内存，限制单次 PutObject 上传大小（默认 10MB，可通过 config.json 配置）
- [x] **并发安全** — `BucketLocks` 提供 per-bucket 互斥锁，保护 CreateBucket/DeleteBucket/PutObject/DeleteObject
- [x] **日志中间件** — 结构化 JSON 请求日志（method、path、status、latency），接入 `log/slog`
- [x] **Metrics 端点** — `GET /_metrics` 返回 JSON 格式的运行指标（请求数、错误数、存储用量、bucket 数量）

## Phase 4: Storage Backend Abstraction ✅

- [x] **StorageBackend 接口** — 统一的存储后端接口，定义 Bucket/Object CRUD + ListObjects
- [x] **LocalBackend** — 本地文件系统实现，复用 PathMapper + service 原子写入
- [x] **Handler 层重构** — ObjectManager/BucketManager 通过 StorageBackend 接口操作存储，不再直接依赖 PathMapper/service/os
- [x] **Backend 工厂** — `cmd/server/main.go` 根据 `backend_type` 配置选择后端

## Phase 5: Erasure Coding ✅

- [x] **GF(2^8) 有限域** — 基于不可约多项式 0x11D，exp/log 查找表实现 Mul/Div/Inv/Add
- [x] **Cauchy Reed-Solomon 编解码器** — Cauchy 矩阵保证任意 K×K 子矩阵可逆，增广矩阵高斯消元解码
- [x] **ECBackend** — K 数据分片 + M 校验分片，分布在 N 个磁盘，降级读支持
- [x] **ECObjectMeta** — 独立 metaStore 存储 EC 对象元数据
- [x] **ECConfig** — disks/data_shards/parity_shards/meta_root 配置

## Phase 6: Distributed Storage ✅

- [x] **一致性哈希** — Ketama 风格，双哈希 FNV-1a + 500 虚拟节点，标准差 < 10%
- [x] **Gossip 成员管理** — SWIM 简化版，Ping → PingReq → Suspect → Dead，Incarnation 反驳
- [x] **Leader Election** — 确定性选举，存活节点中 NodeID 字典序最小者
- [x] **HTTP RPC 通信** — 集群内部端点 `/_cluster/*`，复用 S3 端口，`http.StripPrefix` 路由
- [x] **DistributedBackend** — Quorum R/W（N=3, W=2, R=2），coordinator 模式，节点故障容忍
- [x] **配置支持** — DistributedConfig（node_id、seed_nodes、replication_factor 等）

## Phase 7: Client Tools ✅

- [x] **CLI 客户端**（cmd/client/）— ls/cp/mb/rb/rm/cat/stat 子命令，Sig V2 签名，上传/下载进度条
- [x] **Web UI**（web/）— React 18 + TypeScript SPA，浏览器端 Web Crypto API Sig V2 签名
- [x] Bucket/Object 管理、前缀导航、文件拖拽上传（带进度条）、下载、删除
- [x] embed.FS 嵌入到 Go 二进制，`/_ui/` 路径自动服务
- [x] 19 个 CLI 客户端集成测试

## Phase 8: Multipart Upload ✅

- [x] **MultipartStorage 接口**（storage/backend.go）— 独立于 StorageBackend 的可选扩展接口
- [x] **LocalBackend 实现**（storage/multipart.go）— 7 个方法完整实现
- [x] **6 个 S3 multipart 端点** — Initiate、UploadPart、Complete、Abort、ListParts、ListUploads
- [x] **路由分发**（handler/router.go）— 通过 query param 复用现有路由
- [x] **MultipartManager**（handler/multipart.go）— type assertion 检测后端支持
- [x] ETag 标准算法（单 part MD5 + 最终对象 `MD5(concat)-N`）
- [x] 并发安全（UploadPart 无锁可并行，Complete/Abort 加 bucket 锁）
- [x] `.uploads/` 目录隔离（ListObjects/Metrics 自动跳过）
- [x] EC/Distributed 后端 stub（返回 ErrNotImplemented）
- [x] 32 个集成测试

## Phase 9: Range 请求 ✅

- [x] **ParseRange 解析** — 支持 `bytes=0-99`、`bytes=100-`、`bytes=-50` 格式
- [x] **GetObject 206** — 解析 Range 头，返回 Partial Content + Content-Range
- [x] **HeadObject 206** — 同样支持 Range（仅头信息，无 body）
- [x] **416 错误** — Range 无法满足时返回 Range Not Satisfiable
- [x] **Accept-Ranges 头** — 所有 Get/Head 响应声明 `bytes` 支持
- [x] 集成测试 `test/phase9.go`（60 个）

## Phase 10: AWS Sig V4 认证 ✅

- [x] **Sig V4 签名**（src/auth/v4.go）— HMAC-SHA256 密钥派生、Canonical Request、String to Sign
- [x] **V4/V2 双认证** — Authenticate() 按前缀分发，V2 作为 fallback
- [x] **时间偏移检查** — `|X-Amz-Date - server time| > 15min` → 403
- [x] **Config Region** — 新增 Region 字段（默认 `us-east-1`）
- [x] **客户端升级** — CLI signer + Web UI s3Client 升级为 V4
- [x] 集成测试 `test/phase10.go`（21 个，含额外 V4 测试）

## Phase 11: Presigned URL ✅

- [x] **Presign 生成**（src/auth/presign.go）— GET/PUT 预签名 URL 生成
- [x] **Presign 验证** — Authenticate() 检测 query params 中的签名参数
- [x] **过期检查** — 当前时间 > X-Amz-Date + X-Amz-Expires → 403，最大 7 天
- [x] **CLI presign 子命令** — 命令行生成预签名 URL
- [x] 集成测试 `test/phase11.go`（20 个）
- [x] **[Phase 10 遗留] 修复 object.go 重复 Content-Range**
- [x] **[Phase 10 遗留] Web UI signV4 直接接受 accessKey 参数**
- [x] **[Phase 10 遗留] buildCanonicalHeaders 移除冗余排序**
- [x] **[Phase 10 遗留] 补充 V4 查询字符串、content-type 篾改、Range+V4 测试**

## Phase 12: CORS 配置 ✅

- [x] **CORSConfig**（config.go）— Enabled、AllowedOrigins/Methods/Headers、ExposeHeaders、MaxAge、AllowCredentials
- [x] **CORSMiddleware**（cors/cors.go）— origin 匹配（精确 + 通配符 `*`）、preflight OPTIONS 返回 204
- [x] **中间件链集成** — s3Middleware → logMiddleware → CORSMiddleware → topMux
- [x] 默认启用（`Enabled: true`，`AllowedOrigins: ["*"]`）
- [x] 12 个集成测试 `test/phase12.go`

## Phase 13: EC/Distributed Multipart Upload ✅

- [x] **EC multipart**（src/storage/ec.go）— per-part EC 编解码、元数据管理
- [x] **Distributed multipart**（src/storage/distributed.go）— coordinator 模式、UploadPart 本地存储、CompleteUpload quorum 写入
- [x] **Cluster RPC 扩展**（src/cluster/protocol.go）— `PartInfoMsg`、`UploadId`/`PartNumber`/`Parts` 字段
- [x] 替换 EC/Distributed 后端的 ErrNotImplemented stub
- [x] 集成测试 `test/phase13.go`（EC + 分布式全流程）

## Phase 14: TTL 自动清理 ✅

- [x] **TTLCleaner**（src/storage/cleanup.go）— 后台 goroutine 定期扫描过期 upload
- [x] **Config** — MultipartTTLSeconds（默认 86400）、CleanupIntervalSec（默认 3600）
- [x] **Metrics** — 新增 MultipartCleanups 计数器
- [x] 集成测试 `test/phase14.go`

## Phase 15: 对象版本控制 🔜

- [ ] **VersionedStorage 接口** — SetBucketVersioning/GetBucketVersioning/PutObjectVersion/GetObjectVersion 等
- [ ] **VersionedBackend 装饰器**（src/storage/versioning.go）— 包装任意 StorageBackend
- [ ] **ObjectMeta 扩展** — 新增 VersionId、IsDeleteMarker、IsLatest 字段
- [ ] **Handler 支持** — Get/Put/Delete/Head 支持 `?versionId=X`
- [ ] **版本路由** — `?versioning`、`?versions` 路由分发
- [ ] 集成测试 `test/phase15.go`

## Phase 16: 磁盘健康监控和 Rebalance 🔜

- [ ] **DiskHealthChecker**（src/storage/health.go）— 后台 os.Stat 磁盘检查
- [ ] **Rebalancer** — 磁盘状态变更回调，扫描并重建缺失分片
- [ ] **ECConfig 扩展** — HealthCheckIntervalSec、HealthCheckMode
- [ ] **Metrics** — DiskHealth 状态、RebalancedObjects 计数
- [ ] 集成测试 `test/phase16.go`

## Phase 17: 分布式纠删码存储（EC over Distributed Nodes） ✅

- [x] **ECDistributedBackend**（src/storage/ec_distributed.go）— 融合 EC 编码和分布式存储的新后端
- [x] **ECDistMeta** — 记录分片到节点的映射（`ShardNodes`），支持故障后精确定位分片
- [x] **PutObject** — RS.Encode → 一致性哈希选 K+M 节点 → 并发 RPC ec_put_shard → quorum 复制 ECDistMeta
- [x] **GetObject** — 从 ECDistMeta 读取 ShardNodes → 并发 RPC ec_get_shard → RS.Decode 降级读
- [x] **DeleteObject** — 从 ECDistMeta 读取分片节点 → 并发 RPC ec_delete_shard + ec_delete_meta
- [x] **Bucket 操作** — CreateBucket/DeleteBucket RPC 广播，ListBuckets 合并去重
- [x] **ListObjects** — 本地分片元数据过滤（仅 shard_index=0），合并去重
- [x] **MultipartStorage** — coordinator 模式，UploadPart 本地存储，CompleteUpload 走 EC 分片流程
- [x] **Cluster RPC** — 新增 ec_put_shard/ec_get_shard/ec_delete_shard/ec_put_meta/ec_get_meta/ec_delete_meta/ec_list_shards 操作
- [x] **StorageRequest 扩展** — 新增 ShardIndex/ShardSize/TotalShards 字段
- [x] **Transport 响应限制** — 从 1MB 提升到 64MB，支持大分片传输
- [x] **配置** — `backend_type: "ec_distributed"`，合并 EC 和 Distributed 配置
- [x] 集成测试 `test/phase17.go`（6 节点 4+2 配置，节点故障后读写，Multipart）

### 依赖关系

```
Phase  9 (Range)              → 无依赖
Phase 10 (Sig V4)             → 无依赖
Phase 11 (Presigned URL)      → 依赖 Phase 10
Phase 12 (CORS)               → 无依赖
Phase 13 (EC/Dist Multipart)  → 依赖 Phase 5, 6, 8
Phase 14 (TTL 清理)           → 依赖 Phase 8
Phase 15 (版本控制)           → 建议在 9/13 之后
Phase 16 (磁盘健康)           → 依赖 Phase 5
Phase 17 (EC 分布式)          → 依赖 Phase 5, 6
```

## 参考文档

- 架构设计：`docs/architecture.md`
- 使用指南：`docs/usage.md`
- 技术参考：`docs/technical/`
- Phase 1 总结：`docs/phase1-summary.md`
- Phase 2 总结：`docs/phase2-summary.md`
- Phase 3 总结：`docs/phase3-summary.md`
- Phase 4 总结：`docs/phase4-summary.md`
- Phase 5 总结：`docs/phase5-summary.md`
- Phase 6 总结：`docs/phase6-summary.md`
- Phase 7 总结：`docs/phase7-summary.md`
- Phase 8 总结：`docs/phase8-summary.md`
- Phase 9 总结：`docs/phase9-summary.md`
- Phase 10 总结：`docs/phase10-summary.md`
- Phase 11 总结：`docs/phase11-summary.md`
- Phase 12 总结：`docs/phase12-summary.md`
- 文档索引：`docs/index.md`
