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
	w.WriteHeader(http.StatusOK)
}

// GetObject 处理 GET /{bucket}/{key...}。
func (om *ObjectManager) GetObject(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")
	key := r.PathValue("key")

	data, meta, err := om.backend.GetObject(bucket, key)
	if err != nil {
		if isS3Err(err) {
			s3error.WriteS3Err(w, err.(*s3error.S3APIError), r.URL.Path)
			return
		}
		s3error.WriteS3Error(w, "InternalError", err.Error(), http.StatusInternalServerError, r.URL.Path)
		return
	}

	w.Header().Set("Content-Type", meta.ContentType)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", meta.Size))
	w.Header().Set("ETag", meta.ETag)
	w.Header().Set("Last-Modified", meta.LastModified.UTC().Format(http.TimeFormat))
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

// HeadObject 处理 HEAD /{bucket}/{key...}。
func (om *ObjectManager) HeadObject(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")
	key := r.PathValue("key")

	meta, err := om.backend.HeadObject(bucket, key)
	if err != nil {
		if isS3Err(err) {
			s3error.WriteS3Err(w, err.(*s3error.S3APIError), r.URL.Path)
			return
		}
		s3error.WriteS3Error(w, "InternalError", err.Error(), http.StatusInternalServerError, r.URL.Path)
		return
	}

	w.Header().Set("Content-Type", meta.ContentType)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", meta.Size))
	w.Header().Set("ETag", meta.ETag)
	w.Header().Set("Last-Modified", meta.LastModified.UTC().Format(http.TimeFormat))
	w.WriteHeader(http.StatusOK)
}

// DeleteObject 处理 DELETE /{bucket}/{key...}。
// S3 语义：幂等 — key 不存在时也返回 204。
func (om *ObjectManager) DeleteObject(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")
	key := r.PathValue("key")

	om.locks.Lock(bucket)
	defer om.locks.Unlock(bucket)

	if err := om.backend.DeleteObject(bucket, key); err != nil {
		if isS3Err(err) {
			s3error.WriteS3Err(w, err.(*s3error.S3APIError), r.URL.Path)
			return
		}
		s3error.WriteS3Error(w, "InternalError", err.Error(), http.StatusInternalServerError, r.URL.Path)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// isS3Err 检查错误是否为 S3APIError。
func isS3Err(err error) bool {
	_, ok := err.(*s3error.S3APIError)
	return ok
}
