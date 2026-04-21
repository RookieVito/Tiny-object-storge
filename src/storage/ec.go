package storage

import (
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"tiny-object-storage/src/ec"
	"tiny-object-storage/src/service"
	"tiny-object-storage/src/s3error"
)

// ECBackend 基于纠删码的存储后端。
// 将对象编码为 K 个数据 shard + M 个 parity shard，分布在 N 个磁盘上。
type ECBackend struct {
	disks      []*LocalBackend // N 个磁盘
	diskStates []DiskState     // 每个磁盘的健康状态
	rs         *ec.ReedSolomon
	metaStore  *LocalBackend // EC 元数据独立存储
	dataShards int
	totalShards int
}

// DiskState 表示磁盘的健康状态。
type DiskState struct {
	Path  string
	Alive bool
}

// ECObjectMeta 存储在 metaStore 中的 EC 对象元数据。
type ECObjectMeta struct {
	Key          string            `json:"key"`
	OriginalSize int64             `json:"original_size"`
	ShardSize    int               `json:"shard_size"`
	DataShards   int               `json:"data_shards"`
	ParityShards int               `json:"parity_shards"`
	ETag         string            `json:"etag"`
	ContentType  string            `json:"content_type"`
	LastModified time.Time         `json:"last_modified"`
	UserMetadata map[string]string `json:"user_metadata,omitempty"`
}

// NewECBackend 创建 ECBackend。
// disks: N 个磁盘路径，N 必须 >= K+M。
// metaRoot: EC 元数据存储路径（独立磁盘，保证可用性）。
// dataShards, parityShards: K 和 M 参数。
func NewECBackend(disks []string, metaRoot string, dataShards, parityShards int) (*ECBackend, error) {
	if len(disks) < dataShards+parityShards {
		return nil, s3error.ErrInsufficientStorage
	}

	rs, err := ec.NewReedSolomon(dataShards, parityShards)
	if err != nil {
		return nil, err
	}

	backends := make([]*LocalBackend, len(disks))
	states := make([]DiskState, len(disks))
	for i, d := range disks {
		backends[i] = NewLocalBackend(d)
		states[i] = DiskState{Path: d, Alive: true}
	}

	return &ECBackend{
		disks:       backends,
		diskStates:  states,
		rs:          rs,
		metaStore:   NewLocalBackend(metaRoot),
		dataShards:  dataShards,
		totalShards: dataShards + parityShards,
	}, nil
}

// SetDiskState 设置磁盘的健康状态（用于测试和管理）。
func (eb *ECBackend) SetDiskState(index int, alive bool) {
	if index >= 0 && index < len(eb.diskStates) {
		eb.diskStates[index].Alive = alive
	}
}

// AliveCount 返回当前可用的磁盘数量。
func (eb *ECBackend) AliveCount() int {
	count := 0
	for _, s := range eb.diskStates {
		if s.Alive {
			count++
		}
	}
	return count
}

func (eb *ECBackend) CreateBucket(bucket string) error {
	// 在所有磁盘 + metaStore 创建 bucket。
	for _, disk := range eb.disks {
		if err := disk.CreateBucket(bucket); err != nil && err != s3error.ErrBucketAlreadyExists {
			return err
		}
	}
	if err := eb.metaStore.CreateBucket(bucket); err != nil && err != s3error.ErrBucketAlreadyExists {
		return err
	}
	return nil
}

func (eb *ECBackend) DeleteBucket(bucket string) error {
	// 检查 metaStore 中是否有对象。
	entries, _, _, _, err := eb.listECObjects(bucket, "", "", "", 10000)
	if err != nil {
		if isS3Err(err) {
			return err
		}
		return err
	}
	if len(entries) > 0 {
		return s3error.ErrBucketNotEmpty
	}

	// 在所有磁盘 + metaStore 删除 bucket。
	for _, disk := range eb.disks {
		disk.DeleteBucket(bucket) // 忽略错误（幂等）
	}
	eb.metaStore.DeleteBucket(bucket)
	return nil
}

func (eb *ECBackend) BucketExists(bucket string) (bool, error) {
	return eb.metaStore.BucketExists(bucket)
}

func (eb *ECBackend) ListBuckets() ([]BucketInfo, error) {
	return eb.metaStore.ListBuckets()
}

