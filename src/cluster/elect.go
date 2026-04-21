package cluster

// Leader 返回当前集群的 leader 节点 ID。
// Leader 是所有存活节点中 NodeID 字典序最小的节点。
// 这是一个确定性选举算法：所有节点看到相同的成员列表会计算出相同的 Leader。
func (gm *GossipMembership) Leader() NodeID {
	gm.mu.RLock()
	defer gm.mu.RUnlock()

	var leader NodeID
	for id, info := range gm.members {
		if info.State != StateAlive {
			continue
		}
		if leader == "" || id < leader {
			leader = id
		}
	}
	return leader
}

// IsLeader 检查当前节点是否是 leader。
func (gm *GossipMembership) IsLeader() bool {
	return gm.Leader() == gm.self
}
