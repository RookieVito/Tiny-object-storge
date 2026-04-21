# EC 模式（纠删码）

基于 Reed-Solomon 纠删码的存储模式。上传的文件被编码为 K 个数据分片 + M 个校验分片，分布在 N 个磁盘上。任意 M 个磁盘故障仍可恢复完整数据。

## 配置说明

```json
{
  "port": 9000,
  "backend_type": "ec",
  "ec": {
    "disks": ["./data/disk-0", ..., "./data/disk-5"],  // N 个磁盘路径，N >= K+M
    "data_shards": 4,     // K：数据分片数
    "parity_shards": 2,   // M：校验分片数
    "meta_root": "./data/meta-root"  // EC 元数据独立存储路径
  }
}
```

| 参数 | 说明 | 默认值 |
|------|------|--------|
| `data_shards` | 数据分片数 K | 4 |
| `parity_shards` | 校验分片数 M | 2 |
| `disks` | 磁盘路径列表，长度 >= K+M | - |
| `meta_root` | 元数据存储路径（独立于数据磁盘） | - |

容错能力：最多可丢失 `parity_shards`（M）个磁盘上的分片。

## 启动

```bash
go build -o tiny-storage ./cmd/server/
./tiny-storage --config ./example/ec/config.json
```

## 磁盘布局

```
data/
  disk-0/                      ← 磁盘 0
    mybucket/
      photo.jpg                ← shard-0（数据分片的一部分）
  disk-1/                      ← 磁盘 1
    mybucket/
      photo.jpg                ← shard-1
  disk-2/                      ← 磁盘 2
    mybucket/
      photo.jpg                ← shard-2
  disk-3/                      ← 磁盘 3
    mybucket/
      photo.jpg                ← shard-3
  disk-4/                      ← 磁盘 4
    mybucket/
      photo.jpg                ← shard-4（校验分片）
  disk-5/                      ← 磁盘 5
    mybucket/
      photo.jpg                ← shard-5（校验分片）
  meta-root/                   ← 元数据（独立存储）
    mybucket/
      photo.jpg.ec-meta        ← EC 编码参数 + 原始文件信息
```

每个分片大小 ≈ 原始大小 / K。存储开销 = 原始大小 x (K+M) / K。

## 自修复

当读取文件时发现某个分片缺失（磁盘存活但文件不存在），系统会自动通过 Reed-Solomon 解码恢复所有分片，并将缺失的分片写回原磁盘。

## 使用示例

```bash
# 上传文件（自动编码为 6 个分片）
go run ./cmd/client/ cp photo.jpg s3://mybucket/photo.jpg

# 验证分片分布
ls -la data/disk-0/mybucket/photo.jpg
ls -la data/disk-1/mybucket/photo.jpg
# ... 每个磁盘都有一个分片文件

# 模拟磁盘故障：删除一个分片
rm data/disk-0/mybucket/photo.jpg

# 下载文件（自动恢复）
go run ./cmd/client/ cp s3://mybucket/photo.jpg ./recovered.jpg

# 检查自修复：分片已被写回
ls -la data/disk-0/mybucket/photo.jpg

# 最多可同时删除 2 个分片（parity_shards=2）
rm data/disk-0/mybucket/photo.jpg
rm data/disk-1/mybucket/photo.jpg
go run ./cmd/client/ cp s3://mybucket/photo.jpg ./recovered2.jpg  # 仍然成功
```

## 存储效率对比

| 模式 | 存储开销 | 容错 |
|------|---------|------|
| 3 副本 | 3x | 丢失 2 个副本 |
| EC 4+2 | 1.5x | 丢失 2 个磁盘 |
| EC 8+4 | 1.5x | 丢失 4 个磁盘 |
