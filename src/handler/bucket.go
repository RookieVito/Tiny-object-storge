package handler

import (
	"encoding/xml"
	"net/http"
	"time"

	"tiny-object-storage/src/locks"
	"tiny-object-storage/src/s3error"
	"tiny-object-storage/src/storage"
)

// BucketManager 处理 bucket CRUD 操作。
type BucketManager struct {
	backend storage.StorageBackend
	locks   *locks.BucketLocks
}

// NewBucketManager 创建 BucketManager。
func NewBucketManager(backend storage.StorageBackend, locks *locks.BucketLocks) *BucketManager {
	return &BucketManager{backend: backend, locks: locks}
}

// CreateBucket 处理 PUT /{bucket}。
func (bm *BucketManager) CreateBucket(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")

	bm.locks.Lock(bucket)
	defer bm.locks.Unlock(bucket)

	if err := bm.backend.CreateBucket(bucket); err != nil {
		if isS3Err(err) {
			s3error.WriteS3Err(w, err.(*s3error.S3APIError), r.URL.Path)
			return
		}
		s3error.WriteS3Error(w, "InternalError", err.Error(), http.StatusInternalServerError, r.URL.Path)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// DeleteBucket 处理 DELETE /{bucket}。
func (bm *BucketManager) DeleteBucket(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")

	bm.locks.Lock(bucket)
	defer bm.locks.Unlock(bucket)

	if err := bm.backend.DeleteBucket(bucket); err != nil {
		if isS3Err(err) {
			s3error.WriteS3Err(w, err.(*s3error.S3APIError), r.URL.Path)
			return
		}
		s3error.WriteS3Error(w, "InternalError", err.Error(), http.StatusInternalServerError, r.URL.Path)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// HeadBucket 处理 HEAD /{bucket}。
func (bm *BucketManager) HeadBucket(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")

	if _, err := bm.backend.BucketExists(bucket); err != nil {
		if isS3Err(err) {
			s3error.WriteS3Err(w, err.(*s3error.S3APIError), r.URL.Path)
			return
		}
		s3error.WriteS3Error(w, "InternalError", err.Error(), http.StatusInternalServerError, r.URL.Path)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// --- ListBuckets ---

// listAllMyBucketsResult 是 ListBuckets 的 XML 响应类型。
type listAllMyBucketsResult struct {
	XMLName xml.Name         `xml:"ListAllMyBucketsResult"`
	Owner   s3Owner          `xml:"Owner"`
	Buckets []s3BucketEntry  `xml:"Buckets>Bucket"`
}

type s3Owner struct {
	ID          string `xml:"ID"`
	DisplayName string `xml:"DisplayName"`
}

type s3BucketEntry struct {
	Name         string    `xml:"Name"`
	CreationDate time.Time `xml:"CreationDate"`
}

// ListBuckets 处理 GET /。
func (bm *BucketManager) ListBuckets(w http.ResponseWriter, r *http.Request) {
	buckets, err := bm.backend.ListBuckets()
	if err != nil {
		s3error.WriteS3Error(w, "InternalError", err.Error(), http.StatusInternalServerError, r.URL.Path)
		return
	}

	entries := make([]s3BucketEntry, 0, len(buckets))
	for _, b := range buckets {
		entries = append(entries, s3BucketEntry{
			Name:         b.Name,
			CreationDate: b.CreationDate,
		})
	}

	result := listAllMyBucketsResult{
		Owner:   s3Owner{ID: "owner-id", DisplayName: "owner"},
		Buckets: entries,
	}

	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	xml.NewEncoder(w).Encode(result)
}

// --- ListObjectsV2 ---

// listBucketResult 是 ListObjectsV2 的 XML 响应类型。
type listBucketResult struct {
	XMLName              xml.Name          `xml:"ListBucketResult"`
	Xmlns                string            `xml:"xmlns,attr"`
	Name                 string            `xml:"Name"`
	Prefix               string            `xml:"Prefix"`
	KeyCount             int               `xml:"KeyCount"`
	MaxKeys              int               `xml:"MaxKeys"`
	Delimiter            string            `xml:"Delimiter,omitempty"`
	IsTruncated          bool              `xml:"IsTruncated"`
	NextContinuationToken string           `xml:"NextContinuationToken,omitempty"`
	ContinuationToken    string            `xml:"ContinuationToken,omitempty"`
	Contents             []s3Object        `xml:"Contents,omitempty"`
	CommonPrefixes       []s3CommonPrefix  `xml:"CommonPrefixes>Prefix,omitempty"`
}

type s3Object struct {
	Key          string    `xml:"Key"`
	LastModified time.Time `xml:"LastModified"`
	ETag         string    `xml:"ETag"`
	Size         int64     `xml:"Size"`
	StorageClass string    `xml:"StorageClass"`
}

type s3CommonPrefix struct {
	Prefix string `xml:"Prefix"`
}

// ListObjects 处理 GET /{bucket}，使用 ListObjectsV2 语义。
func (bm *BucketManager) ListObjects(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")

	q := r.URL.Query()
	prefix := q.Get("prefix")
	delimiter := q.Get("delimiter")
	continuationToken := q.Get("continuation-token")
	maxKeysStr := q.Get("max-keys")

	maxKeys := 1000
	if maxKeysStr != "" {
		if n, err := parseMaxKeys(maxKeysStr); err == nil {
			maxKeys = n
		}
	}

	// 解码 continuation token（base64 编码的上一页最后一个 key）。
	startAfter := ""
	if continuationToken != "" {
		if decoded, err := decodeToken(continuationToken); err == nil {
			startAfter = decoded
		}
	}

	entries, commonPrefixes, nextToken, truncated, err := bm.backend.ListObjects(bucket, prefix, delimiter, startAfter, maxKeys)
	if err != nil {
		if isS3Err(err) {
			s3error.WriteS3Err(w, err.(*s3error.S3APIError), r.URL.Path)
			return
		}
		s3error.WriteS3Error(w, "InternalError", err.Error(), http.StatusInternalServerError, r.URL.Path)
		return
	}

	contents := make([]s3Object, 0, len(entries))
	for _, e := range entries {
		contents = append(contents, s3Object{
			Key:          e.Key,
			LastModified: e.LastModified,
			ETag:         e.ETag,
			Size:         e.Size,
			StorageClass: e.StorageClass,
		})
	}

	cp := make([]s3CommonPrefix, 0, len(commonPrefixes))
	for _, p := range commonPrefixes {
		cp = append(cp, s3CommonPrefix{Prefix: p})
	}

	result := listBucketResult{
		Xmlns:                "http://s3.amazonaws.com/doc/2006-03-01/",
		Name:                 bucket,
		Prefix:               prefix,
		KeyCount:             len(contents) + len(cp),
		MaxKeys:              maxKeys,
		Delimiter:            delimiter,
		IsTruncated:          truncated,
		NextContinuationToken: nextToken,
		Contents:             contents,
		CommonPrefixes:       cp,
	}
	if continuationToken != "" {
		result.ContinuationToken = continuationToken
	}

	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	xml.NewEncoder(w).Encode(result)
}
