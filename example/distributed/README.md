# Distributed 模式

多节点分布式存储，使用一致性哈希（Ketama）分配数据、Gossip 协议管理集群成员、Quorum 机制保证读写一致性。

## 架构

```
         Client
           │
    ┌──────┼──────┐
    ▼      ▼      ▼
 Node1  Node2  Node3
 9001    9002    9003
   │       │       │
  └───Gossip 协议──┘
```

- **一致性哈希**：Ketama 风格，FNV-1a 双哈希 + 虚拟节点
- **成员管理**：SWIM 简化版 Gossip 协议
- **Leader 选举**：基于 Gossip 的选举机制
- **Quorum 读写**：W <= N, R <= N, R + W > N

## 配置说明

每个节点一个配置文件，示例为 3 节点集群：

| 参数 | 说明 | 默认值 |
|------|------|--------|
| `node_id` | 节点标识，格式 `host:port` | `localhost:{port}` |
| `seed_nodes` | 种子节点 URL 列表 | `[]` |
| `replication_factor` | 副本数 N | 3 |
| `read_quorum` | 读仲裁 R | 2 |
| `write_quorum` | 写仲裁 W | 2 |
| `virtual_nodes` | 一致性哈希虚拟节点数 | 500 |
| `gossip_interval_ms` | Gossip 间隔（毫秒） | 1000 |
| `rpc_timeout_ms` | RPC 超时（毫秒） | 3000 |

Quorum 约束：`R + W > N`，保证读取时至少覆盖一个最新写入的副本。

## 启动

依次启动 3 个节点（每个需要独立的终端）：

```bash
# 终端 1：启动种子节点
go build -o tiny-storage ./cmd/server/
./tiny-storage --config ./example/distributed/node1-config.json

# 终端 2：启动节点 2（加入节点 1）
./tiny-storage --config ./example/distributed/node2-config.json

# 终端 3：启动节点 3（加入节点 1）
./tiny-storage --config ./example/distributed/node3-config.json
```

节点启动后通过 Gossip 协议自动发现彼此，不需要额外配置。

## 验证集群

```bash
# 查看节点 1 的成员列表
curl http://localhost:9001/_cluster/members

# 通过任意节点访问数据
go run ./cmd/client/ config --endpoint http://localhost:9001 --access-key minioadmin --secret-key minioadmin

go run ./cmd/client/ mb test-bucket
go run ./cmd/client/ cp data.txt s3://test-bucket/data.txt

# 通过其他节点读取相同数据
go run ./cmd/client/ config --endpoint http://localhost:9002 --access-key minioadmin --secret-key minioadmin
go run ./cmd/client/ cat s3://test-bucket/data.txt
```

## 模拟节点故障

```bash
# 停掉节点 2（Ctrl+C）

# 节点 1 和节点 3 仍然可以读写（R=2, W=2, 剩余 2 个节点满足 Quorum）
go run ./cmd/client/ config --endpoint http://localhost:9001
go run ./cmd/client/ ls test-bucket        # 正常
go run ./cmd/client/ cp new.txt s3://test-bucket/new.txt  # 正常

# 重新启动节点 2，自动重新加入集群
./tiny-storage --config ./example/distributed/node2-config.json
```

## 使用示例

```bash
# 配置客户端连接任意节点
go run ./cmd/client/ config --endpoint http://localhost:9001 --access-key minioadmin --secret-key minioadmin

# 创建 Bucket
go run ./cmd/client/ mb mybucket

# 上传文件（自动通过一致性哈希分配到多个节点）
go run ./cmd/client/ cp report.pdf s3://mybucket/report.pdf

# 列出对象
go run ./cmd/client/ ls mybucket

# 下载文件
go run ./cmd/client/ cp s3://mybucket/report.pdf ./downloaded.pdf

# 查看集群成员
curl http://localhost:9001/_cluster/members | python3 -m json.tool
```
