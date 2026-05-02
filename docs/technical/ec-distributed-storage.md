<!-- tags: distributed, erasure-coding, rs-encoding, shard-distribution -->
# 分布式纠删码存储（EC Distributed Backend）

## 概述

分布式纠删码存储（`ECDistributedBackend`）将 RS 编码与节点分布结合：先将对象编码为 K 数据分片 + M 冗余分片，再通过一致性哈希将每个分片发送到不同节点。相比 `DistributedBackend` 的完整副本复制，EC 分布式大幅降低存储开销，代价是读写延迟略高。

## 1. 整体架构

```
                    Client
                      │
          ┌───────────┼───────────┐
          ▼           ▼           ▼
        Node1       Node2       Node3       ...  (K+M 个节点)
          │           │           │
          └─────── Gossip ────────┘

PutObject("mybucket/large.bin", data):
  1. RS 编码 → [D0, D1, D2, D3, P0, P1]（K=4, M=2）
  2. 一致性哈希选定 K+M=6 个节点
  3. 并发 RPC：D0→Node1, D1→Node2, D2→Node3, D3→Node4, P0→Node5, P1→Node6
  4. 存储 ECDistMeta（分片→节点映射）到 R=2 个 meta 副本节点
  5. 至少 K 个分片写入成功 → 返回

GetObject("mybucket/large.bin"):
  1. 读取 ECDistMeta → 获取 ShardNodes 映射
  2. 并发 RPC 读取所有分片
  3. 收集 K 个以上分片 → RS 解码恢复原始数据
```

## 2. 核心数据结构

```go
type ECDistributedBackend struct {
    config     *ECDistributedConfig
    local      *LocalBackend          // 本地文件存储
    ring       *hash.ConsistentHash   // 一致性哈希环
    membership *cluster.GossipMembership
    transport  *cluster.Transport     // 节点间 HTTP RPC
    rs         *ec.ReedSolomon        // RS 编解码器
    mu         sync.RWMutex
    seq        atomic.Int64           // 请求 ID 生成
}

type ECDistMeta struct {
    Key          string            // 原始对象 key
    OriginalSize int64             // 原始文件大小
    ShardSize    int               // 每个分片大小
    DataShards   int               // K
    ParityShards int               // M
    ETag         string
    ContentType  string
    LastModified time.Time
    UserMetadata map[string]string
    ShardNodes   []string          // ShardNodes[i] = shard[i] 所在节点 ID
}

type ShardMeta struct {
    ShardIndex  int    // 分片序号
    ShardSize   int    // 分片大小
    TotalShards int    // K+M
    ObjectKey   string // 原始对象 key
    Bucket      string
    ETag        string
}
```

**关键区别**：`ECDistMeta` 记录了 `ShardNodes` 映射，这是读取时定位分片的基础。即使集群拓扑变化，读取仍依赖写入时记录的映射关系。

## 3. 分片节点选择

```go
func (db *ECDistributedBackend) shardNodes(key string) []string {
    return db.selectNodes(key, db.config.DataShards+db.config.ParityShards)
}

func (db *ECDistributedBackend) selectNodes(key string, n int) []string {
    aliveNodes := db.membership.AliveNodes()
    aliveSet := make(map[string]bool)
    for _, node := range aliveNodes {
        aliveSet[string(node.ID)] = true
    }
    // 逐步扩大候选集，直到凑齐 n 个 alive 节点
    for attempt := n; attempt <= n*3; attempt += n {
        candidates := db.ring.GetNodes(key, attempt)
        // 过滤：只取 alive 且未重复的节点
        ...
    }
}
```

**设计要点**：
- 分片节点数 = K + M（需要 6 个 alive 节点才能完整写入 K=4, M=2）
- meta 副本节点数 = `ReplicationFactor`（默认 2）
- 候选集不足时自动扩大 `n*3` 倍重试

## 4. 写入流程

```
┌─────────────┐
│  原始数据     │
└──────┬──────┘
       │ RS Encode
       ▼
┌──┬──┬──┬──┬──┬──┐
│D0│D1│D2│D3│P0│P1│  ← K=4 个数据分片 + M=2 个冗余分片
└┬─┴┬─┴┬─┴┬─┴┬─┴┬─┘
 │  │  │  │  │  │
 ▼  ▼  ▼  ▼  ▼  ▼
N1 N2 N3 N4 N5 N6  ← 并发 RPC 到各节点

然后存储 ECDistMeta → R=2 个 meta 副本节点
```

```go
func (db *ECDistributedBackend) PutObject(bucket, key string, data []byte, meta *ObjectMeta) error {
    // 1. RS 编码
    shards, shardSize := db.rs.Encode(data)

    // 2. 并发发送分片
    for i := 0; i < totalShards; i++ {
        go func(shardIdx, nodeID) {
            // 本地：putShardLocal（.ec-shards/key#N + .ec-shard-meta/key#N）
            // 远程：RPC ec_put_shard
        }(i, nodes[i])
    }

    // 3. 等待至少 K 个分片成功
    if storedCount < DataShards { return error }

    // 4. 构建 ECDistMeta（包含 ShardNodes 映射）
    ecMeta := &ECDistMeta{
        ShardNodes: shardNodes,  // 按序记录每个分片的节点
        ...
    }

    // 5. 复制 ECDistMeta 到 R 个 meta 副本节点
    // 至少 1 个成功即可
}
```

## 5. 读取流程

