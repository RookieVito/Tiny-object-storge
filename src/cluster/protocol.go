package cluster

// PingRequest gossip ping 消息。
type PingRequest struct {
	From        NodeID     `json:"from"`
	Incarnation int64      `json:"incarnation"`
	Payload     []NodeInfo `json:"payload,omitempty"`
	Seq         int64      `json:"seq"`
}

// PingResponse ping 响应。
type PingResponse struct {
	From        NodeID     `json:"from"`
	Incarnation int64      `json:"incarnation"`
	Payload     []NodeInfo `json:"payload,omitempty"`
}

// PingReqRequest 间接 ping 请求（请第三方节点帮忙 ping 目标节点）。
type PingReqRequest struct {
	Target      NodeID     `json:"target"`
	From        NodeID     `json:"from"`
	Incarnation int64      `json:"incarnation"`
	Payload     []NodeInfo `json:"payload,omitempty"`
	Seq         int64      `json:"seq"`
}

// JoinRequest 节点加入请求。
type JoinRequest struct {
	Node NodeInfo `json:"node"`
}

// JoinResponse 节点加入响应。
type JoinResponse struct {
	Members []NodeInfo `json:"members"`
}

// LeaveRequest 节点主动离开请求。
type LeaveRequest struct {
	Node NodeID `json:"node"`
}

// StorageRequest 节点间存储操作请求。
type StorageRequest struct {
	RequestID string            `json:"request_id"`
	Operation string            `json:"operation"`
	Bucket    string            `json:"bucket"`
	Key       string            `json:"key,omitempty"`
	Data      string            `json:"data,omitempty"` // base64 编码
	Meta      *ObjectMetaMsg    `json:"meta,omitempty"`
	UploadId  string            `json:"upload_id,omitempty"`
	PartNumber int              `json:"part_number,omitempty"`
	Parts      []PartInfoMsg     `json:"parts,omitempty"`
}

// PartInfoMsg 节点间传输的 part 元数据。
type PartInfoMsg struct {
	PartNumber   int    `json:"part_number"`
	Size         int64  `json:"size"`
	ETag         string `json:"etag"`
	LastModified string `json:"last_modified"`
}

// ObjectMetaMsg 节点间传输的对象元数据。
type ObjectMetaMsg struct {
	Key          string            `json:"key"`
	Size         int64             `json:"size"`
	ETag         string            `json:"etag"`
	ContentType  string            `json:"content_type"`
	LastModified string            `json:"last_modified"`
	UserMetadata map[string]string `json:"user_metadata,omitempty"`
}

// StorageResponse 存储操作响应。
type StorageResponse struct {
	RequestID string         `json:"request_id"`
	Status    int            `json:"status"`
	Data      string         `json:"data,omitempty"` // base64 编码
	Meta      *ObjectMetaMsg `json:"meta,omitempty"`
	Error     string         `json:"error,omitempty"`
}
