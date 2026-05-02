<!-- tags: phase-summary -->
# Phase 1 完成总结

## 1. 完成状态：全部完成

Phase 1 的所有目标均已实现并通过 curl 测试验证。项目从零搭建，
当前包含 7 个 Go 源文件，零外部依赖，编译后即可运行。

---

## 2. Phase 1 实现内容

### 2.1 HTTP 服务器

- 基于 Go 1.26 标准库 `net/http`，无第三方框架
- 支持 `--port`（默认 9000）和 `--root`（默认 `./data`）启动参数
- 监听 `SIGINT`/`SIGTERM` 实现优雅关闭（graceful shutdown）
- `s3Middleware` 中间件：为所有响应自动添加 `Server` 和 `Date` 头，
  并从 panic 中恢复返回 500

### 2.2 路由（Router）

利用 Go 1.22+ 增强的 `http.ServeMux`，支持方法 + 路径通配符模式匹配：

```go
// Bucket 操作
GET  /{$}                    → ListBuckets
PUT  /{bucket}               → CreateBucket
DELETE /{bucket}             → DeleteBucket
HEAD /{bucket}               → HeadBucket

// Object 操作
PUT    /{bucket}/{key...}    → PutObject
GET    /{bucket}/{key...}    → GetObject
HEAD   /{bucket}/{key...}    → HeadObject
DELETE /{bucket}/{key...}    → DeleteObject
```

- `{bucket}` 匹配单段路径（bucket 名称）
- `{key...}` 匹配多段路径（支持嵌套 key，如 `photos/2024/cat.jpg`）
- `{$}` 表示精确匹配根路径，避免 `/` 与 `/{bucket}` 冲突

### 2.3 Bucket 管理（BucketManager）

实现了 4 个 S3 Bucket 操作：

| 操作 | HTTP | 行为 | 测试结果 |
|------|------|------|----------|
| CreateBucket | `PUT /{bucket}` | `os.Mkdir` 创建目录，已存在返回 409 | ✅ 200 |
| DeleteBucket | `DELETE /{bucket}` | 检查目录为空后删除，非空返回 409 | ✅ 204/409 |
| HeadBucket | `HEAD /{bucket}` | `os.Stat` 检查是否存在 | ✅ 200/404 |
| ListBuckets | `GET /` | 遍历存储根目录，返回 S3 XML 格式 | ✅ 200 |

- Bucket 名称校验：正则 `[a-z0-9][a-z0-9.\-]{1,61}[a-z0-9]`，拒绝 `..`
- ListBuckets 返回标准的 `<ListAllMyBucketsResult>` XML 响应

### 2.4 Object 管理（ObjectManager）

实现了 4 个 S3 Object 操作：

| 操作 | HTTP | 行为 | 测试结果 |
|------|------|------|----------|
| PutObject | `PUT /{bucket}/{key...}` | 原子写入数据 + 元数据，返回 ETag | ✅ 200 |
| GetObject | `GET /{bucket}/{key...}` | 读取元数据设置响应头，流式返回数据 | ✅ 200/404 |
| HeadObject | `HEAD /{bucket}/{key...}` | 仅读取元数据，设置响应头，不返回 body | ✅ 200/404 |
| DeleteObject | `DELETE /{bucket}/{key...}` | 删除数据 + 元数据文件，幂等（不存在也返回 204） | ✅ 204 |

- 自动创建嵌套目录（`os.MkdirAll`）
- 删除对象后自动递归清理空的父目录，直到 bucket 根目录

### 2.5 元数据存储（MetadataStore）

每个对象对应一个 `.meta` JSON 侧边文件：

```json
{
  "key": "photos/2024/cat.jpg",
  "size": 102400,
  "etag": "\"d41d8cd98f00b204e9800998ecf8427e\"",
  "content_type": "image/jpeg",
  "last_modified": "2024-01-15T10:30:00Z",
  "user_metadata": {
    "X-Amz-Meta-Author": "vito"
  }
}
```

- 写入使用原子模式：`CreateTemp → Write → Sync → Close → Rename`
- `ETag` 使用带引号的 MD5 hex 格式，兼容 S3 客户端
- 自动提取请求中的 `Content-Type` 和 `X-Amz-Meta-*` 头写入元数据

### 2.6 安全：路径遍历防护（PathMapper）

三层防御机制：

| 层级 | 防护 | 说明 |
|------|------|------|
| 第 1 层 | Go ServeMux | 在路由匹配前自动 `cleanPath`，去除 URL 中的 `..` |
| 第 2 层 | 子串拒绝 | `strings.Contains(key, "..")` 直接拒绝 |
| 第 3 层 | 前缀验证 | `filepath.Clean` + `filepath.Join` 后验证结果仍在 bucket 目录内 |

