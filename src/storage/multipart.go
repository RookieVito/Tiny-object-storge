package storage

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"tiny-object-storage/src/service"
	"tiny-object-storage/src/s3error"
)

// GenerateUploadId 生成唯一的 upload ID（UUID v4）。
func GenerateUploadId() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// uploadDir 返回指定 upload 的存储目录。
func (lb *LocalBackend) uploadDir(bucket, uploadId string) (string, error) {
	bucketPath, err := lb.pm.BucketPath(bucket)
	if err != nil {
		return "", err
	}
	return filepath.Join(bucketPath, ".uploads", uploadId), nil
}

// partPath 返回指定 part 的数据文件路径。
func (lb *LocalBackend) partPath(bucket, uploadId string, partNumber int) (string, error) {
	dir, err := lb.uploadDir(bucket, uploadId)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, fmt.Sprintf("part-%04d.bin", partNumber)), nil
}

// partMetaPath 返回指定 part 的元数据文件路径。
func (lb *LocalBackend) partMetaPath(bucket, uploadId string, partNumber int) (string, error) {
	p, err := lb.partPath(bucket, uploadId, partNumber)
	if err != nil {
		return "", err
	}
	return p + ".meta", nil
}

// uploadInfoPath 返回 upload 信息文件路径。
func (lb *LocalBackend) uploadInfoPath(bucket, uploadId string) (string, error) {
	dir, err := lb.uploadDir(bucket, uploadId)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "info.json"), nil
}

// InitiateUpload 创建一个新的 multipart upload，返回 uploadId。
func (lb *LocalBackend) InitiateUpload(bucket, key string, contentType string, userMeta map[string]string) (*UploadInfo, error) {
	if ok, err := lb.BucketExists(bucket); err != nil {
		return nil, err
	} else if !ok {
		return nil, s3error.ErrNoSuchBucket
	}

	uploadId := GenerateUploadId()
	dir, err := lb.uploadDir(bucket, uploadId)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	info := &UploadInfo{
		UploadId:     uploadId,
		Bucket:       bucket,
		Key:          key,
		ContentType:  contentType,
		UserMetadata: userMeta,
		Initiated:    time.Now().UTC(),
	}

	infoPath, err := lb.uploadInfoPath(bucket, uploadId)
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}

	data, err := json.Marshal(info)
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	if err := service.WriteFile(infoPath, data); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}

	return info, nil
}

// UploadPart 上传一个 part 数据。返回该 part 的 ETag。
func (lb *LocalBackend) UploadPart(bucket, key, uploadId string, partNumber int, data []byte) (*PartInfo, error) {
	if _, err := lb.GetUploadInfo(bucket, key, uploadId); err != nil {
		return nil, err
	}

	if partNumber < 1 || partNumber > 10000 {
		return nil, s3error.ErrInvalidPartNumber
	}

	pp, err := lb.partPath(bucket, uploadId, partNumber)
	if err != nil {
		return nil, err
	}

	if err := service.WriteFile(pp, data); err != nil {
		return nil, err
	}

	etag := fmt.Sprintf(`"%x"`, md5.Sum(data))
	now := time.Now().UTC()

	partInfo := &PartInfo{
		PartNumber:   partNumber,
		Size:         int64(len(data)),
		ETag:         etag,
		LastModified: now,
	}

	mp, err := lb.partMetaPath(bucket, uploadId, partNumber)
	if err != nil {
		os.Remove(pp)
		return nil, err
	}

	metaBytes, _ := json.Marshal(partInfo)
	if err := service.WriteFile(mp, metaBytes); err != nil {
		os.Remove(pp)
		return nil, err
	}

	return partInfo, nil
}

// CompleteUpload 将所有 part 按顺序合并为最终对象，返回最终 ETag。
func (lb *LocalBackend) CompleteUpload(bucket, key, uploadId string, requestedParts []PartInfo) (string, error) {
	info, err := lb.GetUploadInfo(bucket, key, uploadId)
	if err != nil {
		return "", err
	}

	const minPartSize = 5 << 20 // 5 MB

	// 读取并验证所有 parts，计算 MD5 摘要。
	md5Hashes := make([][16]byte, len(requestedParts))
	var totalSize int64

	for i, reqPart := range requestedParts {
		pp, err := lb.partPath(bucket, uploadId, reqPart.PartNumber)
		if err != nil {
			return "", err
		}

		data, err := os.ReadFile(pp)
		if err != nil {
			if os.IsNotExist(err) {
				return "", s3error.ErrInvalidPart
			}
			return "", err
		}

		// 验证 ETag 匹配（客户端提供的 ETag）。
		actualETag := fmt.Sprintf(`"%x"`, md5.Sum(data))
		if reqPart.ETag != "" && actualETag != reqPart.ETag {
			return "", s3error.ErrInvalidPart
		}

		// 验证非最后一个 part 的大小 >= 5MB。
		if i < len(requestedParts)-1 && int64(len(data)) < minPartSize {
			return "", s3error.ErrEntityTooSmall
		}

		hash := md5.Sum(data)
		md5Hashes[i] = hash
		totalSize += int64(len(data))
	}

	// 拼接所有 parts 到临时文件。
	tmpDir, err := lb.uploadDir(bucket, uploadId)
	if err != nil {
		return "", err
	}
	tmpFile, err := os.CreateTemp(tmpDir, "assemble-*.tmp")
	if err != nil {
		return "", err
	}
	tmpName := tmpFile.Name()

	for _, reqPart := range requestedParts {
		pp, _ := lb.partPath(bucket, uploadId, reqPart.PartNumber)
		data, err := os.ReadFile(pp)
		if err != nil {
			tmpFile.Close()
			os.Remove(tmpName)
			return "", err
		}
		if _, err := tmpFile.Write(data); err != nil {
			tmpFile.Close()
			os.Remove(tmpName)
			return "", err
		}
	}
	if err := tmpFile.Sync(); err != nil {
		tmpFile.Close()
		os.Remove(tmpName)
		return "", err
	}
	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpName)
		return "", err
	}

	// 计算最终 ETag = MD5(MD5_of_part1 + MD5_of_part2 + ...)-N。
	var concatHash []byte
	for _, h := range md5Hashes {
		concatHash = append(concatHash, h[:]...)
	}
	finalETag := fmt.Sprintf(`"%x-%d"`, md5.Sum(concatHash), len(requestedParts))

	// 构造 ObjectMeta。
	contentType := info.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	meta := &service.ObjectMeta{
		Key:          key,
		Size:         totalSize,
		ETag:         finalETag,
		ContentType:  contentType,
		LastModified: time.Now().UTC(),
		UserMetadata: info.UserMetadata,
	}

	// 原子写入最终对象。
	dataPath, err := lb.pm.ObjectPath(bucket, key)
	if err != nil {
		os.Remove(tmpName)
		return "", err
	}
	metaPath, err := lb.pm.MetaPath(bucket, key)
	if err != nil {
		os.Remove(tmpName)
		return "", err
	}

	if err := service.EnsureDir(dataPath); err != nil {
		os.Remove(tmpName)
		return "", err
	}
	if err := os.Rename(tmpName, dataPath); err != nil {
		os.Remove(tmpName)
		return "", err
	}
	if err := service.WriteMeta(metaPath, meta); err != nil {
		os.Remove(dataPath)
		return "", err
	}

	// 清理 .uploads/{uploadId} 目录。
	dir, _ := lb.uploadDir(bucket, uploadId)
	os.RemoveAll(dir)

	return finalETag, nil
}

