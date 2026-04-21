package cluster

// NodeID 是节点的唯一标识符，格式为 "host:port"。
type NodeID = string

// NodeState 表示节点的状态。
type NodeState int

const (
	StateAlive   NodeState = iota // 节点存活
	StateSuspect                  // 节点疑似失效（待确认）
	StateDead                     // 节点已确认失效
	StateLeft                     // 节点主动离开
)

// String 返回节点状态的可读字符串。
func (s NodeState) String() string {
	switch s {
	case StateAlive:
		return "alive"
	case StateSuspect:
		return "suspect"
	case StateDead:
		return "dead"
	case StateLeft:
		return "left"
	default:
		return "unknown"
	}
}

// NodeInfo 包含节点的完整信息。
type NodeInfo struct {
	ID          NodeID    `json:"id"`
	Addr        string    `json:"addr"`
	State       NodeState `json:"state"`
	Incarnation int64     `json:"incarnation"`
}

// NodeChangeHandler 节点状态变化回调函数类型。
type NodeChangeHandler func(nodeID NodeID)
