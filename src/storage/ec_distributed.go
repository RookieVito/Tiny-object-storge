package storage

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"tiny-object-storage/src/cluster"
	"tiny-object-storage/src/ec"
	"tiny-object-storage/src/hash"
	"tiny-object-storage/src/service"
	"tiny-object-storage/src/s3error"
)

// ECDistributedConfig 分布式 EC 后端配置。
type ECDistributedConfig struct {
	NodeID            string
	Addr              string
	SeedNodes         []string
	DataShards        int
	ParityShards      int
	ReplicationFactor int // ECObjectMeta 的副本数
	VirtualNodes      int
	GossipInterval    time.Duration
	RPCTimeout        time.Duration
}

// ShardMeta 存储在分片节点上的分片元数据。
type ShardMeta struct {
	ShardIndex  int    `json:"shard_index"`
	ShardSize   int    `json:"shard_size"`
	TotalShards int    `json:"total_shards"`
	ObjectKey   string `json:"object_key"`
	Bucket      string `json:"bucket"`
	ETag        string `json:"etag"`
}

// ECDistMeta 分布式 EC 对象元数据（记录分片分布到哪些节点）。
type ECDistMeta struct {
	Key          string            `json:"key"`
	OriginalSize int64             `json:"original_size"`
	ShardSize    int               `json:"shard_size"`
	DataShards   int               `json:"data_shards"`
	ParityShards int               `json:"parity_shards"`
	ETag         string            `json:"etag"`
	ContentType  string            `json:"content_type"`
	LastModified time.Time         `json:"last_modified"`
	UserMetadata map[string]string `json:"user_metadata,omitempty"`
	ShardNodes   []string          `json:"shard_nodes"` // shard[i] 存储在 ShardNodes[i]
}

// ECDistributedBackend 分布式纠删码存储后端。
// 将对象 RS 编码为 K 数据块 + M 冗余块，分片分布到不同节点上。
type ECDistributedBackend struct {
	config     *ECDistributedConfig
	local      *LocalBackend
	ring       *hash.ConsistentHash
	membership *cluster.GossipMembership
	transport  *cluster.Transport
	rs         *ec.ReedSolomon
	mu         sync.RWMutex
	seq        atomic.Int64
}

// NewECDistributedBackend 创建分布式 EC 后端。
func NewECDistributedBackend(cfg *ECDistributedConfig, localRoot string) (*ECDistributedBackend, error) {
	if cfg.DataShards <= 0 {
		cfg.DataShards = 4
	}
	if cfg.ParityShards <= 0 {
		cfg.ParityShards = 2
	}
	if cfg.ReplicationFactor <= 0 {
		cfg.ReplicationFactor = 2
	}
	if cfg.VirtualNodes <= 0 {
		cfg.VirtualNodes = 500
	}
	if cfg.GossipInterval <= 0 {
		cfg.GossipInterval = 1 * time.Second
	}
	if cfg.RPCTimeout <= 0 {
		cfg.RPCTimeout = 3 * time.Second
	}

	rs, err := ec.NewReedSolomon(cfg.DataShards, cfg.ParityShards)
	if err != nil {
		return nil, fmt.Errorf("create reed-solomon: %w", err)
	}

	local := NewLocalBackend(localRoot)
	ring := hash.NewConsistentHash(cfg.VirtualNodes)
	membership := cluster.NewGossipMembership(cluster.NodeID(cfg.NodeID), cfg.Addr, cfg.SeedNodes)
	membership.SetInterval(cfg.GossipInterval)
	transport := cluster.NewTransport(cfg.Addr, cfg.RPCTimeout)
	membership.SetTransport(transport)

	db := &ECDistributedBackend{
		config:     cfg,
		local:      local,
		ring:       ring,
		membership: membership,
		transport:  transport,
		rs:         rs,
	}

	membership.OnJoin(db.onNodeJoin)
	membership.OnLeave(db.onNodeLeave)
	ring.AddNode(cfg.NodeID)

	return db, nil
}

// Start 启动 Gossip 协议并加入集群。
func (db *ECDistributedBackend) Start() error {
	db.membership.Start()
	if len(db.config.SeedNodes) > 0 {
		return db.membership.Join()
	}
	return nil
}

// Stop 停止后端。
func (db *ECDistributedBackend) Stop() {
	db.membership.Leave()
	db.membership.Stop()
}

