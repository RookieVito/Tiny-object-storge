package handler

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"tiny-object-storage/src/locks"
	"tiny-object-storage/src/s3error"
	"tiny-object-storage/src/service"
	"tiny-object-storage/src/storage"
)

// ObjectManager 处理 object CRUD 操作。
type ObjectManager struct {
	backend     storage.StorageBackend
	locks       *locks.BucketLocks
	maxBodySize int64
}

// NewObjectManager 创建 ObjectManager。
func NewObjectManager(backend storage.StorageBackend, locks *locks.BucketLocks, maxBodySize int64) *ObjectManager {
	return &ObjectManager{backend: backend, locks: locks, maxBodySize: maxBodySize}
}

// PutObject 处理 PUT /{bucket}/{key...}。
func (om *ObjectManager) PutObject(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")
	key := r.PathValue("key")

	om.locks.Lock(bucket)
	defer om.locks.Unlock(bucket)

	// 限制请求体大小，防止大文件耗尽内存。
	r.Body = http.MaxBytesReader(w, r.Body, om.maxBodySize)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			s3error.WriteS3Err(w, s3error.ErrRequestEntityTooLarge, r.URL.Path)
			return
		}
		s3error.WriteS3Error(w, "InternalError", "failed to read body", http.StatusInternalServerError, r.URL.Path)
		return
	}

	// 构建元数据。
	meta := service.BuildMetaFromRequest(key, body, r)

	if err := om.backend.PutObject(bucket, key, body, meta); err != nil {
		if isS3Err(err) {
			s3error.WriteS3Err(w, err.(*s3error.S3APIError), r.URL.Path)
			return
		}
		s3error.WriteS3Error(w, "InternalError", err.Error(), http.StatusInternalServerError, r.URL.Path)
		return
	}

	w.Header().Set("ETag", meta.ETag)
	if meta.VersionId != "" {
		w.Header().Set("x-amz-version-id", meta.VersionId)
	}
	w.WriteHeader(http.StatusOK)
}

// GetObject 处理 GET /{bucket}/{key...}。
func (om *ObjectManager) GetObject(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")
	key := r.PathValue("key")
	versionId := r.URL.Query().Get("versionId")

	var data []byte
	var meta *service.ObjectMeta
	var err error

	if versionId != "" {
		vs := om.getVersionedStorage(w, r)
		if vs == nil {
			return
		}
		data, meta, err = vs.GetObjectVersion(bucket, key, versionId)
	} else {
		data, meta, err = om.backend.GetObject(bucket, key)
	}
	if err != nil {
		if isS3Err(err) {
			s3error.WriteS3Err(w, err.(*s3error.S3APIError), r.URL.Path)
			return
		}
		s3error.WriteS3Error(w, "InternalError", err.Error(), http.StatusInternalServerError, r.URL.Path)
		return
	}

	// Range 请求处理。
	rangeHeader := r.Header.Get("Range")
	if rangeHeader != "" {
		ranges, invalid := parseRangeHeader(rangeHeader, int64(len(data)))
		if invalid {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", int64(len(data))))
			s3error.WriteS3Err(w, s3error.ErrInvalidRange, r.URL.Path)
			return
		}
		if len(ranges) == 0 {
			// 格式错误，回退到 200 全量返回
			writeFullObject(w, meta, data)
			return
		}
		// 只支持单 range（多 range 回退到 200）
		if len(ranges) == 1 {
			br := ranges[0]
			end := br.end
			if end == -1 || end >= int64(len(data)) {
				end = int64(len(data)) - 1
			}
			sliced := data[br.start : end+1]
			w.Header().Set("Content-Type", meta.ContentType)
			w.Header().Set("Content-Length", fmt.Sprintf("%d", end-br.start+1))
			w.Header().Set("Content-Range", contentRangeValue(br.start, end, int64(len(data))))
			w.Header().Set("ETag", meta.ETag)
			w.Header().Set("Last-Modified", meta.LastModified.UTC().Format(http.TimeFormat))
			w.Header().Set("Accept-Ranges", "bytes")
			w.WriteHeader(http.StatusPartialContent)
			w.Write(sliced)
			return
		}
		// 多 range → 回退到 200 全量返回
	}

	writeFullObject(w, meta, data)
}

// getVersionedStorage 获取 VersionedStorage，未实现时返回 501。
func (om *ObjectManager) getVersionedStorage(w http.ResponseWriter, r *http.Request) storage.VersionedStorage {
	vs, ok := om.backend.(storage.VersionedStorage)
	if !ok {
		s3error.WriteS3Err(w, s3error.ErrNotImplemented, r.URL.Path)
		return nil
	}
	return vs
}

