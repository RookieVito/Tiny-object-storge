<!-- tags: storage, multipart, backend-abstraction -->
# 存储后端接口与策略模式

## 概述

本项目支持四种存储后端：本地文件系统、纠删码（EC）、分布式副本、分布式纠删码。
四种后端通过统一的 `StorageBackend` 接口实现切换，上层业务代码无需关心底层实现细节。
这就是**策略模式（Strategy Pattern）** 的经典应用。

## 1. 为什么需要统一接口

如果每种存储后端都写一套独立的业务逻辑，会导致：

- 大量重复代码（路由、认证、错误处理都一样）
- 添加新后端时需要修改所有业务代码
- 无法在不同后端之间切换测试

统一接口解决了这些问题：**定义一份契约，多种实现自由替换**。

## 2. 接口定义

```go
// src/storage/backend.go
type StorageBackend interface {
    // Bucket 操作
    CreateBucket(bucket string) error
    DeleteBucket(bucket string) error
    BucketExists(bucket string) (bool, error)
    ListBuckets() ([]BucketInfo, error)

    // Object 操作
    PutObject(bucket, key string, data []byte, meta *ObjectMeta) error
    GetObject(bucket, key string) ([]byte, *ObjectMeta, error)
    HeadObject(bucket, key string) (*ObjectMeta, error)
    DeleteObject(bucket, key string) error

    // ListObjectsV2 语义
    ListObjects(bucket, prefix, delimiter, startAfter string, maxKeys int) (
        entries []ObjectEntry, commonPrefixes []string,
        nextToken string, truncated bool, err error,
    )
}
```

每个方法对应一个 S3 操作，handler 层只调用接口方法，不直接操作文件系统。

## 3. 四种实现

| 后端 | 文件 | 适用场景 |
|------|------|---------|
| `LocalBackend` | `src/storage/local.go` | 单机开发、小规模存储 |
| `ECBackend` | `src/storage/ec.go` | 多磁盘容错，允许部分磁盘故障 |
| `DistributedBackend` | `src/storage/distributed.go` | 多节点集群，完整副本复制 + Quorum 一致性 |
| `ECDistributedBackend` | `src/storage/ec_distributed.go` | 多节点集群，RS 编码分片分布（存储开销低于完整副本） |

四者关系：

```
StorageBackend (接口)
    │
    ├── LocalBackend          ← 基于本地文件系统
    │     └── 内部使用 PathMapper + service.WriteFile/WriteMeta
    │
    ├── ECBackend             ← 纠删码，组合多个 LocalBackend
    │     ├── N 个 LocalBackend（每个磁盘一个）
    │     ├── 1 个 LocalBackend（元数据独立存储）
    │     └── ReedSolomon 编解码器
    │
    ├── DistributedBackend    ← 分布式副本，组合 LocalBackend + Gossip + HashRing
    │     ├── 1 个 LocalBackend（本地存储）
    │     ├── GossipMembership（成员管理）
    │     ├── ConsistentHash（数据分片）
    │     └── Transport（节点间 RPC）
    │
    └── ECDistributedBackend   ← 分布式纠删码，组合 LocalBackend + Gossip + HashRing + RS
          ├── 1 个 LocalBackend（本地存储）
          ├── GossipMembership（成员管理）
          ├── ConsistentHash（分片分布）
          ├── Transport（节点间 RPC）
          └── ReedSolomon（RS 编解码器）
```

## 4. 工厂模式创建后端

在 `cmd/server/main.go` 中，根据配置文件动态选择后端：

```go
var backend storage.StorageBackend
switch cfg.BackendType {
case "local":
    backend = storage.NewLocalBackend(absRoot)
case "ec":
    backend, _ = storage.NewECBackend(absDisks, absMetaRoot, k, m)
case "distributed":
    backend, _ = storage.NewDistributedBackend(distCfg, absRoot)
case "ec_distributed":
    backend, _ = storage.NewECDistributedBackend(ecDistCfg, absRoot)
}
```

handler 层接收 `StorageBackend` 接口，完全不知道具体用的是哪种后端：

```go
bm := NewBucketManager(backend, bucketLocks)    // 只依赖接口
om := NewObjectManager(backend, bucketLocks, maxBodySize)
```

## 5. ECBackend 的组合技巧

`ECBackend` 内部组合了多个 `LocalBackend` 实例——每个磁盘对应一个。这种设计避免了重复实现文件系统操作：

```go
type ECBackend struct {
    disks     []*LocalBackend  // N 个磁盘，每个是一个 LocalBackend
    metaStore *LocalBackend    // 元数据独立存储
    rs        *ReedSolomon     // 编解码器
}

func (eb *ECBackend) PutObject(bucket, key string, data []byte, meta *ObjectMeta) error {
    shards, _ := eb.rs.Encode(data)           // 1. 编码
    for i, shard := range shards {
        eb.disks[i].PutObject(bucket, key, shard, ...)  // 2. 委托给 LocalBackend 写入
    }
    eb.writeECMeta(bucket, key, ecMeta)       // 3. 写 EC 元数据
    return nil
}
```

`ECBackend` 不需要自己实现文件写入逻辑——原子写入、路径映射等全部复用 `LocalBackend`。

## 6. DistributedBackend 与 ECDistributedBackend 的对比

| 特性 | `DistributedBackend` | `ECDistributedBackend` |
|------|---------------------|------------------------|
| 数据分布 | 完整副本复制 | RS 编码后分片分布到不同节点 |
| 存储开销 | N 倍 | (K+M)/K 倍 |
| 容错能力 | 丢失 N-W 个节点 | 丢失 M 个节点 |
| 核心组件 | LocalBackend + HashRing + Gossip | LocalBackend + HashRing + Gossip + ReedSolomon |

`DistributedBackend` 的 `PutObject`：
1. 通过一致性哈希确定 N 个副本节点
2. 并发向各节点发送 RPC 写入请求
3. 当 W 个节点成功写入后返回

`ECDistributedBackend` 的 `PutObject`：
1. RS 编码为 K+M 个分片
2. 通过一致性哈希将每个分片发送到不同节点
3. 存储 ECDistMeta（记录分片到节点的映射）到 R 个 meta 副本节点
4. 至少 K 个分片写入成功即可

## 7. 开源项目中的相同设计

| 项目 | 接口名称 | 说明 |
|------|---------|------|
| **MinIO** | `StorageAPI` | 统一本地磁盘和纠删码层 |
| **Ceph RADOS** | `ObjectStore` | 抽象 BlueStore、FileStore 等存储引擎 |
| **Go 标准库** | `io.Reader` / `io.Writer` | 最经典的策略接口——文件、网络、内存、压缩都实现同一接口 |
| **etcd** | `Storage` | 抽象 BoltDB、内存存储等后端 |

策略模式的核心思想是：**面向接口编程，而非面向实现编程**。当你看到代码中只依赖接口而不依赖具体类型时，大概率就是策略模式。

## 对应实现

| 文件 | 说明 |
|------|------|
| `src/storage/backend.go` | `StorageBackend` + `MultipartStorage` 接口定义 |
| `src/storage/local.go` | LocalBackend（本地文件系统） |
| `src/storage/ec.go` | ECBackend（纠删码） |
| `src/storage/distributed.go` | DistributedBackend（分布式 Quorum） |
| `src/storage/ec_distributed.go` | ECDistributedBackend（分布式纠删码） |
| `src/storage/multipart.go` | LocalBackend 的 MultipartStorage 实现 |

**关键类型：** `StorageBackend`、`MultipartStorage`、`LocalBackend`、`ECBackend`、`DistributedBackend`、`BucketInfo`、`ObjectEntry`
