package handler

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"tiny-object-storage/src/locks"
	"tiny-object-storage/src/s3error"
	"tiny-object-storage/src/storage"
)

// MultipartManager 处理 multipart upload 相关的 HTTP 请求。
type MultipartManager struct {
	mpStorage   storage.MultipartStorage // 可能为 nil（后端不支持时）
	locks       *locks.BucketLocks
	maxBodySize int64
}

// NewMultipartManager 创建 MultipartManager。
func NewMultipartManager(backend storage.StorageBackend, locks *locks.BucketLocks, maxBodySize int64) *MultipartManager {
	var mpStorage storage.MultipartStorage
	if ms, ok := backend.(storage.MultipartStorage); ok {
		mpStorage = ms
	}
	return &MultipartManager{
		mpStorage:   mpStorage,
		locks:       locks,
		maxBodySize: maxBodySize,
	}
}

// requireMultipart 检查 multipart 存储是否可用。
func (mm *MultipartManager) requireMultipart(w http.ResponseWriter) bool {
	if mm.mpStorage == nil {
		s3error.WriteS3Err(w, s3error.ErrNotImplemented, "")
		return false
	}
	return true
}

// —— XML 响应结构体 ——

type initiateMultipartUploadResult struct {
	XMLName  xml.Name `xml:"InitiateMultipartUploadResult"`
	Xmlns    string   `xml:"xmlns,attr"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	UploadId string   `xml:"UploadId"`
}

type completeMultipartUploadRequest struct {
	XMLName xml.Name `xml:"CompleteMultipartUpload"`
	Parts   []s3Part `xml:"Part"`
}

type s3Part struct {
	PartNumber int    `xml:"PartNumber"`
	ETag       string `xml:"ETag"`
}

type completeMultipartUploadResult struct {
	XMLName  xml.Name `xml:"CompleteMultipartUploadResult"`
	Xmlns    string   `xml:"xmlns,attr"`
	Location string   `xml:"Location"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	ETag     string   `xml:"ETag"`
}

type listPartsResult struct {
	XMLName              xml.Name       `xml:"ListPartsResult"`
	Xmlns                string         `xml:"xmlns,attr"`
	Bucket               string         `xml:"Bucket"`
	Key                  string         `xml:"Key"`
	UploadId             string         `xml:"UploadId"`
	PartNumberMarker     int            `xml:"PartNumberMarker,omitempty"`
	NextPartNumberMarker int            `xml:"NextPartNumberMarker,omitempty"`
	MaxParts             int            `xml:"MaxParts"`
	IsTruncated          bool           `xml:"IsTruncated"`
	Parts                []s3PartDetail `xml:"Part"`
}

type s3PartDetail struct {
	PartNumber   int       `xml:"PartNumber"`
	LastModified time.Time `xml:"LastModified"`
	ETag         string    `xml:"ETag"`
	Size         int64     `xml:"Size"`
}

type listMultipartUploadsResult struct {
	XMLName        xml.Name   `xml:"ListMultipartUploadsResult"`
	Xmlns          string     `xml:"xmlns,attr"`
	Bucket         string     `xml:"Bucket"`
	KeyMarker      string     `xml:"KeyMarker,omitempty"`
	UploadIdMarker string     `xml:"UploadIdMarker,omitempty"`
	NextKeyMarker  string     `xml:"NextKeyMarker,omitempty"`
	MaxUploads     int        `xml:"MaxUploads"`
	IsTruncated    bool       `xml:"IsTruncated"`
	Uploads        []s3Upload `xml:"Upload"`
}

type s3Upload struct {
	UploadId  string    `xml:"UploadId"`
	Key       string    `xml:"Key"`
	Initiated time.Time `xml:"Initiated"`
}

const s3Xmlns = "http://s3.amazonaws.com/doc/2006-03-01/"

// —— HTTP Handlers ——

