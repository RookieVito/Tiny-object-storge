package cluster

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// newTestMembership 创建测试用成员管理器。
func newTestMembership(addr string, seeds []string) *GossipMembership {
	gm := NewGossipMembership(NodeID(addr), addr, seeds)
	gm.SetInterval(200 * time.Millisecond)
	gm.SetSuspectTimeout(1 * time.Second)
	return gm
}

func TestSingleMember(t *testing.T) {
	gm := newTestMembership("localhost:9000", nil)
	gm.Start()
	defer gm.Stop()

	// 单节点应该只有自己。
	nodes := gm.AliveNodes()
	if len(nodes) != 1 {
		t.Fatalf("expected 1 alive node, got %d", len(nodes))
	}
	if nodes[0].ID != "localhost:9000" {
		t.Errorf("expected self, got %s", nodes[0].ID)
	}

	if !gm.IsAlive("localhost:9000") {
		t.Error("self should be alive")
	}
}

func TestLeader_Single(t *testing.T) {
	gm := newTestMembership("localhost:9000", nil)
	defer gm.Stop()

	if !gm.IsLeader() {
		t.Error("single node should be leader")
	}
	if gm.Leader() != "localhost:9000" {
		t.Errorf("expected self as leader, got %s", gm.Leader())
	}
}

func TestLeader_Deterministic(t *testing.T) {
	// 模拟多个节点计算同一个 leader。
	nodes := []string{"c:9000", "a:9000", "b:9000"}

	for _, self := range nodes {
		gm := newTestMembership(self, nil)
		// 手动添加所有节点。
		for _, n := range nodes {
			gm.mergePayload([]NodeInfo{{
				ID:          NodeID(n),
				Addr:        n,
				State:       StateAlive,
				Incarnation: 0,
			}})
		}
		leader := gm.Leader()
		if leader != "a:9000" {
			t.Errorf("node %s computed leader=%s, expected a:9000", self, leader)
		}
	}
}

