# Phase 2 完成总结

## 1. 完成状态：全部完成

Phase 2 新增 2 个文件（config.go、auth.go），修改 4 个文件（main.go、router.go、errors.go、metadata.go、bucket.go），
新增 32 个自动化测试全部通过。

---

## 2. Phase 2 实现内容

### 2.1 配置文件系统（config.go）

- JSON 格式配置文件，路径通过 `--config` 指定（默认 `./config.json`）
- 文件不存在时使用全部默认值，不报错
- CLI 参数 `--port`、`--root` 可覆盖配置文件值
- 支持字段：port (9000)、root (./data)、access_key (minioadmin)、secret_key (minioadmin)
- `SetDefaults()` 填充零值字段

### 2.2 Content-Type 自动检测（metadata.go）

- `buildMetaFromRequest` 中，当请求未提供 Content-Type 时，使用 `http.DetectContentType(body)` 替代硬编码的 `application/octet-stream`
- 基于前 512 字节嗅探 MIME 类型：HTML → `text/html; charset=utf-8`，PNG → `image/png` 等
- 检测结果存入 `.meta` 文件，GET/HEAD 时原样返回

### 2.3 AWS Signature V2 认证（auth.go + errors.go + router.go）

**Authenticator 组件：**
- `NewAuthenticator(accessKey, secretKey)` — 从配置创建
- `Authenticate(r, bucket, key) *S3APIError` — 验证请求签名

**签名流程：**
1. 解析 `Authorization: AWS {AccessKey}:{Signature}` 头
2. 校验 AccessKey 匹配
3. 构建 StringToSign：`Method\nContent-MD5\nContent-Type\nDate\n/{bucket}/{key}`
4. HMAC-SHA1(SecretKey, StringToSign) → Base64
5. 使用 `hmac.Equal` 常量时间比较（防时序攻击）

**错误码：**
- 无 Authorization 头 → `AccessDenied` (403)
- AccessKey 不匹配 → `AccessDenied` (403)
- 签名不匹配 → `SignatureDoesNotMatch` (403)

**authMiddleware 路由集成：**
- `authWrap(auth, "bucket"|"object", handler)` 包装每个需要认证的 handler
- 从 `r.PathValue()` 提取 bucket/key，传给 Authenticate
- `ListBuckets` (GET /) 不需要认证

### 2.4 ListObjectsV2（bucket.go）

**路由：** `GET /{bucket}` — 通过 query 参数控制行为

**支持的参数：**
| 参数 | 说明 | 默认值 |
|------|------|--------|
| `prefix` | 只返回以此开头的 key | "" (全部) |
| `delimiter` | 分组分隔符（通常 "/") | "" (不分组) |
| `max-keys` | 最大返回数量 | 1000 |
| `continuation-token` | 分页令牌 (base64 编码的上一页最后一个 key) | "" |

**算法流程：**
1. `filepath.WalkDir` 遍历 bucket 目录，收集常规文件，跳过 `.meta` 侧边文件和目录
2. 从文件系统路径推导 S3 key：`filepath.Rel(bucketPath, path)` → `filepath.ToSlash`
3. 读取每个 `.meta` 获取 size/etag/last-modified
4. 字典序排序
5. prefix 过滤
6. start-after（解码 continuation token）
7. delimiter 分组逻辑：
   - key 在 prefix 之后不含 delimiter → 加入 `Contents[]`
   - key 在 prefix 之后含 delimiter → 提取 `prefix + remainder[:delimIdx+len(delim)]`，去重后加入 `CommonPrefixes[]`
8. max-keys 截断，返回 `IsTruncated` + `NextContinuationToken`

**示例：**
```
keys: [a/b/c, a/b/d, a/e, f]
delimiter="/":
  → Contents: [{Key:"f"}], CommonPrefixes: [{Prefix:"a/"}]

prefix="a/" delimiter="/":
  → Contents: [{Key:"a/e"}], CommonPrefixes: [{Prefix:"a/b/"}]
```

**XML 响应格式：** 符合 S3 `ListBucketResult` 规范，包含 `xmlns="http://s3.amazonaws.com/doc/2006-03-01/"`

---

## 3. 更新后的架构

```
Client Request
  │
  ▼
s3Middleware (Server/Date 头, panic 恢复)
  │
  ▼
authMiddleware (提取 bucket/key, 验证 HMAC-SHA1 签名)
  │
  ▼
ServeMux (方法 + 路径路由)
  │
  ▼
Handler (BucketManager / ObjectManager)
  │
  ▼
PathMapper (路径验证 + 遍历防护)
  │
  ▼
Filesystem (数据 + .meta 文件)
```

### 完整路由表

| 方法 | 路径 | Handler | 认证 |
|------|------|---------|------|
| `GET /{$}` | 精确根 | ListBuckets | 否 |
| `PUT /{bucket}` | 单段 | CreateBucket | 是 |
| `DELETE /{bucket}` | 单段 | DeleteBucket | 是 |
| `HEAD /{bucket}` | 单段 | HeadBucket | 是 |
| `GET /{bucket}` | 单段 | ListObjects | 是 |
| `PUT /{bucket}/{key...}` | 多段 | PutObject | 是 |
| `GET /{bucket}/{key...}` | 多段 | GetObject | 是 |
| `HEAD /{bucket}/{key...}` | 多段 | HeadObject | 是 |
| `DELETE /{bucket}/{key...}` | 多段 | DeleteObject | 是 |

---

## 4. 文件清单（Phase 2 新增/修改）

| 文件 | 操作 | 变更内容 |
|------|------|----------|
| `config.go` | 新增 | Config 结构体、LoadConfig、SetDefaults |
| `auth.go` | 新增 | Authenticator、HMAC-SHA1 签名验证 |
| `main.go` | 修改 | --config 参数、LoadConfig、CLI 覆盖、传 cfg 给 newRouter |
| `router.go` | 修改 | newRouter(root, cfg)、authWrap 中间件、注册 GET /{bucket} |
| `errors.go` | 修改 | 新增 ErrAccessDenied、ErrSignatureDoesNotMatch |
| `metadata.go` | 修改 | http.DetectContentType 替代 application/octet-stream |
| `bucket.go` | 修改 | ListObjectsV2 处理器 + XML 类型 |
| `cmd/test_phase2/main.go` | 新增 | 32 个自动化测试 |

---

## 5. 自动化测试

`go run ./cmd/test_phase2/` 执行 32 个测试，覆盖：

- Bucket 创建（带认证）
- 无认证请求 → 403
- 错误签名 → 403 + SignatureDoesNotMatch
- 5 个对象 PUT（含嵌套 key）
- ListObjects 平铺列举
- ListObjects delimiter=/ 分组
- ListObjects prefix + delimiter 组合
- ListObjects max-keys 分页 + IsTruncated
- Content-Type 自动检测
- GetObject 内容验证
- ListBuckets（无需认证）
- 对象删除 + Bucket 删除

---

## 6. 后续 Phase 规划

### Phase 3：健壮性 ✅
- 请求体大小限制（10MB，可配置）
- 每桶互斥锁（并发安全）
- 日志中间件（log/slog 结构化 JSON）
- Metrics 端点（GET /_metrics）
- 详见 [phase3-summary.md](phase3-summary.md)

### 远期
- 分段上传（Multipart Upload）
- 对象版本控制
- AWS Sig V4 认证
- 分布式多节点模式
- 纠删码（Erasure Coding）