```go
func (db *ECDistributedBackend) GetObject(bucket, key string) ([]byte, *ObjectMeta, error) {
    // 1. 读取 ECDistMeta（先查 meta 副本节点，再回退所有 alive 节点）
    ecMeta := db.resolveECMeta(bucket, key)

    // 2. 从 ShardNodes 映射并发读取分片
    for i, nodeID := range ecMeta.ShardNodes {
        go func(shardIdx, nodeID) {
            // 本地：readShardLocal（.ec-shards/key#N）
            // 远程：RPC ec_get_shard
        }(i, nodeID)
    }

    // 3. 收集分片，至少 K 个即可解码
    if fetchedCount < ecMeta.DataShards { return error }

    // 4. RS 解码 + 截断到原始大小
    db.rs.Decode(shards, ecMeta.ShardSize)
    buf = buf[:ecMeta.OriginalSize]
}
```

**退化读取（Degraded Read）**：即使部分分片节点故障，只要有 K 个分片可用，即可通过 RS 解码恢复完整数据。这是 EC 分布式相比副本模式的核心优势——存储开销更低且容错能力灵活。

## 6. ECDistMeta 解析策略

```go
func (db *ECDistributedBackend) resolveECMeta(bucket, key string) *ECDistMeta {
    // 优先：从 meta 副本节点查找（一致性哈希选定）
    metaNodes := db.metaReplicaNodes(bucket + "/" + key)
    for _, nodeID := range metaNodes {
        m := db.readECMetaFromNode(bucket, key, nodeID)
        if m != nil { return m }
    }
    // 回退：从所有 alive 节点遍历查找
    for _, node := range db.membership.AliveNodes() {
        m := db.readECMetaFromNode(bucket, key, node.ID)
        if m != nil { return m }
    }
    return nil
}
```

**两阶段查找**：先从写入时选定的 meta 副本节点查找（命中率最高），失败后从所有节点回退（应对 meta 副本节点故障）。

## 7. 磁盘布局

```
{root}/{bucket}/
├── .ec-meta/{key}              ← ECDistMeta JSON（分片分布映射）
├── .ec-shards/{key}#0          ← shard 0 数据
├── .ec-shards/{key}#1          ← shard 1 数据
├── .ec-shards/{key}#2          ← shard 2 数据
├── ...
├── .ec-shards/{key}#5          ← shard 5 数据
├── .ec-shard-meta/{key}#0      ← shard 0 元数据
├── .ec-shard-meta/{key}#1      ← shard 1 元数据
└── ...
```

| 目录 | 作用 |
|------|------|
| `.ec-meta/` | ECDistMeta JSON 文件，记录分片→节点映射、原始大小、ETag 等核心元数据 |
| `.ec-shards/` | 分片原始数据，`key#N` 格式 |
| `.ec-shard-meta/` | 分片元数据（ShardMeta JSON），记录分片序号、大小等 |

## 8. RPC 操作

节点间通过 `/_cluster/replicate` 端点通信，EC 分布式扩展了以下操作：

| Operation | 说明 |
|-----------|------|
| `ec_put_shard` | 写入分片数据 |
| `ec_get_shard` | 读取分片数据 |
| `ec_delete_shard` | 删除分片数据 |
| `ec_put_meta` | 写入 ECDistMeta |
| `ec_get_meta` | 读取 ECDistMeta |
| `ec_delete_meta` | 删除 ECDistMeta |
| `ec_list_shards` | 列举节点上所有 EC 对象 |

所有二进制数据通过 base64 编码传输。

## 9. ListObjects 实现细节

```go
func (db *ECDistributedBackend) listShardMetaEntries(bucket, prefix, startAfter string) []ObjectEntry {
    // 从本地 .ec-meta/ 目录列举所有 ECDistMeta 文件
    entries := db.local.ListObjects(bucket, ".ec-meta/", "", "", 100000)

    for _, e := range entries {
        objectKey := strings.TrimPrefix(e.Key, ".ec-meta/")
        // 解析 ECDistMeta JSON 获取 OriginalSize、LastModified、ETag
        // （因为 .ec-meta/ 下的 .meta 文件大小是 JSON 大小，不是原始文件大小）
        data := db.local.GetObject(bucket, ".ec-meta/"+objectKey)
        json.Unmarshal(data, &ecMeta)
        entry.Size = ecMeta.OriginalSize
    }
}
```

**为什么从 `.ec-meta/` 而不是 `.ec-shard-meta/` 列举**：`.ec-shard-meta/` 只包含分片元数据（分片大小），无法获取原始文件大小和完整元信息。`.ec-meta/` 中的 `ECDistMeta` JSON 包含 `OriginalSize`、`LastModified`、`ETag` 等精确信息。

## 10. Multipart Upload 集成

EC 分布式的 Multipart Upload 采用**本地暂存 + 最终 EC 编码**策略：

```
Part 1 ──→ 本地 .uploads/{uploadId}/part-0001.bin
Part 2 ──→ 本地 .uploads/{uploadId}/part-0002.bin
Part 3 ──→ 本地 .uploads/{uploadId}/part-0003.bin
                │
                │ CompleteUpload
                ▼
          拼接所有 parts → RS 编码 → PutObject（EC 分片流程）
          清理本地临时 parts
```

ETag 使用 S3 multipart 标准：`MD5(concat of per-part MD5s)-N`。

## 对应实现

| 文件 | 说明 |
|------|------|
| `src/storage/ec_distributed.go` | ECDistributedBackend 完整实现 |
| `src/ec/reed_solomon.go` | Reed-Solomon 编解码器 |
| `src/cluster/transport.go` | 节点间 HTTP RPC 通信 |
| `src/cluster/member.go` | Gossip 成员管理 |
| `src/hash/consistent_hash.go` | 一致性哈希环 |
