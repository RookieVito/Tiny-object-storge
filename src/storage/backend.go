package storage

import (
	"time"

	"tiny-object-storage/src/s3error"
	"tiny-object-storage/src/service"
)

// BucketInfo 包含 bucket 的基本信息。
type BucketInfo struct {
	Name         string
	CreationDate time.Time
}

// ObjectEntry 包含列举对象时返回的条目信息。
type ObjectEntry struct {
	Key          string
	LastModified time.Time
	ETag         string
	Size         int64
	StorageClass string
}

// StorageBackend 定义了对象存储后端的统一接口。
// 所有存储实现（本地文件系统、Erasure Coding 等）都必须实现此接口。
type StorageBackend interface {
	// Bucket 操作。
	CreateBucket(bucket string) error
	DeleteBucket(bucket string) error
	BucketExists(bucket string) (bool, error)
	ListBuckets() ([]BucketInfo, error)

	// Object 操作。
	PutObject(bucket, key string, data []byte, meta *service.ObjectMeta) error
	GetObject(bucket, key string) ([]byte, *service.ObjectMeta, error)
	HeadObject(bucket, key string) (*service.ObjectMeta, error)
	DeleteObject(bucket, key string) error

	// ListObjectsV2 语义：返回 entries、commonPrefixes、nextToken、truncated。
	ListObjects(bucket, prefix, delimiter, startAfter string, maxKeys int) (
		entries []ObjectEntry, commonPrefixes []string,
		nextToken string, truncated bool, err error,
	)
}

// isS3Err 检查错误是否为 S3APIError。
func isS3Err(err error) bool {
	_, ok := err.(*s3error.S3APIError)
	return ok
}
