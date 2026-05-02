# EC Distributed 模式

分布式纠删码存储，将对象 Reed-Solomon 编码为 K 数据分片 + M 校验分片，分片分布到不同节点上。与 `distributed` 模式（完整副本复制）不同，EC Distributed 以更低的存储开销实现容错。

## 与 distributed 模式的对比

| 特性 | distributed (3 节点, N=3) | ec_distributed (6 节点, K=4, M=2) |
|------|---------------------------|-------------------------------------|
| 数据分布 | 完整副本复制 | RS 编码分片分布 |
| 存储开销 | 3 倍 | 1.5 倍（6/4） |
| 容错能力 | 可丢失 1 个节点 | 可丢失 2 个节点 |
| 写入延迟 | 2 个节点确认 | 4 个节点确认 |
| 适用场景 | 读多写少、需低延迟读 | 大文件存储、带宽受限 |

## 架构

```
                 Client
                   │
         ┌────┬───┼───┬────┐
         ▼    ▼   ▼   ▼    ▼
       N1   N2   N3  N4   N5   N6
      9101 9102 9103 9104 9105 9106
         │    │   │   │    │
         └────────Gossip────────┘

上传 report.pdf（600KB）：
  RS 编码(K=4,M=2) → [Shard0: 150KB] [Shard1: 150KB] [Shard2: 150KB] [Shard3: 150KB]
                      [Shard4: 150KB] [Shard5: 150KB]
  一致性哈希 → N1→Shard0  N3→Shard1  N5→Shard2  N2→Shard3
               N4→Shard4  N6→Shard5

下载 report.pdf：
  读取任意 K=4 个分片 → RS 解码 → 还原完整文件
  （N3、N5 故障时，读取 N1(Shard0) + N2(Shard3) + N4(Shard4) + N6(Shard5) 即可恢复）
```

## 快速启动

> **重要**：必须启动**全部 6 个节点**后才能进行读写操作。写入需要至少 K=4 个节点，读取需要至少 K=4 个节点。

```bash
# 一键启动（后台运行 6 个节点，自动等待 Gossip 收敛）
./example/ec_distributed/start.sh start

# 停止所有节点
./example/ec_distributed/start.sh stop

# 重启
./example/ec_distributed/start.sh restart

# 查看各节点状态
./example/ec_distributed/start.sh status

# 查看指定节点日志（默认节点 1）
./example/ec_distributed/start.sh log 3
```

手动启动（每个需要独立的终端）：

```bash
# 终端 1：种子节点（port 9101）
go run ./cmd/server/ --config ./example/ec_distributed/node1-config.json

# 终端 2-6：工作节点（自动加入种子节点）
go run ./cmd/server/ --config ./example/ec_distributed/node2-config.json
go run ./cmd/server/ --config ./example/ec_distributed/node3-config.json
go run ./cmd/server/ --config ./example/ec_distributed/node4-config.json
go run ./cmd/server/ --config ./example/ec_distributed/node5-config.json
go run ./cmd/server/ --config ./example/ec_distributed/node6-config.json
```

## 验证集群

```bash
# 查看成员列表（确认 6 个节点都是 alive）
curl http://localhost:9101/_cluster/members | python3 -m json.tool
# 等到输出中出现 6 个 "state": 0 的节点后再操作
```

## 配置说明

每个节点一个配置文件，示例为 6 节点集群（K=4, M=2）：

```json
{
  "port": 9101,
  "root": "./data/ec-node-1",
  "access_key": "minioadmin",
  "secret_key": "minioadmin",
  "backend_type": "ec_distributed",
  "ec": {
    "data_shards": 4,
    "parity_shards": 2
  },
  "distributed": {
    "node_id": "localhost:9101",
    "seed_nodes": [],
    "replication_factor": 2,
    "virtual_nodes": 500,
    "gossip_interval_ms": 1000,
    "rpc_timeout_ms": 3000
  }
}
```

| 参数 | 说明 | 默认值 |
|------|------|--------|
| `backend_type` | 必须为 `"ec_distributed"` | — |
| `ec.data_shards` | 数据分片数 K（解码所需最小分片数） | 4 |
| `ec.parity_shards` | 校验分片数 M（可容忍的最大故障节点数） | 2 |
| `distributed.replication_factor` | EC 元数据副本数 R（至少为 2） | 2 |
| `distributed.node_id` | 节点标识，格式 `host:port` | `localhost:{port}` |
| `distributed.seed_nodes` | 种子节点地址列表（首个节点留空 `[]`） | `[]` |
| `distributed.virtual_nodes` | 一致性哈希虚拟节点数 | 500 |
| `distributed.gossip_interval_ms` | Gossip 间隔（毫秒） | 1000 |
| `distributed.rpc_timeout_ms` | RPC 超时（毫秒） | 3000 |

**节点数约束**：集群节点数 ≥ K + M（6 ≥ 4 + 2），每个分片需要独立的节点存储。

## 使用示例

