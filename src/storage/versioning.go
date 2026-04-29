package storage

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"tiny-object-storage/src/service"
	"tiny-object-storage/src/s3error"
)

const bucketMetaKey = ".bucket-meta"

// bucketVersioningConfig 存储桶的版本控制配置。
type bucketVersioningConfig struct {
	Versioning string `json:"versioning"` // "Unversioned" | "Enabled" | "Suspended"
}

// VersionedBackend 装饰器，为任意 StorageBackend 添加对象版本控制。
type VersionedBackend struct {
	inner StorageBackend
}

// NewVersionedBackend 创建版本控制装饰器。
func NewVersionedBackend(inner StorageBackend) *VersionedBackend {
	return &VersionedBackend{inner: inner}
}

// --- StorageBackend 接口实现 ---

func (vb *VersionedBackend) CreateBucket(bucket string) error {
	return vb.inner.CreateBucket(bucket)
}

func (vb *VersionedBackend) DeleteBucket(bucket string) error {
	return vb.inner.DeleteBucket(bucket)
}

func (vb *VersionedBackend) BucketExists(bucket string) (bool, error) {
	return vb.inner.BucketExists(bucket)
}

func (vb *VersionedBackend) ListBuckets() ([]BucketInfo, error) {
	return vb.inner.ListBuckets()
}

func (vb *VersionedBackend) PutObject(bucket, key string, data []byte, meta *service.ObjectMeta) error {
	if key == bucketMetaKey || strings.HasPrefix(key, ".versions/") {
		return vb.inner.PutObject(bucket, key, data, meta)
	}

	versioned, err := vb.isVersionedBucket(bucket)
	if err != nil {
		return err
	}
	if !versioned {
		return vb.inner.PutObject(bucket, key, data, meta)
	}

	// 归档当前版本。
	if err := vb.archiveCurrentVersion(bucket, key); err != nil {
		slog.Warn("failed to archive current version", "bucket", bucket, "key", key, "err", err)
	}

	versionId := generateVersionId()
	meta.VersionId = versionId
	meta.IsLatest = true

	return vb.inner.PutObject(bucket, key, data, meta)
}

func (vb *VersionedBackend) GetObject(bucket, key string) ([]byte, *service.ObjectMeta, error) {
	if strings.HasPrefix(key, ".versions/") {
		return vb.inner.GetObject(bucket, key)
	}

	versioned, err := vb.isVersionedBucket(bucket)
	if err != nil {
		return nil, nil, err
	}
	if !versioned {
		return vb.inner.GetObject(bucket, key)
	}

	// 检查 delete marker 哨兵。
	if vb.isCurrentDeleted(bucket, key) {
		return nil, nil, s3error.ErrNoSuchKey
	}

	data, meta, err := vb.inner.GetObject(bucket, key)
	if err != nil {
		return nil, nil, err
	}
	if meta.VersionId != "" {
		return data, meta, nil
	}
	// 兼容：已存在对象在启用版本控制前写入，无 versionId。
	meta.VersionId = "null"
	return data, meta, nil
}

func (vb *VersionedBackend) HeadObject(bucket, key string) (*service.ObjectMeta, error) {
	if strings.HasPrefix(key, ".versions/") {
		return vb.inner.HeadObject(bucket, key)
	}

	versioned, err := vb.isVersionedBucket(bucket)
	if err != nil {
		return nil, err
	}
	if !versioned {
		return vb.inner.HeadObject(bucket, key)
	}

	if vb.isCurrentDeleted(bucket, key) {
		return nil, s3error.ErrNoSuchKey
	}

	meta, err := vb.inner.HeadObject(bucket, key)
	if err != nil {
		return nil, err
	}
	if meta.VersionId == "" {
		meta.VersionId = "null"
	}
	return meta, nil
}