// MembershipHandler 返回集群 HTTP handler。
func (db *ECDistributedBackend) MembershipHandler() http.Handler {
	return http.StripPrefix("/_cluster", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ping":
			db.membership.HandlePing(w, r)
		case "/ping-req":
			db.membership.HandlePingReq(w, r)
		case "/join":
			db.membership.HandleJoin(w, r)
		case "/leave":
			db.membership.HandleLeave(w, r)
		case "/members":
			db.membership.HandleMembers(w, r)
		case "/replicate":
			db.HandleReplicate(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
}

// --- Ring 回调 ---

func (db *ECDistributedBackend) onNodeJoin(nodeID cluster.NodeID) {
	if nodeID == db.config.NodeID {
		return
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	db.ring.AddNode(nodeID)
	slog.Info("[ec-dist] node added to hash ring", "node", nodeID, "ring_size", db.ring.Size())
}

func (db *ECDistributedBackend) onNodeLeave(nodeID cluster.NodeID) {
	if nodeID == db.config.NodeID {
		return
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	db.ring.RemoveNode(nodeID)
	slog.Info("[ec-dist] node removed from hash ring", "node", nodeID, "ring_size", db.ring.Size())
}

// --- 辅助方法 ---

func (db *ECDistributedBackend) nextRequestID() string {
	n := db.seq.Add(1)
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), n)
}

// shardNodes 返回存储对象分片的 K+M 个节点（仅 alive 节点）。
func (db *ECDistributedBackend) shardNodes(key string) []string {
	return db.selectNodes(key, db.config.DataShards+db.config.ParityShards)
}

// metaReplicaNodes 返回存储 ECObjectMeta 的 R 个节点。
func (db *ECDistributedBackend) metaReplicaNodes(key string) []string {
	return db.selectNodes(key+"_meta", db.config.ReplicationFactor)
}

// selectNodes 从一致性哈希环选取 n 个 alive 节点。
// 如果 GetNodes 返回的候选集不足，扩大候选数重试。
func (db *ECDistributedBackend) selectNodes(key string, n int) []string {
	aliveNodes := db.membership.AliveNodes()
	if len(aliveNodes) == 0 {
		return nil
	}
	aliveSet := make(map[string]bool, len(aliveNodes))
	for _, node := range aliveNodes {
		aliveSet[string(node.ID)] = true
	}

	if n > len(aliveNodes) {
		n = len(aliveNodes)
	}

	db.mu.RLock()
	defer db.mu.RUnlock()

	// 逐步扩大候选集直到凑齐 n 个 alive 节点。
	for attempt := n; attempt <= n*3; attempt += n {
		candidates := db.ring.GetNodes(key, attempt)
		result := make([]string, 0, n)
		seen := make(map[string]bool, n)
		for _, node := range candidates {
			if aliveSet[node] && !seen[node] {
				result = append(result, node)
				seen[node] = true
			}
			if len(result) >= n {
				break
			}
		}
		if len(result) >= n || attempt >= db.ring.Size() {
			return result
		}
	}
	return nil
}

func (db *ECDistributedBackend) isSelf(nodeID string) bool {
	return nodeID == db.config.NodeID
}

// ecDistMetaKey 返回 ECObjectMeta 在 LocalBackend 中的存储 key（key 部分）。
func ecDistMetaKey(bucket, key string) string {
	return ".ec-meta/" + key
}

// shardMetaKey 返回 ShardMeta 在 LocalBackend 中的存储 key（key 部分）。
func shardMetaKey(bucket, key string, shardIndex int) string {
	return ".ec-shard-meta/" + key + "#" + fmt.Sprintf("%d", shardIndex)
}

// readECMeta 从本地读取 ECDistMeta。
func (db *ECDistributedBackend) readECMeta(bucket, key string) (*ECDistMeta, error) {
	metaKey := ecDistMetaKey(bucket, key)
	data, meta, err := db.local.GetObject(bucket, metaKey)
	if err != nil {
		return nil, err
	}
	var ecMeta ECDistMeta
	if err := json.Unmarshal(data, &ecMeta); err != nil {
		return nil, fmt.Errorf("unmarshal ec meta: %w", err)
	}
	ecMeta.LastModified = meta.LastModified
	return &ecMeta, nil
}

// resolveECMeta 从 meta 副本节点或所有 alive 节点查找 ECDistMeta。
// 先尝试 meta 副本节点，失败后遍历所有 alive 节点回退查找。
func (db *ECDistributedBackend) resolveECMeta(bucket, key string) *ECDistMeta {
	// 优先从 meta 副本节点查找。
	metaNodes := db.metaReplicaNodes(bucket + "/" + key)
	for _, nodeID := range metaNodes {
		m, err := db.readECMetaFromNode(bucket, key, nodeID)
		if err == nil {
			return m
		}
	}
	// 回退：从所有 alive 节点查找。
	aliveNodes := db.membership.AliveNodes()
	for _, node := range aliveNodes {
		nodeID := string(node.ID)
		m, err := db.readECMetaFromNode(bucket, key, nodeID)
		if err == nil {
			return m
		}
	}
	return nil
}

// readECMetaFromNode 从指定节点读取 ECDistMeta。
func (db *ECDistributedBackend) readECMetaFromNode(bucket, key string, nodeID string) (*ECDistMeta, error) {
	var ecMeta *ECDistMeta
	var err error
	if db.isSelf(nodeID) {
		ecMeta, err = db.readECMeta(bucket, key)
	} else {
		req := &cluster.StorageRequest{
			RequestID: db.nextRequestID(),
			Operation: "ec_get_meta",
			Bucket:    bucket,
			Key:       key,
		}
		resp, rpcErr := db.transport.Replicate(cluster.NodeID(nodeID), req)
		if rpcErr != nil {
			return nil, rpcErr
		}
		if resp.Status >= 400 {
			return nil, fmt.Errorf("remote ec_get_meta failed: %s", resp.Error)
		}
		var m ECDistMeta
		if unmarshalErr := json.Unmarshal([]byte(resp.Data), &m); unmarshalErr != nil {
			return nil, fmt.Errorf("unmarshal remote ec meta: %w", unmarshalErr)
		}
		ecMeta = &m
	}
	// 验证返回的元数据有效（key 必须匹配）。
	if ecMeta != nil && ecMeta.Key != key {
		ecMeta = nil
		err = s3error.ErrNoSuchKey
	}
	return ecMeta, err
}

// --- StorageBackend 接口实现 ---

func (db *ECDistributedBackend) CreateBucket(bucket string) error {
	aliveNodes := db.membership.AliveNodes()
	if len(aliveNodes) == 0 {
		return db.local.CreateBucket(bucket)
	}

	var wg sync.WaitGroup
	successes := int32(0)
	for _, node := range aliveNodes {
		wg.Add(1)
		go func(nodeID string) {
			defer wg.Done()
			var err error
			if db.isSelf(nodeID) {
				err = db.local.CreateBucket(bucket)
			} else {
				req := &cluster.StorageRequest{
					RequestID: db.nextRequestID(),
					Operation: "create_bucket",
					Bucket:    bucket,
				}
				resp, rpcErr := db.transport.Replicate(cluster.NodeID(nodeID), req)
				if rpcErr != nil {
					err = rpcErr
				} else if resp.Status >= 400 {
					err = fmt.Errorf("remote: %s", resp.Error)
				}
			}
			if err == nil {
				atomic.AddInt32(&successes, 1)
			}
		}(string(node.ID))
	}
	wg.Wait()

	majority := len(aliveNodes)/2 + 1
	if int(successes) >= majority {
		return nil
	}
	return s3error.ErrWriteQuorumFailed
}

func (db *ECDistributedBackend) DeleteBucket(bucket string) error {
	aliveNodes := db.membership.AliveNodes()
	if len(aliveNodes) == 0 {
		return db.local.DeleteBucket(bucket)
	}

	var wg sync.WaitGroup
	for _, node := range aliveNodes {
		wg.Add(1)
		go func(nodeID string) {
			defer wg.Done()
			if db.isSelf(nodeID) {
				// 清理所有 EC 相关数据（.ec-meta、.ec-shards、.ec-shard-meta）。
				for _, prefix := range []string{".ec-meta/", ".ec-shards/", ".ec-shard-meta/"} {
					entries, _, _, _, _ := db.local.ListObjects(bucket, prefix, "", "", 100000)
					for _, e := range entries {
						db.local.DeleteObject(bucket, e.Key)
					}
				}
				db.local.DeleteBucket(bucket)
			} else {
				req := &cluster.StorageRequest{
					RequestID: db.nextRequestID(),
					Operation: "delete_bucket",
					Bucket:    bucket,
				}
				db.transport.Replicate(cluster.NodeID(nodeID), req)
			}
		}(string(node.ID))
	}
	wg.Wait()
	return nil
}

func (db *ECDistributedBackend) BucketExists(bucket string) (bool, error) {
	if ok, _ := db.local.BucketExists(bucket); ok {
		return true, nil
	}
	aliveNodes := db.membership.AliveNodes()
	for _, node := range aliveNodes {
		if db.isSelf(string(node.ID)) {
			continue
		}
		req := &cluster.StorageRequest{
			RequestID: db.nextRequestID(),
			Operation: "head_bucket",
			Bucket:    bucket,
		}
		resp, err := db.transport.Replicate(node.ID, req)
		if err != nil {
			continue
		}
		return resp.Status == 200, nil
	}
	return false, nil
}

func (db *ECDistributedBackend) ListBuckets() ([]BucketInfo, error) {
	aliveNodes := db.membership.AliveNodes()
	resultCh := make(chan []BucketInfo, len(aliveNodes))
	var wg sync.WaitGroup

	for _, node := range aliveNodes {
		wg.Add(1)
		go func(nodeInfo *cluster.NodeInfo) {
			defer wg.Done()
			if db.isSelf(string(nodeInfo.ID)) {
				buckets, err := db.local.ListBuckets()
				if err == nil {
					resultCh <- buckets
				} else {
					resultCh <- nil
				}
				return
			}
			req := &cluster.StorageRequest{
				RequestID: db.nextRequestID(),
				Operation: "list_buckets",
			}
			resp, err := db.transport.Replicate(nodeInfo.ID, req)
			if err != nil || resp.Status != 200 || resp.Data == "" {
				resultCh <- nil
				return
			}
			var buckets []BucketInfo
			if json.Unmarshal([]byte(resp.Data), &buckets) == nil {
				resultCh <- buckets
			} else {
				resultCh <- nil
			}
		}(node)
	}

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	merged := make(map[string]BucketInfo)
	for bucketList := range resultCh {
		for _, b := range bucketList {
			if existing, ok := merged[b.Name]; !ok || b.CreationDate.Before(existing.CreationDate) {
				merged[b.Name] = b
			}
		}
	}

	result := make([]BucketInfo, 0, len(merged))
	for _, b := range merged {
		result = append(result, b)
	}
	return result, nil
}

func (db *ECDistributedBackend) PutObject(bucket, key string, data []byte, meta *service.ObjectMeta) error {
	totalShards := db.config.DataShards + db.config.ParityShards
	nodes := db.shardNodes(bucket + "/" + key)
	if len(nodes) < db.config.DataShards {
		return fmt.Errorf("not enough alive nodes: need %d, got %d", db.config.DataShards, len(nodes))
	}

	// RS 编码。
	shards, shardSize := db.rs.Encode(data)

	etag := fmt.Sprintf("\"%x\"", md5.Sum(data))
	now := time.Now()
	// 并发发送分片到各节点。
	type shardResult struct {
		nodeID string
		index  int
		err    error
	}
	resultCh := make(chan shardResult, totalShards)
	var wg sync.WaitGroup

	for i := 0; i < totalShards; i++ {
		if i >= len(nodes) {
			break // 没有足够节点存这个分片
		}
		wg.Add(1)
		go func(shardIdx int, nodeID string) {
			defer wg.Done()
			b64Shard := base64.StdEncoding.EncodeToString(shards[shardIdx])
			shardMetaJSON, _ := json.Marshal(ShardMeta{
				ShardIndex:  shardIdx,
				ShardSize:   shardSize,
				TotalShards: totalShards,
				ObjectKey:   key,
				Bucket:      bucket,
				ETag:        etag,
			})
			if db.isSelf(nodeID) {
				err := db.putShardLocal(bucket, key, shardIdx, shards[shardIdx], shardMetaJSON)
				resultCh <- shardResult{nodeID: nodeID, index: shardIdx, err: err}
				return
			}
			req := &cluster.StorageRequest{
				RequestID:   db.nextRequestID(),
				Operation:   "ec_put_shard",
				Bucket:      bucket,
				Key:         key,
				Data:        b64Shard,
				ShardIndex:  shardIdx,
				ShardSize:   shardSize,
				TotalShards: totalShards,
				Meta: &cluster.ObjectMetaMsg{
					Key: key,
					ETag: etag,
				},
			}
			resp, err := db.transport.Replicate(cluster.NodeID(nodeID), req)
			if err != nil {
				resultCh <- shardResult{nodeID: nodeID, index: shardIdx, err: err}
				return
			}
			if resp.Status >= 400 {
				resultCh <- shardResult{nodeID: nodeID, index: shardIdx, err: fmt.Errorf("status %d", resp.Status)}
				return
			}
			resultCh <- shardResult{nodeID: nodeID, index: shardIdx}
		}(i, nodes[i])
	}

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	storedCount := 0
	shardNodeMap := make(map[int]string, totalShards)
	for r := range resultCh {
		if r.err != nil {
		} else {
			storedCount++
			shardNodeMap[r.index] = r.nodeID
		}
	}
	// 转为按 index 排序的数组，仅包含成功写入的分片。
	shardNodes := make([]string, 0, storedCount)
	for i := 0; i < totalShards; i++ {
		if nodeID, ok := shardNodeMap[i]; ok {
			shardNodes = append(shardNodes, nodeID)
		}
	}

	if storedCount < db.config.DataShards {
		return fmt.Errorf("failed to store enough shards: stored %d, need %d", storedCount, db.config.DataShards)
	}

	ecMeta := &ECDistMeta{
		Key:          key,
		OriginalSize: int64(len(data)),
		ShardSize:    shardSize,
		DataShards:   db.config.DataShards,
		ParityShards: db.config.ParityShards,
		ETag:         etag,
		ContentType:  meta.ContentType,
		LastModified: now,
		UserMetadata: meta.UserMetadata,
		ShardNodes:   shardNodes,
	}
	ecMetaJSON, _ := json.Marshal(ecMeta)

	// 同步复制 ECDistMeta 到 R 个节点，至少成功 1 个。
	metaNodes := db.metaReplicaNodes(bucket + "/" + key)
	b64Meta := base64.StdEncoding.EncodeToString(ecMetaJSON)
	metaStored := int32(0)
	for _, nodeID := range metaNodes {
		if db.isSelf(nodeID) {
			metaObj := &service.ObjectMeta{
				Key:          key,
				Size:         int64(len(ecMetaJSON)),
				ETag:         etag,
				ContentType:  "application/json",
				LastModified: now,
			}
			if err := db.local.PutObject(bucket, ".ec-meta/"+key, ecMetaJSON, metaObj); err == nil {
				atomic.AddInt32(&metaStored, 1)
			}
		} else {
			req := &cluster.StorageRequest{
				RequestID: db.nextRequestID(),
				Operation: "ec_put_meta",
				Bucket:    bucket,
				Key:       key,
				Data:      b64Meta,
				Meta: &cluster.ObjectMetaMsg{
					Key:          key,
					ETag:         etag,
						Size:         int64(len(ecMetaJSON)),
					ContentType:  "application/json",
					LastModified: now.Format(time.RFC3339Nano),
				},
			}
			resp, rpcErr := db.transport.Replicate(cluster.NodeID(nodeID), req)
			if rpcErr == nil && resp.Status < 400 {
				atomic.AddInt32(&metaStored, 1)
			} else if rpcErr != nil {
			}
		}
	}

	if metaStored == 0 {
		return fmt.Errorf("failed to store ec meta on any node")
	}

	return nil
}

// putShardLocal 本地存储分片数据和分片元数据。
func (db *ECDistributedBackend) putShardLocal(bucket, key string, shardIndex int, shardData []byte, shardMetaJSON []byte) error {
	// 存储分片数据：使用 bucket/shard-key 格式避免与普通对象冲突。
	shardKey := fmt.Sprintf(".ec-shards/%s#%d", key, shardIndex)
	meta := &service.ObjectMeta{
		Key: shardKey,
		Size: int64(len(shardData)),
	}
	if err := db.local.PutObject(bucket, shardKey, shardData, meta); err != nil {
		return err
	}

	// 存储分片元数据。
	smKey := shardMetaKey(bucket, key, shardIndex)
	smMeta := &service.ObjectMeta{
		Key: smKey,
		Size: int64(len(shardMetaJSON)),
	}
	return db.local.PutObject(bucket, smKey, shardMetaJSON, smMeta)
}

func (db *ECDistributedBackend) GetObject(bucket, key string) ([]byte, *service.ObjectMeta, error) {
	// 读取 ECDistMeta：先从 meta 副本节点查找，失败后从所有 alive 节点回退。
	ecMeta := db.resolveECMeta(bucket, key)
	if ecMeta == nil {
		return nil, nil, s3error.ErrNoSuchKey
	}

	// 从元数据中获取分片节点映射，并发读取。
	totalShards := ecMeta.DataShards + ecMeta.ParityShards
	shardCount := len(ecMeta.ShardNodes)

	type shardFetch struct {
		index int
		data  []byte
		err   error
	}
	resultCh := make(chan shardFetch, totalShards)
	var wg sync.WaitGroup

	for i := 0; i < shardCount; i++ {
		nodeID := ecMeta.ShardNodes[i]
		wg.Add(1)
		go func(shardIdx int, nodeID string) {
			defer wg.Done()
			var shardData []byte
			var err error
			if db.isSelf(nodeID) {
				shardData, err = db.readShardLocal(bucket, key, shardIdx)
			} else {
				req := &cluster.StorageRequest{
					RequestID: db.nextRequestID(),
					Operation: "ec_get_shard",
					Bucket:    bucket,
					Key:       key,
					ShardIndex: shardIdx,
				}
				resp, rpcErr := db.transport.Replicate(cluster.NodeID(nodeID), req)
				if rpcErr != nil {
					err = rpcErr
				} else if resp.Status >= 400 {
					err = fmt.Errorf("status %d: %s", resp.Status, resp.Error)
				} else {
					shardData, err = base64.StdEncoding.DecodeString(resp.Data)
				}
			}
			resultCh <- shardFetch{index: shardIdx, data: shardData, err: err}
		}(i, nodeID)
	}

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	// 收集分片。
	shards := make([][]byte, totalShards)
	fetchedCount := 0
	for r := range resultCh {
		if r.err != nil {
			continue
		}
		if r.index < totalShards {
			shards[r.index] = r.data
			fetchedCount++
		}
	}

	if fetchedCount < ecMeta.DataShards {
		return nil, nil, fmt.Errorf("not enough shards to decode: got %d, need %d", fetchedCount, ecMeta.DataShards)
	}

	// RS 解码。
	if err := db.rs.Decode(shards, ecMeta.ShardSize); err != nil {
		return nil, nil, fmt.Errorf("rs decode: %w", err)
	}

	// 拼接数据块并截断到原始大小。
	var buf []byte
	for i := 0; i < ecMeta.DataShards; i++ {
		buf = append(buf, shards[i]...)
	}
	if int64(len(buf)) > ecMeta.OriginalSize {
		buf = buf[:ecMeta.OriginalSize]
	}

	objMeta := &service.ObjectMeta{
		Key:          key,
		Size:         ecMeta.OriginalSize,
		ETag:         ecMeta.ETag,
		ContentType:  ecMeta.ContentType,
		LastModified: ecMeta.LastModified,
		UserMetadata: ecMeta.UserMetadata,
	}
	return buf, objMeta, nil
}

// readShardLocal 从本地读取分片数据。
func (db *ECDistributedBackend) readShardLocal(bucket, key string, shardIndex int) ([]byte, error) {
	shardKey := fmt.Sprintf(".ec-shards/%s#%d", key, shardIndex)
	data, _, err := db.local.GetObject(bucket, shardKey)
	return data, err
}

func (db *ECDistributedBackend) HeadObject(bucket, key string) (*service.ObjectMeta, error) {
	ecMeta := db.resolveECMeta(bucket, key)
	if ecMeta == nil {
		return nil, s3error.ErrNoSuchKey
	}

	return &service.ObjectMeta{
		Key:          key,
		Size:         ecMeta.OriginalSize,
		ETag:         ecMeta.ETag,
		ContentType:  ecMeta.ContentType,
		LastModified: ecMeta.LastModified,
		UserMetadata: ecMeta.UserMetadata,
	}, nil
}

func (db *ECDistributedBackend) DeleteObject(bucket, key string) error {
	// 先读取元数据获取分片分布。
	ecMeta := db.resolveECMeta(bucket, key)

	totalShards := db.config.DataShards + db.config.ParityShards

	var wg sync.WaitGroup
	// 并发删除分片。
	for i := 0; i < totalShards; i++ {
		nodeID := ""
		if ecMeta != nil && i < len(ecMeta.ShardNodes) {
			nodeID = ecMeta.ShardNodes[i]
		}
		if nodeID == "" {
			continue
		}
		wg.Add(1)
		go func(shardIdx int, nodeID string) {
			defer wg.Done()
			if db.isSelf(nodeID) {
				db.deleteShardLocal(bucket, key, shardIdx)
				return
			}
			req := &cluster.StorageRequest{
				RequestID:  db.nextRequestID(),
				Operation:  "ec_delete_shard",
				Bucket:     bucket,
				Key:        key,
				ShardIndex: shardIdx,
			}
			db.transport.Replicate(cluster.NodeID(nodeID), req)
		}(i, nodeID)
	}

	// 删除 ECDistMeta（遍历所有 alive 节点，不依赖 ring 重算）。
	metaDelNodes := db.membership.AliveNodes()
	for _, node := range metaDelNodes {
		wg.Add(1)
		go func(nid string) {
			defer wg.Done()
			if db.isSelf(nid) {
				db.local.DeleteObject(bucket, ".ec-meta/"+key)
			} else {
				req := &cluster.StorageRequest{
					RequestID: db.nextRequestID(),
					Operation: "ec_delete_meta",
					Bucket:    bucket,
					Key:       key,
				}
				db.transport.Replicate(cluster.NodeID(nid), req)
			}
		}(string(node.ID))
	}
	wg.Wait()
	return nil
}

func (db *ECDistributedBackend) deleteShardLocal(bucket, key string, shardIndex int) {
	shardKey := fmt.Sprintf(".ec-shards/%s#%d", key, shardIndex)
	db.local.DeleteObject(bucket, shardKey)
	smKey := shardMetaKey(bucket, key, shardIndex)
	db.local.DeleteObject(bucket, smKey)
}

func (db *ECDistributedBackend) ListObjects(bucket, prefix, delimiter, startAfter string, maxKeys int) (
	[]ObjectEntry, []string, string, bool, error,
) {
	aliveNodes := db.membership.AliveNodes()
	resultCh := make(chan []ObjectEntry, len(aliveNodes))
	var wg sync.WaitGroup

	for _, node := range aliveNodes {
		wg.Add(1)
		go func(nodeInfo *cluster.NodeInfo) {
			defer wg.Done()
			if db.isSelf(string(nodeInfo.ID)) {
				entries := db.listShardMetaEntries(bucket, prefix, startAfter)
				resultCh <- entries
				return
			}
			req := &cluster.StorageRequest{
				RequestID: db.nextRequestID(),
				Operation: "ec_list_shards",
				Bucket:    bucket,
			}
			resp, err := db.transport.Replicate(nodeInfo.ID, req)
			if err != nil || resp.Status != 200 || resp.Data == "" {
				resultCh <- nil
				return
			}
			type listResp struct {
				Entries []ObjectEntry `json:"entries"`
			}
			var lr listResp
			if json.Unmarshal([]byte(resp.Data), &lr) == nil {
				resultCh <- lr.Entries
			} else {
				resultCh <- nil
			}
		}(node)
	}

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	// 合并去重：按 key 去重，取最新 LastModified。
	merged := make(map[string]ObjectEntry)
	for entryList := range resultCh {
		for _, e := range entryList {
			if existing, ok := merged[e.Key]; !ok || e.LastModified.After(existing.LastModified) {
				merged[e.Key] = e
			}
		}
	}

	// 过滤 prefix 和 startAfter。
	allEntries := make([]ObjectEntry, 0, len(merged))
	for _, e := range merged {
		if prefix != "" && !strings.HasPrefix(e.Key, prefix) {
			continue
		}
		if startAfter != "" && e.Key <= startAfter {
			continue
		}
		allEntries = append(allEntries, e)
	}
	sort.Slice(allEntries, func(i, j int) bool {
		return allEntries[i].Key < allEntries[j].Key
	})

	// 应用 delimiter 分组和 maxKeys 截断。
	var contents []ObjectEntry
	var commonPrefixes []string
	cpSet := make(map[string]bool)
	count := 0
	truncated := false

	for _, e := range allEntries {
		if count >= maxKeys {
			truncated = true
			break
		}
		if delimiter == "" {
			contents = append(contents, e)
			count++
		} else {
			remainder := strings.TrimPrefix(e.Key, prefix)
			idx := strings.Index(remainder, delimiter)
			if idx < 0 {
				contents = append(contents, e)
				count++
			} else {
				cp := prefix + remainder[:idx+len(delimiter)]
				if !cpSet[cp] {
					commonPrefixes = append(commonPrefixes, cp)
					cpSet[cp] = true
					count++
				}
			}
		}
	}

	nextToken := ""
	if truncated && len(allEntries) > 0 {
		lastKey := allEntries[count-1].Key
		nextToken = base64Encode(lastKey)
	}

	return contents, commonPrefixes, nextToken, truncated, nil
}

// listShardMetaEntries 从本地列举 shard_index=0 的分片元数据，作为对象列表。
// 从 key 名（.ec-shard-meta/{objectKey}#{shardIndex}）解析 shard index，避免逐一读取文件内容。
func (db *ECDistributedBackend) listShardMetaEntries(bucket, prefix, startAfter string) []ObjectEntry {
	// 从本地 .ec-meta 构建对象列表（包含正确的 Size、LastModified、ETag）。
	entries, _, _, _, err := db.local.ListObjects(bucket, ".ec-meta/", "", "", 100000)
	if err != nil {
		return nil
	}

	var objects []ObjectEntry
	for _, e := range entries {
		objectKey := strings.TrimPrefix(e.Key, ".ec-meta/")
		if prefix != "" && !strings.HasPrefix(objectKey, prefix) {
			continue
		}
		if startAfter != "" && objectKey <= startAfter {
			continue
		}
		entry := ObjectEntry{
			Key:          objectKey,
			LastModified: e.LastModified,
			Size:         e.Size,
			ETag:         e.ETag,
		}
		// 尝试从 ECDistMeta JSON 读取精确的原始大小和时间。
		if data, _, readErr := db.local.GetObject(bucket, ".ec-meta/"+objectKey); readErr == nil {
			var m ECDistMeta
			if json.Unmarshal(data, &m) == nil {
				entry.Size = m.OriginalSize
				entry.LastModified = m.LastModified
				entry.ETag = m.ETag
			}
		}
		objects = append(objects, entry)
	}
	return objects
}

// --- MultipartStorage 接口实现 ---

func (db *ECDistributedBackend) InitiateUpload(bucket, key string, contentType string, userMeta map[string]string) (*UploadInfo, error) {
	return db.local.InitiateUpload(bucket, key, contentType, userMeta)
}

func (db *ECDistributedBackend) UploadPart(bucket, key, uploadId string, partNumber int, data []byte) (*PartInfo, error) {
	return db.local.UploadPart(bucket, key, uploadId, partNumber, data)
}

func (db *ECDistributedBackend) CompleteUpload(bucket, key, uploadId string, parts []PartInfo) (string, error) {
	// 从本地读取所有 parts 并拼接。
	partsCopy := make([]PartInfo, len(parts))
	copy(partsCopy, parts)
	sort.Slice(partsCopy, func(i, j int) bool {
		return partsCopy[i].PartNumber < partsCopy[j].PartNumber
	})

	var assembled []byte
	for _, part := range partsCopy {
		data, err := db.readPartData(bucket, uploadId, part.PartNumber)
		if err != nil {
			return "", fmt.Errorf("read part %d: %w", part.PartNumber, err)
		}
		assembled = append(assembled, data...)
	}

	// 计算 ETag（MD5(concat of per-part MD5s)-N）。
	var concatHash []byte
	for _, part := range partsCopy {
		etagStr := strings.Trim(part.ETag, "\"")
		h, _ := hex.DecodeString(etagStr)
		concatHash = append(concatHash, h...)
	}
	etag := fmt.Sprintf("\"%x-%d\"", md5.Sum(concatHash), len(partsCopy))

	// 通过 PutObject（EC 分片流程）写入最终对象，传入 multipart ETag。
	contentType := "application/octet-stream"
	userMeta := map[string]string{}
	if info, err := db.local.GetUploadInfo(bucket, key, uploadId); err == nil {
		contentType = info.ContentType
		userMeta = info.UserMetadata
	}
	meta := &service.ObjectMeta{
		Key:          key,
		Size:         int64(len(assembled)),
		ETag:         etag,
		ContentType:  contentType,
		UserMetadata: userMeta,
		LastModified: time.Now(),
	}
	if err := db.PutObject(bucket, key, assembled, meta); err != nil {
		return "", err
	}

	// 清理本地临时 parts。
	db.local.AbortUpload(bucket, "", uploadId)

	return etag, nil
}

func (db *ECDistributedBackend) readPartData(bucket, uploadId string, partNumber int) ([]byte, error) {
	partKey := fmt.Sprintf(".uploads/%s/part-%04d.bin", uploadId, partNumber)
	data, _, err := db.local.GetObject(bucket, partKey)
	return data, err
}

func (db *ECDistributedBackend) AbortUpload(bucket, key, uploadId string) error {
	return db.local.AbortUpload(bucket, key, uploadId)
}

func (db *ECDistributedBackend) ListParts(bucket, key, uploadId string) ([]PartInfo, error) {
	return db.local.ListParts(bucket, key, uploadId)
}

func (db *ECDistributedBackend) ListUploads(bucket, prefix, keyMarker string, maxUploads int) ([]UploadInfo, string, bool, error) {
	return db.local.ListUploads(bucket, prefix, keyMarker, maxUploads)
}

func (db *ECDistributedBackend) GetUploadInfo(bucket, key, uploadId string) (*UploadInfo, error) {
	return db.local.GetUploadInfo(bucket, key, uploadId)
}

// --- Replicate Handler ---

func (db *ECDistributedBackend) HandleReplicate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req cluster.StorageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeReplicateError(w, http.StatusBadRequest, "bad request: %v", err)
		return
	}

	resp := &cluster.StorageResponse{RequestID: req.RequestID}

	switch req.Operation {
	case "create_bucket":
		err := db.local.CreateBucket(req.Bucket)
		if err != nil {
			writeReplicateS3Err(w, resp, err)
			return
		}
		resp.Status = 200

	case "delete_bucket":
		db.local.DeleteBucket(req.Bucket)
		resp.Status = 204

	case "head_bucket":
		ok, err := db.local.BucketExists(req.Bucket)
		if err != nil {
			writeReplicateS3Err(w, resp, err)
			return
		}
		if !ok {
			resp.Status = 404
			resp.Error = "bucket not found"
		} else {
			resp.Status = 200
		}

	case "list_buckets":
		buckets, err := db.local.ListBuckets()
		if err != nil {
			writeReplicateS3Err(w, resp, err)
			return
		}
		data, _ := json.Marshal(buckets)
		resp.Status = 200
		resp.Data = string(data)

	case "put":
		meta, err := fromMetaMsg(req.Meta)
		if err != nil {
			writeReplicateError(w, http.StatusBadRequest, "invalid meta: %v", err)
			return
		}
		data, err := base64.StdEncoding.DecodeString(req.Data)
		if err != nil {
			writeReplicateError(w, http.StatusBadRequest, "invalid base64 data: %v", err)
			return
		}
		if err := db.local.PutObject(req.Bucket, req.Key, data, meta); err != nil {
			writeReplicateS3Err(w, resp, err)
			return
		}
		resp.Status = 200

	case "get":
		data, meta, err := db.local.GetObject(req.Bucket, req.Key)
		if err != nil {
			writeReplicateS3Err(w, resp, err)
			return
		}
		resp.Status = 200
		resp.Data = base64.StdEncoding.EncodeToString(data)
		resp.Meta = toMetaMsg(meta)

	case "head":
		meta, err := db.local.HeadObject(req.Bucket, req.Key)
		if err != nil {
			writeReplicateS3Err(w, resp, err)
			return
		}
		resp.Status = 200
		resp.Meta = toMetaMsg(meta)

	case "delete":
		db.local.DeleteObject(req.Bucket, req.Key)
		resp.Status = 204

	case "list_objects":
		entries, _, _, _, err := db.local.ListObjects(req.Bucket, "", "", "", 100000)
		if err != nil {
			writeReplicateS3Err(w, resp, err)
			return
		}
		type listResp struct {
			Entries []ObjectEntry `json:"entries"`
		}
		data, _ := json.Marshal(listResp{Entries: entries})
		resp.Status = 200
		resp.Data = string(data)

	// --- EC 分布式操作 ---

	case "ec_put_shard":
		data, err := base64.StdEncoding.DecodeString(req.Data)
		if err != nil {
			writeReplicateError(w, http.StatusBadRequest, "invalid base64 data: %v", err)
			return
		}
		etag := ""
		if req.Meta != nil {
			etag = req.Meta.ETag
		}
		shardMetaJSON, _ := json.Marshal(ShardMeta{
			ShardIndex:  req.ShardIndex,
			ShardSize:   req.ShardSize,
			TotalShards: req.TotalShards,
			ObjectKey:   req.Key,
			Bucket:      req.Bucket,
			ETag:        etag,
		})
		if err := db.putShardLocal(req.Bucket, req.Key, req.ShardIndex, data, shardMetaJSON); err != nil {
			writeReplicateError(w, http.StatusInternalServerError, "put shard: %v", err)
			return
		}
		resp.Status = 200

	case "ec_get_shard":
		data, err := db.readShardLocal(req.Bucket, req.Key, req.ShardIndex)
		if err != nil {
			resp.Status = 404
			resp.Error = fmt.Sprintf("shard not found: %v", err)
			writeReplicateJSON(w, resp)
			return
		}
		resp.Status = 200
		resp.Data = base64.StdEncoding.EncodeToString(data)

	case "ec_delete_shard":
		db.deleteShardLocal(req.Bucket, req.Key, req.ShardIndex)
		resp.Status = 204

	case "ec_put_meta":
		data, err := base64.StdEncoding.DecodeString(req.Data)
		if err != nil {
			writeReplicateError(w, http.StatusBadRequest, "invalid base64 meta: %v", err)
			return
		}
		meta, err := fromMetaMsg(req.Meta)
		if err != nil {
			meta = &service.ObjectMeta{
				Key:         req.Key,
				ContentType: "application/json",
				LastModified: time.Now(),
			}
		}
		if err := db.local.PutObject(req.Bucket, ".ec-meta/"+req.Key, data, meta); err != nil {
			writeReplicateError(w, http.StatusInternalServerError, "put meta: %v", err)
			return
		}
		resp.Status = 200

	case "ec_get_meta":
		ecMeta, err := db.readECMeta(req.Bucket, req.Key)
		if err != nil {
			resp.Status = 404
			resp.Error = fmt.Sprintf("meta not found: %v", err)
			writeReplicateJSON(w, resp)
			return
		}
		data, _ := json.Marshal(ecMeta)
		resp.Status = 200
		resp.Data = string(data)

	case "ec_delete_meta":
		db.local.DeleteObject(req.Bucket, ".ec-meta/"+req.Key)
		resp.Status = 204

	case "ec_list_shards":
		entries := db.listShardMetaEntries(req.Bucket, "", "")
		type listResp struct {
			Entries []ObjectEntry `json:"entries"`
		}
		data, _ := json.Marshal(listResp{Entries: entries})
		resp.Status = 200
		resp.Data = string(data)

	default:
		writeReplicateError(w, http.StatusBadRequest, "unknown operation: %s", req.Operation)
		return
	}

	writeReplicateJSON(w, resp)
}
