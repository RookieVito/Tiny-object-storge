<!-- tags: health-check, rebalance, disk-failure, ec -->
# 磁盘健康监控与自动修复

## 概述

EC 模式下，磁盘故障会导致分片丢失。本项目实现了两级自动应对机制：**DiskHealthChecker** 负责定期巡检磁盘状态，**Rebalancer** 负责在磁盘恢复后自动修复缺失分片。

## 1. 整体架构

```
┌──────────────────┐    状态变更     ┌──────────────┐
│ DiskHealthChecker │──────────────→│  Rebalancer   │
│                  │  onStateChange │              │
│  os.Stat 巡检    │  (alive=true   │  扫描 EC 对象 │
│  定时 ticker     │   触发修复)    │  重建缺失分片 │
└──────────────────┘               └──────────────┘
         │                                │
         ▼                                ▼
   ┌──────────┐                    ┌──────────────┐
   │ ECBackend │←── SetDiskState ──│ RepairObject │
   │ diskStates│   RepairObject →  │ RS 编码重建  │
   └──────────┘                    └──────────────┘
```

**触发条件**：磁盘故障后恢复时触发 Rebalance。磁盘故障本身不触发修复（因为需要足够的 alive 磁盘才能解码）。

## 2. DiskHealthChecker

### 数据结构

```go
type DiskHealthChecker struct {
    ecBackend     *ECBackend
    interval      time.Duration
    onCheck       func()                           // 每次检查完成后回调（通知 metrics）
    onStateChange func(diskIndex int, alive bool)  // 状态变更回调（触发 Rebalancer）
    mu            sync.Mutex
    running       bool
}
```

### 健康检查流程

```go
func (h *DiskHealthChecker) Check() {
    var changes []diskStateChange

    h.mu.Lock()
    for i := 0; i < n; i++ {
        diskPath := h.ecBackend.DiskPath(i)
        _, err := os.Stat(diskPath)
        alive := err == nil

        // 对比当前状态，仅变更时记录
        if alive == h.ecBackend.IsDiskAlive(i) {
            continue
        }
        h.ecBackend.SetDiskState(i, alive)
        changes = append(changes, diskStateChange{index: i, alive: alive})
    }
    h.mu.Unlock()

    // 回调在锁外执行，避免死锁
    for _, c := range changes {
        h.onStateChange(c.index, c.alive)
    }
}
```

**设计要点**：
- 使用 `os.Stat()` 检测磁盘可访问性（最轻量的检测方式）
- 仅在状态**实际变化**时触发回调，避免不必要的 Rebalance
- 回调在锁外执行——Rebalancer 内部会调用 ECBackend 方法，可能产生锁竞争

### 启动与调度

```go
func (h *DiskHealthChecker) run(ctx context.Context) {
    ticker := time.NewTicker(h.interval)  // 默认 60 秒
    h.Check()                              // 启动时立即执行一次

    for {
        select {
        case <-ctx.Done(): return
        case <-ticker.C:  h.Check()
        }
    }
}
```

## 3. ECBackend 磁盘状态管理

```go
type DiskState struct {
    Path  string
    Alive bool
}

type ECBackend struct {
    diskStates []DiskState
    // ...
}

func (eb *ECBackend) SetDiskState(index int, alive bool)
func (eb *ECBackend) IsDiskAlive(index int) bool
func (eb *ECBackend) AliveCount() int
func (eb *ECBackend) DiskPath(index int) string
func (eb *ECBackend) DataShards() int
```

所有磁盘状态的读写通过 `DiskState` 数组管理，`ECBackend` 的 Put/Get/List 操作在执行前检查 `IsDiskAlive`，跳过故障磁盘。

## 4. Rebalancer

### 数据结构

```go
type Rebalancer struct {
    ecBackend   *ECBackend
    onRebalance func(repairedObjects int64)  // 回调通知 metrics
    mu          sync.Mutex                   // 防止并发 Rebalance
}
```

