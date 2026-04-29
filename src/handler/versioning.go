package handler

import (
	"encoding/xml"
	"io"
	"net/http"
	"strconv"

	"tiny-object-storage/src/s3error"
	"tiny-object-storage/src/storage"
)

// VersioningManager 处理对象版本控制相关的 HTTP 请求。
type VersioningManager struct {
	backend  storage.StorageBackend
	vStorage storage.VersionedStorage
}

// NewVersioningManager 创建 VersioningManager。
func NewVersioningManager(backend storage.StorageBackend) *VersioningManager {
	vs, _ := backend.(storage.VersionedStorage)
	return &VersioningManager{backend: backend, vStorage: vs}
}

// PutBucketVersioning 处理 PUT /{bucket}?versioning。
func (vm *VersioningManager) PutBucketVersioning(w http.ResponseWriter, r *http.Request) {
	if vm.vStorage == nil {
		s3error.WriteS3Err(w, s3error.ErrNotImplemented, r.URL.Path)
		return
	}

	bucket := r.PathValue("bucket")
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s3error.WriteS3Error(w, "InvalidRequest", "failed to read request body", http.StatusBadRequest, r.URL.Path)
		return
	}

	var req struct {
		XMLName xml.Name `xml:"VersioningConfiguration"`
		Status  string   `xml:"Status"`
	}
	if err := xml.Unmarshal(body, &req); err != nil {
		s3error.WriteS3Error(w, "MalformedXML", "failed to parse versioning configuration", http.StatusBadRequest, r.URL.Path)
		return
	}

	if err := vm.vStorage.PutBucketVersioning(bucket, req.Status); err != nil {
		if isS3Err(err) {
			s3error.WriteS3Err(w, err.(*s3error.S3APIError), r.URL.Path)
			return
		}
		s3error.WriteS3Error(w, "InternalError", err.Error(), http.StatusInternalServerError, r.URL.Path)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// GetBucketVersioning 处理 GET /{bucket}?versioning。
func (vm *VersioningManager) GetBucketVersioning(w http.ResponseWriter, r *http.Request) {
	if vm.vStorage == nil {
		s3error.WriteS3Err(w, s3error.ErrNotImplemented, r.URL.Path)
		return
	}

	bucket := r.PathValue("bucket")
	status, err := vm.vStorage.GetBucketVersioning(bucket)
	if err != nil {
		s3error.WriteS3Error(w, "InternalError", err.Error(), http.StatusInternalServerError, r.URL.Path)
		return
	}

	resp := struct {
		XMLName xml.Name `xml:"VersioningConfiguration"`
		Status  string   `xml:"Status,omitempty"`
	}{
		Status: status,
	}
	if resp.Status == "Unversioned" {
		resp.Status = ""
	}

	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	xml.NewEncoder(w).Encode(resp)
}

// ListObjectVersions 处理 GET /{bucket}?versions。
func (vm *VersioningManager) ListObjectVersions(w http.ResponseWriter, r *http.Request) {
	if vm.vStorage == nil {
		s3error.WriteS3Err(w, s3error.ErrNotImplemented, r.URL.Path)
		return
	}

	bucket := r.PathValue("bucket")
	q := r.URL.Query()

	prefix := q.Get("prefix")
	delimiter := q.Get("delimiter")
	keyMarker := q.Get("key-marker")
	versionIdMarker := q.Get("version-id-marker")
	maxKeys := 1000
	if mk := q.Get("max-keys"); mk != "" {
		if n, err := strconv.Atoi(mk); err == nil && n > 0 {
			maxKeys = n
		}
	}

	versions, deleteMarkers, commonPrefixes, nextKeyMarker, nextVersionIdMarker, isTruncated, err :=
		vm.vStorage.ListObjectVersions(bucket, prefix, delimiter, keyMarker, versionIdMarker, maxKeys)
	if err != nil {
		s3error.WriteS3Error(w, "InternalError", err.Error(), http.StatusInternalServerError, r.URL.Path)
		return
	}

	type xmlVersion struct {
		XMLName        xml.Name `xml:"Version"`
		Key            string   `xml:"Key"`
		VersionId      string   `xml:"VersionId"`
		IsLatest       bool     `xml:"IsLatest"`
		LastModified   string   `xml:"LastModified"`
		ETag           string   `xml:"ETag,omitempty"`
		Size           int64    `xml:"Size"`
		StorageClass   string   `xml:"StorageClass,omitempty"`
		Owner          *struct {
			ID          string `xml:"ID"`
			DisplayName string `xml:"DisplayName"`
		} `xml:"Owner,omitempty"`
	}

	type xmlDeleteMarker struct {
		XMLName        xml.Name `xml:"DeleteMarker"`
		Key            string   `xml:"Key"`
		VersionId      string   `xml:"VersionId"`
		IsLatest       bool     `xml:"IsLatest"`
		LastModified   string   `xml:"LastModified"`
		Owner          *struct {
			ID          string `xml:"ID"`
			DisplayName string `xml:"DisplayName"`
		} `xml:"Owner,omitempty"`
	}

	type resp struct {
		XMLName             xml.Name `xml:"ListObjectVersionsOutput"`
		Name                string   `xml:"Name"`
		Prefix              string   `xml:"Prefix,omitempty"`
		MaxKeys             int      `xml:"MaxKeys"`
		Delimiter           string   `xml:"Delimiter,omitempty"`
		IsTruncated         bool     `xml:"IsTruncated"`
		KeyMarker           string   `xml:"KeyMarker,omitempty"`
		VersionIdMarker     string   `xml:"VersionIdMarker,omitempty"`
		NextKeyMarker       string   `xml:"NextKeyMarker,omitempty"`
		NextVersionIdMarker string   `xml:"NextVersionIdMarker,omitempty"`
		Versions            []xmlVersion      `xml:"Version"`
		DeleteMarkers       []xmlDeleteMarker `xml:"DeleteMarker"`
		CommonPrefixes      []struct {
			Prefix string `xml:"Prefix"`
		} `xml:"CommonPrefixes"`
	}

	rsp := resp{
		Name:            bucket,
		Prefix:          prefix,
		MaxKeys:         maxKeys,
		Delimiter:       delimiter,
		IsTruncated:     isTruncated,
		KeyMarker:       keyMarker,
		VersionIdMarker: versionIdMarker,
		NextKeyMarker:   nextKeyMarker,
		NextVersionIdMarker: nextVersionIdMarker,
	}

	for _, v := range versions {
		rsp.Versions = append(rsp.Versions, xmlVersion{
			Key:          v.Key,
			VersionId:    v.VersionId,
			IsLatest:     v.IsLatest,
			LastModified: v.LastModified.UTC().Format("2006-01-02T15:04:05.000Z"),
			ETag:         v.ETag,
			Size:         v.Size,
			StorageClass: v.StorageClass,
		})
	}

	for _, dm := range deleteMarkers {
		rsp.DeleteMarkers = append(rsp.DeleteMarkers, xmlDeleteMarker{
			Key:          dm.Key,
			VersionId:    dm.VersionId,
			IsLatest:     dm.IsLatest,
			LastModified: dm.LastModified.UTC().Format("2006-01-02T15:04:05.000Z"),
		})
	}

	for _, cp := range commonPrefixes {
		rsp.CommonPrefixes = append(rsp.CommonPrefixes, struct {
			Prefix string `xml:"Prefix"`
		}{Prefix: cp})
	}

	w.Header().Set("Content-Type", "application/xml")
	xml.NewEncoder(w).Encode(rsp)
}
