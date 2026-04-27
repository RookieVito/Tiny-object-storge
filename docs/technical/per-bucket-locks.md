<!-- tags: concurrency, locking -->
# Per-Bucket Locks — 并发安全设计

## 问题

Go 的 `net/http` 天然并发（每个请求一个 goroutine）。Phase 1-2 依赖以下原子性保证：

- `os.Rename` 在 Linux 上是原子的（COW 或同设备原子操作）
- 并发 `read()`/`write()` 在 OS 层面安全

但仍存在竞态条件：

1. 两个并发 `CreateBucket("my-bucket")` — `os.Mkdir` 可能都被调用，第二个返回错误
2. `PutObject` 和 `DeleteBucket` 同时操作同一 bucket — 数据可能写入已被删除的目录
3. 并发 `DeleteObject` 删除同一 key 的嵌套目录 — `removeEmptyParents` 可能误删非空目录

## 方案选择

| 方案 | 优点 | 缺点 |
|------|------|------|
| 全局 `sync.Mutex` | 最简单 | 所有 bucket 串行化，吞吐量受限 |
| Per-bucket `sync.Mutex` | 不同 bucket 并行 | 实现稍复杂 |
| `sync.RWMutex` per-bucket | 读写并行 | 读操作本身不需要锁（OS 保证安全） |

**选择：Per-bucket `sync.Mutex`。** 不同 bucket 的写操作可并行，读操作不需要锁。

## 实现

```go
type BucketLocks struct {
    mu    sync.Mutex              // 保护 map 本身
    locks map[string]*sync.Mutex  // per-bucket 锁
}
```

**加锁流程：**
1. 获取外层 mutex（极短时间，仅 map 查找/插入）
2. 查找或创建 bucket 对应的 mutex
3. 释放外层 mutex
4. 获取 bucket mutex（持有直到操作完成）

**未选择 `sync.Map` 的原因：**
- `sync.Map` 优化的是 key 稳定、append-only 的场景
- bucket 名称频繁创建和删除
- `map + sync.Mutex` 更简单直接，外层锁持有时间极短

## 加锁范围

### 加锁的写操作

| 操作 | 文件 | 锁定资源 |
|------|------|---------|
| CreateBucket | bucket.go | bucket |
| DeleteBucket | bucket.go | bucket |
| PutObject | object.go | bucket |
| DeleteObject | object.go | bucket |

### 不加锁的读操作

| 操作 | 理由 |
|------|------|
| GetObject | Linux 并发 read/write 安全；os.Rename 原子性 |
| HeadObject | 同上，仅读取 .meta 文件 |
| ListObjects | WalkDir 遍历，可能看到或不看到正在写入的对象 |
| HeadBucket | os.Stat 是原子的 |
| ListBuckets | os.ReadDir 是原子的 |

### 锁获取时机

```
StorageBackend 验证（纯计算，不需要锁）
    ↓
Lock(bucket) ← 在文件系统操作之前
    ↓
文件系统操作（Mkdir/Remove/Write）
    ↓
Unlock(bucket) ← defer
```

StorageBackend 验证（在 LocalBackend 中通过 PathMapper 完成）只做字符串处理和路径拼接，不涉及文件系统，因此不需要锁保护。但加锁仍在 handler 层执行，因为锁的生命周期与 HTTP 请求绑定。

## 跨组件共享

`BucketManager` 和 `ObjectManager` 在 `NewRouter` 中接收同一个 `*BucketLocks` 实例和同一个 `StorageBackend`：

```go
locks := locks.NewBucketLocks()
bm := NewBucketManager(backend, locks)
om := NewObjectManager(backend, locks, cfg.MaxBodySize)
```

这确保：
- `PutObject("my-bucket", "key")` 和 `DeleteBucket("my-bucket")` 被同一把锁序列化
- `CreateBucket("a")` 和 `PutObject("b", "key")` 可并行执行（不同 bucket）

## 对应实现

| 文件 | 说明 |
|------|------|
| `src/locks/locks.go` | BucketLocks per-bucket 互斥锁 |

**关键类型：** `BucketLocks`
**关键函数：** `NewBucketLocks()`、`Lock()`、`Unlock()`