// writeFullObject 写入完整的对象响应（200 OK）。
func writeFullObject(w http.ResponseWriter, meta *service.ObjectMeta, data []byte) {
	w.Header().Set("Content-Type", meta.ContentType)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", meta.Size))
	w.Header().Set("ETag", meta.ETag)
	w.Header().Set("Last-Modified", meta.LastModified.UTC().Format(http.TimeFormat))
	w.Header().Set("Accept-Ranges", "bytes")
	if meta.VersionId != "" {
		w.Header().Set("x-amz-version-id", meta.VersionId)
	}
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

// HeadObject 处理 HEAD /{bucket}/{key...}。
func (om *ObjectManager) HeadObject(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")
	key := r.PathValue("key")
	versionId := r.URL.Query().Get("versionId")

	var meta *service.ObjectMeta
	var err error

	if versionId != "" {
		vs := om.getVersionedStorage(w, r)
		if vs == nil {
			return
		}
		meta, err = vs.HeadObjectVersion(bucket, key, versionId)
	} else {
		meta, err = om.backend.HeadObject(bucket, key)
	}
	if err != nil {
		if isS3Err(err) {
			s3error.WriteS3Err(w, err.(*s3error.S3APIError), r.URL.Path)
			return
		}
		s3error.WriteS3Error(w, "InternalError", err.Error(), http.StatusInternalServerError, r.URL.Path)
		return
	}

	w.Header().Set("Content-Type", meta.ContentType)
	w.Header().Set("ETag", meta.ETag)
	w.Header().Set("Last-Modified", meta.LastModified.UTC().Format(http.TimeFormat))
	w.Header().Set("Accept-Ranges", "bytes")
	if meta.VersionId != "" {
		w.Header().Set("x-amz-version-id", meta.VersionId)
	}

	rangeHeader := r.Header.Get("Range")
	if rangeHeader != "" {
		ranges, invalid := parseRangeHeader(rangeHeader, meta.Size)
		if invalid {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", meta.Size))
			s3error.WriteS3Err(w, s3error.ErrInvalidRange, r.URL.Path)
			return
		}
		if len(ranges) == 1 {
			br := ranges[0]
			end := br.end
			if end == -1 || end >= meta.Size {
				end = meta.Size - 1
			}
			w.Header().Set("Content-Length", fmt.Sprintf("%d", end-br.start+1))
			w.Header().Set("Content-Range", contentRangeValue(br.start, end, meta.Size))
			w.WriteHeader(http.StatusPartialContent)
			return
		}
		// 多 range → 回退到 200
	}

	w.Header().Set("Content-Length", fmt.Sprintf("%d", meta.Size))
	w.WriteHeader(http.StatusOK)
}

// DeleteObject 处理 DELETE /{bucket}/{key...}。
// S3 语义：幂等 — key 不存在时也返回 204。
func (om *ObjectManager) DeleteObject(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")
	key := r.PathValue("key")
	versionId := r.URL.Query().Get("versionId")

	om.locks.Lock(bucket)
	defer om.locks.Unlock(bucket)

	if versionId != "" {
		vs := om.getVersionedStorage(w, r)
		if vs == nil {
			return
		}
		if err := vs.DeleteObjectVersion(bucket, key, versionId); err != nil {
			if isS3Err(err) {
				s3error.WriteS3Err(w, err.(*s3error.S3APIError), r.URL.Path)
				return
			}
			s3error.WriteS3Error(w, "InternalError", err.Error(), http.StatusInternalServerError, r.URL.Path)
			return
		}
		w.Header().Set("x-amz-version-id", versionId)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if err := om.backend.DeleteObject(bucket, key); err != nil {
		if isS3Err(err) {
			s3error.WriteS3Err(w, err.(*s3error.S3APIError), r.URL.Path)
			return
		}
		s3error.WriteS3Error(w, "InternalError", err.Error(), http.StatusInternalServerError, r.URL.Path)
		return
	}

	// 版本化 bucket 的 delete 返回 delete marker 头。
	if vs, ok := om.backend.(storage.VersionedStorage); ok {
		status, _ := vs.GetBucketVersioning(bucket)
		if status == "Enabled" {
			w.Header().Set("x-amz-delete-marker", "true")
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// isS3Err 检查错误是否为 S3APIError。
func isS3Err(err error) bool {
	_, ok := err.(*s3error.S3APIError)
	return ok
}
