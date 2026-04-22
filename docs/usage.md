# 使用指南

本文档介绍 Tiny Object Storage 的所有访问方式。

---

## 1. Web UI

浏览器访问 `http://localhost:9000/_ui/`。

**功能：** Bucket/Object 管理、前缀导航（文件夹视图）、文件拖拽上传（带进度条）、下载、删除。

认证通过浏览器端 Web Crypto API 实现 AWS Sig V2 签名，凭据保存在 sessionStorage 中，关闭标签页后失效。

### 首次使用

1. 打开 `http://localhost:9000/_ui/`
2. 输入服务器地址（默认当前地址）、Access Key、Secret Key
3. 点击「连接」进入管理界面

### 生产构建

开发模式使用 Vite dev server（热重载），生产模式将前端编译并嵌入 Go 二进制：

```bash
# 开发
cd web && npm install && npm run dev
# 访问 http://localhost:5173，API 请求自动代理到 :9000

# 生产构建
cd web && npm run build
cp -r dist/ ../cmd/server/static/dist/
go run ./cmd/server/
# 访问 http://localhost:9000/_ui/
```

---

## 2. Go CLI 客户端

内置命令行客户端，自动处理 Sig V2 签名，支持上传/下载进度条。

### 配置

```bash
# 交互式配置（保存到 ~/.tiny-storage/config.json）
go run ./cmd/client/ config --endpoint http://localhost:9000 --access-key minioadmin --secret-key minioadmin

# 查看当前配置
go run ./cmd/client/ config

# 也可通过环境变量配置
export TOS_ENDPOINT=http://localhost:9000
export TOS_ACCESS_KEY=minioadmin
export TOS_SECRET_KEY=minioadmin
```

### 命令参考

```bash
# Bucket 操作
go run ./cmd/client/ ls                          # 列出所有 Bucket
go run ./cmd/client/ mb my-bucket                # 创建 Bucket
go run ./cmd/client/ rb my-bucket                # 删除 Bucket

# Object 操作
go run ./cmd/client/ ls my-bucket                # 列出 Bucket 内对象
go run ./cmd/client/ ls my-bucket photos/        # 按前缀筛选
go run ./cmd/client/ cp localfile s3://b/key     # 上传（带进度条）
go run ./cmd/client/ cp s3://b/key localfile     # 下载（带进度条）
go run ./cmd/client/ cat s3://bucket/key         # 输出到 stdout
go run ./cmd/client/ stat s3://bucket/key        # 查看元数据
go run ./cmd/client/ rm s3://bucket/key          # 删除对象
```

### 配置文件格式

`~/.tiny-storage/config.json`：

```json
{
  "endpoint": "http://localhost:9000",
  "access_key": "minioadmin",
  "secret_key": "minioadmin"
}
```

环境变量优先级高于配置文件。

---

## 3. curl + AWS Sig V2

直接通过 HTTP 请求调用 S3 API，需要手动构造签名头。

### 认证

除 `GET /`（ListBuckets）、`GET /_metrics`、`/_ui/*` 外，所有请求需要签名。

签名计算：

```
Signature = Base64(HMAC-SHA1(SecretKey, StringToSign))

StringToSign = HTTP-Method + "\n"
             + Content-MD5 + "\n"
             + Content-Type + "\n"
             + Date + "\n"
             + CanonicalizedResource
```

### 示例：创建 Bucket

```bash
DATE=$(date -u +"%a, %d %b %Y %H:%M:%S GMT")
SIG=$(echo -ne "PUT\n\n\n${DATE}\n/my-bucket" | openssl dgst -sha1 -hmac "minioadmin" -binary | base64)
curl -X PUT http://localhost:9000/my-bucket \
  -H "Authorization: AWS minioadmin:${SIG}" \
  -H "Date: ${DATE}"
```

### 示例：上传对象

```bash
DATE=$(date -u +"%a, %d %b %Y %H:%M:%S GMT")
SIG=$(echo -ne "PUT\n\ntext/plain\n${DATE}\n/my-bucket/hello.txt" | openssl dgst -sha1 -hmac "minioadmin" -binary | base64)
curl -X PUT http://localhost:9000/my-bucket/hello.txt \
  -H "Authorization: AWS minioadmin:${SIG}" \
  -H "Date: ${DATE}" \
  -H "Content-Type: text/plain" \
  -d "Hello, World!"
```