// InitiateMultipartUpload 处理 POST /{bucket}/{key...}?uploads
func (mm *MultipartManager) InitiateMultipartUpload(w http.ResponseWriter, r *http.Request) {
	if !mm.requireMultipart(w) {
		return
	}

	bucket := r.PathValue("bucket")
	key := r.PathValue("key")

	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	userMeta := make(map[string]string)
	for k, v := range r.Header {
		if strings.HasPrefix(strings.ToLower(k), "x-amz-meta-") {
			userMeta[k] = v[0]
		}
	}

	info, err := mm.mpStorage.InitiateUpload(bucket, key, contentType, userMeta)
	if err != nil {
		if isS3Err(err) {
			s3error.WriteS3Err(w, err.(*s3error.S3APIError), r.URL.Path)
			return
		}
		s3error.WriteS3Error(w, "InternalError", err.Error(), http.StatusInternalServerError, r.URL.Path)
		return
	}

	result := initiateMultipartUploadResult{
		Xmlns:    s3Xmlns,
		Bucket:   bucket,
		Key:      key,
		UploadId: info.UploadId,
	}

	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	xml.NewEncoder(w).Encode(result)
}

// UploadPart 处理 PUT /{bucket}/{key...}?partNumber=N&uploadId=X
func (mm *MultipartManager) UploadPart(w http.ResponseWriter, r *http.Request) {
	if !mm.requireMultipart(w) {
		return
	}

	bucket := r.PathValue("bucket")
	key := r.PathValue("key")
	q := r.URL.Query()

	uploadId := q.Get("uploadId")
	partNumberStr := q.Get("partNumber")

	if uploadId == "" {
		s3error.WriteS3Error(w, "InvalidRequest", "Missing uploadId", http.StatusBadRequest, r.URL.Path)
		return
	}
	if partNumberStr == "" {
		s3error.WriteS3Error(w, "InvalidRequest", "Missing partNumber", http.StatusBadRequest, r.URL.Path)
		return
	}
	partNumber, err := strconv.Atoi(partNumberStr)
	if err != nil || partNumber < 1 || partNumber > 10000 {
		s3error.WriteS3Err(w, s3error.ErrInvalidPartNumber, r.URL.Path)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, mm.maxBodySize)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			s3error.WriteS3Err(w, s3error.ErrRequestEntityTooLarge, r.URL.Path)
			return
		}
		s3error.WriteS3Error(w, "InternalError", "failed to read body", http.StatusInternalServerError, r.URL.Path)
		return
	}

	partInfo, err := mm.mpStorage.UploadPart(bucket, key, uploadId, partNumber, data)
	if err != nil {
		if isS3Err(err) {
			s3error.WriteS3Err(w, err.(*s3error.S3APIError), r.URL.Path)
			return
		}
		s3error.WriteS3Error(w, "InternalError", err.Error(), http.StatusInternalServerError, r.URL.Path)
		return
	}

	w.Header().Set("ETag", partInfo.ETag)
	w.WriteHeader(http.StatusOK)
}

// CompleteMultipartUpload 处理 POST /{bucket}/{key...}?uploadId=X
func (mm *MultipartManager) CompleteMultipartUpload(w http.ResponseWriter, r *http.Request) {
	if !mm.requireMultipart(w) {
		return
	}

	bucket := r.PathValue("bucket")
	key := r.PathValue("key")
	uploadId := r.URL.Query().Get("uploadId")

	if uploadId == "" {
		s3error.WriteS3Error(w, "InvalidRequest", "Missing uploadId", http.StatusBadRequest, r.URL.Path)
		return
	}

	var req completeMultipartUploadRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		s3error.WriteS3Error(w, "MalformedXML", "Failed to parse XML", http.StatusBadRequest, r.URL.Path)
		return
	}

	// 验证 parts 按 partNumber 升序排列。
	for i := 1; i < len(req.Parts); i++ {
		if req.Parts[i].PartNumber <= req.Parts[i-1].PartNumber {
			s3error.WriteS3Err(w, s3error.ErrInvalidPartOrder, r.URL.Path)
			return
		}
	}

	// Complete 会写入最终对象，必须与 PutObject 互斥。
	mm.locks.Lock(bucket)
	defer mm.locks.Unlock(bucket)

	parts := make([]storage.PartInfo, len(req.Parts))
	for i, p := range req.Parts {
		parts[i] = storage.PartInfo{
			PartNumber: p.PartNumber,
			ETag:       p.ETag,
		}
	}

	etag, err := mm.mpStorage.CompleteUpload(bucket, key, uploadId, parts)
	if err != nil {
		if isS3Err(err) {
			s3error.WriteS3Err(w, err.(*s3error.S3APIError), r.URL.Path)
			return
		}
		s3error.WriteS3Error(w, "InternalError", err.Error(), http.StatusInternalServerError, r.URL.Path)
		return
	}

	result := completeMultipartUploadResult{
		Xmlns:    s3Xmlns,
		Location: fmt.Sprintf("http://localhost/%s/%s", bucket, key),
		Bucket:   bucket,
		Key:      key,
		ETag:     etag,
	}

	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	xml.NewEncoder(w).Encode(result)
}

