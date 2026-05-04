package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"tiny-object-storage/src/service"
	"tiny-object-storage/src/s3error"
)

// --- VersionedBackend 单元测试 ---
// 使用内存模拟的 mockStorage 实现 StorageBackend 接口。

type mockStorage struct {
	buckets map[string]bool
	objects map[string]*mockObject // key: "bucket/key"
}

type mockObject struct {
	data []byte
	meta *service.ObjectMeta
}

func newMockStorage() *mockStorage {
	return &mockStorage{
		buckets: make(map[string]bool),
		objects: make(map[string]*mockObject),
	}
}

func (m *mockStorage) CreateBucket(bucket string) error {
	m.buckets[bucket] = true
	return nil
}

func (m *mockStorage) DeleteBucket(bucket string) error {
	delete(m.buckets, bucket)
	return nil
}

func (m *mockStorage) BucketExists(bucket string) (bool, error) {
	return m.buckets[bucket], nil
}

func (m *mockStorage) ListBuckets() ([]BucketInfo, error) {
	var result []BucketInfo
	for name := range m.buckets {
		result = append(result, BucketInfo{Name: name, CreationDate: time.Now().UTC()})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result, nil
}

func (m *mockStorage) PutObject(bucket, key string, data []byte, meta *service.ObjectMeta) error {
	m.objects[bucket+"/"+key] = &mockObject{data: data, meta: meta}
	return nil
}

func (m *mockStorage) GetObject(bucket, key string) ([]byte, *service.ObjectMeta, error) {
	obj, ok := m.objects[bucket+"/"+key]
	if !ok {
		return nil, nil, s3error.ErrNoSuchKey
	}
	return obj.data, obj.meta, nil
}

func (m *mockStorage) HeadObject(bucket, key string) (*service.ObjectMeta, error) {
	obj, ok := m.objects[bucket+"/"+key]
	if !ok {
		return nil, s3error.ErrNoSuchKey
	}
	return obj.meta, nil
}

func (m *mockStorage) DeleteObject(bucket, key string) error {
	delete(m.objects, bucket+"/"+key)
	return nil
}

func (m *mockStorage) ListObjects(bucket, prefix, delimiter, startAfter string, maxKeys int) ([]ObjectEntry, []string, string, bool, error) {
	var entries []ObjectEntry
	for k, obj := range m.objects {
		// 过滤 bucket
		bucketKey := bucket + "/"
		if len(k) <= len(bucketKey) {
			continue
		}
		objKey := k[len(bucketKey):]

		if prefix != "" && objKey != prefix && objKey[:len(prefix)] != prefix {
			continue
		}
		if startAfter != "" && objKey <= startAfter {
			continue
		}

		entries = append(entries, ObjectEntry{
			Key:          objKey,
			ETag:         obj.meta.ETag,
			Size:         obj.meta.Size,
			LastModified: obj.meta.LastModified,
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Key < entries[j].Key
	})

	if maxKeys > 0 && len(entries) > maxKeys {
		token := entries[maxKeys-1].Key
		return entries[:maxKeys], nil, token, true, nil
	}
	return entries, nil, "", false, nil
}

// --- VersionedBackend 测试 ---

func TestVersionedBackend_BucketOps(t *testing.T) {
	inner := newMockStorage()
	vb := NewVersionedBackend(inner)

	if err := vb.CreateBucket("test"); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	exists, err := vb.BucketExists("test")
	if err != nil || !exists {
		t.Fatalf("BucketExists: expected true, got %v %v", exists, err)
	}
	if err := vb.DeleteBucket("test"); err != nil {
		t.Fatalf("DeleteBucket: %v", err)
	}
}

func TestVersionedBackend_UnversionedPutGet(t *testing.T) {
	inner := newMockStorage()
	vb := NewVersionedBackend(inner)
	inner.CreateBucket("test")

	meta := &service.ObjectMeta{Key: "k", Size: 5, ETag: `"abc"`, ContentType: "text/plain", LastModified: time.Now().UTC()}
	if err := vb.PutObject("test", "k", []byte("hello"), meta); err != nil {
		t.Fatalf("PutObject: %v", err)
	}

	data, gotMeta, err := vb.GetObject("test", "k")
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("expected 'hello', got %q", string(data))
	}
	if gotMeta.VersionId != "" {
		t.Fatalf("unversioned bucket should not have versionId, got %q", gotMeta.VersionId)
	}
}

func TestVersionedBackend_VersionedPutGet(t *testing.T) {
	inner := newMockStorage()
	vb := NewVersionedBackend(inner)
	inner.CreateBucket("test")

	// 启用版本控制
	if err := vb.PutBucketVersioning("test", "Enabled"); err != nil {
		t.Fatalf("PutBucketVersioning: %v", err)
	}
	status, err := vb.GetBucketVersioning("test")
	if err != nil || status != "Enabled" {
		t.Fatalf("GetBucketVersioning: expected Enabled, got %s %v", status, err)
	}

	// 写入第一个版本
	meta1 := &service.ObjectMeta{Key: "k", Size: 3, ETag: `"v1"`, ContentType: "text/plain", LastModified: time.Now().UTC()}
	if err := vb.PutObject("test", "k", []byte("v1"), meta1); err != nil {
		t.Fatalf("PutObject v1: %v", err)
	}
	data1, meta1r, _ := vb.GetObject("test", "k")
	if string(data1) != "v1" {
		t.Fatalf("expected 'v1', got %q", string(data1))
	}
	vid1 := meta1r.VersionId

	// 写入第二个版本
	meta2 := &service.ObjectMeta{Key: "k", Size: 3, ETag: `"v2"`, ContentType: "text/plain", LastModified: time.Now().UTC()}
	vb.PutObject("test", "k", []byte("v2"), meta2)
	data2, meta2r, _ := vb.GetObject("test", "k")
	if string(data2) != "v2" {
		t.Fatalf("expected 'v2', got %q", string(data2))
	}
	vid2 := meta2r.VersionId

	if vid1 == vid2 {
		t.Fatal("versions should have different IDs")
	}

	// 读取历史版本
	data1h, _, err := vb.GetObjectVersion("test", "k", vid1)
	if err != nil {
		t.Fatalf("GetObjectVersion: %v", err)
	}
	if string(data1h) != "v1" {
		t.Fatalf("expected 'v1', got %q", string(data1h))
	}
}

func TestVersionedBackend_DeleteMarker(t *testing.T) {
	inner := newMockStorage()
	vb := NewVersionedBackend(inner)
	inner.CreateBucket("test")

	vb.PutBucketVersioning("test", "Enabled")

	meta := &service.ObjectMeta{Key: "k", Size: 1, ETag: `"x"`, ContentType: "text/plain", LastModified: time.Now().UTC()}
	vb.PutObject("test", "k", []byte("x"), meta)

	// 删除
	vb.DeleteObject("test", "k")

	// GetObject 应返回 NoSuchKey
	_, _, err := vb.GetObject("test", "k")
	if err == nil || err != s3error.ErrNoSuchKey {
		t.Fatalf("expected NoSuchKey after delete marker, got %v", err)
	}

	// HeadObject 同样
	_, err = vb.HeadObject("test", "k")
	if err == nil || err != s3error.ErrNoSuchKey {
		t.Fatalf("expected NoSuchKey, got %v", err)
	}
}

func TestVersionedBackend_DeleteObjectVersion(t *testing.T) {
	inner := newMockStorage()
	vb := NewVersionedBackend(inner)
	inner.CreateBucket("test")

	vb.PutBucketVersioning("test", "Enabled")

	meta := &service.ObjectMeta{Key: "k", Size: 1, ETag: `"x"`, ContentType: "text/plain", LastModified: time.Now().UTC()}
	vb.PutObject("test", "k", []byte("x"), meta)

	data, m, _ := vb.GetObject("test", "k")
	vid := m.VersionId

	// 删除特定版本
	err := vb.DeleteObjectVersion("test", "k", vid)
	if err != nil {
		t.Fatalf("DeleteObjectVersion: %v", err)
	}

	// 版本应不存在
	_, _, err = vb.GetObjectVersion("test", "k", vid)
	if err == nil || err != s3error.ErrNoSuchVersion {
		t.Fatalf("expected NoSuchVersion, got %v", err)
	}

	_ = data
}

func TestVersionedBackend_DeleteObjectVersion_NonExistent(t *testing.T) {
	inner := newMockStorage()
	vb := NewVersionedBackend(inner)
	inner.CreateBucket("test")

	vb.PutBucketVersioning("test", "Enabled")

	err := vb.DeleteObjectVersion("test", "k", "nonexistent")
	if err == nil || err != s3error.ErrNoSuchVersion {
		t.Fatalf("expected NoSuchVersion, got %v", err)
	}
}

func TestVersionedBackend_PutBucketVersioning_AlreadyEnabled(t *testing.T) {
	inner := newMockStorage()
	vb := NewVersionedBackend(inner)
	inner.CreateBucket("test")

	vb.PutBucketVersioning("test", "Enabled")
	err := vb.PutBucketVersioning("test", "Enabled")
	if err == nil || err != s3error.ErrVersioningAlreadyEnabled {
		t.Fatalf("expected VersioningAlreadyEnabled, got %v", err)
	}
}

func TestVersionedBackend_PutBucketVersioning_InvalidStatus(t *testing.T) {
	inner := newMockStorage()
	vb := NewVersionedBackend(inner)
	inner.CreateBucket("test")

	err := vb.PutBucketVersioning("test", "Invalid")
	if err == nil {
		t.Fatal("expected error for invalid status")
	}
}

func TestVersionedBackend_ListObjects_HiddenKeys(t *testing.T) {
	inner := newMockStorage()
	vb := NewVersionedBackend(inner)
	inner.CreateBucket("test")

	vb.PutBucketVersioning("test", "Enabled")
	meta := &service.ObjectMeta{Key: "k", Size: 1, ETag: `"x"`, ContentType: "text/plain", LastModified: time.Now().UTC()}
	vb.PutObject("test", "k", []byte("x"), meta)
	vb.DeleteObject("test", "k")

	// ListObjects 应隐藏 .versions/ 和 .bucket-meta 等内部 key
	entries, _, _, _, err := vb.ListObjects("test", "", "", "", 100)
	if err != nil {
		t.Fatalf("ListObjects: %v", err)
	}
	for _, e := range entries {
		if e.Key == ".bucket-meta" || e.Key == ".versions" {
			t.Errorf("ListObjects should not expose internal key: %s", e.Key)
		}
	}
}

func TestVersionedBackend_MultipartStorage_NotImplemented(t *testing.T) {
	inner := newMockStorage()
	vb := NewVersionedBackend(inner)

	_, err := vb.InitiateUpload("test", "k", "text/plain", nil)
	if err == nil || err != s3error.ErrNotImplemented {
		t.Fatalf("expected NotImplemented for non-multipart inner, got %v", err)
	}
	_, err = vb.UploadPart("test", "k", "uid", 1, []byte{})
	if err == nil || err != s3error.ErrNotImplemented {
		t.Fatalf("expected NotImplemented, got %v", err)
	}
}

// --- LocalBackend 基本测试 ---

func TestLocalBackend_CRUD(t *testing.T) {
	tmpDir := t.TempDir()
	backend := NewLocalBackend(tmpDir)

	if err := backend.CreateBucket("test"); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}

	exists, err := backend.BucketExists("test")
	if err != nil || !exists {
		t.Fatalf("BucketExists: %v", exists)
	}

	meta := &service.ObjectMeta{
		Key:          "hello.txt",
		Size:         5,
		ETag:         `"d41d8cd98f00b204e9800998ecf8427e"`,
		ContentType:  "text/plain",
		LastModified: time.Now().UTC(),
	}
	if err := backend.PutObject("test", "hello.txt", []byte("hello"), meta); err != nil {
		t.Fatalf("PutObject: %v", err)
	}

	data, gotMeta, err := backend.GetObject("test", "hello.txt")
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("expected 'hello', got %q", string(data))
	}
	if gotMeta.Key != "hello.txt" {
		t.Fatalf("expected key hello.txt, got %s", gotMeta.Key)
	}

	headMeta, err := backend.HeadObject("test", "hello.txt")
	if err != nil {
		t.Fatalf("HeadObject: %v", err)
	}
	if headMeta.ETag != meta.ETag {
		t.Fatalf("expected ETag %s, got %s", meta.ETag, headMeta.ETag)
	}

	if err := backend.DeleteObject("test", "hello.txt"); err != nil {
		t.Fatalf("DeleteObject: %v", err)
	}
	_, _, err = backend.GetObject("test", "hello.txt")
	if err == nil {
		t.Fatal("expected error after delete")
	}
}

