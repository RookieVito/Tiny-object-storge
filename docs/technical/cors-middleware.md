<!-- tags: cors, middleware, cross-origin, preflight -->
# CORS 中间件

## 概述

CORS（Cross-Origin Resource Sharing）中间件控制浏览器跨域请求行为。本项目通过 `CORSMiddleware` 函数实现，支持精确的 Origin 匹配、Preflight 缓存和凭据控制。

## 1. 为什么需要 CORS

浏览器同源策略（Same-Origin Policy）阻止网页向不同域发送请求。Web UI 运行在 `localhost:5173`（开发模式），而 S3 API 在 `localhost:9000`——不同端口视为不同源。CORS 头告诉浏览器"允许这个跨域请求"。

## 2. 配置

```go
type CORSConfig struct {
    AllowedOrigins   []string // 非空时启用 CORS
    AllowedMethods   []string
    AllowedHeaders   []string
    ExposeHeaders    []string
    MaxAge           int
    AllowCredentials bool
}
```

| 字段 | 默认值 | 说明 |
|------|--------|------|
| `allowed_origins` | `["*"]` | 允许的 Origin 列表。空数组 = 禁用 CORS |
| `allowed_methods` | `[GET,PUT,POST,DELETE,HEAD,OPTIONS]` | Preflight 允许的方法 |
| `allowed_headers` | `[Authorization,Content-Type,X-Amz-Date,X-Amz-Content-Sha256]` | Preflight 允许的请求头 |
| `expose_headers` | `[ETag]` | 响应中暴露给 JS 的头 |
| `max_age` | `3600` | Preflight 缓存秒数 |
| `allow_credentials` | `false` | 是否允许携带 Cookie |

## 3. 中间件逻辑

```
请求进入
  │
  ├─ AllowedOrigins 为空？→ 直接放行（CORS 禁用）
  │
  ├─ Origin 头为空？→ 直接放行（非浏览器请求）
  │
  ├─ Origin 不在 AllowedOrigins？→ 添加 Vary: Origin → 放行（不附加 CORS 头）
  │
  ├─ OPTIONS 方法（Preflight）？
  │    ├─ 返回 ACAO + ACAM + ACAH + ACMA + ACAC
  │    └─ 204 No Content（不转发到后端）
  │
  └─ 普通请求
       ├─ 附加 ACAO
       ├─ 附加 ACAC（如果 AllowCredentials）
       ├─ 附加 ACEH（如果有 ExposeHeaders）
       └─ 转发到后端 handler
```

## 4. Origin 匹配算法

```go
func matchOrigin(allowed []string, origin string) bool {
    for _, a := range allowed {
        if a == "*" { return true }   // 通配符匹配所有
        if a == origin { return true } // 精确匹配
    }
    return false
}
```

**设计选择**：仅支持精确匹配和通配符 `*`，不支持通配符子域（如 `*.example.com`）。这简化了实现且覆盖了绝大多数使用场景。

## 5. Access-Control-Allow-Origin 值选择

```go
func allowOriginValue(cfg CORSConfig, origin string, matched bool) string {
    if hasWildcard(cfg.AllowedOrigins) {
        if !cfg.AllowCredentials { return "*" }      // 通配符 + 无凭据 → 返回 *
        return origin                                 // 通配符 + 凭据 → 返回具体 origin
    }
    return origin                                     // 精确匹配 → 返回具体 origin
}
```

**为什么 `AllowCredentials=true` 时不能返回 `*`**：浏览器规范要求，当 `Access-Control-Allow-Credentials: true` 时，`Access-Control-Allow-Origin` 不能是通配符，必须是具体的 Origin。中间件在启动时检测这种不安全组合并输出警告日志。

## 6. Preflight 请求处理

浏览器在发送跨域非简单请求前，先发送 OPTIONS 请求确认服务端允许：

```
OPTIONS /mybucket/mykey HTTP/1.1
Origin: http://localhost:5173
Access-Control-Request-Method: PUT
Access-Control-Request-Headers: content-type,x-amz-date,authorization

→ 响应：
Access-Control-Allow-Origin: *
Access-Control-Allow-Methods: GET, PUT, POST, DELETE, HEAD, OPTIONS
Access-Control-Allow-Headers: Authorization, Content-Type, X-Amz-Date, X-Amz-Content-Sha256
Access-Control-Max-Age: 3600
```

`Max-Age` 告诉浏览器在 3600 秒内缓存此 Preflight 结果，避免每次请求都发送 OPTIONS。

## 7. 中间件位置

CORS 中间件位于请求链中的认证层之前：

```
s3Middleware → logMiddleware → CORSMiddleware → authMiddleware → Handler
```

**为什么在 auth 之前**：Preflight OPTIONS 请求不携带认证信息，必须在 `authMiddleware` 之前被拦截并响应，否则会被认证层拒绝。

## 8. 安全注意事项

- `AllowCredentials=true` + `AllowedOrigins=["*"]`：中间件启动时输出 `slog.Warn` 警告，但仍正常运行（返回具体 origin 而非 `*`）
- Origin 不匹配时：添加 `Vary: Origin` 头，防止 CDN 缓存错误的 CORS 响应给不同 Origin 的请求
- CORS 完全禁用：`AllowedOrigins` 设为空数组或不配置

## 对应实现

| 文件 | 说明 |
|------|------|
| `src/cors/cors.go` | CORSMiddleware 实现 |
| `src/config/config.go` | CORSConfig 定义 + 默认值 |
| `src/handler/router.go` | 中间件链组装 |
