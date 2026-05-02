<!-- tags: phase-summary -->
# Phase 17 完成总结

## 1. 完成状态：全部完成

Phase 17 新增 1 个文件（ec_distributed.go，1269 行），新增 test/phase17.go（322 行），修改 4 个文件（protocol.go、transport.go、main.go、run.sh），新增 28 个集成测试断言全部通过，Phase 1-16 全量回归无新增失败。

---

## 2. Phase 17 实现内容

### 2.1 ECDistributedBackend（src/storage/ec_distributed.go）

融合 Reed-Solomon 纠删码与分布式存储的新后端，实现 `StorageBackend` + `MultipartStorage` 接口。核心思路：**RS 编码产生 K+M 个分片，每个分片存储在不同节点上，实现非完整复制的分布式存储**。

与 DistributedBackend（完整复制 N 份）和 ECBackend（单机多磁盘）的关键区别：

| 维度 | ECBackend | DistributedBackend | ECDistributedBackend |
|------|-----------|--------------------|---------------------|
| 数据分布 | 单机多磁盘 | 多节点完整复制 | 多节点分片存储 |
| 存储开销 | (K+M)/K | N 倍 | (K+M)/K |
| 故障容忍 | M 个磁盘 | N-R 个节点 | M 个节点 |
| 网络开销 | 无 | 完整数据传输 | 分片级传输 |

### 2.2 核心数据结构

**ECDistMeta** — 分布式 EC 对象元数据，记录分片到节点的映射：

```go
type ECDistMeta struct {
    Key          string            `json:"key"`
    OriginalSize int64             `json:"original_size"`
    ShardSize    int               `json:"shard_size"`
    DataShards   int               `json:"data_shards"`
    ParityShards int               `json:"parity_shards"`
    ETag         string            `json:"etag"`
    ContentType  string            `json:"content_type"`
    LastModified time.Time         `json:"last_modified"`
    UserMetadata map[string]string `json:"user_metadata,omitempty"`
    ShardNodes   []string          `json:"shard_nodes"` // shard[i] 存储在 ShardNodes[i]
}
```

**ShardMeta** — 存储在每个分片节点上的分片元数据：

```go
type ShardMeta struct {
    ShardIndex  int    `json:"shard_index"`
    ShardSize   int    `json:"shard_size"`
    TotalShards int    `json:"total_shards"`
    ObjectKey   string `json:"object_key"`
    Bucket      string `json:"bucket"`
    ETag        string `json:"etag"`
}
```

### 2.3 操作流程

**PutObject：**
1. RS.Encode(data) → K+M 个分片
2. `shardNodes(key)` 通过一致性哈希选取 K+M 个不同节点
3. 并发 RPC `ec_put_shard` 将每个分片发送到对应节点
4. 验证至少 K 个分片写入成功
5. 构造 ECDistMeta（包含 ShardNodes 映射）
6. 并发 RPC `ec_put_meta` 将 ECDistMeta 复制到 R 个节点

**GetObject：**
1. `resolveECMeta(bucket, key)` 两阶段查找：先从 meta 副本节点查找，失败后从所有 alive 节点回退
2. 从 ECDistMeta 读取 ShardNodes
3. 并发 RPC `ec_get_shard` 从各分片节点读取
4. 可用分片 >= K 时 RS.Decode 降级读
5. 截断到 OriginalSize 返回

**DeleteObject：**
1. `resolveECMeta` 读取 ECDistMeta
2. 并发 RPC `ec_delete_shard` 删除所有分片
3. 遍历所有 alive 节点 RPC `ec_delete_meta` 清理元数据
4. 删除本地 ec-meta

**Bucket 操作：**
- CreateBucket/DeleteBucket：RPC 广播到所有 alive 节点
- ListBuckets：合并所有 alive 节点的 bucket 列表去重

**ListObjects：**
- 扫描本地 `.ec-shard-meta/` 目录
- 仅保留 shardIndex=0 的条目（避免重复计数）
- 应用 prefix/delimiter/pagination 过滤

### 2.4 集群 RPC 扩展

新增 7 个 EC 分布式操作，通过 `/_cluster/replicate` 端点分发：

| Operation | 描述 |
|-----------|------|
| `ec_put_shard` | 存储分片数据 + ShardMeta 到本地 |
| `ec_get_shard` | 读取本地分片数据 |
| `ec_delete_shard` | 删除本地分片数据和元数据 |
| `ec_put_meta` | 存储 ECDistMeta 到本地 `.ec-meta/` |
| `ec_get_meta` | 读取本地 ECDistMeta |
| `ec_delete_meta` | 删除本地 ECDistMeta |
| `ec_list_shards` | 列出本地所有 EC 分片元数据 |

