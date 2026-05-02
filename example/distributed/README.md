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

- **一致性哈希**：Ketama 风格，FNV-1a 双哈希 + 500 虚拟节点
- **成员管理**：SWIM 简化版 Gossip 协议（Ping → PingReq → Suspect → Dead）
- **Quorum 读写**：N=3 副本，W=2 写仲裁，R=2 读仲裁，R+W > N 保证一致性

## 快速启动

> **重要**：必须启动**全部 3 个节点**后才能进行读写操作。分布式模式使用 Quorum 机制（W=2），至少 2 个节点在线才能写入。只启动 1 个节点时创建 Bucket 或上传文件会报错 `Failed to achieve write quorum`。

```bash
# 一键启动（后台运行 3 个节点，自动等待 Gossip 收敛）
./example/distributed/start.sh start

# 停止所有节点
./example/distributed/start.sh stop

# 重启
./example/distributed/start.sh restart

# 查看各节点状态
./example/distributed/start.sh status

# 查看指定节点日志（默认节点 1）
./example/distributed/start.sh log 2
```

手动启动（每个需要独立的终端）：

```bash
# 终端 1：种子节点（port 9001）
go run ./cmd/server/ --config ./example/distributed/node1-config.json

# 终端 2：节点 2（port 9002，自动加入种子节点）
go run ./cmd/server/ --config ./example/distributed/node2-config.json

# 终端 3：节点 3（port 9003，自动加入种子节点）
go run ./cmd/server/ --config ./example/distributed/node3-config.json
```

节点启动后通过 Gossip 协议自动发现彼此（约 1-2 秒收敛），不需要额外配置。

## 配置说明

每个节点一个配置文件，示例为 3 节点集群：

```json
{
  "port": 9001,
  "root": "./data/node-1",
  "access_key": "minioadmin",
  "secret_key": "minioadmin",
  "backend_type": "distributed",
  "distributed": {
    "node_id": "localhost:9001",
    "seed_nodes": ["localhost:9001"],  // 注意：不含 http:// 前缀
    "replication_factor": 3,
    "read_quorum": 2,
    "write_quorum": 2,
    "virtual_nodes": 500,
    "gossip_interval_ms": 1000,
    "rpc_timeout_ms": 3000
  }
}
```

| 参数 | 说明 | 默认值 |
|------|------|--------|
| `port` | 监听端口 | 9000 |
| `root` | 数据存储目录 | `./data` |
| `backend_type` | 必须为 `"distributed"` | — |
| `node_id` | 节点标识，格式 `host:port` | `localhost:{port}` |
| `seed_nodes` | 种子节点地址列表（不含 `http://` 前缀，首个节点留空） | `[]` |
| `replication_factor` | 副本数 N | 3 |
| `read_quorum` | 读仲裁 R | 2 |
| `write_quorum` | 写仲裁 W | 2 |
| `virtual_nodes` | 一致性哈希虚拟节点数 | 500 |
| `gossip_interval_ms` | Gossip 间隔（毫秒） | 1000 |
| `rpc_timeout_ms` | RPC 超时（毫秒） | 3000 |

Quorum 约束：`R + W > N`，保证读取时至少覆盖一个最新写入的副本。

## 验证集群

```bash
# 查看成员列表（确认 3 个节点都是 alive）
curl http://localhost:9001/_cluster/members
# 等到输出中出现 3 个 "alive": true 的节点后再操作
```

> **必须等待所有节点就绪后再操作**。只有 1 个节点时写入会返回 `Failed to achieve write quorum`。

## 使用示例

```bash
# 配置客户端连接节点 1
go run ./cmd/client/ config --endpoint http://localhost:9001 --access-key minioadmin --secret-key minioadmin

# 创建 Bucket
go run ./cmd/client/ mb mybucket

# 上传文件（自动通过一致性哈希分配到多个节点）
go run ./cmd/client/ cp report.pdf s3://mybucket/report.pdf

# 列出对象
go run ./cmd/client/ ls mybucket

# 下载文件
go run ./cmd/client/ cp s3://mybucket/report.pdf ./downloaded.pdf

# 通过节点 2 读取相同数据（验证副本复制）
go run ./cmd/client/ config --endpoint http://localhost:9002
go run ./cmd/client/ cat s3://mybucket/report.pdf
```

## 模拟节点故障

```bash
# 停掉节点 2（Ctrl+C）

# 节点 1 和节点 3 仍然可以读写（R=2, W=2, 剩余 2 个节点满足 Quorum）
go run ./cmd/client/ config --endpoint http://localhost:9001
go run ./cmd/client/ ls mybucket
go run ./cmd/client/ cp new.txt s3://mybucket/new.txt

# 重新启动节点 2，自动重新加入集群
go run ./cmd/server/ --config ./example/distributed/node2-config.json
```

## 运行测试

```bash
# 分布式集成测试（自动启动 3 个节点进程，不需要手动启动服务器）
go run ./test/ phase6

# 一致性哈希单元测试
go test ./src/hash/...

# Gossip 成员管理单元测试
go test ./src/cluster/...
```

## 存储布局

每个节点使用 LocalBackend 布局，一致性哈希环决定数据分布：

```
data/node-1/              ← 节点 1
  mybucket/
    hello.txt              ← 副本（一致性哈希选中此节点）
    hello.txt.meta
data/node-2/              ← 节点 2
  mybucket/
    hello.txt              ← 副本
    hello.txt.meta
data/node-3/              ← 节点 3
  mybucket/
    hello.txt              ← 副本
    hello.txt.meta
```

## Web UI

通过任意节点的端口访问 Web UI：

- http://localhost:9001/_ui/
- http://localhost:9002/_ui/
- http://localhost:9003/_ui/

默认凭证：`minioadmin` / `minioadmin`。连接任意节点均可操作，数据通过一致性哈希自动分布到集群。

功能：Bucket 管理、对象浏览、前缀导航、文件拖拽上传、下载、删除。
