package hash

import (
	"hash/fnv"
	"sort"
	"strconv"
	"sync"
)

// ConsistentHash 一致性哈希环，使用虚拟节点实现均匀分布。
// Ketama 风格：每个物理节点在环上放置 replicas 个虚拟节点。
type ConsistentHash struct {
	mu       sync.RWMutex
	ring     []uint32          // 排序后的哈希值环
	nodes    map[uint32]string // hash → nodeID 映射
	nodeSet  map[string]bool   // 所有物理节点集合
	replicas int               // 每个物理节点的虚拟节点数
}

// NewConsistentHash 创建一致性哈希环。
// replicas: 每个物理节点的虚拟节点数，推荐 150。
func NewConsistentHash(replicas int) *ConsistentHash {
	if replicas <= 0 {
		replicas = 150
	}
	return &ConsistentHash{
		ring:     make([]uint32, 0),
		nodes:    make(map[uint32]string),
		nodeSet:  make(map[string]bool),
		replicas: replicas,
	}
}

// hashKey 计算字符串的哈希值。
// 使用双重哈希减少碰撞：先 FNV-1a 32 位，再用 CRC32 混合。
func hashKey(key string) uint32 {
	h1 := fnv.New32a()
	h1.Write([]byte(key))
	v1 := h1.Sum32()

	// 第二轮哈希混合，打乱 FNV 的分布偏差。
	h2 := fnv.New32a()
	h2.Write([]byte{byte(v1), byte(v1 >> 8), byte(v1 >> 16), byte(v1 >> 24)})
	return h2.Sum32()
}

// AddNode 将物理节点加入哈希环。
func (ch *ConsistentHash) AddNode(nodeID string) {
	ch.mu.Lock()
	defer ch.mu.Unlock()

	if ch.nodeSet[nodeID] {
		return // 已存在
	}
	ch.nodeSet[nodeID] = true

	for i := 0; i < ch.replicas; i++ {
		vnode := nodeID + "#" + strconv.Itoa(i)
		h := hashKey(vnode)
		ch.ring = append(ch.ring, h)
		ch.nodes[h] = nodeID
	}
	sort.Slice(ch.ring, func(i, j int) bool {
		return ch.ring[i] < ch.ring[j]
	})
}

// RemoveNode 将物理节点从哈希环移出。
func (ch *ConsistentHash) RemoveNode(nodeID string) {
	ch.mu.Lock()
	defer ch.mu.Unlock()

	if !ch.nodeSet[nodeID] {
		return
	}
	delete(ch.nodeSet, nodeID)

	newRing := make([]uint32, 0, len(ch.ring)-ch.replicas)
	for _, h := range ch.ring {
		if ch.nodes[h] == nodeID {
			delete(ch.nodes, h)
		} else {
			newRing = append(newRing, h)
		}
	}
	ch.ring = newRing
}

// GetNode 返回 key 应该存储的主节点。
// 从 key 的哈希值开始在环上顺时针查找第一个虚拟节点对应的物理节点。
func (ch *ConsistentHash) GetNode(key string) string {
	ch.mu.RLock()
	defer ch.mu.RUnlock()

	if len(ch.ring) == 0 {
		return ""
	}

	h := hashKey(key)
	idx := sort.Search(len(ch.ring), func(i int) bool {
		return ch.ring[i] >= h
	})

	// 环绕：如果 idx 到达末尾，回到起点。
	if idx >= len(ch.ring) {
		idx = 0
	}

	return ch.nodes[ch.ring[idx]]
}

// GetNodes 返回 key 的 N 个副本节点。
// 从主节点开始在环上顺时针取 N 个不同物理节点。
// 如果可用节点数不足 N，返回所有可用节点。
func (ch *ConsistentHash) GetNodes(key string, n int) []string {
	ch.mu.RLock()
	defer ch.mu.RUnlock()

	if len(ch.ring) == 0 || n <= 0 {
		return nil
	}

	available := make([]string, 0, len(ch.nodeSet))
	for id := range ch.nodeSet {
		available = append(available, id)
	}
	if len(available) < n {
		n = len(available)
	}

	h := hashKey(key)
	startIdx := sort.Search(len(ch.ring), func(i int) bool {
		return ch.ring[i] >= h
	})
	if startIdx >= len(ch.ring) {
		startIdx = 0
	}

	result := make([]string, 0, n)
	seen := make(map[string]bool)
	// 从 startIdx 开始遍历环，最多遍历一圈。
	for i := 0; i < len(ch.ring) && len(result) < n; i++ {
		idx := (startIdx + i) % len(ch.ring)
		nodeID := ch.nodes[ch.ring[idx]]
		if !seen[nodeID] {
			seen[nodeID] = true
			result = append(result, nodeID)
		}
	}

	return result
}

// Size 返回环上的物理节点数量。
func (ch *ConsistentHash) Size() int {
	ch.mu.RLock()
	defer ch.mu.RUnlock()
	return len(ch.nodeSet)
}
