# Go ServeMux 路由模式

## 概述

Go 1.22 引入了增强的 `net/http.ServeMux`，支持方法匹配和路径通配符，
无需第三方路由框架即可实现复杂的 REST API 路由。本项目完全基于此特性构建。

## 1. 模式语法

### 1. 精确匹配 `{$}`

```go
mux.HandleFunc("GET /{$}", handler)
```

- `{$}` 表示路径必须精确匹配 `/`，不接受 `/` 后有任何字符
- 没有 `{$}` 的 `GET /` 会匹配 `/anything` 作为前缀
- **用途**：根路径的精确匹配（如 `GET /` → ListBuckets）

### 1. 单段通配 `{name}`

```go
mux.HandleFunc("PUT /{bucket}", handler)
```

- `{bucket}` 匹配单个路径段
- `PUT /mybucket` → `bucket = "mybucket"`
- `PUT /mybucket/extra` → 不匹配（有两个段，需要 `{key...}` 模式）
- `PUT /` → 不匹配（缺少通配符段）

### 1. 多段通配 `{name...}`

```go
mux.HandleFunc("GET /{bucket}/{key...}", handler)
```

- `{key...}` 匹配一个或多个路径段（`...` 是尾随通配）
- `GET /mybucket/file.txt` → `key = "file.txt"`
- `GET /mybucket/photos/2024/cat.jpg` → `key = "photos/2024/cat.jpg"`
- `GET /mybucket/` → `key = ""`（空字符串，一个段被通配后为空）

### 1. 方法匹配

```go
mux.HandleFunc("PUT /{bucket}", putHandler)   // 只匹配 PUT
mux.HandleFunc("GET /{bucket}", getHandler)   // 只匹配 GET
```

- Go 1.22+ 支持在模式前指定 HTTP 方法
- `GET /{bucket}` 只匹配 GET 请求，POST/PUT/DELETE 不匹配
- 没有方法前缀的模式匹配所有方法（旧行为，向后兼容）

## 2. 匹配优先级：最长前缀匹配

当多个模式可能匹配同一个请求时，ServeMux 选择最具体的（最长路径的）模式：

```go
mux.HandleFunc("GET /{bucket}", handlerA)        // 2 段
mux.HandleFunc("GET /{bucket}/{key...}", handlerB) // 3+ 段
```

| 请求路径 | 匹配模式 | 调用 |
|---------|---------|------|
| `GET /mybucket` | `/GET /{bucket}` | handlerA |
| `GET /mybucket/file.txt` | `/GET /{bucket}/{key...}` | handlerB |
| `GET /mybucket/` | `/GET /{bucket}/{key...}` | handlerB（key=""） |

**注意**：不存在冲突。`PUT /mybucket`（2 段）和 `PUT /{bucket}/{key...}`（3+ 段）由
ServeMux 的最长前缀规则正确区分。带 `/` 后缀的路径 `/mybucket/` 有 3 段（`""`, `"mybucket"`, `""`），
所以匹配 `{key...}` 而不是 `{bucket}`。

## 3. PathValue 提取路径参数

```go
func handler(w http.ResponseWriter, r *http.Request) {
    bucket := r.PathValue("bucket")  // 从路径中提取
    key := r.PathValue("key")        // 从路径中提取
}
```

- `PathValue` 返回通配符匹配的实际值
- 如果路径不匹配该模式，返回空字符串（不会 panic）

### 3.1 key 为空的情况

对于 `GET /{bucket}/{key...}` 模式：
- `GET /mybucket/` → `bucket="mybucket"`, `key=""`
- `GET /mybucket` → 不匹配此模式（匹配 `/GET /{bucket}`）

## 4. 在本项目中的应用

### 4.1 路由注册表

```go
mux.HandleFunc("GET /{$}",                    bm.ListBuckets)
mux.HandleFunc("PUT /{bucket}",               bm.CreateBucket)
mux.HandleFunc("DELETE /{bucket}",             bm.DeleteBucket)
mux.HandleFunc("HEAD /{bucket}",               bm.HeadBucket)
mux.HandleFunc("GET /{bucket}",               bm.ListObjects)     // ← 注意
mux.HandleFunc("PUT /{bucket}/{key...}",      om.PutObject)
mux.HandleFunc("GET /{bucket}/{key...}",      om.GetObject)
mux.HandleFunc("HEAD /{bucket}/{key...}",     om.HeadObject)
mux.HandleFunc("DELETE /{bucket}/{key...}",    om.DeleteObject)
```

### 4.2 GET /{bucket} 的路由决策

`GET /mybucket` 同时匹配 `GET /{bucket}` 和 `GET /{bucket}/{key...}`（key=""）。
ServeMux 按注册顺序处理。由于 `GET /{bucket}` 先注册，且更短，最长前缀匹配会选择它。

实际上 Go ServeMux 的行为是：**更具体的模式优先**。对于 `GET /mybucket`（2 段）：
- `GET /{bucket}` 需要 2 段：`/` + `mybucket` ✓
- `GET /{bucket}/{key...}` 需要 3+ 段：`/` + `mybucket` + 至少一个字符 ✗
- 所以匹配 `GET /{bucket}`

### 4.3 中间件包装

```go
mux.HandleFunc("PUT /{bucket}", authWrap(auth, "bucket", bm.CreateBucket))
```

`authWrap` 是一个高阶函数，在路由匹配后、handler 执行前插入认证逻辑：

```go
func authWrap(auth *Authenticator, resourceType string, next http.HandlerFunc) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        bucket := r.PathValue("bucket")     // ← 此时路由已匹配，PathValue 可用
        key := ""
        if resourceType == "object" {
            key = r.PathValue("key")
        }
        if err := auth.Authenticate(r, bucket, key); err != nil {
            writeS3Err(w, err, r.URL.Path)
            return
        }
        next.ServeHTTP(w, r)
    }
}
```

**关键**：`r.PathValue()` 必须在 ServeMux 路由匹配之后才能调用。在 middleware 中调用
是安全的，因为 middleware 包装在 ServeMux 内部，ServeMux 先完成路由匹配再调用 middleware。

### 4.4 ListBuckets 的特殊处理

```go
mux.HandleFunc("GET /{$}", bm.ListBuckets)  // 无 authWrap 包装
```

`GET /` 不经过认证中间件，因为 `r.PathValue("bucket")` 对根路径不可用。
这是开发阶段的便利性设计——ListBuckets 无需签名即可调用。
生产环境应在 authWrap 中添加特殊处理。