func TestTwoNodeJoin(t *testing.T) {
	// 启动两个 HTTP 服务器模拟两个节点。
	gm1 := newTestMembership("localhost:9101", nil)
	gm2 := newTestMembership("localhost:9102", []string{"localhost:9101"})

	srv1 := httptest.NewServer(http.HandlerFunc(gm1.ServeHTTP))
	defer srv1.Close()
	srv2 := httptest.NewServer(http.HandlerFunc(gm2.ServeHTTP))
	defer srv2.Close()

	// 更新地址为测试服务器地址。
	gm1 = newTestMembership("localhost:9101", nil)
	gm1.SetTransport(NewTransport("localhost:9101", 3*time.Second))
	gm1.Start()
	defer gm1.Stop()

	gm2 = newTestMembership("localhost:9102", []string{"localhost:9101"})
	gm2.SetTransport(NewTransport("localhost:9102", 3*time.Second))
	gm2.Start()
	defer gm2.Stop()

	// 让两个成员管理器互相监听。
	go func() {
		// gm1 的请求转发到 srv2，gm2 的请求转发到 srv1
		// 简化处理：直接注册到各自的 httptest server
	}()

	// 由于 httptest 的端口和实际 member 地址不同，
	// 这里改用直接调用 HandleJoin 来模拟。
	joinReq := JoinRequest{Node: *gm2.SelfInfo()}
	body, _ := json.Marshal(joinReq)
	req := httptest.NewRequest("POST", "/_cluster/join", io.NopCloser(bytes.NewReader(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	gm1.HandleJoin(w, req)

	// gm1 应该知道 gm2。
	if !gm1.IsAlive("localhost:9102") {
		t.Error("gm1 should see gm2 as alive after join")
	}

	joinReq2 := JoinRequest{Node: *gm1.SelfInfo()}
	body2, _ := json.Marshal(joinReq2)
	req2 := httptest.NewRequest("POST", "/_cluster/join", bytes.NewReader(body2))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	gm2.HandleJoin(w2, req2)

	if !gm2.IsAlive("localhost:9101") {
		t.Error("gm2 should see gm1 as alive after join")
	}
}

func TestPingRoundtrip(t *testing.T) {
	gm := newTestMembership("localhost:9200", nil)
	defer gm.Stop()

	pingReq := PingRequest{
		From:        "remote:9201",
		Incarnation: 0,
		Payload:     nil,
		Seq:         1,
	}
	body, _ := json.Marshal(pingReq)
	req := httptest.NewRequest("POST", "/_cluster/ping", io.NopCloser(bytes.NewReader(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	gm.HandlePing(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp PingResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.From != "localhost:9200" {
		t.Errorf("expected from=localhost:9200, got %s", resp.From)
	}
}

func TestPingReqRoundtrip(t *testing.T) {
	// gm1 收到 ping-req，转发到 gm2。
	gm1 := newTestMembership("localhost:9301", nil)
	defer gm1.Stop()

	// 模拟 gm2 用 httptest。
	gm2 := newTestMembership("localhost:9302", nil)
	defer gm2.Stop()

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gm2.ServeHTTP(w, r)
	}))
	defer srv2.Close()

	// gm1 的 transport 指向 gm2。
	// 但 httptest 的端口是随机的，我们需要手动处理。
	// 简化测试：直接调用 handler 方法。

	// 先让 gm1 知道 gm2 存在。
	gm1.mergePayload([]NodeInfo{{
		ID:          "localhost:9302",
		Addr:        srv2.URL,
		State:       StateAlive,
		Incarnation: 0,
	}})

	// 发送 ping-req 到 gm1，让它转发给 gm2。
	pingReq := PingReqRequest{
		Target:      "localhost:9302",
		From:        "originator:9300",
		Incarnation: 0,
		Seq:         1,
	}
	body, _ := json.Marshal(pingReq)
	req := httptest.NewRequest("POST", "/_cluster/ping-req", io.NopCloser(bytes.NewReader(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	// gm1 的 transport 需要能实际连接 gm2。
	// 替换 transport 指向 srv2。
	realTransport := NewTransport("localhost:9301", 1*time.Second)
	gm1.SetTransport(realTransport)

	// 修改 gm1 的成员表中 gm2 的地址为实际 URL。
	gm1.mu.Lock()
	gm1.members["localhost:9302"].Addr = srv2.URL[7:] // 去掉 "http://"
	gm1.mu.Unlock()

	gm1.HandlePingReq(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestLeave(t *testing.T) {
	gm := newTestMembership("localhost:9400", nil)
	defer gm.Stop()

	// 添加一个节点。
	gm.mergePayload([]NodeInfo{{
		ID:          "localhost:9401",
		Addr:        "localhost:9401",
		State:       StateAlive,
		Incarnation: 0,
	}})

	if !gm.IsAlive("localhost:9401") {
		t.Fatal("node should be alive before leave")
	}

	// 模拟收到 leave 请求。
	leaveReq := LeaveRequest{Node: "localhost:9401"}
	body, _ := json.Marshal(leaveReq)
	req := httptest.NewRequest("POST", "/_cluster/leave", io.NopCloser(bytes.NewReader(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	gm.HandleLeave(w, req)

	if gm.IsAlive("localhost:9401") {
		t.Error("node should not be alive after leave")
	}
}

func TestIncarnationRefutation(t *testing.T) {
	gm := newTestMembership("localhost:9500", nil)
	defer gm.Stop()

	// 收到关于自己的 rumor，incarnation 更高。
	gm.mergePayload([]NodeInfo{{
		ID:          "localhost:9500",
		Addr:        "localhost:9500",
		State:       StateSuspect,
		Incarnation: 5,
	}})

	if gm.selfInfo.Incarnation != 6 {
		t.Errorf("expected incarnation 6 after refutation, got %d", gm.selfInfo.Incarnation)
	}
	if gm.selfInfo.State != StateAlive {
		t.Error("self should remain alive after refutation")
	}
}

func TestOnJoinCallback(t *testing.T) {
	gm := newTestMembership("localhost:9600", nil)
	defer gm.Stop()

	var joined NodeID
	var mu sync.Mutex
	gm.OnJoin(func(nodeID NodeID) {
		mu.Lock()
		defer mu.Unlock()
		joined = nodeID
	})

	gm.mergePayload([]NodeInfo{{
		ID:          "new-node:9601",
		Addr:        "new-node:9601",
		State:       StateAlive,
		Incarnation: 0,
	}})

	// 给回调一点时间执行。
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	if joined != "new-node:9601" {
		t.Errorf("expected join callback for new-node:9601, got %s", joined)
	}
	mu.Unlock()
}

func TestGetMembers(t *testing.T) {
	gm := newTestMembership("localhost:9700", nil)
	defer gm.Stop()

	// 添加一些节点。
	for i := 1; i <= 3; i++ {
		gm.mergePayload([]NodeInfo{{
			ID:          NodeID(fmt.Sprintf("node-%d:970%d", i, i)),
			Addr:        fmt.Sprintf("node-%d:970%d", i, i),
			State:       StateAlive,
			Incarnation: 0,
		}})
	}

	members := gm.AllMembers()
	if len(members) != 4 {
		t.Errorf("expected 4 members, got %d", len(members))
	}

	aliveNodes := gm.AliveNodes()
	if len(aliveNodes) != 4 {
		t.Errorf("expected 4 alive nodes, got %d", len(aliveNodes))
	}
}

func TestHandleMembers(t *testing.T) {
	gm := newTestMembership("localhost:9800", nil)
	defer gm.Stop()

	gm.mergePayload([]NodeInfo{{
		ID:          "other:9801",
		Addr:        "other:9801",
		State:       StateAlive,
		Incarnation: 0,
	}})

	req := httptest.NewRequest("GET", "/_cluster/members", nil)
	w := httptest.NewRecorder()
	gm.HandleMembers(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var members []NodeInfo
	if err := json.NewDecoder(w.Body).Decode(&members); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}

	if len(members) != 2 {
		t.Errorf("expected 2 members, got %d", len(members))
	}
}