即使攻击者使用 URL 编码（`%2e%2e`），Go 的 ServeMux 解码后
会被第 2/3 层拦截。双重编码（`%252e`）不会被解码为 `..`，
文件以字面路径存储，不会逃出 bucket 目录。

### 2.7 错误处理

- 定义 `S3APIError` 类型实现 `error` 接口，携带 S3 错误码和 HTTP 状态码
- 预定义错误：`NoSuchBucket`(404)、`NoSuchKey`(404)、`BucketAlreadyExists`(409)、
  `BucketNotEmpty`(409)、`InvalidBucketName`(400)、`InvalidKey`(400)
- 所有错误响应输出 S3 标准 XML 格式：
  ```xml
  <Error>
    <Code>NoSuchKey</Code>
    <Message>The specified key does not exist.</Message>
    <Resource>/test-bucket/hello.txt</Resource>
    <RequestId>tiny-req-id</RequestId>
  </Error>
  ```

---

## 3. 当前架构

```
                    ┌─────────────────────┐
                    │     curl / aws-cli   │
                    └──────────┬──────────┘
                               │ HTTP
                    ┌──────────▼──────────┐
                    │  s3Middleware        │
                    │  ├ Server/Date 头    │
                    │  └ panic 恢复 → 500  │
                    └──────────┬──────────┘
                               │
              ┌────────────────┼────────────────┐
              │                │                │
     ┌────────▼──────┐ ┌──────▼──────┐ ┌───────▼───────┐
     │ BucketManager  │ │ObjectManager│ │ PathMapper    │
     │                │ │             │ │               │
     │ CreateBucket   │ │ PutObject   │ │ BucketPath()  │
     │ DeleteBucket   │ │ GetObject   │ │ ObjectPath()  │
     │ HeadBucket     │ │ HeadObject  │ │ MetaPath()    │
     │ ListBuckets    │ │ DeleteObject│ │               │
     └────────┬──────┘ └──────┬──────┘ └───────────────┘
              │               │               │
              │        ┌──────▼──────┐        │
              │        │ Metadata    │        │
              │        │ Store       │        │
              │        │             │        │
              │        │ .meta JSON  │        │
              │        │ 原子写入     │        │
              │        │ MD5 ETag    │        │
              │        └──────┬──────┘        │
              │               │               │
              └───────┬───────┴───────┬───────┘
                      │               │
              ┌───────▼───────────────▼───────┐
              │      Linux Filesystem         │
              │                               │
              │  data/                        │
              │  ├── {bucket}/                │
              │  │   ├── {key}        ← 数据  │
              │  │   └── {key}.meta   ← 元数据 │
              └───────────────────────────────┘
```

### 文件清单

```
tiny-object-storge/
├── go.mod           # Go 模块定义，无外部依赖
├── main.go          # 入口：参数解析、存储根目录创建、优雅关闭
├── router.go        # 路由注册 + s3Middleware 中间件
├── pathmapper.go    # 路径映射 + 三层遍历防护
├── bucket.go        # Bucket CRUD 处理器
├── object.go        # Object CRUD 处理器
├── metadata.go      # .meta 文件读写 + 原子写入工具函数
├── errors.go        # S3APIError 类型 + XML 错误响应
└── docs/
    ├── architecture.md   # 完整架构设计文档
    └── phase1-summary.md # 本文件
```

### 数据流示例：PUT /mybucket/hello.txt

```
请求 → ServeMux 匹配 PUT /{bucket}/{key...}
     → s3Middleware 添加 Server/Date 头
     → ObjectManager.PutObject()
       → PathMapper.ObjectPath("mybucket", "hello.txt")
         → 校验 bucket 名称（正则）
         → 校验 key（无 ".."）
         → filepath.Clean(filepath.Join(root, "mybucket", "hello.txt"))
         → 前缀验证（仍在 bucket 目录内）
       → io.ReadAll(r.Body) 读取请求体
       → buildMetaFromRequest() 生成元数据
         → crypto/md5 计算 ETag
         → 提取 Content-Type、X-Amz-Meta-*
       → writeFile() 原子写入数据文件
         → CreateTemp → Write → Sync → Close → Rename
       → writeMeta() 原子写入 .meta 文件
       → 返回 200 + ETag 头
```

---

## 4. 后续 Phase 规划

### Phase 2：S3 协议兼容
- **ListObjectsV2**：前缀过滤 + delimiter 分组（最复杂的 S3 操作）
- **AWS Signature V2 认证**：HMAC-SHA1 签名验证
- **Content-Type 探测**：未知类型时从文件内容推断 MIME

### Phase 3：健壮性
- 请求体大小限制
- 每桶互斥锁（并发安全）
- 日志中间件
- 配置文件支持

### 远期
- 分段上传（Multipart Upload）
- 对象版本控制
- AWS Sig V4 认证
- 分布式多节点模式
- 纠删码（Erasure Coding）
