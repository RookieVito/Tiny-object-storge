<!-- tags: http-routing, middleware, authentication, logging, observability, cors -->
# 中间件链与责任链模式

## 概述

HTTP 请求到达业务逻辑之前，需要经过一系列预处理：设置响应头、记录日志、验证认证。
本项目使用**中间件链**模式将这些横切关注点（cross-cutting concerns）与业务逻辑解耦。

这就是设计模式中的**责任链模式（Chain of Responsibility）**：请求沿链传递，每个环节可以处理请求或传递给下一个环节。

## 1. 请求处理流程

```
客户端请求
    │
    ▼
┌─────────────────┐
│  s3Middleware    │  设置 Server/Date 头，panic 恢复
│  (最外层)        │
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│  logMiddleware   │  记录请求日志，更新 metrics 计数
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│  CORSMiddleware  │  Origin 匹配、preflight OPTIONS → 204
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│  topMux          │  精确路径匹配（/_metrics、/_cluster）
│  ├─ /_metrics    │
│  ├─ /_cluster/   │
│  └─ / (fallback) │
└────────┬────────┘
         │ (匹配到 S3 路由)
         ▼
┌─────────────────┐
│  authWrap        │  验证 AWS Sig V4/V2 签名
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│  Handler         │  业务逻辑（BucketManager/ObjectManager）
└─────────────────┘
```

## 2. Go 中间件的实现方式

Go 的 `net/http` 包没有内置中间件概念。中间件通过**高阶函数**实现——接收一个 handler，返回一个新的 handler：

```go
func s3Middleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // 请求处理前
        w.Header().Set("Server", "tiny-object-storage/0.1")
        w.Header().Set("Date", time.Now().UTC().Format(time.RFC1123))

        defer func() {
            // 请求处理后（panic 恢复）
            if rec := recover(); rec != nil {
                w.WriteHeader(http.StatusInternalServerError)
            }
        }()

        next.ServeHTTP(w, r)  // ← 调用下一个 handler
    })
}
```

中间件的嵌套（链式组合）：

```go
// 组合顺序：从外到内
handler := s3Middleware(logMiddleware(m, cors.CORSMiddleware(cfg.CORS, topMux)))
```

等价于：

```
请求 → s3Middleware → logMiddleware → CORSMiddleware → topMux → 业务 handler
响应 ← s3Middleware ← logMiddleware ← CORSMiddleware ← topMux ← 业务 handler
```

## 3. 各中间件的职责

### s3Middleware：协议层

- **设置响应头**：Server、Date（S3 协议要求）
- **panic 恢复**：防止 handler 中的 panic 导致整个服务崩溃

```go
func s3Middleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        defer func() {
            if rec := recover(); rec != nil {
                // 返回 S3 格式的内部错误
                w.Header().Set("Content-Type", "application/xml")
                w.WriteHeader(http.StatusInternalServerError)
                w.Write([]byte(`<Error><Code>InternalError</Code>...</Error>`))
            }
        }()
        w.Header().Set("Server", "tiny-object-storage/0.1")
        w.Header().Set("Date", time.Now().UTC().Format(time.RFC1123))
        next.ServeHTTP(w, r)
    })
}
```

### logMiddleware：可观测性层

- **结构化日志**：记录 method、path、status、latency、remote_addr
- **metrics 更新**：TotalRequests +1，错误时 TotalErrors +1

```go
func logMiddleware(m *Metrics, next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        start := time.Now()
        sw := &statusWriter{ResponseWriter: w}

        next.ServeHTTP(sw, r)

        m.TotalRequests.Add(1)
        if sw.statusCode >= 400 {
            m.TotalErrors.Add(1)
        }
        slog.Info("request", "method", r.Method, "status", sw.statusCode,
            "duration_ms", time.Since(start).Milliseconds())
    })
}
```

### authWrap：认证层

与 s3Middleware、logMiddleware 不同，authWrap 是**路由级别的中间件**——只包裹需要认证的 handler：

```go
mux.HandleFunc("PUT /{bucket}", authWrap(a, "bucket", bm.CreateBucket))
mux.HandleFunc("GET /{$}",      bm.ListBuckets)  // ← 无认证
```

不需要认证的路由直接注册 handler，不走 authWrap。

## 4. 执行顺序的重要性

中间件的执行顺序不是随意的，有严格的依赖关系：

| 顺序 | 中间件 | 原因 |
|------|--------|------|
| 最外层 | s3Middleware | panic 恢复必须包裹所有内容，包括其他中间件的异常 |
| 第二层 | logMiddleware | 需要捕获完整请求的耗时，包括 CORS、认证和业务逻辑 |
| 第三层 | CORSMiddleware | preflight 请求在此拦截返回 204，不进入业务路由；正常请求设置 CORS 头后继续传递 |
| 第四层 | topMux | 路由分发 |
| 路由级 | authWrap | 在路由匹配后、业务逻辑前执行认证 |

如果顺序错了会发生什么？

- **logMiddleware 放在最外层**：s3Middleware 中的 panic 不会被 logMiddleware 记录（因为 panic 发生在更深层）
- **authWrap 放在 logMiddleware 之前**：认证失败的请求不会被计入 metrics（因为 logMiddleware 在内层，还没执行到就被 auth 拦截了）

## 5. 两层 ServeMux 的设计

为了避免路由冲突（`/_metrics` 与 `{bucket}` 通配符），使用两层 ServeMux：

```go
// 内层 mux：S3 路由
mux := http.NewServeMux()
mux.HandleFunc("GET /{$}", bm.ListBuckets)
mux.HandleFunc("PUT /{bucket}", ...)

// 外层 mux：精确路由 + fallback
topMux := http.NewServeMux()
topMux.Handle("GET /_metrics", m)    // 精确匹配
topMux.Handle("/", mux)               // fallback 到内层
```

请求先到外层 topMux，如果匹配 `/_metrics` 就直接处理，否则 fallback 到内层 S3 mux。

## 6. 开源项目中的相同设计

| 项目 | 中间件实现 | 说明 |
|------|-----------|------|
| **Go 标准库** | `http.Handler` 接口 | Go 的中间件模式天然来自接口设计 |
| **Chi** | `func(http.Handler) http.Handler` | 最流行的 Go 中间件库 |
| **Gin** | `func(*Context)` | 另一种中间件风格，使用 Context 对象 |
| **Express.js** | `app.use(middleware)` | Node.js 生态的中间件模式 |
| **Kubernetes API Server** | Filter Chain | 请求经过认证、审计、限流等过滤器链 |

中间件模式的核心思想：**将通用的处理逻辑从业务代码中抽离出来，以可组合的方式叠加到请求处理流程中**。
