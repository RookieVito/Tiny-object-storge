package storage

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"tiny-object-storage/src/cluster"
	"tiny-object-storage/src/hash"
	"tiny-object-storage/src/service"
	"tiny-object-storage/src/s3error"
)

// DistributedConfig 分布式后端配置。
type DistributedConfig struct {
	NodeID            string
	Addr             string
	SeedNodes        []string
	ReplicationFactor int
	ReadQuorum        int
	WriteQuorum       int
	VirtualNodes      int
	GossipInterval    time.Duration
	RPCTimeout        time.Duration
}

// DistributedBackend 分布式存储后端，实现 StorageBackend 接口。
// 使用一致性哈希分片 + Quorum 读写 + Gossip 成员管理。
type DistributedBackend struct {
	config     *DistributedConfig
	local      *LocalBackend
	ring       *hash.ConsistentHash
	membership *cluster.GossipMembership
	transport  *cluster.Transport
	mu         sync.RWMutex  // 保护 ring 更新
	seq        atomic.Int64   // 请求序号
}

// NewDistributedBackend 创建分布式后端。
func NewDistributedBackend(cfg *DistributedConfig, localRoot string) (*DistributedBackend, error) {
	// 参数校验。
	if cfg.ReplicationFactor <= 0 {
		cfg.ReplicationFactor = 3
	}
	if cfg.ReadQuorum <= 0 {
		cfg.ReadQuorum = 2
	}
	if cfg.WriteQuorum <= 0 {
		cfg.WriteQuorum = 2
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
	if cfg.ReadQuorum+cfg.WriteQuorum <= cfg.ReplicationFactor {
		return nil, fmt.Errorf("R(%d) + W(%d) must be > N(%d)", cfg.ReadQuorum, cfg.WriteQuorum, cfg.ReplicationFactor)
	}

	local := NewLocalBackend(localRoot)
	ring := hash.NewConsistentHash(cfg.VirtualNodes)
	membership := cluster.NewGossipMembership(cluster.NodeID(cfg.NodeID), cfg.Addr, cfg.SeedNodes)
	membership.SetInterval(cfg.GossipInterval)
	transport := cluster.NewTransport(cfg.Addr, cfg.RPCTimeout)
	membership.SetTransport(transport)

	db := &DistributedBackend{
		config:     cfg,
		local:      local,
		ring:       ring,
		membership: membership,
		transport:  transport,
	}

	// 注册节点状态变更回调。
	membership.OnJoin(db.onNodeJoin)
	membership.OnLeave(db.onNodeLeave)

	// 将自己加入哈希环。
	ring.AddNode(cfg.NodeID)

	return db, nil
}

// Start 启动 Gossip 协议并加入集群。
func (db *DistributedBackend) Start() error {
	db.membership.Start()
	if len(db.config.SeedNodes) > 0 {
		return db.membership.Join()
	}
	return nil
}

// Stop 停止分布式后端（优雅关闭）。
func (db *DistributedBackend) Stop() {
	db.membership.Leave()
	db.membership.Stop()
}

// MembershipHandler 返回集群 HTTP handler，用于注册到 HTTP 服务器。
// 使用 http.StripPrefix 处理 /_cluster/ 前缀，内部按 r.URL.Path 路由。
func (db *DistributedBackend) MembershipHandler() http.Handler {
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

// --- Ring 更新回调 ---

func (db *DistributedBackend) onNodeJoin(nodeID cluster.NodeID) {
	if nodeID == db.config.NodeID {
		return
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	db.ring.AddNode(nodeID)
	slog.Info("node added to hash ring", "node", nodeID, "ring_size", db.ring.Size())
}

func (db *DistributedBackend) onNodeLeave(nodeID cluster.NodeID) {
	if nodeID == db.config.NodeID {
		return
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	db.ring.RemoveNode(nodeID)
	slog.Info("node removed from hash ring", "node", nodeID, "ring_size", db.ring.Size())
}

// --- 辅助方法 ---

// nextRequestID 生成唯一请求 ID。
func (db *DistributedBackend) nextRequestID() string {
	n := db.seq.Add(1)
	b := make([]byte, 8)
	b[0] = byte(n)
	b[1] = byte(n >> 8)
	b[2] = byte(n >> 16)
	b[3] = byte(n >> 24)
	b[4] = byte(n >> 32)
	b[5] = byte(n >> 40)
	b[6] = byte(n >> 48)
	b[7] = byte(n >> 56)
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), n)
}

// replicas 返回 key 的 N 个副本节点（包含自己或远程）。
func (db *DistributedBackend) replicas(key string) []string {
	db.mu.RLock()
	defer db.mu.RUnlock()
	return db.ring.GetNodes(key, db.config.ReplicationFactor)
}

// isSelf 检查节点 ID 是否是本节点。
func (db *DistributedBackend) isSelf(nodeID string) bool {
	return nodeID == db.config.NodeID
}

// toMetaMsg 将 service.ObjectMeta 转换为传输用的 ObjectMetaMsg。
func toMetaMsg(meta *service.ObjectMeta) *cluster.ObjectMetaMsg {
	if meta == nil {
		return nil
	}
	return &cluster.ObjectMetaMsg{
		Key:          meta.Key,
		Size:         meta.Size,
		ETag:         meta.ETag,
		ContentType:  meta.ContentType,
		LastModified: meta.LastModified.Format(time.RFC3339Nano),
		UserMetadata: meta.UserMetadata,
	}
}

// fromMetaMsg 将 ObjectMetaMsg 转换为 service.ObjectMeta。
func fromMetaMsg(msg *cluster.ObjectMetaMsg) (*service.ObjectMeta, error) {
	if msg == nil {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339Nano, msg.LastModified)
	if err != nil {
		return nil, fmt.Errorf("parse last_modified: %w", err)
	}
	return &service.ObjectMeta{
		Key:          msg.Key,
		Size:         msg.Size,
		ETag:         msg.ETag,
		ContentType:  msg.ContentType,
		LastModified: t,
		UserMetadata: msg.UserMetadata,
	}, nil
}

// --- StorageBackend 接口实现 ---

func (db *DistributedBackend) CreateBucket(bucket string) error {
	reps := db.replicas(bucket)
	var wg sync.WaitGroup
	successes := int32(0)
	errs := make([]error, len(reps))

	for i, nodeID := range reps {
		wg.Add(1)
		go func(idx int, node string) {
			defer wg.Done()
			var err error
			if db.isSelf(node) {
				err = db.local.CreateBucket(bucket)
			} else {
				req := &cluster.StorageRequest{
					RequestID: db.nextRequestID(),
					Operation: "create_bucket",
					Bucket:    bucket,
				}
				resp, rpcErr := db.transport.Replicate(cluster.NodeID(node), req)
				if rpcErr != nil {
					err = rpcErr
				} else if resp.Status >= 400 {
					err = fmt.Errorf("remote error: %s", resp.Error)
				}
			}
			errs[idx] = err
			if err == nil {
				atomic.AddInt32(&successes, 1)
			}
		}(i, nodeID)
	}
	wg.Wait()

	if int(successes) >= db.config.WriteQuorum {
		return nil
	}
	return s3error.ErrWriteQuorumFailed
}

func (db *DistributedBackend) DeleteBucket(bucket string) error {
	// 先检查副本节点上是否为空。
	reps := db.replicas(bucket)
	var wg sync.WaitGroup
	notEmptyCount := int32(0)

	for _, nodeID := range reps {
		wg.Add(1)
		go func(node string) {
			defer wg.Done()
			if db.isSelf(node) {
				if ok, _ := db.local.BucketExists(bucket); !ok {
					return
				}
				entries, _, _, _, err := db.local.ListObjects(bucket, "", "", "", 1)
				if err != nil || len(entries) > 0 {
					atomic.AddInt32(&notEmptyCount, 1)
				}
			} else {
				req := &cluster.StorageRequest{
					RequestID: db.nextRequestID(),
					Operation: "head_bucket",
					Bucket:    bucket,
				}
				resp, err := db.transport.Replicate(cluster.NodeID(node), req)
				if err != nil {
					return
				}
				if resp.Status == 404 {
					return
				}
				if resp.Status == 409 || resp.Status == 200 {
					atomic.AddInt32(&notEmptyCount, 1)
				}
			}
		}(nodeID)
	}
	wg.Wait()

	if notEmptyCount > 0 {
		return s3error.ErrBucketNotEmpty
	}

	// 在所有副本上删除。
	successes := int32(0)
	for _, nodeID := range reps {
		wg.Add(1)
		go func(node string) {
			defer wg.Done()
			if db.isSelf(node) {
				if err := db.local.DeleteBucket(bucket); err != nil {
					if !isS3Err(err) {
						slog.Debug("delete bucket error on local", "err", err)
					}
					return
				}
			} else {
				req := &cluster.StorageRequest{
					RequestID: db.nextRequestID(),
					Operation: "delete_bucket",
					Bucket:    bucket,
				}
				db.transport.Replicate(cluster.NodeID(node), req) // 忽略错误
			}
			atomic.AddInt32(&successes, 1)
		}(nodeID)
	}
	wg.Wait()

	if int(successes) >= db.config.WriteQuorum {
		return nil
	}
	return s3error.ErrWriteQuorumFailed
}

func (db *DistributedBackend) BucketExists(bucket string) (bool, error) {
	reps := db.replicas(bucket)
	for _, nodeID := range reps {
		if db.isSelf(nodeID) {
			return db.local.BucketExists(bucket)
		}
		req := &cluster.StorageRequest{
			RequestID: db.nextRequestID(),
			Operation: "head_bucket",
			Bucket:    bucket,
		}
		resp, err := db.transport.Replicate(cluster.NodeID(nodeID), req)
		if err != nil {
			continue
		}
		return resp.Status == 200, nil
	}
	return false, s3error.ErrReadQuorumFailed
}

func (db *DistributedBackend) ListBuckets() ([]BucketInfo, error) {
	// 查询所有存活节点的本地 bucket 列表并合并去重。
	aliveNodes := db.membership.AliveNodes()
	type bucketResult struct {
		info BucketInfo
	}
	resultCh := make(chan []BucketInfo, len(aliveNodes))
	var wg sync.WaitGroup

	for _, node := range aliveNodes {
		wg.Add(1)
		go func(nodeInfo *cluster.NodeInfo) {
			defer wg.Done()
			if db.isSelf(nodeInfo.ID) {
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
			if err != nil {
				resultCh <- nil
				return
			}
			if resp.Status != 200 || resp.Data == "" {
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

	// 合并去重：同名 bucket 取最早的 CreationDate。
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

func (db *DistributedBackend) PutObject(bucket, key string, data []byte, meta *service.ObjectMeta) error {
	reps := db.replicas(bucket + "/" + key)
	if len(reps) == 0 {
		return s3error.ErrWriteQuorumFailed
	}

	b64Data := base64.StdEncoding.EncodeToString(data)
	metaMsg := toMetaMsg(meta)

	var wg sync.WaitGroup
	successes := int32(0)

	for _, nodeID := range reps {
		wg.Add(1)
		go func(node string) {
			defer wg.Done()
			if db.isSelf(node) {
				if err := db.local.PutObject(bucket, key, data, meta); err != nil {
					if !isS3Err(err) {
						slog.Debug("put object error on local", "err", err)
					}
					return
				}
				atomic.AddInt32(&successes, 1)
				return
			}

			req := &cluster.StorageRequest{
				RequestID: db.nextRequestID(),
				Operation: "put",
				Bucket:    bucket,
				Key:       key,
				Data:      b64Data,
				Meta:      metaMsg,
			}
			resp, err := db.transport.Replicate(cluster.NodeID(node), req)
			if err != nil {
				slog.Debug("replicate put error", "target", node, "err", err)
				return
			}
			if resp.Status >= 200 && resp.Status < 300 {
				atomic.AddInt32(&successes, 1)
			}
		}(nodeID)
	}
	wg.Wait()

	if int(successes) >= db.config.WriteQuorum {
		return nil
	}
	return s3error.ErrWriteQuorumFailed
}

func (db *DistributedBackend) GetObject(bucket, key string) ([]byte, *service.ObjectMeta, error) {
	reps := db.replicas(bucket + "/" + key)
	if len(reps) == 0 {
		return nil, nil, s3error.ErrReadQuorumFailed
	}

	// 并发读，取第一个成功的结果。
	type getResult struct {
		data []byte
		meta *service.ObjectMeta
		err  error
	}
	resultCh := make(chan *getResult, len(reps))
	var wg sync.WaitGroup
	successes := int32(0)

	for _, nodeID := range reps {
		wg.Add(1)
		go func(node string) {
			defer wg.Done()
			if db.isSelf(node) {
				data, meta, err := db.local.GetObject(bucket, key)
				resultCh <- &getResult{data: data, meta: meta, err: err}
				if err == nil {
					atomic.AddInt32(&successes, 1)
				}
				return
			}

			req := &cluster.StorageRequest{
				RequestID: db.nextRequestID(),
				Operation: "get",
				Bucket:    bucket,
				Key:       key,
			}
			resp, err := db.transport.Replicate(cluster.NodeID(node), req)
			if err != nil {
				resultCh <- &getResult{err: err}
				return
			}
			if resp.Status >= 400 {
				if resp.Status == 404 {
					resultCh <- &getResult{err: s3error.ErrNoSuchKey}
				} else {
					resultCh <- &getResult{err: fmt.Errorf("remote error: %s", resp.Error)}
				}
				return
			}
			atomic.AddInt32(&successes, 1)

			data, decodeErr := base64.StdEncoding.DecodeString(resp.Data)
			if decodeErr != nil {
				resultCh <- &getResult{err: decodeErr}
				return
			}
			meta, metaErr := fromMetaMsg(resp.Meta)
			if metaErr != nil {
				resultCh <- &getResult{err: metaErr}
				return
			}
			resultCh <- &getResult{data: data, meta: meta}
		}(nodeID)
	}

	// 等待第一个成功结果或全部完成。
	var firstSuccess *getResult
	var anyS3Err error
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	for {
		select {
		case result := <-resultCh:
			if result.err == nil {
				if firstSuccess == nil {
					firstSuccess = result
				}
				// 收集够了就返回。
				if int(successes) >= db.config.ReadQuorum && firstSuccess != nil {
					return firstSuccess.data, firstSuccess.meta, nil
				}
			} else if isS3Err(result.err) {
				if anyS3Err == nil {
					anyS3Err = result.err
				}
			}
		case <-done:
			if firstSuccess != nil {
				return firstSuccess.data, firstSuccess.meta, nil
			}
			if anyS3Err != nil {
				return nil, nil, anyS3Err
			}
			return nil, nil, s3error.ErrReadQuorumFailed
		}
	}
}

func (db *DistributedBackend) HeadObject(bucket, key string) (*service.ObjectMeta, error) {
	reps := db.replicas(bucket + "/" + key)
	if len(reps) == 0 {
		return nil, s3error.ErrReadQuorumFailed
	}

	for _, nodeID := range reps {
		if db.isSelf(nodeID) {
			meta, err := db.local.HeadObject(bucket, key)
			if err == nil {
				return meta, nil
			}
			if isS3Err(err) {
				return nil, err
			}
			continue
		}

		req := &cluster.StorageRequest{
			RequestID: db.nextRequestID(),
			Operation: "head",
			Bucket:    bucket,
			Key:       key,
		}
		resp, err := db.transport.Replicate(cluster.NodeID(nodeID), req)
		if err != nil {
			continue
		}
		if resp.Status == 404 {
			return nil, s3error.ErrNoSuchKey
		}
		if resp.Status >= 400 {
			continue
		}
		return fromMetaMsg(resp.Meta)
	}

	return nil, s3error.ErrReadQuorumFailed
}

func (db *DistributedBackend) DeleteObject(bucket, key string) error {
	reps := db.replicas(bucket + "/" + key)
	var wg sync.WaitGroup
	successes := int32(0)

	for _, nodeID := range reps {
		wg.Add(1)
		go func(node string) {
			defer wg.Done()
			if db.isSelf(node) {
				db.local.DeleteObject(bucket, key) // 幂等
			} else {
				req := &cluster.StorageRequest{
					RequestID: db.nextRequestID(),
					Operation: "delete",
					Bucket:    bucket,
					Key:       key,
				}
				db.transport.Replicate(cluster.NodeID(node), req) // 幂等，忽略错误
			}
			atomic.AddInt32(&successes, 1)
		}(nodeID)
	}
	wg.Wait()

	if int(successes) >= db.config.WriteQuorum {
		return nil
	}
	return s3error.ErrWriteQuorumFailed
}

func (db *DistributedBackend) ListObjects(bucket, prefix, delimiter, startAfter string, maxKeys int) (
	[]ObjectEntry, []string, string, bool, error,
) {
	reps := db.replicas(bucket)
	if len(reps) == 0 {
		return nil, nil, "", false, s3error.ErrReadQuorumFailed
	}

	// 从 R 个副本收集本地列表。
	type listResult struct {
		entries []ObjectEntry
		err     error
	}
	resultCh := make(chan *listResult, len(reps))
	var wg sync.WaitGroup
	successes := int32(0)

	for _, nodeID := range reps {
		wg.Add(1)
		go func(node string) {
			defer wg.Done()
			if db.isSelf(node) {
				entries, cp, token, truncated, err := db.local.ListObjects(bucket, prefix, delimiter, startAfter, maxKeys)
				if err != nil {
					resultCh <- &listResult{err: err}
					return
				}
				_ = cp
				_ = token
				_ = truncated
				resultCh <- &listResult{entries: entries}
				atomic.AddInt32(&successes, 1)
				return
			}

			req := &cluster.StorageRequest{
				RequestID: db.nextRequestID(),
				Operation: "list_objects",
				Bucket:    bucket,
			}
			resp, err := db.transport.Replicate(cluster.NodeID(node), req)
			if err != nil {
				resultCh <- &listResult{err: err}
				return
			}
			if resp.Status >= 400 {
				resultCh <- &listResult{err: fmt.Errorf("remote error: %s", resp.Error)}
				return
			}
			atomic.AddInt32(&successes, 1)

			// 解析 entries JSON。
			type listResp struct {
				Entries []ObjectEntry `json:"entries"`
			}
			var lr listResp
			if json.Unmarshal([]byte(resp.Data), &lr) == nil {
				resultCh <- &listResult{entries: lr.Entries}
			} else {
				resultCh <- &listResult{entries: nil}
			}
		}(nodeID)
	}

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	// 合并：按 key 去重，取最新的 LastModified。
	merged := make(map[string]ObjectEntry)
	anyErr := false
	for result := range resultCh {
		if result.err != nil {
			anyErr = true
			continue
		}
		for _, e := range result.entries {
			if existing, ok := merged[e.Key]; !ok || e.LastModified.After(existing.LastModified) {
				merged[e.Key] = e
			}
		}
	}

	if anyErr && len(merged) == 0 {
		// 检查 bucket 是否存在。
		exists, err := db.BucketExists(bucket)
		if err != nil {
			return nil, nil, "", false, err
		}
		if !exists {
			return nil, nil, "", false, s3error.ErrNoSuchBucket
		}
	}

	// 收集所有 entries 并排序。
	allEntries := make([]ObjectEntry, 0, len(merged))
	for _, e := range merged {
		allEntries = append(allEntries, e)
	}
	sortEntries(allEntries)

	// 应用 prefix 过滤。
	var filtered []ObjectEntry
	for _, e := range allEntries {
		if prefix != "" && !hasPrefix(e.Key, prefix) {
			continue
		}
		if startAfter != "" && e.Key <= startAfter {
			continue
		}
		filtered = append(filtered, e)
	}

	// 应用 delimiter 分组和 maxKeys 截断。
	var contents []ObjectEntry
	var commonPrefixes []string
	cpSet := make(map[string]bool)
	count := 0
	truncated := false

	for _, e := range filtered {
		if count >= maxKeys {
			truncated = true
			break
		}
		if delimiter == "" {
			contents = append(contents, e)
			count++
		} else {
			remainder := trimPrefixStr(e.Key, prefix)
			idx := indexOfStr(remainder, delimiter)
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
	if truncated && len(filtered) > 0 {
		lastKey := filtered[count-1].Key
		nextToken = base64Encode(lastKey)
	}

	return contents, commonPrefixes, nextToken, truncated, nil
}

// --- Replicate Handler ---

// HandleReplicate 处理来自其他节点的存储复制请求。
// 注册到 /_cluster/replicate 端点。
func (db *DistributedBackend) HandleReplicate(w http.ResponseWriter, r *http.Request) {
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
		err := db.local.DeleteBucket(req.Bucket)
		if err != nil {
			writeReplicateS3Err(w, resp, err)
			return
		}
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

	default:
		writeReplicateError(w, http.StatusBadRequest, "unknown operation: %s", req.Operation)
		return
	}

	writeReplicateJSON(w, resp)
}

// --- 辅助函数 ---

func writeReplicateJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeReplicateError(w http.ResponseWriter, status int, format string, args ...any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(&cluster.StorageResponse{
		Status: status,
		Error:  fmt.Sprintf(format, args...),
	})
}

func writeReplicateS3Err(w http.ResponseWriter, resp *cluster.StorageResponse, err error) {
	if s3Err, ok := err.(*s3error.S3APIError); ok {
		resp.Status = s3Err.Status
		resp.Error = s3Err.Code + ": " + s3Err.Message
	} else {
		resp.Status = 500
		resp.Error = err.Error()
	}
	writeReplicateJSON(w, resp)
}

// 字符串辅助函数（避免引入 strings 包对某些操作的开销）。

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func trimPrefixStr(s, prefix string) string {
	if hasPrefix(s, prefix) {
		return s[len(prefix):]
	}
	return s
}

func indexOfStr(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

func sortEntries(entries []ObjectEntry) {
	for i := 0; i < len(entries)-1; i++ {
		for j := i + 1; j < len(entries); j++ {
			if entries[j].Key < entries[i].Key {
				entries[i], entries[j] = entries[j], entries[i]
			}
		}
	}
}