func (eb *ECBackend) PutObject(bucket, key string, data []byte, meta *service.ObjectMeta) error {
	// 1. Reed-Solomon 编码。
	shards, shardSize := eb.rs.Encode(data)

	// 2. 将分片写入各磁盘。
	for i := 0; i < eb.totalShards; i++ {
		if i >= len(eb.disks) {
			break // 安全检查
		}
		shardMeta := &service.ObjectMeta{
			Key:          key,
			Size:         int64(len(shards[i])),
			ETag:         meta.ETag,
			ContentType:  "application/octet-stream",
			LastModified: meta.LastModified,
		}
		if err := eb.disks[i].PutObject(bucket, key, shards[i], shardMeta); err != nil {
			return err
		}
	}

	// 3. 写入 ECObjectMeta 到 metaStore。
	ecMeta := &ECObjectMeta{
		Key:          key,
		OriginalSize: meta.Size,
		ShardSize:    shardSize,
		DataShards:   eb.dataShards,
		ParityShards: eb.rs.ParityShards(),
		ETag:         meta.ETag,
		ContentType:  meta.ContentType,
		LastModified: meta.LastModified,
		UserMetadata: meta.UserMetadata,
	}
	if err := eb.writeECMeta(bucket, key, ecMeta); err != nil {
		return err
	}

	return nil
}

func (eb *ECBackend) GetObject(bucket, key string) ([]byte, *service.ObjectMeta, error) {
	// 1. 读取 ECObjectMeta。
	ecMeta, err := eb.readECMeta(bucket, key)
	if err != nil {
		return nil, nil, err
	}

	// 2. 检查可用磁盘数。
	if eb.AliveCount() < eb.dataShards {
		return nil, nil, s3error.ErrInsufficientStorage
	}

	// 3. 从可用磁盘读取分片。
	shards := make([][]byte, eb.totalShards)
	aliveIndices := 0
	missingIndices := make([]int, 0)
	for i := 0; i < eb.totalShards && i < len(eb.disks); i++ {
		if !eb.diskStates[i].Alive {
			missingIndices = append(missingIndices, i)
			continue
		}
		data, _, err := eb.disks[i].GetObject(bucket, key)
		if err != nil {
			if isS3Err(err) {
				shards[i] = nil
				missingIndices = append(missingIndices, i)
				continue
			}
			return nil, nil, err
		}
		shards[i] = data
		aliveIndices++
	}

	if aliveIndices < eb.dataShards {
		return nil, nil, s3error.ErrInsufficientStorage
	}

	// 4. Reed-Solomon 解码恢复原始数据。
	needsRepair := len(missingIndices) > 0
	if err := eb.rs.Decode(shards, ecMeta.ShardSize); err != nil {
		return nil, nil, err
	}

	// 5. 自修复：将缺失的分片写回对应磁盘。
	if needsRepair {
		eb.repairShards(bucket, key, shards, missingIndices, ecMeta)
	}

	// 6. 拼接数据块并截断到原始大小。
	recovered := make([]byte, 0, ecMeta.OriginalSize)
	for i := 0; i < eb.dataShards; i++ {
		recovered = append(recovered, shards[i]...)
	}
	recovered = recovered[:ecMeta.OriginalSize]

	// 7. 构造 ObjectMeta。
	objMeta := &service.ObjectMeta{
		Key:          ecMeta.Key,
		Size:         ecMeta.OriginalSize,
		ETag:         ecMeta.ETag,
		ContentType:  ecMeta.ContentType,
		LastModified: ecMeta.LastModified,
		UserMetadata: ecMeta.UserMetadata,
	}

	return recovered, objMeta, nil
}

// repairShards 将缺失的分片写回对应磁盘（自修复）。
// 修复失败不影响读取结果，静默忽略。
func (eb *ECBackend) repairShards(bucket, key string, shards [][]byte, missingIndices []int, ecMeta *ECObjectMeta) {
	for _, i := range missingIndices {
		if i >= len(eb.disks) || !eb.diskStates[i].Alive || shards[i] == nil {
			continue
		}
		shardMeta := &service.ObjectMeta{
			Key:          key,
			Size:         int64(len(shards[i])),
			ETag:         ecMeta.ETag,
			ContentType:  "application/octet-stream",
			LastModified: ecMeta.LastModified,
		}
		if err := eb.disks[i].PutObject(bucket, key, shards[i], shardMeta); err != nil {
			// 修复失败不影响读取
			_ = err
		}
	}
}

func (eb *ECBackend) HeadObject(bucket, key string) (*service.ObjectMeta, error) {
	ecMeta, err := eb.readECMeta(bucket, key)
	if err != nil {
		return nil, err
	}
	return &service.ObjectMeta{
		Key:          ecMeta.Key,
		Size:         ecMeta.OriginalSize,
		ETag:         ecMeta.ETag,
		ContentType:  ecMeta.ContentType,
		LastModified: ecMeta.LastModified,
		UserMetadata: ecMeta.UserMetadata,
	}, nil
}

func (eb *ECBackend) DeleteObject(bucket, key string) error {
	// 从所有磁盘删除分片。
	for _, disk := range eb.disks {
		disk.DeleteObject(bucket, key) // 忽略错误（幂等）
	}
	// 删除 ECObjectMeta。
	eb.metaStore.DeleteObject(bucket, ecMetaKey(key))
	return nil
}

