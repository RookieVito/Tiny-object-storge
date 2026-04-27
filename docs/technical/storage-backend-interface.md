<!-- tags: storage, multipart, backend-abstraction -->
# 存储后端接口与策略模式

## 概述

本项目支持三种存储后端：本地文件系统、纠删码（EC）、分布式集群。
三种后端通过统一的 `StorageBackend` 接口实现切换，上层业务代码无需关心底层实现细节。
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

## 3. 三种实现

| 后端 | 文件 | 适用场景 |
|------|------|---------|
| `LocalBackend` | `src/storage/local.go` | 单机开发、小规模存储 |
| `ECBackend` | `src/storage/ec.go` | 多磁盘容错，允许部分磁盘故障 |
| `DistributedBackend` | `src/storage/distributed.go` | 多节点集群，数据分片 + 副本 |

三者关系：

```
StorageBackend (接口)
    │
    ├── LocalBackend          ← 基于本地文件系统
    │     └── 内部使用 PathMapper + service.WriteFile/WriteMeta
    │
    ├── ECBackend             ← 纠删码，组合 LocalBackend
    │     ├── N 个 LocalBackend（每个磁盘一个）
    │     ├── 1 个 LocalBackend（元数据独立存储）
    │     └── ReedSolomon 编解码器
    │
    └── DistributedBackend    ← 分布式，组合 LocalBackend + Gossip + HashRing
          ├── 1 个 LocalBackend（本地存储）
          ├── GossipMembership（成员管理）
          ├── ConsistentHash（数据分片）
          └── Transport（节点间 RPC）
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

## 6. DistributedBackend 的组合技巧

类似地，`DistributedBackend` 组合了 `LocalBackend` + 一致性哈希 + Gossip：

```go
type DistributedBackend struct {
    local      *LocalBackend          // 本地存储（读写本地数据）
    ring       *hash.ConsistentHash   // 数据分片（决定数据存在哪个节点）
    membership *cluster.GossipMembership  // 成员管理（知道哪些节点活着）
}
```

`PutObject` 的逻辑：
1. 通过一致性哈希确定 N 个副本节点
2. 并发向各节点发送 RPC 写入请求
3. 当 W 个节点成功写入后返回

## 7. 开源项目中的相同设计

| 项目 | 接口名称 | 说明 |
|------|---------|------|
| **MinIO** | `StorageAPI` | 统一本地磁盘和纠删码层 |
| **Ceph RADOS** | `ObjectStore` | 抽象 BlueStore、FileStore 等存储引擎 |
| **Go 标准库** | `io.Reader` / `io.Writer` | 最经典的策略接口——文件、网络、内存、压缩都实现同一接口 |
| **etcd** | `Storage` | 抽象 BoltDB、内存存储等后端 |

策略模式的核心思想是：**面向接口编程，而非面向实现编程**。当你看到代码中只依赖接口而不依赖具体类型时，大概率就是策略模式。