func (vb *VersionedBackend) DeleteObject(bucket, key string) error {
	if strings.HasPrefix(key, ".versions/") || key == bucketMetaKey {
		return vb.inner.DeleteObject(bucket, key)
	}

	versioned, err := vb.isVersionedBucket(bucket)
	if err != nil {
		return err
	}
	if !versioned {
		return vb.inner.DeleteObject(bucket, key)
	}

	// 幂等：当前已是 delete marker → 直接返回。
	if vb.isCurrentDeleted(bucket, key) {
		return nil
	}

	versionId := generateVersionId()

	// 归档当前版本（如果存在）。
	currentData, currentMeta, err := vb.inner.GetObject(bucket, key)
	if err == nil {
		// 有当前版本 → 归档后创建 delete marker。
		archiveKey := ".versions/" + safeKey(key) + "/" + currentMeta.VersionId
		currentMeta.IsLatest = false
		currentMeta.IsDeleteMarker = false
		if currentMeta.VersionId == "" {
			currentMeta.VersionId = "null"
		}
		vb.inner.PutObject(bucket, archiveKey, currentData, currentMeta)
		vb.inner.DeleteObject(bucket, key)
	} else {
		// 当前不存在 → 直接创建 delete marker（幂等）。
	}

	// 写入 delete marker。
	dmMeta := &service.ObjectMeta{
		Key:            key,
		Size:           0,
		ETag:           "",
		ContentType:    "",
		LastModified:   time.Now().UTC(),
		VersionId:      versionId,
		IsLatest:       true,
		IsDeleteMarker: true,
	}
	dmKey := ".versions/" + safeKey(key) + "/.dm-" + versionId
	if err := vb.inner.PutObject(bucket, dmKey, []byte{}, dmMeta); err != nil {
		return err
	}

	// 写入 delete marker 哨兵。
	sentinelMeta := &service.ObjectMeta{
		Key:          ".current-delete-marker",
		Size:         0,
		LastModified: time.Now().UTC(),
		VersionId:    versionId,
	}
	sentinelKey := ".versions/" + safeKey(key) + "/.current-delete-marker"
	return vb.inner.PutObject(bucket, sentinelKey, []byte{}, sentinelMeta)
}

func (vb *VersionedBackend) ListObjects(bucket, prefix, delimiter, startAfter string, maxKeys int) (
	[]ObjectEntry, []string, string, bool, error,
) {
	// 请求足够多的条目以补偿过滤后的数量。
	requestMax := maxKeys * 3
	if requestMax < 1000 {
		requestMax = 1000
	}

	var allEntries []ObjectEntry
	var allCPs []string
	cpSet := make(map[string]bool)
	innerStart := startAfter
	for {
		entries, cps, _, truncated, err := vb.inner.ListObjects(bucket, prefix, delimiter, innerStart, requestMax)
		if err != nil {
			return nil, nil, "", false, err
		}
		for _, e := range entries {
			if !strings.HasPrefix(e.Key, ".") {
				allEntries = append(allEntries, e)
			}
		}
		for _, cp := range cps {
			if !cpSet[cp] {
				cpSet[cp] = true
				allCPs = append(allCPs, cp)
			}
		}
		if !truncated || len(entries) == 0 {
			break
		}
		if len(allEntries) >= maxKeys {
			break
		}
		innerStart = entries[len(entries)-1].Key
	}

	if len(allEntries) > maxKeys {
		allEntries = allEntries[:maxKeys]
		token := allEntries[maxKeys-1].Key
		return allEntries, allCPs, token, true, nil
	}
	return allEntries, allCPs, "", false, nil
}

// --- VersionedStorage 接口实现 ---

