package cluster

import (
	"encoding/json"
	"log/slog"
	"math/rand"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

const (
	pingTimeout     = 500 * time.Millisecond
	pingReqTimeout  = 500 * time.Millisecond
	indirectProbes  = 2
	maxPayloadSize  = 10
	deadGCTimeout   = 30 * time.Second
)

// GossipMembership 基于 SWIM 简化版的 Gossip 协议成员管理器。
type GossipMembership struct {
	self       NodeID
	selfInfo   *NodeInfo
	members    map[NodeID]*NodeInfo
	mu         sync.RWMutex
	transport  *Transport
	httpServer *http.Server
	seedNodes  []string
	interval   time.Duration
	suspectTimeout time.Duration
	stopCh     chan struct{}
	wg         sync.WaitGroup
	seq        atomic.Int64

	onJoinHandlers  []NodeChangeHandler
	onLeaveHandlers []NodeChangeHandler
}

// NewGossipMembership 创建成员管理器。
func NewGossipMembership(self NodeID, addr string, seeds []string) *GossipMembership {
	info := &NodeInfo{
		ID:          self,
		Addr:        addr,
		State:       StateAlive,
		Incarnation: 0,
	}
	return &GossipMembership{
		self:           self,
		selfInfo:       info,
		members:        map[NodeID]*NodeInfo{self: info},
		transport:      NewTransport(addr, 3*time.Second),
		seedNodes:      seeds,
		interval:       1 * time.Second,
		suspectTimeout: 5 * time.Second,
		stopCh:         make(chan struct{}),
	}
}

// SetInterval 设置 gossip 间隔。
func (gm *GossipMembership) SetInterval(d time.Duration) {
	gm.interval = d
}

// SetSuspectTimeout 设置 suspect → dead 超时。
func (gm *GossipMembership) SetSuspectTimeout(d time.Duration) {
	gm.suspectTimeout = d
}

// SetTransport 设置自定义 Transport（用于测试）。
func (gm *GossipMembership) SetTransport(t *Transport) {
	gm.transport = t
}

// Transport 获取当前 Transport（用于注册 HTTP handler）。
func (gm *GossipMembership) Transport() *Transport {
	return gm.transport
}

// nextSeq 递增并返回消息序列号。
func (gm *GossipMembership) nextSeq() int64 {
	return gm.seq.Add(1)
}

// Self 返回本节点 ID。
func (gm *GossipMembership) Self() NodeID {
	return gm.self
}

// SelfInfo 返回本节点信息。
func (gm *GossipMembership) SelfInfo() *NodeInfo {
	gm.mu.RLock()
	defer gm.mu.RUnlock()
	return gm.selfInfo
}

// OnJoin 注册节点加入回调。
func (gm *GossipMembership) OnJoin(handler NodeChangeHandler) {
	gm.mu.Lock()
	defer gm.mu.Unlock()
	gm.onJoinHandlers = append(gm.onJoinHandlers, handler)
}

// OnLeave 注册节点离开回调。
func (gm *GossipMembership) OnLeave(handler NodeChangeHandler) {
	gm.mu.Lock()
	defer gm.mu.Unlock()
	gm.onLeaveHandlers = append(gm.onLeaveHandlers, handler)
}

// fireJoin 触发节点加入回调。
func (gm *GossipMembership) fireJoin(nodeID NodeID) {
	gm.mu.RLock()
	handlers := make([]NodeChangeHandler, len(gm.onJoinHandlers))
	copy(handlers, gm.onJoinHandlers)
	gm.mu.RUnlock()
	for _, h := range handlers {
		h(nodeID)
	}
}

// fireLeave 触发节点离开回调。
func (gm *GossipMembership) fireLeave(nodeID NodeID) {
	gm.mu.RLock()
	handlers := make([]NodeChangeHandler, len(gm.onLeaveHandlers))
	copy(handlers, gm.onLeaveHandlers)
	gm.mu.RUnlock()
	for _, h := range handlers {
		h(nodeID)
	}
}

// randomPayload 从成员列表中随机选取最多 maxPayloadSize 个成员状态用于 piggyback 传播。
func (gm *GossipMembership) randomPayload(exclude NodeID) []NodeInfo {
	gm.mu.RLock()
	defer gm.mu.RUnlock()

	alive := make([]*NodeInfo, 0)
	for _, info := range gm.members {
		if info.ID != exclude && (info.State == StateAlive || info.State == StateSuspect) {
			alive = append(alive, info)
		}
	}

	// 随机选取。
	rand.Shuffle(len(alive), func(i, j int) {
		alive[i], alive[j] = alive[j], alive[i]
	})

	n := len(alive)
	if n > maxPayloadSize {
		n = maxPayloadSize
	}

	payload := make([]NodeInfo, n)
	for i := 0; i < n; i++ {
		payload[i] = *alive[i]
	}
	return payload
}

// mergePayload 合并收到的成员状态到本地成员表。
func (gm *GossipMembership) mergePayload(payload []NodeInfo) {
	gm.mu.Lock()
	defer gm.mu.Unlock()

	for _, remote := range payload {
		local, exists := gm.members[remote.ID]
		if !exists {
			// 新节点，直接加入（alive 状态）。
			gm.members[remote.ID] = &NodeInfo{
				ID:          remote.ID,
				Addr:        remote.Addr,
				State:       StateAlive,
				Incarnation: remote.Incarnation,
			}
			slog.Info("new node discovered via gossip", "node", remote.ID)
			go gm.fireJoin(remote.ID)
			continue
		}

		// 如果收到关于自己的信息，且 incarnation 更高，需要反驳。
		if remote.ID == gm.self && remote.Incarnation > gm.selfInfo.Incarnation {
			gm.selfInfo.Incarnation = remote.Incarnation + 1
			slog.Info("refuting suspect rumor, incremented incarnation",
				"node", gm.self, "incarnation", gm.selfInfo.Incarnation)
			continue
		}

		// incarnation 更高则更新状态。
		if remote.Incarnation > local.Incarnation {
			oldState := local.State
			local.Incarnation = remote.Incarnation
			local.State = remote.State
			local.Addr = remote.Addr

			if oldState != StateDead && remote.State == StateDead {
				slog.Info("node marked dead via gossip", "node", remote.ID)
				go gm.fireLeave(remote.ID)
			}
			if oldState == StateDead && remote.State == StateAlive {
				slog.Info("node resurrected via gossip", "node", remote.ID)
				go gm.fireJoin(remote.ID)
			}
		}
	}
}

// AliveNodes 返回所有存活节点列表。
func (gm *GossipMembership) AliveNodes() []*NodeInfo {
	gm.mu.RLock()
	defer gm.mu.RUnlock()

	var result []*NodeInfo
	for _, info := range gm.members {
		if info.State == StateAlive {
			result = append(result, info)
		}
	}
	return result
}

// NodeIDs 返回所有存活节点的 ID 列表。
func (gm *GossipMembership) NodeIDs() []NodeID {
	gm.mu.RLock()
	defer gm.mu.RUnlock()

	var result []NodeID
	for _, info := range gm.members {
		if info.State == StateAlive {
			result = append(result, info.ID)
		}
	}
	return result
}

// IsAlive 检查指定节点是否存活。
func (gm *GossipMembership) IsAlive(id NodeID) bool {
	gm.mu.RLock()
	defer gm.mu.RUnlock()
	info, exists := gm.members[id]
	return exists && info.State == StateAlive
}

// GetMember 获取指定节点的信息。
func (gm *GossipMembership) GetMember(id NodeID) *NodeInfo {
	gm.mu.RLock()
	defer gm.mu.RUnlock()
	if info, exists := gm.members[id]; exists {
		copy := *info
		return &copy
	}
	return nil
}

// AllMembers 返回所有成员（包括 dead）。
func (gm *GossipMembership) AllMembers() []NodeInfo {
	gm.mu.RLock()
	defer gm.mu.RUnlock()

	result := make([]NodeInfo, 0, len(gm.members))
	for _, info := range gm.members {
		result = append(result, *info)
	}
	return result
}

// Start 启动 gossip 协议（后台 goroutine）。
func (gm *GossipMembership) Start() {
	gm.wg.Add(1)
	go gm.gossipLoop()
}

// Stop 停止 gossip 协议。
func (gm *GossipMembership) Stop() {
	close(gm.stopCh)
	gm.wg.Wait()
}

// Join 向种子节点发送加入请求。
func (gm *GossipMembership) Join() error {
	gm.mu.RLock()
	selfCopy := *gm.selfInfo
	gm.mu.RUnlock()

	for _, seed := range gm.seedNodes {
		if seed == gm.self {
			continue
		}

		slog.Info("joining cluster via seed", "seed", seed)
		resp, err := gm.transport.Join(NodeID(seed), &selfCopy)
		if err != nil {
			slog.Warn("failed to join via seed", "seed", seed, "err", err)
			continue
		}

		gm.mergePayload(resp.Members)
		slog.Info("joined cluster", "seed", seed, "members_seen", len(resp.Members))
		return nil
	}

	if len(gm.seedNodes) > 0 {
		slog.Warn("failed to join any seed node, running as single node")
	}
	return nil
}

// Leave 主动离开集群。
func (gm *GossipMembership) Leave() {
	// 通知所有存活节点。
	aliveNodes := gm.AliveNodes()
	for _, node := range aliveNodes {
		if node.ID == gm.self {
			continue
		}
		if err := gm.transport.Leave(node.ID, gm.self); err != nil {
			slog.Debug("failed to notify leave", "target", node.ID, "err", err)
		}
	}

	// 更新自己的状态。
	gm.mu.Lock()
	gm.selfInfo.State = StateLeft
	gm.members[gm.self].State = StateLeft
	gm.mu.Unlock()
}

// gossipLoop gossip 主循环。
func (gm *GossipMembership) gossipLoop() {
	defer gm.wg.Done()

	ticker := time.NewTicker(gm.interval)
	defer ticker.Stop()

	for {
		select {
		case <-gm.stopCh:
			return
		case <-ticker.C:
			gm.gossipCycle()
		}
	}
}

// gossipCycle 执行一次 gossip 周期。
func (gm *GossipMembership) gossipCycle() {
	peers := gm.AliveNodes()
	if len(peers) <= 1 {
		return // 只有自己，无需 gossip
	}

	// 随机选一个 peer（排除自己）。
	target := peers[rand.Intn(len(peers))]
	if target.ID == gm.self {
		return
	}

	gm.mu.RLock()
	selfIncarnation := gm.selfInfo.Incarnation
	gm.mu.RUnlock()

	req := &PingRequest{
		From:        gm.self,
		Incarnation: selfIncarnation,
		Payload:     gm.randomPayload(target.ID),
		Seq:         gm.nextSeq(),
	}

	// 1. 直接 Ping。
	resp, err := gm.transport.Ping(target.ID, req)
	if err == nil {
		gm.mergePayload(resp.Payload)
		gm.mergePayload([]NodeInfo{*target}) // 确认目标存活
		return
	}

	slog.Debug("ping failed", "target", target.ID, "err", err)

	// 2. 间接 Ping：选 indirectProbes 个其他节点帮忙探测。
	others := make([]*NodeInfo, 0, len(peers))
	for _, p := range peers {
		if p.ID != gm.self && p.ID != target.ID {
			others = append(others, p)
		}
	}
	rand.Shuffle(len(others), func(i, j int) {
		others[i], others[j] = others[j], others[i]
	})

	probeCount := indirectProbes
	if probeCount > len(others) {
		probeCount = len(others)
	}

	acked := false
	for i := 0; i < probeCount; i++ {
		pingReq := &PingReqRequest{
			Target:      target.ID,
			From:        gm.self,
			Incarnation: selfIncarnation,
			Payload:     req.Payload,
			Seq:         gm.nextSeq(),
		}
		resp, err := gm.transport.PingReq(others[i].ID, pingReq)
		if err == nil && resp != nil {
			gm.mergePayload(resp.Payload)
			acked = true
			break
		}
	}

	if acked {
		// 间接探测成功，目标仍然存活。
		gm.mergePayload([]NodeInfo{*target})
		return
	}

	// 3. 标记为 Suspect。
	gm.mu.Lock()
	if info, exists := gm.members[target.ID]; exists {
		if info.State == StateAlive {
			info.State = StateSuspect
			info.Incarnation++
			slog.Info("node marked suspect", "node", target.ID)
		}
	}
	gm.mu.Unlock()

	// 启动超时计时器，suspectTimeout 后标记为 dead。
	go func(nodeID NodeID, incarnation int64) {
		select {
		case <-gm.stopCh:
			return
		case <-time.After(gm.suspectTimeout):
			gm.mu.Lock()
			info, exists := gm.members[nodeID]
			if exists && info.State == StateSuspect && info.Incarnation == incarnation {
				info.State = StateDead
				slog.Info("node marked dead", "node", nodeID)
				gm.mu.Unlock()
				gm.fireLeave(nodeID)
			} else {
				gm.mu.Unlock()
			}
		}
	}(target.ID, gm.GetMember(target.ID).Incarnation)
}

// --- HTTP Handler ---

// ServeHTTP 处理集群内部 HTTP 请求（保留给直接调用场景）。
func (gm *GossipMembership) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/_cluster/ping":
		gm.HandlePing(w, r)
	case "/_cluster/ping-req":
		gm.HandlePingReq(w, r)
	case "/_cluster/join":
		gm.HandleJoin(w, r)
	case "/_cluster/leave":
		gm.HandleLeave(w, r)
	case "/_cluster/members":
		gm.HandleMembers(w, r)
	default:
		http.NotFound(w, r)
	}
}

