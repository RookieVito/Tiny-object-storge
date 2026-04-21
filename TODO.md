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

## Future Enhancements（Post-MVP）

- [ ] **Multipart Upload** — 支持大文件分片上传（CreateMultipartUpload / UploadPart / CompleteMultipartUpload）
- [ ] **对象版本控制** — 同一 key 保留多个历史版本，通过 version ID 区分
- [ ] **AWS Sig V4 认证** — 替换 Sig V2，支持 region、service、签名密钥派生
- [ ] **Presigned URL** — 允许客户端通过预签名 URL 在有限时间内访问私有对象，无需签名
- [ ] **Range 请求** — `GET` 支持 `Range` 头，实现大文件分块下载和断点续传
- [ ] **CORS 配置** — 支持跨域资源共享，便于浏览器端直传
- [ ] **磁盘健康监控和 Rebalance** — 定期检查磁盘状态，自动重建缺失分片

## 参考文档

- 架构设计：`docs/architecture.md`
- 技术参考：`docs/technical/`
- Phase 1 总结：`docs/phase1-summary.md`
- Phase 2 总结：`docs/phase2-summary.md`
- Phase 3 总结：`docs/phase3-summary.md`
- Phase 4 总结：`docs/phase4-summary.md`
- Phase 5 总结：`docs/phase5-summary.md`
- Phase 6 总结：`docs/phase6-summary.md`