func (vb *VersionedBackend) PutBucketVersioning(bucket, status string) error {
	current, _ := vb.getBucketVersioning(bucket)
	switch status {
	case "Enabled":
		if current == "Enabled" {
			return s3error.ErrVersioningAlreadyEnabled
		}
	case "Suspended":
		if current == "Suspended" {
			return s3error.ErrVersioningAlreadySuspended
		}
	default:
		return fmt.Errorf("invalid versioning status: %s", status)
	}

	cfg := bucketVersioningConfig{Versioning: status}
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	meta := &service.ObjectMeta{
		Key:          bucketMetaKey,
		Size:         int64(len(data)),
		ContentType:  "application/json",
		LastModified: time.Now().UTC(),
	}
	return vb.inner.PutObject(bucket, bucketMetaKey, data, meta)
}

func (vb *VersionedBackend) GetBucketVersioning(bucket string) (string, error) {
	return vb.getBucketVersioning(bucket)
}

func (vb *VersionedBackend) GetObjectVersion(bucket, key, versionId string) ([]byte, *service.ObjectMeta, error) {
	sk := safeKey(key)
	// 先检查归档版本。
	if data, meta, err := vb.inner.GetObject(bucket, ".versions/"+sk+"/"+versionId); err == nil {
		return data, meta, nil
	}
	// 检查归档 delete marker。
	if _, meta, err := vb.inner.GetObject(bucket, ".versions/"+sk+"/.dm-"+versionId); err == nil {
		return []byte{}, meta, nil
	}
	// 检查当前版本。
	data, meta, err := vb.inner.GetObject(bucket, key)
	if err != nil {
		return nil, nil, s3error.ErrNoSuchVersion
	}
	if meta.VersionId == versionId {
		return data, meta, nil
	}
	return nil, nil, s3error.ErrNoSuchVersion
}

func (vb *VersionedBackend) HeadObjectVersion(bucket, key, versionId string) (*service.ObjectMeta, error) {
	sk := safeKey(key)
	// 先检查归档版本。
	if meta, err := vb.inner.HeadObject(bucket, ".versions/"+sk+"/"+versionId); err == nil {
		return meta, nil
	}
	// 检查归档 delete marker。
	if meta, err := vb.inner.HeadObject(bucket, ".versions/"+sk+"/.dm-"+versionId); err == nil {
		return meta, nil
	}
	// 检查当前版本。
	meta, err := vb.inner.HeadObject(bucket, key)
	if err != nil {
		return nil, s3error.ErrNoSuchVersion
	}
	if meta.VersionId == versionId {
		return meta, nil
	}
	return nil, s3error.ErrNoSuchVersion
}

