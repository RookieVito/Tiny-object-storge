# Local 模式

最简单的存储模式，文件直接以原始形态存放在本地文件系统中。支持所有 S3 操作和 Multipart Upload。

## 快速启动

```bash
# 一键启动（后台运行，自动等待就绪）
./example/local/start.sh start

# 停止
./example/local/start.sh stop

# 重启
./example/local/start.sh restart

# 查看状态
./example/local/start.sh status

# 查看日志
./example/local/start.sh log
```

手动启动：

```bash
# 方式 1：使用配置文件
go run ./cmd/server/ --config ./example/local/config.json

# 方式 2：命令行参数（默认 local 模式）
go run ./cmd/server/ --port 9000

# 方式 3：零参数启动（使用全部默认值）
go run ./cmd/server/
```

默认凭证：`minioadmin` / `minioadmin`，默认存储目录：`./data`

## 配置说明

```json
{
  "port": 9000,
  "backend_type": "local",
  "root": "./data",
  "access_key": "minioadmin",
  "secret_key": "minioadmin",
  "max_body_size": 10485760
}
```

| 参数 | 说明 | 默认值 |
|------|------|--------|
| `port` | 监听端口 | 9000 |
| `backend_type` | 存储后端类型 | `"local"` |
| `root` | 数据存储根目录 | `./data` |
| `access_key` | 访问密钥 | `minioadmin` |
| `secret_key` | 密钥 | `minioadmin` |
| `max_body_size` | 单次请求体大小限制（字节） | 10485760（10MB） |

CLI flags 优先级高于配置文件，环境变量 `TOS_ENDPOINT` / `TOS_ACCESS_KEY` / `TOS_SECRET_KEY` 可覆盖客户端配置。

## 磁盘布局

```
data/
  mybucket/
    photo.jpg              ← 原始文件
    photo.jpg.meta         ← JSON 元数据
    docs/
      report.pdf
      report.pdf.meta
    .uploads/              ← Multipart 临时目录（ListObjects 不可见）
      {uploadId}/
        info.json
        part-0001.bin
        part-0001.bin.meta
```

## 使用示例

### CLI 客户端

```bash
# 配置连接
go run ./cmd/client/ config --endpoint http://localhost:9000 --access-key minioadmin --secret-key minioadmin

# 创建 Bucket
go run ./cmd/client/ mb test-bucket

# 上传文件
go run ./cmd/client/ cp readme.md s3://test-bucket/readme.md

# 列出 Bucket
go run ./cmd/client/ ls

# 列出对象
go run ./cmd/client/ ls test-bucket

# 下载文件
go run ./cmd/client/ cp s3://test-bucket/readme.md ./downloaded.md

# 查看元数据
go run ./cmd/client/ stat s3://test-bucket/readme.md

# 输出到 stdout
go run ./cmd/client/ cat s3://test-bucket/readme.md

# 删除文件
go run ./cmd/client/ rm s3://test-bucket/readme.md

# 删除 Bucket
go run ./cmd/client/ rb test-bucket
```

### curl 示例

```bash
# 创建 Bucket
curl -X PUT http://minioadmin:minioadmin@localhost:9000/mybucket

# 上传文件
curl -X PUT -T photo.jpg http://localhost:9000/mybucket/photo.jpg

# 列出对象
curl http://minioadmin:minioadmin@localhost:9000/mybucket

# 下载文件
curl -O http://localhost:9000/mybucket/photo.jpg
```

> 注意：curl 的签名方式与 AWS Sig V2 不同，仅供简单测试。生产环境请使用 CLI 客户端或 Web UI。

### Multipart Upload（大文件）

```bash
# Multipart Upload 通过 S3 API 直接使用，支持 > 10MB 的文件
# 当前 CLI 客户端暂不支持自动分片，可使用 Web UI 或 curl

# Web UI 支持拖拽上传，会自动使用 Multipart Upload
# 浏览器访问 http://localhost:9000/_ui/
```

### Web UI

浏览器访问 http://localhost:9000/_ui/，默认凭证 `minioadmin` / `minioadmin`。

功能：Bucket 管理、对象浏览、前缀导航、文件拖拽上传、下载、删除。

### Metrics

```bash
curl http://localhost:9000/_metrics
```

返回 JSON 格式的运行时统计（请求数、错误数、Bucket 数、存储字节数）。

## 运行测试

```bash
# 全量集成测试（Phase 1-8）
go run ./test/

# 运行指定 Phase
go run ./test/ phase1
go run ./test/ phase8
```