// AbortUpload 取消并清理一个 multipart upload 的所有数据。
func (lb *LocalBackend) AbortUpload(bucket, key, uploadId string) error {
	if _, err := lb.GetUploadInfo(bucket, key, uploadId); err != nil {
		return err
	}

	dir, err := lb.uploadDir(bucket, uploadId)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

// ListParts 返回指定 upload 的所有已上传 part。
func (lb *LocalBackend) ListParts(bucket, key, uploadId string) ([]PartInfo, error) {
	if _, err := lb.GetUploadInfo(bucket, key, uploadId); err != nil {
		return nil, err
	}

	dir, err := lb.uploadDir(bucket, uploadId)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var parts []PartInfo
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "part-") || !strings.HasSuffix(e.Name(), ".meta") {
			continue
		}
		metaPath := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(metaPath)
		if err != nil {
			continue
		}
		var pi PartInfo
		if json.Unmarshal(data, &pi) != nil {
			continue
		}
		parts = append(parts, pi)
	}

	sort.Slice(parts, func(i, j int) bool {
		return parts[i].PartNumber < parts[j].PartNumber
	})

	return parts, nil
}

// ListUploads 返回指定 bucket 中所有进行中的 multipart upload。
func (lb *LocalBackend) ListUploads(bucket, prefix, keyMarker string, maxUploads int) ([]UploadInfo, string, bool, error) {
	bucketPath, err := lb.pm.BucketPath(bucket)
	if err != nil {
		return nil, "", false, err
	}
	if _, err := os.Stat(bucketPath); err != nil {
		if os.IsNotExist(err) {
			return nil, "", false, s3error.ErrNoSuchBucket
		}
		return nil, "", false, err
	}

	uploadsDir := filepath.Join(bucketPath, ".uploads")
	dirEntries, err := os.ReadDir(uploadsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []UploadInfo{}, "", false, nil
		}
		return nil, "", false, err
	}

	var uploads []UploadInfo
	for _, de := range dirEntries {
		if !de.IsDir() {
			continue
		}
		uploadId := de.Name()
		infoPath := filepath.Join(uploadsDir, uploadId, "info.json")
		data, err := os.ReadFile(infoPath)
		if err != nil {
			continue
		}
		var info UploadInfo
		if json.Unmarshal(data, &info) != nil {
			continue
		}

		if prefix != "" && !strings.HasPrefix(info.Key, prefix) {
			continue
		}
		if keyMarker != "" && info.Key <= keyMarker {
			continue
		}
		uploads = append(uploads, info)
	}

	sort.Slice(uploads, func(i, j int) bool {
		if uploads[i].Key != uploads[j].Key {
			return uploads[i].Key < uploads[j].Key
		}
		return uploads[i].Initiated.Before(uploads[j].Initiated)
	})

	truncated := false
	if len(uploads) > maxUploads {
		truncated = true
		uploads = uploads[:maxUploads]
	}

	nextKeyMarker := ""
	if truncated && len(uploads) > 0 {
		nextKeyMarker = uploads[len(uploads)-1].Key
	}

	return uploads, nextKeyMarker, truncated, nil
}

// GetUploadInfo 返回指定 upload 的元数据。不存在时返回错误。
func (lb *LocalBackend) GetUploadInfo(bucket, key, uploadId string) (*UploadInfo, error) {
	infoPath, err := lb.uploadInfoPath(bucket, uploadId)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(infoPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, s3error.ErrNoSuchUpload
		}
		return nil, err
	}
	var info UploadInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("corrupt upload info: %w", err)
	}

	if info.Bucket != bucket || info.Key != key {
		return nil, s3error.ErrNoSuchUpload
	}

	return &info, nil
}
