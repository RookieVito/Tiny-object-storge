<!-- tags: storage, ec, health, rebalance -->
# Phase 16: 磁盘健康监控和 Rebalance — 完成总结

## 实现日期

2026-05-02

## 概述

为 EC 后端实现磁盘健康监控和自动 Rebalance 功能。后台 goroutine 定期检查磁盘可访问性，检测到磁盘故障/恢复时通过回调通知，恢复后自动扫描并重建所有缺失分片。

## 核心实现

### DiskHealthChecker（src/storage/health.go）

后台 goroutine 定期通过 `os.Stat` 检查磁盘可访问性：

- **Check()**：遍历所有磁盘，对比当前可访问性与 `diskStates` 状态，变更时触发回调
- **并发安全**：`sync.Mutex` 保护 `Check()` 方法，防止并发执行
- **启动即检查**：`Start()` 后立即执行首次检查，然后按 interval 定期执行
- **Context 驱动**：通过 `context.Context` 优雅停止

### Rebalancer（src/storage/rebalance.go）

磁盘恢复后自动扫描所有 EC 对象并重建缺失分片：

- **Rebalance()**：遍历所有 bucket → 扫描 metaStore 中 `.ec-meta` → 调用 `RepairObject`
- **并发守卫**：`sync.Mutex` 防止多个 goroutine 同时执行 rebalance
- **可用磁盘不足时跳过**：`AliveCount() < DataShards()` 时记录 warn 日志并跳过
- **统计回调**：修复完成后通过 `onRebalance` 回调通知 Metrics

### ReedSolomon.Reconstruct（src/ec/reedsolomon.go）

新增方法，在 `Decode` 恢复数据分片后，用编码矩阵重新计算缺失的 parity 分片：

- `Decode` 仅恢复 `shards[0..K-1]`（数据分片）
- `Reconstruct` 在 Decode 后额外恢复 `shards[K..N-1]`（parity 分片）
- 自修复和 RepairObject 都使用 `Reconstruct` 以确保全部分片完整

### ECBackend.RepairObject（src/storage/ec.go）

主动修复指定对象缺失分片的方法：

1. 读取 ECObjectMeta
2. 对每个磁盘检查分片存在性（`HeadObject`）
3. 收集缺失索引，验证可用磁盘数 >= K
4. 从可用磁盘读取分片 → `Reconstruct` 恢复全部分片
5. 将缺失分片写回对应磁盘

### GetObject 自修复升级

当有分片缺失时，`GetObject` 使用 `Reconstruct`（恢复全部分片含 parity）替代 `Decode`（仅恢复数据分片），确保自修复能重建 parity 分片。

## 修改文件

| 文件 | 类型 | 说明 |
|------|------|------|
| `src/storage/health.go` | 新增 | DiskHealthChecker（93 行） |
| `src/storage/rebalance.go` | 新增 | Rebalancer（82 行） |
| `src/storage/ec.go` | 修改 | 新增 RepairObject、DataShards、DiskPath、DiskCount、IsDiskAlive 方法；GetObject 使用 Reconstruct |
| `src/ec/reedsolomon.go` | 修改 | 新增 Reconstruct 方法 |
| `src/config/config.go` | 修改 | ECConfig 新增 HealthCheckIntervalSec |
| `src/metrics/metrics.go` | 修改 | 新增 DiskHealthChecks、RebalancedObjects 计数器 |
| `cmd/server/main.go` | 修改 | 集成 DiskHealthChecker + Rebalancer |
| `test/phase16.go` | 新增 | 集成测试（23 个） |

## 测试覆盖

23 个集成测试，覆盖 5 个场景：

1. **健康检查基本功能** — metrics 端点、`disk_health_checks` 计数递增
2. **EC 降级读 + 自修复** — 删除分片后 GetObject 仍可用，分片自动重建
3. **磁盘故障检测** — 删除磁盘目录后健康检查发现，降级模式下读写正常
4. **磁盘恢复 + Rebalance** — 恢复磁盘目录后自动重建缺失分片，`rebalanced_objects` 计数递增
5. **多磁盘故障容忍** — 同时 2 磁盘故障时仍可读取，恢复后全部正常

## 回归测试

全量回归通过：Phase 1-15（local）、Phase 5（EC）、Phase 14/15/16（自启动服务器）、unit（EC/hash/cluster）。

## 技术文档

- [erasure-coding.md](technical/erasure-coding.md) — 自修复和 Reconstruct 补充
