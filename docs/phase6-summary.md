<!-- tags: phase-summary -->
# Phase 6 完成总结

## 1. 完成状态：全部完成

Phase 6 新增 11 个文件（consistent.go、consistent_test.go、node.go、protocol.go、transport.go、member.go、elect.go、member_test.go、distributed.go、phase6.go、cluster/transport_test.go），
修改 4 个文件（config.go、error.go、router.go、main.go），新增 20 个分布式集成测试和 20 个单元测试全部通过，Phase 1-5 全量回归零回归。

---

## 2. Phase 6 实现内容

### 2.1 一致性哈希（hash/consistent.go）

Ketama 风格一致性哈希环，双哈希 FNV-1a + 虚拟节点：

- 双哈希函数（两轮 FNV-1a + 字节混合）保证均匀分布
- 每物理节点 500 个虚拟节点，标准差 < 10%
- `GetNodes(key, n)` 顺时针取 N 个不同物理节点
- `AddNode` / `RemoveNode` 最小化数据迁移

### 2.2 Gossip 成员管理（cluster/member.go）

SWIM 简化版 Gossip 协议：

- **故障检测**：Ping → PingReq（间接探测）→ Suspect → Dead
- **Incarnation number**：防止 stale rumor 导致误判
- **Piggyback 传播**：每条消息携带最多 10 个随机成员状态
- 回调机制：`OnJoin` / `OnLeave` 注册节点变更回调

### 2.3 Leader Election（cluster/elect.go）

确定性选举：所有存活节点中 NodeID 字典序最小者为 Leader。
纯学习用途，无 split-brain 防护。

### 2.4 节点间 HTTP RPC（cluster/transport.go + protocol.go）

- HTTP RPC 复用 S3 端口，集群内部端点 `/_cluster/*`
- `Ping` / `PingReq` / `Join` / `Leave` / `Replicate` / `GetMembers`
- `StorageRequest` 数据 base64 编码传输
- `http.StripPrefix` 方式注册集群路由（避免 Go ServeMux prefix strip 问题）

### 2.5 DistributedBackend（storage/distributed.go）

实现 `StorageBackend` 接口的分布式后端：

- **Quorum 读写**：N=3（副本数）、W=2（写仲裁）、R=2（读仲裁），R + W > N
- **Coordinator 模式**：任何收到客户端请求的节点都可充当 coordinator
- **PutObject**：一致性哈希选 N 个副本 → 并发写入 → W 个成功即返回
- **GetObject**：一致性哈希选 N 个副本 → 并发读 R 个 → 返回第一个成功结果
- **ListBuckets**：查询所有存活节点 → 按 name 去重合并
- **Ring 更新**：OnJoin/OnLeave 回调自动更新一致性哈希环
- **节点故障容忍**：停止 1 个节点后读写正常（剩余 2 节点 >= W 和 R）

### 2.6 集群内部端点

| Method | Path | 用途 |
|--------|------|------|
| POST | `/_cluster/ping` | Gossip ping |
| POST | `/_cluster/ping-req` | 间接 ping |
| POST | `/_cluster/join` | 节点加入 |
| POST | `/_cluster/leave` | 节点离开 |
| POST | `/_cluster/replicate` | 存储复制请求 |
| GET | `/_cluster/members` | 成员列表 |

### 2.7 配置

新增 `DistributedConfig` 结构体：

```json
{
  "port": 9000,
  "backend_type": "distributed",
  "access_key": "minioadmin",
  "secret_key": "minioadmin",
  "distributed": {
    "node_id": "localhost:9000",
    "seed_nodes": ["localhost:9001", "localhost:9002"],
    "replication_factor": 3,
    "read_quorum": 2,
    "write_quorum": 2,
    "virtual_nodes": 500,
    "gossip_interval_ms": 200,
    "rpc_timeout_ms": 2000
  }
}
```

### 2.8 磁盘布局

每个节点本地存储与其他后端相同（LocalBackend），一致性哈希决定数据分布：

```
# 节点 1（localhost:9000）
data-node1/p6-bucket/hello.txt          # 数据文件
data-node1/p6-bucket/hello.txt.meta     # 元数据

# 节点 2（localhost:9001）
data-node2/p6-bucket/hello.txt          # 副本
data-node2/p6-bucket/hello.txt.meta     # 副本元数据

# 节点 3（localhost:9002）
data-node3/p6-bucket/hello.txt          # 副本
data-node3/p6-bucket/hello.txt.meta     # 副本元数据
```

---

## 3. 依赖关系

```
hash/       ← 新增包，无依赖（纯数据结构）
cluster/    ← 新增包，无外部包依赖（仅 stdlib）
storage/    ← 新增依赖 hash, cluster（DistributedBackend）
```

依赖图保持无环。

---

## 4. 测试覆盖

**一致性哈希单元测试（go test ./src/hash/...）：9 个**
- 空环处理、节点增删、确定性、不同物理节点、分布均匀性、最小迁移、环一致性

**Gossip 成员管理单元测试（go test ./src/cluster/...）：11 个**
- 单节点自注册、Leader 选举确定性、两节点 Join、Ping 往返、PingReq 间接探测、Leave、Incarnation 反驳、Join 回调、GetMembers、HandleMembers

**Phase 6 分布式集成测试（test/phase6.go）：20 个**
- 3 节点自动启动和 gossip 收敛
- 成员发现（3 members alive）
- 基本 Put/Get 往返（通过 node1）
- 跨节点读取（node2/node3 验证副本复制）
- HeadObject Content-Length 验证
- 通过 node2 写入，从 node1/node3 读取
- DeleteObject + 跨节点 404 验证
- ListBuckets 跨节点合并
- 节点故障容忍（停止 node3 后读写正常）
