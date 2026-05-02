# EC 模式（纠删码）

基于 Reed-Solomon 纠删码的存储模式。上传的文件被编码为 K 个数据分片 + M 个校验分片，分布在 N 个磁盘上。任意 M 个磁盘故障仍可恢复完整数据。

## 快速启动

```bash
# 一键启动（后台运行，自动等待就绪）
./example/ec/start.sh start

# 停止
./example/ec/start.sh stop

# 重启
./example/ec/start.sh restart

# 查看状态
./example/ec/start.sh status

# 查看日志
./example/ec/start.sh log
```

手动启动：

```bash
go run ./cmd/server/ --config ./example/ec/config.json
```

## 配置说明

```json
{
  "port": 9000,
  "backend_type": "ec",
  "access_key": "minioadmin",
  "secret_key": "minioadmin",
  "ec": {
    "disks": ["./data/disk-0", "./data/disk-1", "./data/disk-2", "./data/disk-3", "./data/disk-4", "./data/disk-5"],
    "data_shards": 4,
    "parity_shards": 2,
    "meta_root": "./data/meta-root"
  }
}
```

| 参数 | 说明 | 默认值 |
|------|------|--------|
| `port` | 监听端口 | 9000 |
| `backend_type` | 必须为 `"ec"` | — |
| `data_shards` | 数据分片数 K | 4 |
| `parity_shards` | 校验分片数 M | 2 |
| `disks` | 磁盘路径列表，长度 >= K+M | — |
| `meta_root` | EC 元数据独立存储路径 | — |

容错能力：最多可丢失 `parity_shards`（M）个磁盘上的分片。

## 磁盘布局

```
data/
  disk-0/                      ← 磁盘 0
    mybucket/
      photo.jpg                ← 分片 0
  disk-1/                      ← 磁盘 1
    mybucket/
      photo.jpg                ← 分片 1
  disk-2/                      ← 磁盘 2
    mybucket/
      photo.jpg                ← 分片 2
  disk-3/                      ← 磁盘 3
    mybucket/
      photo.jpg                ← 分片 3
  disk-4/                      ← 磁盘 4（校验）
    mybucket/
      photo.jpg
  disk-5/                      ← 磁盘 5（校验）
    mybucket/
      photo.jpg
  meta-root/                   ← EC 元数据（独立存储）
    mybucket/
      photo.jpg.ec-meta
```

每个分片大小 ≈ 原始大小 / K。存储开销 = 原始大小 × (K+M) / K（4+2 配置下为 1.5x）。

## 使用示例

```bash
# 配置客户端
go run ./cmd/client/ config --endpoint http://localhost:9000 --access-key minioadmin --secret-key minioadmin

# 创建 Bucket
go run ./cmd/client/ mb mybucket

# 上传文件（自动编码为 6 个分片）
go run ./cmd/client/ cp photo.jpg s3://mybucket/photo.jpg

# 验证分片分布（每个磁盘都有一个分片）
ls data/disk-0/mybucket/photo.jpg
ls data/disk-1/mybucket/photo.jpg

# 下载文件（正常读取）
go run ./cmd/client/ cp s3://mybucket/photo.jpg ./downloaded.jpg

# 模拟磁盘故障：删除一个分片
rm data/disk-0/mybucket/photo.jpg

# 下载文件（自动恢复 + 自修复）
go run ./cmd/client/ cp s3://mybucket/photo.jpg ./recovered.jpg

# 检查自修复：分片已被写回
ls data/disk-0/mybucket/photo.jpg

# 最多可同时删除 2 个分片（parity_shards=2）
rm data/disk-0/mybucket/photo.jpg
rm data/disk-1/mybucket/photo.jpg
go run ./cmd/client/ cp s3://mybucket/photo.jpg ./recovered2.jpg  # 仍然成功
```

## 运行测试

```bash
# EC 模式集成测试（服务器需使用此配置启动）
go run ./cmd/server/ --config ./example/ec/config.json &
go run ./test/ phase5

# EC 单元测试（不需要服务器）
go test ./src/ec/...
```

## 存储效率对比

| 模式 | 存储开销 | 容错 |
|------|---------|------|
| 3 副本 | 3x | 丢失 2 个副本 |
| EC 4+2 | 1.5x | 丢失 2 个磁盘 |
| EC 8+4 | 1.5x | 丢失 4 个磁盘 |

## Web UI

浏览器访问 http://localhost:9000/_ui/，默认凭证 `minioadmin` / `minioadmin`。

功能：Bucket 管理、对象浏览、前缀导航、文件拖拽上传、下载、删除。
