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

// PartInfo 存储单个已上传 part 的信息。
type PartInfo struct {
	PartNumber   int
	Size         int64
	ETag         string // MD5 hex digest, quoted format: "\"abcd1234\""
	LastModified time.Time
}

// UploadInfo 存储一个正在进行的 multipart upload 的元数据。
type UploadInfo struct {
	UploadId     string            `json:"upload_id"`
	Bucket       string            `json:"bucket"`
	Key          string            `json:"key"`
	ContentType  string            `json:"content_type,omitempty"`
	UserMetadata map[string]string `json:"user_metadata,omitempty"`
	Initiated    time.Time         `json:"initiated"`
}

// MultipartStorage 定义 multipart upload 的存储接口。
// 后端可选实现此接口，未实现时 handler 返回 ErrNotImplemented。
type MultipartStorage interface {
	// InitiateUpload 创建一个新的 multipart upload，返回 uploadId。
	InitiateUpload(bucket, key string, contentType string, userMeta map[string]string) (*UploadInfo, error)

	// UploadPart 上传一个 part 数据。返回该 part 的 ETag。
	// partNumber 从 1 开始。
	UploadPart(bucket, key, uploadId string, partNumber int, data []byte) (*PartInfo, error)

	// CompleteUpload 将所有 part 按顺序合并为最终对象。
	// parts 列表指定了各 part 的编号和 ETag，用于验证。
	// 返回最终对象的 ETag。
	CompleteUpload(bucket, key, uploadId string, parts []PartInfo) (string, error)

	// AbortUpload 取消并清理一个 multipart upload 的所有数据。
	AbortUpload(bucket, key, uploadId string) error

	// ListParts 返回指定 upload 的所有已上传 part。
	ListParts(bucket, key, uploadId string) ([]PartInfo, error)

	// ListUploads 返回指定 bucket 中所有进行中的 multipart upload。
	ListUploads(bucket, prefix, keyMarker string, maxUploads int) ([]UploadInfo, string, bool, error)

	// GetUploadInfo 返回指定 upload 的元数据。不存在时返回错误。
	GetUploadInfo(bucket, key, uploadId string) (*UploadInfo, error)
}

// isS3Err 检查错误是否为 S3APIError。
func isS3Err(err error) bool {
	_, ok := err.(*s3error.S3APIError)
	return ok
}