func TestLocalBackend_NestedKeys(t *testing.T) {
	tmpDir := t.TempDir()
	backend := NewLocalBackend(tmpDir)
	backend.CreateBucket("test")

	meta := &service.ObjectMeta{Key: "a/b/c.txt", Size: 1, ContentType: "text/plain", LastModified: time.Now().UTC()}
	if err := backend.PutObject("test", "a/b/c.txt", []byte("x"), meta); err != nil {
		t.Fatalf("PutObject: %v", err)
	}

	data, _, err := backend.GetObject("test", "a/b/c.txt")
	if err != nil || string(data) != "x" {
		t.Fatalf("GetObject nested: %v", err)
	}
}

func TestLocalBackend_ListObjects(t *testing.T) {
	tmpDir := t.TempDir()
	backend := NewLocalBackend(tmpDir)
	backend.CreateBucket("test")

	now := time.Now().UTC()
	for _, key := range []string{"a.txt", "b.txt", "dir/c.txt"} {
		meta := &service.ObjectMeta{Key: key, Size: 1, ContentType: "text/plain", LastModified: now}
		backend.PutObject("test", key, []byte("x"), meta)
	}

	entries, _, _, truncated, err := backend.ListObjects("test", "", "", "", 100)
	if err != nil || truncated {
		t.Fatalf("ListObjects: %v truncated=%v", err, truncated)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
}

func TestLocalBackend_DeleteBucket(t *testing.T) {
	tmpDir := t.TempDir()
	backend := NewLocalBackend(tmpDir)
	backend.CreateBucket("test")

	err := backend.DeleteBucket("test")
	if err != nil {
		t.Fatalf("DeleteBucket: %v", err)
	}
}

func TestLocalBackend_BucketAlreadyExists(t *testing.T) {
	tmpDir := t.TempDir()
	backend := NewLocalBackend(tmpDir)
	backend.CreateBucket("test")
	err := backend.CreateBucket("test")
	if err == nil {
		t.Fatal("expected error for duplicate bucket")
	}
}

// --- service 元数据测试 ---

func TestWriteMeta_ReadMeta(t *testing.T) {
	tmpDir := t.TempDir()
	metaPath := filepath.Join(tmpDir, "test.meta")

	meta := &service.ObjectMeta{
		Key:          "hello.txt",
		Size:         42,
		ETag:         `"abc123"`,
		ContentType:  "text/plain",
		LastModified: time.Now().UTC(),
	}
	if err := service.WriteMeta(metaPath, meta); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	readMeta, err := service.ReadMeta(metaPath)
	if err != nil {
		t.Fatalf("ReadMeta: %v", err)
	}
	if readMeta.Key != meta.Key {
		t.Fatalf("expected key %s, got %s", meta.Key, readMeta.Key)
	}
	if readMeta.Size != meta.Size {
		t.Fatalf("expected size %d, got %d", meta.Size, readMeta.Size)
	}
	if readMeta.ETag != meta.ETag {
		t.Fatalf("expected ETag %s, got %s", meta.ETag, readMeta.ETag)
	}
}

func TestReadMeta_NotExist(t *testing.T) {
	_, err := service.ReadMeta("/nonexistent/path.meta")
	if err != s3error.ErrNoSuchKey {
		t.Fatalf("expected ErrNoSuchKey, got %v", err)
	}
}

func TestReadMeta_Corrupt(t *testing.T) {
	tmpDir := t.TempDir()
	metaPath := filepath.Join(tmpDir, "bad.meta")
	if err := os.WriteFile(metaPath, []byte("not json"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := service.ReadMeta(metaPath)
	if err == nil {
		t.Fatal("expected error for corrupt metadata")
	}
}

func TestWriteFile_Atomic(t *testing.T) {
	tmpDir := t.TempDir()
	destPath := filepath.Join(tmpDir, "subdir", "file.txt")

	if err := service.WriteFile(destPath, []byte("hello")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	data, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("expected 'hello', got %q", string(data))
	}
}

func TestWriteMeta_Atomic(t *testing.T) {
	tmpDir := t.TempDir()
	metaPath := filepath.Join(tmpDir, "test.meta")

	meta1 := &service.ObjectMeta{Key: "v1", Size: 1, LastModified: time.Now().UTC()}
	service.WriteMeta(metaPath, meta1)

	meta2 := &service.ObjectMeta{Key: "v2", Size: 2, LastModified: time.Now().UTC()}
	service.WriteMeta(metaPath, meta2)

	readMeta, err := service.ReadMeta(metaPath)
	if err != nil {
		t.Fatalf("ReadMeta: %v", err)
	}
	if readMeta.Key != "v2" {
		t.Fatalf("expected 'v2', got %s (atomic write may have failed)", readMeta.Key)
	}
}

func TestEnsureDir(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "a", "b", "c", "file.txt")
	if err := service.EnsureDir(filePath); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	// EnsureDir 创建父目录
	if _, err := os.Stat(filepath.Dir(filePath)); err != nil {
		t.Fatalf("parent dir not created: %v", err)
	}
}

func TestDirEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	empty, err := service.DirEmpty(tmpDir)
	if err != nil || !empty {
		t.Fatalf("DirEmpty on empty dir: %v %v", empty, err)
	}

	f, _ := os.Create(filepath.Join(tmpDir, "file"))
	f.Close()
	empty, err = service.DirEmpty(tmpDir)
	if err != nil || empty {
		t.Fatalf("DirEmpty on non-empty dir: %v %v", empty, err)
	}
}

func TestDirEmpty_NotExist(t *testing.T) {
	_, err := service.DirEmpty("/nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent dir")
	}
}