func (eb *ECBackend) ListObjects(bucket, prefix, delimiter, startAfter string, maxKeys int) (
	[]ObjectEntry, []string, string, bool, error,
) {
	return eb.listECObjects(bucket, prefix, delimiter, startAfter, maxKeys)
}

// --- 内部方法 ---

// ecMetaPath 返回 EC 元数据在 metaStore 中的 key。
// 使用 .ec-meta 后缀避免与普通对象冲突。
func ecMetaKey(key string) string {
	return key + ".ec-meta"
}

func (eb *ECBackend) writeECMeta(bucket, key string, ecMeta *ECObjectMeta) error {
	data, err := json.Marshal(ecMeta)
	if err != nil {
		return err
	}
	return eb.metaStore.PutObject(bucket, ecMetaKey(key), data, &service.ObjectMeta{
		Key:          ecMetaKey(key),
		Size:         int64(len(data)),
		ContentType:  "application/json",
		LastModified: ecMeta.LastModified,
	})
}

func (eb *ECBackend) readECMeta(bucket, key string) (*ECObjectMeta, error) {
	data, _, err := eb.metaStore.GetObject(bucket, ecMetaKey(key))
	if err != nil {
		return nil, err
	}
	var ecMeta ECObjectMeta
	if err := json.Unmarshal(data, &ecMeta); err != nil {
		return nil, err
	}
	return &ecMeta, nil
}

// listECObjects 从 metaStore 中列出 EC 对象，应用 prefix/delimiter/pagination 逻辑。
func (eb *ECBackend) listECObjects(bucket, prefix, delimiter, startAfter string, maxKeys int) (
	[]ObjectEntry, []string, string, bool, error,
) {
	// 检查 bucket 是否存在于 metaStore。
	if ok, err := eb.metaStore.BucketExists(bucket); err != nil {
		return nil, nil, "", false, err
	} else if !ok {
		return nil, nil, "", false, s3error.ErrNoSuchBucket
	}

	// 列出所有 .ec-meta 文件。
	allEntries, _, _, _, err := eb.metaStore.ListObjects(bucket, "", "", "", 100000)
	if err != nil {
		return nil, nil, "", false, err
	}

	// 解析 ECObjectMeta 并提取用户可见的对象 key。
	type rawEntry struct {
		key  string
		meta *ECObjectMeta
	}
	var entries []rawEntry
	for _, e := range allEntries {
		if !strings.HasSuffix(e.Key, ".ec-meta") {
			continue
		}
		objKey := strings.TrimSuffix(e.Key, ".ec-meta")

		data, _, err := eb.metaStore.GetObject(bucket, e.Key)
		if err != nil {
			continue
		}
		var ecMeta ECObjectMeta
		if json.Unmarshal(data, &ecMeta) != nil {
			continue
		}
		entries = append(entries, rawEntry{key: objKey, meta: &ecMeta})
	}

	// 字典序排序。
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].key < entries[j].key
	})

	// prefix 过滤。
	var filtered []rawEntry
	for _, e := range entries {
		if strings.HasPrefix(e.key, prefix) {
			filtered = append(filtered, e)
		}
	}

	// start-after 分页。
	if startAfter != "" {
		idx := sort.Search(len(filtered), func(i int) bool {
			return filtered[i].key > startAfter
		})
		filtered = filtered[idx:]
	}

	// delimiter 分组，收集到 maxKeys。
	var contents []ObjectEntry
	var commonPrefixes []string
	commonPrefixSet := make(map[string]bool)
	count := 0

	for _, e := range filtered {
		if count >= maxKeys {
			break
		}
		if delimiter == "" {
			contents = append(contents, ObjectEntry{
				Key:          e.key,
				LastModified: e.meta.LastModified,
				ETag:         e.meta.ETag,
				Size:         e.meta.OriginalSize,
				StorageClass: "STANDARD",
			})
			count++
		} else {
			remainder := strings.TrimPrefix(e.key, prefix)
			delimIdx := strings.Index(remainder, delimiter)
			if delimIdx < 0 {
				contents = append(contents, ObjectEntry{
					Key:          e.key,
					LastModified: e.meta.LastModified,
					ETag:         e.meta.ETag,
					Size:         e.meta.OriginalSize,
					StorageClass: "STANDARD",
				})
				count++
			} else {
				commonPrefix := prefix + remainder[:delimIdx+len(delimiter)]
				if !commonPrefixSet[commonPrefix] {
					commonPrefixes = append(commonPrefixes, commonPrefix)
					commonPrefixSet[commonPrefix] = true
					count++
				}
			}
		}
	}

	// 分页判断。
	isTruncated := count < len(filtered)
	nextToken := ""
	if isTruncated {
		lastKey := filtered[count-1].key
		nextToken = base64Encode(lastKey)
	}

	return contents, commonPrefixes, nextToken, isTruncated, nil
}

func base64Encode(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}