func (vb *VersionedBackend) DeleteObjectVersion(bucket, key, versionId string) error {
	sk := safeKey(key)
	normalKey := ".versions/" + sk + "/" + versionId
	dmKey := ".versions/" + sk + "/.dm-" + versionId

	// 检查是否为归档的普通版本。
	_, normalErr := vb.inner.HeadObject(bucket, normalKey)
	isArchivedNormal := normalErr == nil

	// 检查是否为归档的 delete marker。
	_, dmErr := vb.inner.HeadObject(bucket, dmKey)
	isArchivedDM := dmErr == nil

	// 检查是否为当前版本（未归档）。
	isCurrent := false
	var currentMeta *service.ObjectMeta
	if !isArchivedNormal && !isArchivedDM {
		cm, cmErr := vb.inner.HeadObject(bucket, key)
		if cmErr == nil && cm.VersionId == versionId {
			isCurrent = true
			currentMeta = cm
		}
	}

	if !isArchivedNormal && !isArchivedDM && !isCurrent {
		return s3error.ErrNoSuchVersion
	}

	// 如果是当前版本，先归档再删除。
	if isCurrent {
		if currentMeta.IsDeleteMarker {
			// 当前版本是 delete marker（不应出现，但防御性处理）。
			archiveKey := ".versions/" + sk + "/.dm-" + versionId
			vb.inner.PutObject(bucket, archiveKey, []byte{}, currentMeta)
		} else {
			vb.archiveCurrentVersion(bucket, key)
		}
		vb.inner.DeleteObject(bucket, key)
		// 归档后版本已在 .versions/ 中。
		isArchivedNormal = !currentMeta.IsDeleteMarker
		isArchivedDM = currentMeta.IsDeleteMarker
	}

	// 删除归档的版本数据。
	if isArchivedNormal {
		if err := vb.inner.DeleteObject(bucket, normalKey); err != nil {
			return err
		}
	} else if isArchivedDM {
		if err := vb.inner.DeleteObject(bucket, dmKey); err != nil {
			return err
		}
	}

	// 如果删除的是当前 delete marker，提升前一版本。
	if isArchivedDM {
		sentinelKey := ".versions/" + sk + "/.current-delete-marker"
		sentinelMeta, sErr := vb.inner.HeadObject(bucket, sentinelKey)
		if sErr != nil || sentinelMeta.VersionId != versionId {
			return nil // 不是当前 delete marker，无需提升。
		}
		vb.inner.DeleteObject(bucket, sentinelKey)

		// 查找该 key 的所有剩余版本。
		entries, _, _, _, err := vb.inner.ListObjects(bucket, ".versions/"+sk+"/", "", "", 1000)
		if err != nil || len(entries) == 0 {
			return nil
		}

		// 找到最新版本（按 LastModified 排序）。
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].LastModified.After(entries[j].LastModified)
		})
		latestKey := entries[0].Key

		isLatestDM := strings.Contains(latestKey, "/.dm-")

		if isLatestDM {
			// 最新仍是 delete marker → 更新哨兵。
			latestVid := strings.TrimPrefix(latestKey, ".versions/"+sk+"/.dm-")
			newSentinel := &service.ObjectMeta{
				Key:          ".current-delete-marker",
				Size:         0,
				LastModified: time.Now().UTC(),
				VersionId:    latestVid,
			}
			vb.inner.PutObject(bucket, ".versions/"+sk+"/.current-delete-marker", []byte{}, newSentinel)
		} else {
			// 提升为当前版本。
			latestData, _, err := vb.inner.GetObject(bucket, latestKey)
			if err != nil {
				return nil
			}
			latestMeta, err := vb.inner.HeadObject(bucket, latestKey)
			if err != nil {
				return nil
			}
			latestMeta.IsLatest = true
			vb.inner.PutObject(bucket, key, latestData, latestMeta)
			vb.inner.DeleteObject(bucket, latestKey)
			vb.inner.DeleteObject(bucket, latestKey+".meta")
		}
	}

	return nil
}

