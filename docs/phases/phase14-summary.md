<!-- tags: phase-summary -->
# Phase 14 完成总结

## 1. 完成状态：全部完成

Phase 14 新增 2 个文件（cleanup.go、phase14.go），修改 7 个文件（config.go、metrics.go、main.go、architecture.md、CLAUDE.md、run.sh、TODO.md），新增 16 个集成测试断言全部通过，Phase 1-13 全量回归无新增失败。

---

## 2. Phase 14 实现内容

### 2.1 TTLCleaner（src/storage/cleanup.go）

后台 goroutine 定期扫描并清理过期的 multipart upload：

- **构造**：`NewTTLCleaner(backend, ttl, interval, onCleanup)` 通过 type assertion 检测后端是否支持 `MultipartStorage`，不支持时返回 nil
- **启动**：`Start(ctx)` 启动后台 goroutine，通过 `context.Context` 支持优雅停止
- **扫描逻辑**：
  - 启动后立即执行一次 `sweep()`（不等第一个 interval）
  - 按 `time.Ticker` 间隔周期性执行
  - `sweep()` 遍历所有 bucket → 按分页（每批 1000）列出 uploads → 对比 `Initiated` 时间与 TTL 阈值 → 过期则 `AbortUpload`
  - 单个 upload 清理失败仅 slog.Warn，不中断整体扫描
  - 清理完成后通过 `onCleanup` 回调通知 metrics

### 2.2 配置（src/config/config.go）

新增两个配置字段：

| 字段 | 默认值 | 描述 |
|------|--------|------|
| `MultipartTTLSeconds` | 86400（24 小时） | multipart upload 过期时间 |
| `CleanupIntervalSec` | 3600（1 小时） | TTL 扫描间隔 |

### 2.3 Metrics 集成（src/metrics/metrics.go）

新增 `MultipartCleanups` 原子计数器，通过 TTLCleaner 的 `onCleanup` 回调递增。通过 `GET /_metrics` 端点以 JSON 格式暴露：

```json
{
  "multipart_cleanups": 3
}
```

### 2.4 服务器集成（cmd/server/main.go）

在 main.go 中初始化 TTLCleaner 并启动：

```go
ttl := time.Duration(cfg.MultipartTTLSeconds) * time.Second
interval := time.Duration(cfg.CleanupIntervalSec) * time.Second
cleaner := storage.NewTTLCleaner(backend, ttl, interval, metrics.IncMultipartCleanups)
if cleaner != nil {
    cleaner.Start(ctx)
}
```

TTLCleaner 的生命周期与服务器一致，通过 `context.Context` 在 graceful shutdown 时自动停止。

---

## 3. 依赖关系

```
storage/cleanup.go   ← 新增文件，依赖 storage (StorageBackend, MultipartStorage)、slog
config/config.go     ← 新增 2 个配置字段
metrics/metrics.go   ← 新增 MultipartCleanups 计数器
cmd/server/main.go   ← 初始化并启动 TTLCleaner
```

依赖图保持无环。

---

## 4. 测试覆盖

**Phase 14 集成测试（test/phase14.go）：16 个断言**

测试采用自启动服务器模式（编译 → 启动 → 测试 → 清理），使用短 TTL（3s）和短间隔（1s）加速验证：

- **过期清理**：创建 upload → 等待 5s（超过 TTL + interval）→ 验证 ListUploads 不再包含该 upload
- **未过期保留**：创建 upload → 等待 1s（低于 3s TTL）→ 验证 upload 仍可见
- **Metrics 计数器**：验证 `GET /_metrics` 返回 `multipart_cleanups > 0`
- **已完成 upload 不受影响**：InitiateUpload → UploadPart → CompleteUpload → 等待 5s → 验证对象仍存在且内容正确

---

## 5. 设计决策

1. **优雅降级**：后端不支持 MultipartStorage 时 `NewTTLCleaner` 返回 nil，服务器无需额外判断
2. **立即首次扫描**：启动后立即执行一次 sweep，而非等待第一个 interval，避免服务器重启后遗留过期数据
3. **分页扫描**：`ListUploads` 每批 1000 条，支持大量过期 upload 的清理
4. **回调解耦**：TTLCleaner 通过 `onCleanup func(int64)` 回调通知 metrics，不直接依赖 metrics 包
5. **UTC 时间基准**：使用 `time.Now().UTC()` 计算截止时间，与 S3 时间规范一致