### 示例：列出对象

```bash
DATE=$(date -u +"%a, %d %b %Y %H:%M:%S GMT")
SIG=$(echo -ne "GET\n\n\n${DATE}\n/my-bucket" | openssl dgst -sha1 -hmac "minioadmin" -binary | base64)
curl http://localhost:9000/my-bucket \
  -H "Authorization: AWS minioadmin:${SIG}" \
  -H "Date: ${DATE}"
```

### 示例：ListObjectsV2 查询参数

```bash
# 使用 delimiter 模拟目录结构
curl "http://localhost:9000/my-bucket?delimiter=/&max-keys=100" \
  -H "Authorization: AWS minioadmin:${SIG}" -H "Date: ${DATE}"

# 按前缀过滤
curl "http://localhost:9000/my-bucket?prefix=photos/&delimiter=/" \
  -H "Authorization: AWS minioadmin:${SIG}" -H "Date: ${DATE}"

# 分页（使用上一页返回的 NextContinuationToken）
curl "http://localhost:9000/my-bucket?continuation-token=TOKEN&max-keys=10" \
  -H "Authorization: AWS minioadmin:${SIG}" -H "Date: ${DATE}"
```

### 无需认证的端点

```bash
# 列出所有 Bucket
curl http://localhost:9000/

# Metrics
curl http://localhost:9000/_metrics

# Web UI
curl http://localhost:9000/_ui/
```

---

## 4. 服务器配置

配置文件为 JSON 格式（`config.json`），所有字段可选，未指定字段使用默认值。

```json
{
  "port": 9000,
  "root": "./data",
  "access_key": "minioadmin",
  "secret_key": "minioadmin",
  "max_body_size": 10485760,
  "backend_type": "local"
}
```

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `port` | int | 9000 | HTTP 监听端口 |
| `root` | string | `./data` | 存储根目录 |
| `access_key` | string | `minioadmin` | Access Key |
| `secret_key` | string | `minioadmin` | Secret Key |
| `max_body_size` | int64 | 10485760 (10MB) | 单次上传最大请求体 |
| `backend_type` | string | `local` | 存储后端：`local` / `ec` / `distributed` |

CLI 参数优先级高于配置文件：

```bash
./tiny-storage --config ./config.json --port 8080
```

---

## 5. API 参考

### Bucket 操作

| 方法 | 路径 | 认证 | 说明 |
|------|------|------|------|
| `GET /` | 无 | 列出所有 Bucket |
| `PUT /{bucket}` | 需要 | 创建 Bucket（200 / 409） |
| `DELETE /{bucket}` | 需要 | 删除 Bucket，必须为空（204 / 409） |
| `HEAD /{bucket}` | 需要 | 检查 Bucket 是否存在（200 / 404） |
| `GET /{bucket}` | 需要 | ListObjectsV2 |

### Object 操作

| 方法 | 路径 | 认证 | 说明 |
|------|------|------|------|
| `PUT /{bucket}/{key}` | 需要 | 上传对象（200 / 413） |
| `GET /{bucket}/{key}` | 需要 | 下载对象（200 / 404） |
| `HEAD /{bucket}/{key}` | 需要 | 获取元数据（200 / 404） |
| `DELETE /{bucket}/{key}` | 需要 | 删除对象（204，幂等） |

### ListObjectsV2 查询参数

| 参数 | 说明 |
|------|------|
| `prefix` | 按前缀过滤 |
| `delimiter` | 分隔符（通常 `/`，模拟目录结构） |
| `max-keys` | 每页最大数量（默认 1000） |
| `continuation-token` | 分页 token（上一页返回的 NextContinuationToken） |

### 系统端点

| 方法 | 路径 | 认证 | 说明 |
|------|------|------|------|
| `GET /_metrics` | 无 | 运行指标 JSON |
| `GET /_ui/{path...}` | 无 | Web UI 静态资源 |
| `POST /_cluster/*` | 无 | 集群内部协议（分布式模式） |

### Multipart Upload 操作（Phase 8）

支持大文件分片上传，每个分片最小 5MB（最后一个分片除外）。