### 修复流程

```
Rebalance()
  │
  ├─ 检查：AliveCount >= DataShards？（alive 磁盘不足则跳过）
  │
  ├─ ListBuckets()
  │
  └─ 对每个 bucket:
       │
       ├─ metaStore.ListObjects() → 找到所有 .ec-meta 文件
       │
       └─ 对每个对象:
            │
            ├─ RepairObject(bucket, key)
            │    │
            │    ├─ 从 alive 磁盘读取现有分片
            │    ├─ RS 解码恢复完整数据
            │    ├─ RS 重新编码
            │    └─ 写入缺失的分片到恢复的磁盘
            │
            └─ 统计修复数量
```

```go
func (r *Rebalancer) Rebalance() {
    r.mu.Lock()
    defer r.mu.Unlock()

    // 前提：alive 磁盘数 >= 数据分片数（否则无法解码）
    if r.ecBackend.AliveCount() < r.ecBackend.DataShards() {
        return
    }

    for _, bucket := range buckets {
        repaired := r.rebalanceBucket(bucket)
    }
}

func (r *Rebalancer) rebalanceBucket(bucket string) int {
    // 扫描元数据存储中的 .ec-meta 文件
    entries := r.ecBackend.metaStore.ListObjects(bucket, "", "", "", 100000)

    for _, e := range entries {
        if !strings.HasSuffix(e.Key, ".ec-meta") { continue }
        objKey := strings.TrimSuffix(e.Key, ".ec-meta")

        count, err := r.ecBackend.RepairObject(bucket, objKey)
        if count > 0 { repaired++ }
    }
}
```

### RepairObject 实现

```go
func (eb *ECBackend) RepairObject(bucket, key string) (int, error) {
    // 1. 读取 EC 元数据
    ecMeta := eb.readECMeta(bucket, key)

    // 2. 从 alive 磁盘收集现有分片
    for i := 0; i < totalShards; i++ {
        if !eb.diskStates[i].Alive { continue }
        shards[i] = eb.disks[i].GetObject(...)
    }

    // 3. RS 解码恢复完整数据
    eb.rs.Decode(shards, ecMeta.ShardSize)

    // 4. RS 重新编码
    newShards := eb.rs.Encode(data)

    // 5. 写入缺失的分片（仅写入恢复的磁盘）
    repaired := 0
    for i := 0; i < totalShards; i++ {
        if eb.diskStates[i].Alive && shards[i] == nil {
            eb.disks[i].PutObject(...)
            repaired++
        }
    }
    return repaired, nil
}
```

## 5. 在 main.go 中的集成

```go
rebalancer := storage.NewRebalancer(ecBackend, func(count int64) {
    m.RebalancedObjects.Add(count)  // 通知 metrics
})

healthChecker := storage.NewDiskHealthChecker(ecBackend, healthInterval,
    func() {                          // onCheck：每次检查完成
        m.DiskHealthChecks.Add(1)
    },
    func(diskIndex int, alive bool) { // onStateChange：磁盘恢复时触发修复
        slog.Info("disk state changed", "disk", diskIndex, "alive", alive)
        if alive {
            go rebalancer.Rebalance()  // 异步执行，不阻塞健康检查
        }
    },
)
healthChecker.Start(cleanupCtx)
```

**设计选择**：
- Rebalance 异步执行（`go rebalancer.Rebalance()`），不阻塞健康检查周期
- 仅在 `alive=true`（磁盘恢复）时触发，磁盘故障时不触发（因为缺少磁盘无法解码）
- Rebalancer 自身有 `sync.Mutex` 防止并发执行

## 对应实现

| 文件 | 说明 |
|------|------|
| `src/storage/health.go` | DiskHealthChecker 实现 |
| `src/storage/rebalance.go` | Rebalancer 实现 |
| `src/storage/ec.go` | DiskState 管理 + RepairObject |
| `src/metrics/metrics.go` | RebalancedObjects 计数器 |
