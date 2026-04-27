<!-- tags: distributed, hashing, data-partitioning -->
# 一致性哈希（Consistent Hashing）

## 概述

在分布式系统中，需要决定数据存储在哪个节点上。最朴素的方法是**哈希取模**：
`node = hash(key) % N`。但这种方法在节点增减时会导致大规模数据迁移。

**一致性哈希** 解决了这个问题：增删节点时，只有少量数据需要迁移。

## 1. 为什么不用哈希取模

```
3 个节点，hash(key) % 3:
  key1 → 节点 A
  key2 → 节点 B
  key3 → 节点 C

新增节点 D（变成 4 个节点），hash(key) % 4:
  key1 → 节点 B    ← 从 A 迁移到 B
  key2 → 节点 C    ← 从 B 迁移到 C
  key3 → 节点 A    ← 从 C 迁移到 A
```

**所有数据的位置都变了**——这在存储系统中是不可接受的，因为大量数据迁移会导致服务不可用。

## 2. 一致性哈希环的基本原理

一致性哈希将整个哈希空间组织成一个**环**（0 ~ 2^32-1）：

```
                     0
                   /   \
                3072   512      ← 节点 A 在位置 3072
               /         \
           2048           1024   ← 节点 B 在位置 2048
              \         /
               \       /
                4096
                   |
                节点 C 在位置 4096
```

**写入规则**：给定一个 key，计算 `hash(key)`，顺时针找到环上的第一个节点，数据就存在那个节点上。

**增删节点的影响**：只在相邻节点之间影响数据分布。

```
新增节点 D（位置 1536）：
  原来在 1024~2048（节点 B）之间的 key，现在变成 1024~1536 归 B，1536~2048 归 D。

  → 只有 B 的部分数据需要迁移到 D，其他节点完全不受影响。
```

## 3. 虚拟节点（Virtual Nodes）

**问题**：如果节点很少（比如 3 个），哈希分布可能不均匀——某些节点承担了更多数据。

**解决**：为每个物理节点创建多个虚拟节点，均匀分布在环上。虚拟节点数通常设为 150~500。

```
物理节点 A 创建 3 个虚拟节点：A#0, A#1, A#2
物理节点 B 创建 3 个虚拟节点：B#0, B#1, B#2
物理节点 C 创建 3 个虚拟节点：C#0, C#1, C#2

环上有 9 个虚拟节点，分布更均匀。
无论 key 落在哪个虚拟节点，最终都映射到对应的物理节点。
```

本项目实现：

```go
// src/hash/consistent.go
type ConsistentHash struct {
    ring     []uint32          // 排序后的哈希值
    nodes    map[uint32]string // hash → 物理节点 ID
    nodeSet  map[string]bool   // 所有物理节点
    replicas int               // 每个物理节点的虚拟节点数
}

func (ch *ConsistentHash) AddNode(nodeID string) {
    for i := 0; i < ch.replicas; i++ {
        vnode := nodeID + "#" + strconv.Itoa(i)  // 虚拟节点名
        h := hashKey(vnode)
        ch.ring = append(ch.ring, h)
        ch.nodes[h] = nodeID                     // 映射回物理节点
    }
    sort.Slice(ch.ring, ...)                     // 保持有序
}
```

## 4. 查找算法

```go
func (ch *ConsistentHash) GetNode(key string) string {
    h := hashKey(key)
    // 二分查找第一个 >= h 的位置
    idx := sort.Search(len(ch.ring), func(i int) bool {
        return ch.ring[i] >= h
    })
    // 环绕：超出末尾则回到起点
    if idx >= len(ch.ring) {
        idx = 0
    }
    return ch.nodes[ch.ring[idx]]  // 返回物理节点
}
```

时间复杂度：`O(log N)`，其中 N 是虚拟节点总数。

## 5. 多副本：GetNodes

分布式存储通常需要多个副本来保证数据安全。`GetNodes(key, n)` 从主节点开始顺时针取 N 个**不同物理节点**：

```go
func (ch *ConsistentHash) GetNodes(key string, n int) []string {
    // 从 key 的位置开始，顺时针遍历环
    // 跳过已见过的物理节点，收集 n 个不同的节点
    seen := make(map[string]bool)
    for i := 0; i < len(ch.ring) && len(result) < n; i++ {
        nodeID := ch.nodes[ch.ring[idx]]
        if !seen[nodeID] {
            seen[nodeID] = true
            result = append(result, nodeID)
        }
    }
    return result
}
```

## 6. 双重哈希

本项目使用双重哈希减少碰撞——先 FNV-1a，再用 CRC32 混合：

```go
func hashKey(key string) uint32 {
    // 第一轮：FNV-1a 32 位
    h1 := fnv.New32a()
    h1.Write([]byte(key))
    v1 := h1.Sum32()

    // 第二轮：用 v1 的字节作为输入再哈希，打乱分布偏差
    h2 := fnv.New32a()
    h2.Write([]byte{byte(v1), byte(v1 >> 8), byte(v1 >> 16), byte(v1 >> 24)})
    return h2.Sum32()
}
```

为什么需要两轮？FNV-1a 在某些输入模式下会出现分布偏差。第二轮哈希以第一轮的结果作为输入，可以有效打散偏差。

## 7. 本项目中的应用

在分布式模式下，一致性哈希决定每个 key 存储在哪些节点：

```go
// 写入时确定副本节点
reps := db.replicas(bucket + "/" + key)  // → ["node-A", "node-C", "node-B"]
// 并发向这 3 个节点写入数据

// 读取时同样确定副本节点
reps := db.replicas(bucket + "/" + key)
// 从其中 R 个成功读取即返回
```

当节点加入或离开时，通过回调自动更新哈希环：

```go
membership.OnJoin(db.onNodeJoin)   // 新节点加入 → ring.AddNode()
membership.OnLeave(db.onNodeLeave) // 节点离开 → ring.RemoveNode()
```

## 8. 开源项目中的使用

| 项目 | 说明 |
|------|------|
| **DynamoDB** | Amazon 的论文中详细描述了一致性哈希 + 虚拟节点的设计 |
| **Cassandra** | 使用一致性哈希进行数据分片，每个 token 范围对应一个节点 |
| **Redis Cluster** | 16384 个 slot 组成的哈希环，每个节点负责一段连续 slot |
| **Ketama** | Memcached 的一致性哈希库，本项目参考的算法来源 |
| **libketama** | Ketama 的 C 实现，是事实标准 |

一致性哈希是分布式系统中最基础的数据分片技术，几乎所有分布式数据库和缓存系统都在使用。