```bash
# 配置客户端连接节点 1
go run ./cmd/client/ config --endpoint http://localhost:9101 --access-key minioadmin --secret-key minioadmin

# 创建 Bucket
go run ./cmd/client/ mb mybucket

# 上传文件（自动 RS 编码为 6 个分片，分布到不同节点）
go run ./cmd/client/ cp report.pdf s3://mybucket/report.pdf

# 列出对象
go run ./cmd/client/ ls mybucket

# 下载文件（自动收集分片，RS 解码还原）
go run ./cmd/client/ cp s3://mybucket/report.pdf ./downloaded.pdf

# 通过任意节点读取相同数据（验证分片分布）
go run ./cmd/client/ config --endpoint http://localhost:9104
go run ./cmd/client/ cat s3://mybucket/report.pdf
```

## 验证分片分布

上传后可以在各节点数据目录中观察到分片存储（而非完整文件）：

```bash
# 查看各节点存储的分片
for i in 1 2 3 4 5 6; do
  echo "=== Node $i ==="
  ls data/ec-node-$i/mybucket/.ec-shards/ 2>/dev/null || echo "(empty)"
done

# 典型输出（分片由一致性哈希决定分布）：
# === Node 1 ===
# report.pdf#0              ← shard 0 数据（约原始大小的 1/4）
# === Node 2 ===
# report.pdf#3              ← shard 3
# === Node 3 ===
# report.pdf#1              ← shard 1
# === Node 4 ===
# report.pdf#4              ← shard 4（校验分片）
# === Node 5 ===
# report.pdf#2              ← shard 2
# === Node 6 ===
# report.pdf#5              ← shard 5（校验分片）

# 查看 EC 分布元数据（记录分片到节点的映射）
cat data/ec-node-1/mybucket/.ec-meta/report.pdf | python3 -m json.tool
# 输出示例：
# {
#   "key": "report.pdf",
#   "original_size": 614400,
#   "shard_size": 153600,
#   "data_shards": 4,
#   "parity_shards": 2,
#   "shard_nodes": ["localhost:9101","localhost:9103","localhost:9105","localhost:9102","localhost:9104","localhost:9106"]
# }
```

## 模拟节点故障

EC Distributed 的核心优势在于容忍多节点故障：

```bash
# 停掉节点 3 和节点 5（Ctrl+C）
# 此时仍有 4 个节点（K=4），满足读写要求

go run ./cmd/client/ config --endpoint http://localhost:9101
go run ./cmd/client/ cat s3://mybucket/report.pdf
# 成功：从剩余 4 个节点的分片解码还原

# 仍可写入新数据（4 个节点满足 K=4）
go run ./cmd/client/ cp newdata.bin s3://mybucket/newdata.bin

# 重新启动节点 3 和节点 5，自动重新加入集群
go run ./cmd/server/ --config ./example/ec_distributed/node3-config.json
go run ./cmd/server/ --config ./example/ec_distributed/node5-config.json
```

> **注意**：
> - 在线节点数 ≥ K 时可正常读写
> - 在线节点数 < K 时将无法读取数据（EC 解码需要至少 K 个分片）
> - 在线节点数 < K + M 时，新写入的数据无法提供 M 级别容错

## 运行测试

```bash
# 一致性哈希单元测试
go test ./src/hash/...

# Gossip 成员管理单元测试
go test ./src/cluster/...

# 全量回归测试
./test/scripts/run.sh
```

## 存储布局

每个节点使用 LocalBackend 布局，分片通过一致性哈希分布：

```
data/ec-node-1/              ← 节点 1
  mybucket/
    .ec-shards/
      report.pdf#0           ← shard 0 数据（约原始大小的 1/4，非完整文件）
    .ec-shard-meta/
      report.pdf#0           ← shard 0 元数据（ShardIndex、ShardSize 等）
    .ec-meta/
      report.pdf             ← EC 分布元数据（ShardNodes 映射，R=2 副本）
data/ec-node-2/              ← 节点 2
  mybucket/
    .ec-shards/
      report.pdf#3
    .ec-shard-meta/
      report.pdf#3
    .ec-meta/
      report.pdf             ← EC 元数据副本
data/ec-node-3/              ← 节点 3
  mybucket/
    .ec-shards/
      report.pdf#1
    .ec-shard-meta/
      report.pdf#1
data/ec-node-4/              ← 节点 4
  mybucket/
    .ec-shards/
      report.pdf#4           ← 校验分片
    .ec-shard-meta/
      report.pdf#4
data/ec-node-5/              ← 节点 5
  mybucket/
    .ec-shards/
      report.pdf#2
    .ec-shard-meta/
      report.pdf#2
data/ec-node-6/              ← 节点 6
  mybucket/
    .ec-shards/
      report.pdf#5           ← 校验分片
    .ec-shard-meta/
      report.pdf#5
```

## Web UI

通过任意节点的端口访问 Web UI：

- http://localhost:9101/_ui/
- http://localhost:9102/_ui/
- http://localhost:9103/_ui/
- http://localhost:9104/_ui/
- http://localhost:9105/_ui/
- http://localhost:9106/_ui/

默认凭证：`minioadmin` / `minioadmin`。连接任意节点均可操作，数据通过 RS 编码自动分片分布到集群。
