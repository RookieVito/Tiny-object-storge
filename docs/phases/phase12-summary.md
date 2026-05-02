<!-- tags: cors, middleware, config -->
# Phase 12: CORS 配置 — 完成总结

## 实现日期

2026-04-27

## 概述

新增 CORS（跨源资源共享）中间件，支持浏览器跨域访问 S3 API。默认启用，`AllowedOrigins: ["*"]` 匹配所有来源。

## 变更文件

| 文件 | 变更 |
|------|------|
| `src/config/config.go` | 新增 `CORSConfig` 结构体和 `SetCORSDefaults()` 方法，`Config` 增加 `CORS` 字段 |
| `src/cors/cors.go` | **新建** CORS 中间件：origin 匹配、preflight OPTIONS、Access-Control 头设置 |
| `src/handler/router.go` | 中间件链插入 `CORSMiddleware`：`s3Middleware → logMiddleware → CORSMiddleware → topMux` |
| `test/phase12.go` | 12 个集成测试 |
| `test/helper.go` | 已有 `DoWithHeaders`、`DoV4WithHeaders` 辅助函数 |

## 功能详情

### CORSConfig

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `AllowedOrigins` | []string | `["*"]` | 允许的 Origin（非空启用 CORS，`[]` 禁用） |
| `AllowedMethods` | []string | GET,PUT,POST,DELETE,HEAD,OPTIONS | Preflight 允许的方法 |
| `AllowedHeaders` | []string | Authorization,Content-Type,X-Amz-Date,X-Amz-Content-Sha256 | Preflight 允许的请求头 |
| `ExposeHeaders` | []string | `["ETag"]` | 响应中暴露给 JS 的头 |
| `MaxAge` | int | `3600` | Preflight 缓存秒数 |
| `AllowCredentials` | bool | `false` | 是否允许携带凭证（与 `*` 互斥） |

### CORSMiddleware 逻辑

1. `AllowedOrigins` 为空 → 透传，不处理（CORS 禁用）
2. 无 `Origin` 请求头 → 透传
3. Origin 不匹配 → 添加 `Vary: Origin`，透传
4. OPTIONS preflight → 设置 CORS 头 + 返回 204
5. 正常请求 → 设置 `Allow-Origin`、`Expose-Headers` → 透传到后续中间件

### Origin 匹配

- `"*"` 通配符匹配任意 origin
- 精确匹配 origin 字符串
- `AllowCredentials=true` 时，通配符返回具体 origin 而非 `"*"`
- 启动时 `AllowCredentials=true` + 通配符会输出安全警告日志

### 中间件位置

```
s3Middleware → logMiddleware → CORSMiddleware → topMux → authWrap → handler
```

CORS 位于 logMiddleware 之后，确保 preflight 请求也被日志记录。位于业务路由之前，确保 preflight 不需要认证。

## 测试覆盖

12 个测试，全部通过：

- GET 带 Origin → `Access-Control-Allow-Origin` 已设置
- GET 带 Origin → `Access-Control-Expose-Headers: ETag`
- OPTIONS preflight → `Access-Control-Max-Age` 已设置
- GET 无 Origin → 不返回 CORS 头
- 通配符 `*` → `Allow-Origin: *`
- 通配符匹配任意 origin
- PUT 带 Origin → CORS 头
- V4 认证 + Origin → CORS 头共存
- ListBuckets（无认证）+ Origin → CORS 头
- OPTIONS preflight → 204 状态码
- OPTIONS → `Allow-Methods` 和 `Allow-Headers`

## 回归测试

`./test/scripts/run.sh local` — Phase 1-12 全部通过（Phase 9: 60, Phase 8: 32, Phase 4: 34, Phase 10: 21, Phase 11: 20, Phase 12: 12 等，共 ~300 个测试）。