func (vb *VersionedBackend) ListObjectVersions(bucket, prefix, delimiter, keyMarker, versionIdMarker string, maxKeys int) (
	versions, deleteMarkers []VersionEntry, commonPrefixes []string,
	nextKeyMarker, nextVersionIdMarker string, isTruncated bool, err error,
) {
	archivePrefix := ".versions/"
	archiveEntries, _, _, _, listErr := vb.inner.ListObjects(bucket, archivePrefix, "", "", 10000)
	if listErr != nil {
		return nil, nil, nil, "", "", false, listErr
	}

	type versionInfo struct {
		key, versionId   string
		isDeleteMarker   bool
		lastModified     time.Time
		etag             string
		size             int64
	}

	var allVersions []versionInfo
	deletedKeys := make(map[string]bool) // 当前被 delete marker 覆盖的 key

	for _, e := range archiveEntries {
		rel := strings.TrimPrefix(e.Key, archivePrefix)
		parts := strings.SplitN(rel, "/", 2)
		if len(parts) != 2 {
			continue
		}
		safeK, filePart := parts[0], parts[1]

		if filePart == ".current-delete-marker" {
			deletedKeys[unSafeKey(safeK)] = true
			continue
		}
		if strings.HasPrefix(filePart, ".dm-") {
			deletedKeys[unSafeKey(safeK)] = true
		}

		origKey := unSafeKey(safeK)
		isDM := strings.HasPrefix(filePart, ".dm-")
		vid := filePart
		if isDM {
			vid = strings.TrimPrefix(filePart, ".dm-")
		}

		allVersions = append(allVersions, versionInfo{
			key:            origKey,
			versionId:      vid,
			isDeleteMarker: isDM,
			lastModified:   e.LastModified,
			etag:           e.ETag,
			size:           e.Size,
		})
	}

	// 收集当前对象（未归档的最新版本，即当前 key 本身）。
	currentObjects, _, _, _, err := vb.inner.ListObjects(bucket, "", "", "", 10000)
	if err != nil {
		return nil, nil, nil, "", "", false, err
	}
	for _, e := range currentObjects {
		if strings.HasPrefix(e.Key, ".") {
			continue
		}
		if !deletedKeys[e.Key] {
			allVersions = append(allVersions, versionInfo{
				key:          e.Key,
				versionId:    "null",
				lastModified: e.LastModified,
				etag:         e.ETag,
				size:         e.Size,
			})
		}
	}

	// 排序：按 key 字典序，同 key 按时间倒序。
	sort.Slice(allVersions, func(i, j int) bool {
		if allVersions[i].key != allVersions[j].key {
			return allVersions[i].key < allVersions[j].key
		}
		return allVersions[i].lastModified.After(allVersions[j].lastModified)
	})

	// 前缀过滤 + keyMarker 跳过。
	var filtered []versionInfo
	for _, vi := range allVersions {
		if prefix != "" && !strings.HasPrefix(vi.key, prefix) {
			continue
		}
		if keyMarker != "" {
			if vi.key < keyMarker {
				continue
			}
			if vi.key == keyMarker && versionIdMarker != "" && vi.versionId < versionIdMarker {
				continue
			}
		}
		filtered = append(filtered, vi)
	}

	// delimiter 分组。
	if delimiter != "" {
		prefixSet := make(map[string]bool)
		var result []versionInfo
		for _, vi := range filtered {
			afterPrefix := strings.TrimPrefix(vi.key, prefix)
			idx := strings.Index(afterPrefix, delimiter)
			if idx >= 0 {
				cp := prefix + afterPrefix[:idx] + delimiter
				if !prefixSet[cp] {
					prefixSet[cp] = true
					commonPrefixes = append(commonPrefixes, cp)
				}
			} else {
				result = append(result, vi)
			}
		}
		filtered = result
	}

	// 确定 isLatest：排序后同 key 按时间倒序，第一个是 latest。
	seenKey := make(map[string]bool)

	if maxKeys <= 0 {
		maxKeys = 1000
	}

	// 分离 versions/delete markers + 分页。
	count := 0
	for _, vi := range filtered {
		isLatest := !seenKey[vi.key]
		seenKey[vi.key] = true

		entry := VersionEntry{
			Key:            vi.key,
			VersionId:      vi.versionId,
			IsLatest:       isLatest,
			IsDeleteMarker: vi.isDeleteMarker,
			ETag:           vi.etag,
			Size:           vi.size,
			LastModified:   vi.lastModified,
		}
		if vi.isDeleteMarker {
			deleteMarkers = append(deleteMarkers, entry)
		} else {
			versions = append(versions, entry)
		}
		count++
		if count >= maxKeys {
			nextKeyMarker = vi.key
			nextVersionIdMarker = vi.versionId
			isTruncated = true
			break
		}
	}

	return versions, deleteMarkers, commonPrefixes, nextKeyMarker, nextVersionIdMarker, isTruncated, nil
}

// --- MultipartStorage 委托 ---

var _ MultipartStorage = (*VersionedBackend)(nil)

func (vb *VersionedBackend) InitiateUpload(bucket, key string, contentType string, userMeta map[string]string) (*UploadInfo, error) {
	if ms, ok := vb.inner.(MultipartStorage); ok {
		return ms.InitiateUpload(bucket, key, contentType, userMeta)
	}
	return nil, s3error.ErrNotImplemented
}

