<!-- tags: distributed, membership, failure-detection -->
# Gossip 协议与集群成员管理

## 概述

在分布式系统中，每个节点需要知道集群中有哪些其他节点以及它们的状态（存活/故障）。
本项目使用 **SWIM 协议**（Scalable Weakly-consistent Infection-style Process Group Membership）
的简化版本来管理集群成员。

**Gossip** 的核心思想：节点之间定期交换信息，像谣言一样在集群中传播。

## 1. 为什么不用中心化方案

| 方案 | 问题 |
|------|------|
| 中心化（如 ZooKeeper） | 引入外部依赖，单点故障风险 |
| 全量广播 | 节点数增加时网络开销爆炸 |
| **Gossip** | 去中心化，每轮只和 1 个节点通信，扩展性好 |

## 2. 节点状态机

每个节点有四种状态：

```
         加入集群
            │
            ▼
        ┌───────┐    超时未响应    ┌─────────┐    suspect 超时    ┌──────┐
        │ Alive │ ──────────────→ │ Suspect │ ────────────────→  │ Dead │
        └───────┘                 └─────────┘                   └──────┘
            ▲                           │                            │
            │        收到反驳（incarnation 更高）                       │
            └───────────────────────────┘                            │
            │                                                         │
            │                      主动离开                            │
            └────────────────────────────────────────────────────→ Left
```

- **Alive**：正常工作，可以接收请求
- **Suspect**：可能故障，等待确认（避免因网络抖动误判）
- **Dead**：确认故障，触发数据迁移
- **Left**：节点主动退出（如优雅关闭）

## 3. 故障检测三阶段

每个 Gossip 周期（默认 1 秒），随机选一个目标节点，执行三级探测：

```
阶段 1: 直接 Ping
    本节点 ──── Ping ────→ 目标节点
    本节点 ←── Pong ────── 目标节点
    ✓ 成功 → 目标仍然存活

阶段 2: 间接 Ping（如果直接 Ping 超时）
    本节点 ── PingReq ──→ 中间节点 ── Ping ──→ 目标节点
    本节点 ←─ Pong ────── 中间节点 ←── Pong ── 目标节点
    ✓ 成功 → 目标存活（可能是本节点和目标之间的网络问题）

阶段 3: 标记 Suspect（如果间接 Ping 也超时）
    标记目标为 Suspect，启动 suspectTimeout（默认 5 秒）计时器
    超时后标记为 Dead，触发 onLeave 回调
```

**为什么需要间接 Ping**？两个节点之间可能有网络分区，但它们各自和其他节点的连接正常。通过第三方节点间接探测，可以更准确地判断是目标节点故障还是网络问题。

## 4. Incarnation 号（化身号）

**问题**：节点 A 标记节点 B 为 Suspect，但 B 实际上还活着，只是网络延迟高。B 怎么反驳？

**解决**：每个节点维护一个 incarnation 号（递增整数）：

```go
// 收到关于自己的 Suspect 信息，且 incarnation 更高
if remote.ID == gm.self && remote.Incarnation > gm.selfInfo.Incarnation {
    gm.selfInfo.Incarnation = remote.Incarnation + 1  // 反驳：自己的 incarnation 更高
}
```

状态更新规则：**只有 incarnation 更高时才接受状态变更**。这防止了旧信息覆盖新信息（状态回滚）。

## 5. Piggyback 传播

每次 Ping/Pong 消息都附带一个随机的成员状态列表（最多 10 个）：

```go
type PingRequest struct {
    From        NodeID     `json:"from"`
    Incarnation int64      `json:"incarnation"`
    Payload     []NodeInfo `json:"payload,omitempty"`  // ← 附带的成员信息
    Seq         int64      `json:"seq"`
}
```

**为什么**：不需要额外的消息来传播成员变更。每条消息都顺便携带一些状态，信息像谣言一样在集群中扩散（因此叫 Gossip）。

## 6. 节点加入与离开

### 加入

```go
func (gm *GossipMembership) Join() error {
    // 向种子节点发送 JoinRequest
    resp, _ := gm.transport.Join(seed, &selfInfo)
    // 种子节点返回当前所有成员列表
    gm.mergePayload(resp.Members)
}
```

新节点联系种子节点获取成员列表，然后开始参与 Gossip。

### 离开

```go
func (gm *GossipMembership) Leave() {
    // 主动通知所有存活节点
    for _, node := range gm.AliveNodes() {
        gm.transport.Leave(node.ID, gm.self)
    }
    gm.selfInfo.State = StateLeft
}
```

## 7. Leader 选举

本项目的 Leader 选举非常简单：**所有存活节点中 NodeID 字典序最小的就是 Leader**。

```go
func (gm *GossipMembership) Leader() NodeID {
    var leader NodeID
    for id, info := range gm.members {
        if info.State == StateAlive && (leader == "" || id < leader) {
            leader = id
        }
    }
    return leader
}
```

这是一个确定性算法——所有节点看到相同的成员列表会计算出相同的 Leader。不需要选举协议，但代价是所有节点看到的成员列表必须一致。

## 8. 开源项目中的使用

| 项目 | 协议 | 说明 |
|------|------|------|
| **SWIM** | 原始论文 | 本项目实现的参考原型 |
| **Serf** | SWIM 变体 | HashiCorp 的集群管理工具 |
| **Consul** | SWIM + Gossip | HashiCorp 的服务发现和配置管理 |
| **Cassandra** | Gossip | 用于节点发现和故障检测 |
| **Riak** | Gossip | 用于集群成员管理和 Hinted Handoff |

Gossip 协议是分布式系统中去中心化成员管理的主流方案，特别适合节点规模在数十到数千的场景。

## 对应实现

| 文件 | 说明 |
|------|------|
| `src/cluster/member.go` | GossipMembership 核心实现（状态机、Ping/Pong、Incarnation） |
| `src/cluster/protocol.go` | 协议消息类型（PingRequest、PingAck、JoinRequest 等） |
| `src/cluster/transport.go` | HTTP RPC 传输层 |
| `src/cluster/node.go` | 节点管理工具函数 |
| `src/cluster/elect.go` | Leader 选举（确定性，NodeID 字典序最小者） |
| `src/cluster/member_test.go` | 单元测试 |

**关键类型：** `GossipMembership`、`NodeInfo`、`NodeID`、`NodeChangeHandler`
**关键函数：** `NewGossipMembership()`、`Start()`、`Stop()`、`Join()`、`Leave()`、`AliveNodes()`、`Leader()`