// AbortMultipartUpload 处理 DELETE /{bucket}/{key...}?uploadId=X
func (mm *MultipartManager) AbortMultipartUpload(w http.ResponseWriter, r *http.Request) {
	if !mm.requireMultipart(w) {
		return
	}

	bucket := r.PathValue("bucket")
	key := r.PathValue("key")
	uploadId := r.URL.Query().Get("uploadId")

	if uploadId == "" {
		s3error.WriteS3Error(w, "InvalidRequest", "Missing uploadId", http.StatusBadRequest, r.URL.Path)
		return
	}

	mm.locks.Lock(bucket)
	defer mm.locks.Unlock(bucket)

	if err := mm.mpStorage.AbortUpload(bucket, key, uploadId); err != nil {
		if isS3Err(err) {
			s3error.WriteS3Err(w, err.(*s3error.S3APIError), r.URL.Path)
			return
		}
		s3error.WriteS3Error(w, "InternalError", err.Error(), http.StatusInternalServerError, r.URL.Path)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ListParts 处理 GET /{bucket}/{key...}?uploadId=X
func (mm *MultipartManager) ListParts(w http.ResponseWriter, r *http.Request) {
	if !mm.requireMultipart(w) {
		return
	}

	bucket := r.PathValue("bucket")
	key := r.PathValue("key")
	uploadId := r.URL.Query().Get("uploadId")

	if uploadId == "" {
		s3error.WriteS3Error(w, "InvalidRequest", "Missing uploadId", http.StatusBadRequest, r.URL.Path)
		return
	}

	parts, err := mm.mpStorage.ListParts(bucket, key, uploadId)
	if err != nil {
		if isS3Err(err) {
			s3error.WriteS3Err(w, err.(*s3error.S3APIError), r.URL.Path)
			return
		}
		s3error.WriteS3Error(w, "InternalError", err.Error(), http.StatusInternalServerError, r.URL.Path)
		return
	}

	s3Parts := make([]s3PartDetail, len(parts))
	for i, p := range parts {
		s3Parts[i] = s3PartDetail{
			PartNumber:   p.PartNumber,
			LastModified: p.LastModified,
			ETag:         p.ETag,
			Size:         p.Size,
		}
	}

	result := listPartsResult{
		Xmlns:    s3Xmlns,
		Bucket:   bucket,
		Key:      key,
		UploadId: uploadId,
		MaxParts: len(s3Parts),
		Parts:    s3Parts,
	}

	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	xml.NewEncoder(w).Encode(result)
}

// ListMultipartUploads 处理 GET /{bucket}?uploads
func (mm *MultipartManager) ListMultipartUploads(w http.ResponseWriter, r *http.Request) {
	if !mm.requireMultipart(w) {
		return
	}

	bucket := r.PathValue("bucket")
	q := r.URL.Query()

	prefix := q.Get("prefix")
	keyMarker := q.Get("key-marker")
	maxUploads := 1000
	if s := q.Get("max-uploads"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			maxUploads = n
		}
	}

	uploads, nextKeyMarker, truncated, err := mm.mpStorage.ListUploads(bucket, prefix, keyMarker, maxUploads)
	if err != nil {
		if isS3Err(err) {
			s3error.WriteS3Err(w, err.(*s3error.S3APIError), r.URL.Path)
			return
		}
		s3error.WriteS3Error(w, "InternalError", err.Error(), http.StatusInternalServerError, r.URL.Path)
		return
	}

	s3Uploads := make([]s3Upload, len(uploads))
	for i, u := range uploads {
		s3Uploads[i] = s3Upload{
			UploadId:  u.UploadId,
			Key:       u.Key,
			Initiated: u.Initiated,
		}
	}

	result := listMultipartUploadsResult{
		Xmlns:         s3Xmlns,
		Bucket:        bucket,
		KeyMarker:     keyMarker,
		NextKeyMarker: nextKeyMarker,
		MaxUploads:    maxUploads,
		IsTruncated:   truncated,
		Uploads:       s3Uploads,
	}

	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	xml.NewEncoder(w).Encode(result)
}
