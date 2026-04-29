package cluster

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Transport 节点间 HTTP 通信层。
type Transport struct {
	client    *http.Client
	localAddr string
}

// NewTransport 创建 Transport。
// addr: 本节点地址（"host:port"），用于设置 Host 头。
// timeout: HTTP 请求超时时间。
func NewTransport(addr string, timeout time.Duration) *Transport {
	return &Transport{
		client: &http.Client{
			Timeout: timeout,
		},
		localAddr: addr,
	}
}

// SetTimeout 更新 RPC 超时时间。
func (t *Transport) SetTimeout(timeout time.Duration) {
	t.client.Timeout = timeout
}

// nodeURL 构造目标节点的完整 URL。
func (t *Transport) nodeURL(target NodeID, path string) string {
	return fmt.Sprintf("http://%s/_cluster/%s", target, path)
}

// postJSON 发送 JSON POST 请求并解析 JSON 响应。
func (t *Transport) postJSON(target NodeID, path string, req, resp any) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	url := t.nodeURL(target, path)
	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Node-Addr", t.localAddr)

	httpResp, err := t.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("send request to %s: %w", target, err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(httpResp.Body, 64<<20)) // 限制 64MB
	if err != nil {
		return fmt.Errorf("read response from %s: %w", target, err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return fmt.Errorf("node %s returned status %d: %s", target, httpResp.StatusCode, string(respBody))
	}

	if resp != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, resp); err != nil {
			return fmt.Errorf("unmarshal response from %s: %w", target, err)
		}
	}

	return nil
}

// Ping 向目标节点发送 ping 请求。
func (t *Transport) Ping(target NodeID, req *PingRequest) (*PingResponse, error) {
	resp := &PingResponse{}
	err := t.postJSON(target, "ping", req, resp)
	return resp, err
}

// PingReq 请求目标节点间接 ping 另一个节点。
func (t *Transport) PingReq(target NodeID, req *PingReqRequest) (*PingResponse, error) {
	resp := &PingResponse{}
	err := t.postJSON(target, "ping-req", req, resp)
	return resp, err
}

// Join 向种子节点发送加入请求。
func (t *Transport) Join(seed NodeID, self *NodeInfo) (*JoinResponse, error) {
	req := &JoinRequest{Node: *self}
	resp := &JoinResponse{}
	err := t.postJSON(seed, "join", req, resp)
	return resp, err
}

// Leave 向目标节点通知本节点离开。
func (t *Transport) Leave(target NodeID, nodeID NodeID) error {
	req := &LeaveRequest{Node: nodeID}
	return t.postJSON(target, "leave", req, nil)
}

// Replicate 向目标节点发送存储复制请求。
func (t *Transport) Replicate(target NodeID, req *StorageRequest) (*StorageResponse, error) {
	resp := &StorageResponse{}
	err := t.postJSON(target, "replicate", req, resp)
	return resp, err
}

// GetMembers 从目标节点获取成员列表。
func (t *Transport) GetMembers(target NodeID) ([]NodeInfo, error) {
	url := t.nodeURL(target, "members")
	httpResp, err := t.client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("get members from %s: %w", target, err)
	}
	defer httpResp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(httpResp.Body, 64<<20))
	if err != nil {
		return nil, err
	}

	var members []NodeInfo
	if err := json.Unmarshal(body, &members); err != nil {
		return nil, err
	}
	return members, nil
}
