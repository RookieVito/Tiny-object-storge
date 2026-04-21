# Local 模式

最简单的存储模式，文件直接以原始形态存放在本地文件系统中。

## 启动

```bash
go build -o tiny-storage ./cmd/server/
./tiny-storage --config ./example/local/config.json
```

也可以不指定配置文件，默认就是 local 模式：

```bash
./tiny-storage --port 9000
```

## 磁盘布局

```
data/
  mybucket/
    photo.jpg              ← 原始文件
    photo.jpg.meta         ← JSON 元数据
    docs/
      report.pdf
      report.pdf.meta
```

## 适用场景

开发调试、单机小规模存储、测试验证。

## CLI 客户端示例

```bash
# 配置连接
go run ./cmd/client/ config --endpoint http://localhost:9000 --access-key minioadmin --secret-key minioadmin

# 创建 Bucket
go run ./cmd/client/ mb test-bucket

# 上传文件
go run ./cmd/client/ cp readme.md s3://test-bucket/readme.md

# 列出对象
go run ./cmd/client/ ls test-bucket

# 下载文件
go run ./cmd/client/ cp s3://test-bucket/readme.md ./downloaded.md

# 查看元数据
go run ./cmd/client/ stat s3://test-bucket/readme.md

# 删除文件
go run ./cmd/client/ rm s3://test-bucket/readme.md
```

## Web UI

浏览器访问 http://localhost:9000/_ui/

默认凭证：`minioadmin` / `minioadmin`