func TestObjectMeta_JSONRoundTrip(t *testing.T) {
	meta := &service.ObjectMeta{
		Key:            "test/key.txt",
		Size:           1024,
		ETag:           `"d41d8cd98f00b204e9800998ecf8427e"`,
		ContentType:    "application/octet-stream",
		LastModified:   time.Now().UTC(),
		UserMetadata:   map[string]string{"x-amz-meta-foo": "bar"},
		VersionId:      "abc123",
		IsLatest:       true,
		IsDeleteMarker: false,
	}

	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var decoded service.ObjectMeta
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.Key != meta.Key {
		t.Fatalf("Key mismatch: %s vs %s", decoded.Key, meta.Key)
	}
	if decoded.VersionId != meta.VersionId {
		t.Fatalf("VersionId mismatch: %s vs %s", decoded.VersionId, meta.VersionId)
	}
	if decoded.UserMetadata["x-amz-meta-foo"] != "bar" {
		t.Fatalf("UserMetadata lost")
	}
}

// --- VersionedBackend 辅助函数测试 ---

func TestSafeKey(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "simple"},
		{"a/b/c", "a%2Fb%2Fc"},
		{"", ""},
	}
	for _, tc := range tests {
		got := safeKey(tc.input)
		if got != tc.want {
			t.Errorf("safeKey(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestUnSafeKey(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "simple"},
		{"a%2Fb%2Fc", "a/b/c"},
		{"", ""},
	}
	for _, tc := range tests {
		got := unSafeKey(tc.input)
		if got != tc.want {
			t.Errorf("unSafeKey(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestGenerateVersionId(t *testing.T) {
	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := generateVersionId()
		if ids[id] {
			t.Fatalf("duplicate version ID: %s", id)
		}
		ids[id] = true
		if len(id) != 36 { // UUID 格式: 8-4-4-4-12
			t.Fatalf("invalid version ID format: %s", id)
		}
	}
}

// --- isS3Err 测试 ---

func TestIsS3Err(t *testing.T) {
	if !isS3Err(s3error.ErrNoSuchKey) {
		t.Fatal("isS3Err should return true for S3APIError")
	}
	if isS3Err(nil) {
		t.Fatal("isS3Err should return false for nil")
	}
}
