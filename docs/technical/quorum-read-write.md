# Quorum 读写与分布式一致性

## 概述

在分布式存储中，数据有多个副本。写入时写几个副本？读取时读几个副本？
**Quorum（仲裁）** 是解决这些问题的经典策略。

## 1. 核心思想

Quorum 规则定义三个参数：
- **N（Replication Factor）**：每个数据的总副本数
- **W（Write Quorum）**：写入成功必须达到的副本数
- **R（Read Quorum）**：读取成功必须达到的副本数

**一致性保证**：当 `W + R > N` 时，读取操作一定能看到最新的写入。

### 为什么？

```
N = 3, W = 2, R = 2

写入：
  副本 A ✓  ← 写入成功
  副本 B ✓  ← 写入成功
  副本 C ✗  ← 写入失败（但 W=2 已满足）

读取：
  必须从 2 个副本读取（R=2）
  由于 W=2 和 R=2，两个集合必有交集 → 至少 1 个副本同时被写入和读取
  → 读取一定能看到最新数据
```

用集合论解释：写入副本集合有 W 个元素，读取副本集合有 R 个元素。`W + R > N` 意味着两个集合必定有交集。

## 2. 本项目的实现

### 配置

```go
// src/config/config.go
type DistributedConfig struct {
    ReplicationFactor int  // N = 3
    ReadQuorum        int  // R = 2
    WriteQuorum       int  // W = 2
}
```

默认配置 N=3, W=2, R=2。启动时校验：

```go
if cfg.ReadQuorum + cfg.WriteQuorum <= cfg.ReplicationFactor {
    return nil, fmt.Errorf("R(%d) + W(%d) must be > N(%d)", ...)
}
```

### 写入流程

```go
func (db *DistributedBackend) PutObject(bucket, key string, data []byte, meta *ObjectMeta) error {
    reps := db.replicas(bucket + "/" + key)  // 一致性哈希确定 N 个副本节点

    var successes int32
    var wg sync.WaitGroup

    for _, nodeID := range reps {
        wg.Add(1)
        go func(node string) {
            defer wg.Done()
            // 并发向每个副本节点发送写入请求
            if db.isSelf(node) {
                db.local.PutObject(bucket, key, data, meta)  // 本地写入
            } else {
                db.transport.Replicate(node, req)  // 远程 RPC 写入
            }
            atomic.AddInt32(&successes, 1)
        }(nodeID)
    }
    wg.Wait()

    if int(successes) >= db.config.WriteQuorum {
        return nil  // W 个副本写入成功
    }
    return ErrWriteQuorumFailed
}
```

### 读取流程

```go
func (db *DistributedBackend) GetObject(bucket, key string) ([]byte, *ObjectMeta, error) {
    reps := db.replicas(bucket + "/" + key)

    // 并发从所有副本节点读取
    for _, nodeID := range reps {
        go func(node string) {
            // ... 读取数据
            if err == nil {
                atomic.AddInt32(&successes, 1)
            }
        }(nodeID)
    }

    // 等待第一个成功的结果
    // 当成功数 >= R 时返回（保证读到最新数据）
    for {
        select {
        case result := <-resultCh:
            if result.err == nil && successes >= R {
                return result.data, result.meta, nil  // 返回第一个成功且满足 Quorum 的结果
            }
        case <-done:
            // 所有副本都读完了但 Quorum 不满足
            return nil, nil, ErrReadQuorumFailed
        }
    }
}
```

## 3. Quorum 配置的权衡

| 配置 | 一致性 | 可用性 | 说明 |
|------|--------|--------|------|
| W=N, R=1 | 最强 | 较低 | 每次写入都写所有副本，但任意副本可读 |
| W=1, R=N | 最弱（读取时） | 最高 | 写入只需 1 个副本，但读取需要所有副本 |
| W=N/2+1, R=N/2+1 | 平衡 | 平衡 | **本项目默认配置** |
| W=R=N | 最强 | 最低 | 写和读都需要所有副本，任何副本故障都无法操作 |

本项目的默认配置（N=3, W=2, R=2）在一致性和可用性之间取得了较好的平衡：
- 可以容忍 1 个节点故障（N-W=1 或 N-R=1）
- 读取保证看到最新写入

## 4. 幂等设计

写操作是幂等的——多次执行结果相同：

```go
func (db *DistributedBackend) DeleteObject(bucket, key string) error {
    // 幂等：删除不存在的 key 也算成功
    db.local.DeleteObject(bucket, key)
    // ...
    atomic.AddInt32(&successes, 1)  // 删除成功也算
}
```

这保证了在网络重试场景下，重复的删除请求不会导致错误。

## 5. 开源项目中的使用

| 项目 | Quorum 机制 | 说明 |
|------|------------|------|
| **Amazon DynamoDB** | R+W>N | 原始 Dynamo 论文定义了 Quorum 模型 |
| **Apache Cassandra** | R+W>N, 可配置 CL | 支持 EACH_QUORUM、LOCAL_QUORUM 等细粒度配置 |
| **Riak** | R+W>N, 可配置 | 和 Dynamo 相同的 Quorum 模型 |
| **etcd** | Raft 多数派 | R=W=多数派（简化版 Quorum） |
| **MongoDB** | Write Concern + Read Concern | 类似 Quorum 的一致性控制 |

Quorum 是分布式存储中最基本的一致性保证机制。理解了 `W + R > N` 这个不等式，就理解了大多数分布式数据库的一致性配置。