**StorageRequest 新增字段**（src/cluster/protocol.go）：

```go
ShardIndex  int `json:"shard_index,omitempty"`
ShardSize   int `json:"shard_size,omitempty"`
TotalShards int `json:"total_shards,omitempty"`
```

### 2.5 Transport 响应限制提升

`cluster/transport.go` 中 `io.LimitReader` 的上限从 1MB 提升到 **64MB**（`64<<20`），支持大分片传输。这在 EC 场景中至关重要——单个分片可能达到数 MB。

### 2.6 磁盘布局

```
# 每个节点本地存储
{root}/{bucket}/.ec-shards/{key}#0          # 分片 0 数据
{root}/{bucket}/.ec-shards/{key}#1          # 分片 1 数据
...
{root}/{bucket}/.ec-shard-meta/{key}#0      # 分片 0 元数据 (ShardMeta JSON)
{root}/{bucket}/.ec-shard-meta/{key}#1      # 分片 1 元数据
...
{root}/{bucket}/.ec-meta/{key}              # ECDistMeta JSON（副本存储）
```

分片数据路径 `key#N` 中的 N 为 shard index，确保同一对象的不同分片存储在不同路径下。元数据与数据分离存储，支持独立查询。

### 2.7 MultipartStorage

ECDistributedBackend 实现 `MultipartStorage` 接口，采用 Coordinator 模式：

- **InitiateUpload / UploadPart / ListParts / ListUploads / GetUploadInfo**：委托给本地 LocalBackend
- **CompleteUpload**：从本地 assemble 所有 parts → RS.Encode → 走 EC 分布式 PutObject 流程写入 → 清理本地 upload 数据
- **AbortUpload**：委托给本地 LocalBackend

### 2.8 配置

```json
{
  "port": 9101,
  "root": "./data",
  "backend_type": "ec_distributed",
  "ec": {
    "data_shards": 4,
    "parity_shards": 2
  },
  "distributed": {
    "node_id": "localhost:9101",
    "seed_nodes": ["localhost:9102", "localhost:9103"],
    "replication_factor": 2,
    "read_quorum": 1,
    "write_quorum": 1,
    "virtual_nodes": 500,
    "gossip_interval_ms": 200,
    "rpc_timeout_ms": 1000
  }
}
```

EC 和 Distributed 配置合并使用，`backend_type` 设为 `"ec_distributed"`。

---

## 3. 依赖关系

```
storage/ec_distributed.go  ← 新增文件，依赖 ec、hash、cluster、service、s3error
cluster/protocol.go        ← 新增 ShardIndex、ShardSize、TotalShards 字段
cluster/transport.go       ← 响应限制从 1MB → 64MB
cmd/server/main.go         ← 新增 ec_distributed 后端工厂分支
```

依赖图保持无环。

---

## 4. 测试覆盖

**Phase 17 EC 分布式集成测试（test/phase17.go）：28 个断言**

测试环境：6 节点集群（端口 19301-19306），4+2 EC 配置，2x meta 复制。

- 编译服务器 + 6 节点自动启动 + Gossip 收敛（6/6 alive）
- CreateBucket → 200
- PutObject → 200
- GetObject（node1）→ 200，内容匹配
- HeadObject → 200，Content-Length 正确
- GetObject（node3/node6）→ 200，跨节点读取正确
- ListBuckets → 200，包含目标 bucket
- ListObjects → 200，包含目标 key
- 节点故障容忍：停止 node5 + node6 → PutObject → 200
- DeleteObject → 204，验证 404
- Multipart Upload：Initiate → UploadPart（5MB×2）→ Complete → GetObject → 200，内容匹配
- AbortMultipartUpload → 204，GetObject → 404

---

## 5. 设计决策

1. **ShardNodes 映射写入 ECDistMeta**：分片分布信息随元数据持久化，读取时精确定位分片节点，无需重算一致性哈希。即使哈希环因节点变更漂移，仍能通过旧映射找到分片。
2. **两阶段 Meta 解析**：`resolveECMeta` 先查 meta 副本节点，失败后回退到所有 alive 节点。提高元数据可用性，避免因 meta 副本节点故障导致数据不可读。
3. **shardIndex=0 过滤**：ListObjects 仅展示 shardIndex=0 的元数据条目，一个对象只列出一次。
4. **Transport 64MB 限制**：EC 分片可能数 MB，原有 1MB 限制会导致大对象上传失败。
5. **Multipart Coordinator 模式**：Parts 存储在 coordinator 本地，Complete 时走完整的 EC 编码+分布式写入流程。与 DistributedBackend 的 Coordinator 模式一致，复用现有代码路径。
6. **灵活节点选择**：`shardNodes()` 在可用节点不足 K+M 时自动扩展候选集，尽力满足写入需求。