func (vb *VersionedBackend) UploadPart(bucket, key, uploadId string, partNumber int, data []byte) (*PartInfo, error) {
	if ms, ok := vb.inner.(MultipartStorage); ok {
		return ms.UploadPart(bucket, key, uploadId, partNumber, data)
	}
	return nil, s3error.ErrNotImplemented
}

func (vb *VersionedBackend) CompleteUpload(bucket, key, uploadId string, parts []PartInfo) (string, error) {
	if ms, ok := vb.inner.(MultipartStorage); ok {
		return ms.CompleteUpload(bucket, key, uploadId, parts)
	}
	return "", s3error.ErrNotImplemented
}

func (vb *VersionedBackend) AbortUpload(bucket, key, uploadId string) error {
	if ms, ok := vb.inner.(MultipartStorage); ok {
		return ms.AbortUpload(bucket, key, uploadId)
	}
	return s3error.ErrNotImplemented
}

func (vb *VersionedBackend) ListParts(bucket, key, uploadId string) ([]PartInfo, error) {
	if ms, ok := vb.inner.(MultipartStorage); ok {
		return ms.ListParts(bucket, key, uploadId)
	}
	return nil, s3error.ErrNotImplemented
}

func (vb *VersionedBackend) ListUploads(bucket, prefix, keyMarker string, maxUploads int) ([]UploadInfo, string, bool, error) {
	if ms, ok := vb.inner.(MultipartStorage); ok {
		return ms.ListUploads(bucket, prefix, keyMarker, maxUploads)
	}
	return nil, "", false, s3error.ErrNotImplemented
}

func (vb *VersionedBackend) GetUploadInfo(bucket, key, uploadId string) (*UploadInfo, error) {
	if ms, ok := vb.inner.(MultipartStorage); ok {
		return ms.GetUploadInfo(bucket, key, uploadId)
	}
	return nil, s3error.ErrNotImplemented
}

// --- 内部方法 ---

func (vb *VersionedBackend) isVersionedBucket(bucket string) (bool, error) {
	status, err := vb.getBucketVersioning(bucket)
	if err != nil {
		return false, err
	}
	return status == "Enabled", nil
}

func (vb *VersionedBackend) getBucketVersioning(bucket string) (string, error) {
	data, _, err := vb.inner.GetObject(bucket, bucketMetaKey)
	if err != nil {
		return "Unversioned", nil
	}
	var cfg bucketVersioningConfig
	if jsonErr := json.Unmarshal(data, &cfg); jsonErr != nil {
		slog.Warn("failed to parse bucket versioning config", "bucket", bucket, "err", jsonErr)
		return "Unversioned", nil
	}
	if cfg.Versioning == "" {
		return "Unversioned", nil
	}
	return cfg.Versioning, nil
}

func (vb *VersionedBackend) isCurrentDeleted(bucket, key string) bool {
	sentinelKey := ".versions/" + safeKey(key) + "/.current-delete-marker"
	_, _, err := vb.inner.GetObject(bucket, sentinelKey)
	return err == nil
}

func (vb *VersionedBackend) archiveCurrentVersion(bucket, key string) error {
	currentData, currentMeta, err := vb.inner.GetObject(bucket, key)
	if err != nil {
		// 当前不存在（可能是 delete marker 或首次写入）→ 清理哨兵。
		sentinelKey := ".versions/" + safeKey(key) + "/.current-delete-marker"
		vb.inner.DeleteObject(bucket, sentinelKey)
		return nil
	}

	versionId := currentMeta.VersionId
	if versionId == "" {
		versionId = "null"
	}

	currentMeta.IsLatest = false
	archiveKey := ".versions/" + safeKey(key) + "/" + versionId
	return vb.inner.PutObject(bucket, archiveKey, currentData, currentMeta)
}

func generateVersionId() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func safeKey(key string) string {
	return strings.ReplaceAll(key, "/", "%2F")
}

func unSafeKey(s string) string {
	return strings.ReplaceAll(s, "%2F", "/")
}
