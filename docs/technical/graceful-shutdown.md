<!-- tags: observability, lifecycle -->
# 优雅关闭（Graceful Shutdown）

## 概述

当服务器收到终止信号（Ctrl+C 或 `kill` 命令）时，如果直接退出，正在处理的请求会被中断，
可能导致数据损坏或客户端收到连接重置错误。**优雅关闭**确保服务器完成所有正在处理的请求后再退出。

## 1. 问题：非优雅关闭

```
服务器正在处理 3 个请求
    │
    ├─ 请求 A：正在写入文件（WriteFile 执行到一半）
    ├─ 请求 B：正在从远程节点读取数据
    └─ 请求 C：正在编码纠删码分片

收到 SIGTERM 信号
    │
    ✗ 进程立即退出
    │
    → 请求 A 的文件可能损坏（半成品）
    → 请求 B 的远程节点收到连接重置
    → 请求 C 的分片丢失
    → 客户端收到 "connection reset by peer" 错误
```

## 2. 解决方案

```go
// cmd/server/main.go
go func() {
    sigCh := make(chan os.Signal, 1)
    signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
    <-sigCh  // 阻塞等待信号

    slog.Info("shutting down...")

    // 1. 分布式模式：先离开集群（通知其他节点）
    if cfg.BackendType == "distributed" {
        db.Stop()  // GossipMembership.Leave() + Stop()
    }

    // 2. 优雅关闭 HTTP 服务器
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()
    srv.Shutdown(ctx)  // 等待所有正在处理的请求完成
}()

srv.ListenAndServe()  // 阻塞主 goroutine
```

## 3. 关键步骤解析

### 3.1 信号捕获

```go
sigCh := make(chan os.Signal, 1)
signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
```

- `SIGINT`：用户按 Ctrl+C
- `SIGTERM`：`kill` 命令发送的默认信号
- 缓冲区大小为 1：确保信号不会丢失
- 在独立 goroutine 中监听，不阻塞主 goroutine

### 3.2 集群退出

```go
if cfg.BackendType == "distributed" {
    db.Stop()  // → membership.Leave() + membership.Stop()
}
```

在分布式模式下，退出前需要：
1. **Leave**：主动通知所有存活节点自己要离开，让它们从哈希环中移除自己
2. **Stop**：停止 Gossip 协议的后台 goroutine

如果不做这一步，其他节点会在超时后将本节点标记为 Suspect → Dead，期间数据请求可能被路由到已下线的节点上。

### 3.3 HTTP 服务器关闭

```go
ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()
srv.Shutdown(ctx)
```

`http.Server.Shutdown()` 的行为：
1. **立即**停止接受新连接
2. **等待**所有正在处理的请求完成（阻塞）
3. 等待时间由 `ctx` 的超时控制（10 秒）
4. 超时后强制关闭

## 4. 关闭序列图

```
时间线 →

SIGINT/SIGTERM
    │
    ├─ 1. slog.Info("shutting down...")
    │
    ├─ 2. [分布式] db.Stop()
    │      ├─ membership.Leave()  → 通知所有节点
    │      └─ membership.Stop()   → 停止 Gossip goroutine
    │
    ├─ 3. srv.Shutdown(ctx)
    │      ├─ 停止接受新请求
    │      ├─ 等待请求 A 完成 ✓
    │      ├─ 等待请求 B 完成 ✓
    │      └─ 等待请求 C 完成 ✓
    │
    └─ 4. 进程退出（日志："server stopped"）
```

## 5. Go 标准库的支持

Go 1.8 引入了 `http.Server.Shutdown()`，替代了之前直接 `Close()` 的粗暴方式：

| 方法 | 行为 |
|------|------|
| `srv.Close()` | 立即关闭所有连接，正在处理的请求被中断 |
| `srv.Shutdown(ctx)` | 停止接受新连接，等待正在处理的请求完成 |

本项目的实现完全基于标准库，没有引入第三方依赖。

## 6. 分布式场景的特殊考量

在分布式存储中，优雅关闭还有一个重要目的：**数据一致性**。

```
正在写入对象 "photo.jpg" 到 3 个副本
    ├─ 副本 A ✓ 写入完成
    ├─ 副本 B ✓ 写入完成
    └─ 副本 C ... 写入中

此时 W=2 已满足，写入会返回成功。
但如果副本 C 的写入还在进行中，直接关闭会导致 C 上的数据不完整。
```

本项目的处理方式：
- `Shutdown()` 等待所有 HTTP 请求完成（包括正在进行的 RPC 请求）
- 副本 C 的写入会正常完成后再退出
- 客户端不会受到影响

## 7. 开源项目中的实现

| 项目 | 方式 | 说明 |
|------|------|------|
| **Go 标准库** | `srv.Shutdown(ctx)` | Go 1.8+ 内置支持 |
| **Kubernetes** | `PreStop hook + Shutdown` | 先从 Service Endpoints 摘除，再等待请求排空 |
| **Nginx** | `worker_shutdown_timeout` | 等待 worker 处理完请求后再退出 |
| **Spring Boot** | `@PreDestroy` | Java 生态的优雅关闭注解 |

优雅关闭是生产环境的必备能力。没有它，每次部署或重启都可能导致数据不一致和用户体验下降。

## 对应实现

| 文件 | 说明 |
|------|------|
| `cmd/server/main.go` | 服务器入口（SIGINT/SIGTERM 处理、shutdown with timeout） |

**关键逻辑：** 信号监听 → 停止 Accept → 等待连接排空 → 分布式后端清理 → 日志输出