| 方法 | 路径 | 认证 | 说明 |
|------|------|------|------|
| `POST /{bucket}/{key}?uploads` | 需要 | 发起分片上传，返回 UploadId |
| `PUT /{bucket}/{key}?partNumber=N&uploadId=X` | 需要 | 上传分片，返回 ETag |
| `POST /{bucket}/{key}?uploadId=X` | 需要 | 完成上传，请求体为 XML 分片列表 |
| `DELETE /{bucket}/{key}?uploadId=X` | 需要 | 取消上传 |
| `GET /{bucket}/{key}?uploadId=X` | 需要 | 列出已上传分片 |
| `GET /{bucket}?uploads` | 需要 | 列出进行中的上传 |

> EC 和 Distributed 后端暂不支持 multipart upload，返回 501 NotImplemented。

### 错误码

| HTTP 状态码 | S3 错误码 | 场景 |
|-------------|----------|------|
| 400 | InvalidBucketName | Bucket 名称不合法 |
| 400 | InvalidKey | Key 为空或包含 `..` |
| 400 | EntityTooSmall | 非 final part 小于 5MB |
| 400 | InvalidPartNumber | partNumber 不在 1-10000 范围 |
| 400 | InvalidPartOrder | parts 未按编号升序 |
| 403 | AccessDenied | 缺少 Authorization 头或 AccessKey 不匹配 |
| 403 | SignatureDoesNotMatch | 签名验证失败 |
| 404 | NoSuchBucket | Bucket 不存在 |
| 404 | NoSuchKey | 对象不存在 |
| 404 | NoSuchUpload | 指定的 multipart upload 不存在 |
| 409 | BucketAlreadyExists | 创建已存在的 Bucket |
| 409 | BucketNotEmpty | 删除非空 Bucket |
| 413 | RequestEntityTooLarge | 上传超过大小限制 |
| 501 | NotImplemented | 后端不支持的操作（如 EC/Distributed 的 multipart） |
| 503 | WriteQuorumFailed | 分布式写入 quorum 未满足 |
| 503 | ReadQuorumFailed | 分布式读取 quorum 未满足 |

错误响应格式：

```xml
<Error>
  <Code>NoSuchBucket</Code>
  <Message>The specified bucket does not exist.</Message>
  <Resource>/my-bucket</Resource>
</Error>
```

---

## 6. 测试

```bash
# 启动服务器
go run ./cmd/server/ &

# 运行全量测试（Phase 1-8）
go run ./test/

# 运行指定 Phase
go run ./test/ phase1    # 核心 CRUD
go run ./test/ phase2    # S3 兼容
go run ./test/ phase3    # 健壮性
go run ./test/ phase4    # 存储抽象
go run ./test/ phase5    # 纠删码（需要 EC 配置启动服务器）
go run ./test/ phase6    # 分布式（自动启动 3 节点）
go run ./test/ phase7    # CLI 客户端集成测试
go run ./test/ phase8    # Multipart Upload 集成测试

# 单元测试（不需要服务器）
go test ./src/ec/...
go test ./src/hash/...
go test ./src/cluster/...

# 一键全量测试脚本（自动编译、启停服务器、清理数据）
./test/scripts/run.sh            # 全量（local + EC + distributed + unit）
./test/scripts/run.sh local      # 仅 local 模式
./test/scripts/run.sh ec         # 仅 EC 模式
./test/scripts/run.sh distributed # 仅分布式
./test/scripts/run.sh unit       # 仅单元测试
```

---

## 7. 磁盘存储结构

**Local 模式：**
```
{root}/
├── my-bucket/
│   ├── hello.txt              # 数据文件
│   ├── hello.txt.meta         # 元数据（JSON）
│   ├── .uploads/              # Multipart 临时目录（ListObjects 自动跳过）
│   │   └── {uploadId}/
│   │       ├── info.json      # UploadInfo 元数据
│   │       ├── part-0001.bin  # Part 数据
│   │       └── part-0001.bin.meta  # PartInfo 元数据
│   └── photos/
│       └── 2024/
│           ├── cat.jpg
│           └── cat.jpg.meta
```

**EC 模式：**
```
disk-0/{bucket}/{key}          # 数据分片 0
disk-1/{bucket}/{key}          # 数据分片 1
disk-2/{bucket}/{key}          # 校验分片 0
disk-3/{bucket}/{key}          # 校验分片 1
meta-root/{bucket}/{key}.ec-meta
```

**Distributed 模式：** 每节点使用 LocalBackend 布局，一致性哈希环决定副本分布。
