package hash

import (
	"math"
	"strconv"
	"testing"
)

func TestNewConsistentHash(t *testing.T) {
	ch := NewConsistentHash(100)
	if ch.Size() != 0 {
		t.Errorf("expected empty ring, got %d nodes", ch.Size())
	}
	if ch.GetNode("any-key") != "" {
		t.Error("expected empty string for empty ring")
	}
}

func TestAddRemoveNode(t *testing.T) {
	ch := NewConsistentHash(10)

	ch.AddNode("node1")
	if ch.Size() != 1 {
		t.Errorf("expected 1 node, got %d", ch.Size())
	}

	// 重复添加不会增加节点数。
	ch.AddNode("node1")
	if ch.Size() != 1 {
		t.Errorf("expected 1 node after duplicate add, got %d", ch.Size())
	}

	ch.AddNode("node2")
	ch.AddNode("node3")
	if ch.Size() != 3 {
		t.Errorf("expected 3 nodes, got %d", ch.Size())
	}

	ch.RemoveNode("node2")
	if ch.Size() != 2 {
		t.Errorf("expected 2 nodes after remove, got %d", ch.Size())
	}

	// 移除不存在的节点不会出错。
	ch.RemoveNode("nonexistent")
	if ch.Size() != 2 {
		t.Errorf("expected 2 nodes after removing nonexistent, got %d", ch.Size())
	}
}

func TestGetNode_Deterministic(t *testing.T) {
	ch := NewConsistentHash(100)
	ch.AddNode("node1")
	ch.AddNode("node2")
	ch.AddNode("node3")

	// 相同 key 总是映射到相同节点。
	for i := 0; i < 100; i++ {
		key := "test-key"
		node := ch.GetNode(key)
		node2 := ch.GetNode(key)
		if node != node2 {
			t.Errorf("key %q mapped to different nodes: %s vs %s", key, node, node2)
		}
	}
}

func TestGetNodes_DistinctNodes(t *testing.T) {
	ch := NewConsistentHash(100)
	ch.AddNode("node1")
	ch.AddNode("node2")
	ch.AddNode("node3")
	ch.AddNode("node4")
	ch.AddNode("node5")

	nodes := ch.GetNodes("my-object", 3)
	if len(nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(nodes))
	}

	// 所有返回的节点应该不同。
	seen := make(map[string]bool)
	for _, n := range nodes {
		if seen[n] {
			t.Errorf("duplicate node %q in result", n)
		}
		seen[n] = true
	}
}

func TestGetNodes_FewerThanAvailable(t *testing.T) {
	ch := NewConsistentHash(100)
	ch.AddNode("node1")
	ch.AddNode("node2")

	// 请求 5 个节点，但只有 2 个可用。
	nodes := ch.GetNodes("test-key", 5)
	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes (all available), got %d", len(nodes))
	}
}

func TestGetNodes_FewerThanOne(t *testing.T) {
	ch := NewConsistentHash(100)
	ch.AddNode("node1")

	// N=0 或 N<0 返回 nil。
	nodes := ch.GetNodes("test-key", 0)
	if nodes != nil {
		t.Errorf("expected nil for n=0, got %v", nodes)
	}

	nodes = ch.GetNodes("test-key", -1)
	if nodes != nil {
		t.Errorf("expected nil for n=-1, got %v", nodes)
	}
}

func TestDistribution(t *testing.T) {
	ch := NewConsistentHash(500)
	nodeCount := 10
	for i := 0; i < nodeCount; i++ {
		ch.AddNode("node-" + strconv.Itoa(i))
	}

	// 分配 10000 个 key，检查分布均匀性。
	distribution := make(map[string]int)
	totalKeys := 10000
	for i := 0; i < totalKeys; i++ {
		key := "object-" + strconv.Itoa(i)
		node := ch.GetNode(key)
		distribution[node]++
	}

	// 每个节点应该分配到大约 totalKeys/nodeCount 个 key。
	expected := float64(totalKeys) / float64(nodeCount)
	tolerance := 0.15 // 允许 15% 的偏差。

	for i := 0; i < nodeCount; i++ {
		nodeID := "node-" + strconv.Itoa(i)
		count := distribution[nodeID]
		deviation := math.Abs(float64(count)-expected) / expected
		if deviation > tolerance {
			t.Errorf("node %q: %d keys (%.2f%% deviation, expected %.0f±%.0f)",
				nodeID, count, deviation*100, expected, expected*tolerance)
		}
	}
}

func TestRedistribution_Minimal(t *testing.T) {
	ch := NewConsistentHash(500)
	nodeCount := 10
	for i := 0; i < nodeCount; i++ {
		ch.AddNode("node-" + strconv.Itoa(i))
	}

	// 记录添加新节点前的映射。
	totalKeys := 10000
	oldMapping := make(map[string]string)
	for i := 0; i < totalKeys; i++ {
		key := "object-" + strconv.Itoa(i)
		oldMapping[key] = ch.GetNode(key)
	}

	// 添加新节点。
	ch.AddNode("node-10")

	// 计算有多少 key 移动了。
	moved := 0
	for i := 0; i < totalKeys; i++ {
		key := "object-" + strconv.Itoa(i)
		newNode := ch.GetNode(key)
		if newNode != oldMapping[key] {
			moved++
		}
	}

	// 理论上只有约 1/(N+1) = 1/11 ≈ 9% 的 key 应该移动。
	expectedMoved := float64(totalKeys) / float64(nodeCount+1)
	tolerance := 0.5 // 允许 50% 的偏差（因为虚拟节点数有限）。
	maxAllowed := int(expectedMoved * (1 + tolerance))

	if moved > maxAllowed {
		t.Errorf("too many keys moved: %d/%d (%.2f%%), expected ~%.0f (max %d)",
			moved, totalKeys, float64(moved)/float64(totalKeys)*100,
			expectedMoved, maxAllowed)
	}
}

func TestRemoveNode_RingConsistency(t *testing.T) {
	ch := NewConsistentHash(50)
	ch.AddNode("node1")
	ch.AddNode("node2")
	ch.AddNode("node3")

	// 所有 key 应该映射到存活的节点。
	for i := 0; i < 1000; i++ {
		key := "key-" + strconv.Itoa(i)
		node := ch.GetNode(key)
		if node != "node1" && node != "node2" && node != "node3" {
			t.Errorf("key %q mapped to unknown node %q", key, node)
		}
	}

	// 移除 node2 后，所有 key 应该映射到 node1 或 node3。
	ch.RemoveNode("node2")
	for i := 0; i < 1000; i++ {
		key := "key-" + strconv.Itoa(i)
		node := ch.GetNode(key)
		if node != "node1" && node != "node3" {
			t.Errorf("after remove, key %q mapped to removed/unknown node %q", key, node)
		}
	}
}