// HandlePing 处理 ping 请求。
func (gm *GossipMembership) HandlePing(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req PingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// 合并 payload。
	gm.mergePayload(req.Payload)

	// 如果 ping 的目标是自己，确保状态是 alive。
	if req.From == gm.self {
		gm.mu.Lock()
		if gm.selfInfo.State != StateAlive {
			gm.selfInfo.State = StateAlive
			gm.selfInfo.Incarnation++
		}
		gm.mu.Unlock()
	}

	gm.mu.RLock()
	selfIncarnation := gm.selfInfo.Incarnation
	gm.mu.RUnlock()

	resp := PingResponse{
		From:        gm.self,
		Incarnation: selfIncarnation,
		Payload:     gm.randomPayload(req.From),
	}
	writeJSON(w, resp)
}

// HandlePingReq 处理间接 ping 请求。
func (gm *GossipMembership) HandlePingReq(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req PingReqRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	gm.mergePayload(req.Payload)

	// 向目标节点发送 ping。
	pingReq := &PingRequest{
		From:        gm.self,
		Incarnation: gm.selfInfo.Incarnation,
		Payload:     gm.randomPayload(req.Target),
		Seq:         gm.nextSeq(),
	}

	// 设置短超时。
	oldTimeout := gm.transport.client.Timeout
	gm.transport.SetTimeout(pingReqTimeout)
	defer gm.transport.SetTimeout(oldTimeout)

	resp, err := gm.transport.Ping(req.Target, pingReq)
	if err != nil {
		// 目标不可达，返回错误。
		writeJSON(w, PingResponse{
			From:        gm.self,
			Incarnation: gm.selfInfo.Incarnation,
		})
		return
	}

	// 合并目标响应中的 payload。
	gm.mergePayload(resp.Payload)

	writeJSON(w, *resp)
}

// HandleJoin 处理节点加入请求。
func (gm *GossipMembership) HandleJoin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req JoinRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	gm.mergePayload([]NodeInfo{req.Node})

	members := gm.AllMembers()
	writeJSON(w, JoinResponse{Members: members})
}

// HandleLeave 处理节点离开请求。
func (gm *GossipMembership) HandleLeave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req LeaveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	gm.mu.Lock()
	if info, exists := gm.members[req.Node]; exists && info.State == StateAlive {
		info.State = StateLeft
		gm.mu.Unlock()
		gm.fireLeave(req.Node)
	} else {
		gm.mu.Unlock()
	}

	w.WriteHeader(http.StatusOK)
}

// HandleMembers 返回当前成员列表。
func (gm *GossipMembership) HandleMembers(w http.ResponseWriter, r *http.Request) {
	members := gm.AllMembers()
	writeJSON(w, members)
}

// writeJSON 写入 JSON 响应。
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
