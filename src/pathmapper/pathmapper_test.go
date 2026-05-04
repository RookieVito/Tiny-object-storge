package pathmapper

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewPathMapper(t *testing.T) {
	pm := NewPathMapper("/tmp/test")
	if pm.Root() != "/tmp/test" {
		t.Fatalf("expected /tmp/test, got %s", pm.Root())
	}
}

func TestBucketPath_Valid(t *testing.T) {
	pm := NewPathMapper("/data")
	tests := []struct {
		bucket string
		want   string
	}{
		{"mybucket", "/data/mybucket"},
		{"ab", "/data/ab"},
		{"a-b", "/data/a-b"},
		{"a.b", "/data/a.b"},
		{"abc-123", "/data/abc-123"},
		{"my-test.bucket.name", "/data/my-test.bucket.name"},
	}
	for _, tc := range tests {
		got, err := pm.BucketPath(tc.bucket)
		if err != nil {
			t.Errorf("BucketPath(%q) unexpected error: %v", tc.bucket, err)
			continue
		}
		// filepath.Join 平台无关，统一使用 filepath.Join 生成期望值
		want := filepath.Join("/data", tc.bucket)
		if got != want {
			t.Errorf("BucketPath(%q) = %q, want %q", tc.bucket, got, want)
		}
	}
}

func TestBucketPath_Invalid(t *testing.T) {
	pm := NewPathMapper("/data")
	tests := []struct {
		bucket string
		reason string
	}{
		{"", "空 bucket"},
		{"ABC", "大写字母"},
		{"-startdash", "以连字符开头"},
		{"enddash-", "以连字符结尾"},
		{".startdot", "以点号开头"},
		{"enddot.", "以点号结尾"},
		{"my../bucket", "包含 .."},
		{"a\nb", "换行符"},
		{"a b", "空格"},
		{"a", "单字符"},
	}
	for _, tc := range tests {
		_, err := pm.BucketPath(tc.bucket)
		if err == nil {
			t.Errorf("BucketPath(%q) expected error (%s), got nil", tc.bucket, tc.reason)
		}
	}
}

func TestObjectPath_Valid(t *testing.T) {
	pm := NewPathMapper("/data")
	tests := []struct {
		bucket string
		key    string
	}{
		{"mybucket", "file.txt"},
		{"mybucket", "dir/file.txt"},
		{"mybucket", "dir/subdir/file.txt"},
		{"mybucket", "a/b/c/d/e/file"},
		{"mybucket", "file with spaces.txt"},
		{"mybucket", "中文文件名.txt"},
	}
	for _, tc := range tests {
		got, err := pm.ObjectPath(tc.bucket, tc.key)
		if err != nil {
			t.Errorf("ObjectPath(%q, %q) unexpected error: %v", tc.bucket, tc.key, err)
			continue
		}
		expected := filepath.Join("/data", tc.bucket, tc.key)
		if got != expected {
			t.Errorf("ObjectPath(%q, %q) = %q, want %q", tc.bucket, tc.key, got, expected)
		}
		if !filepath.IsAbs(got) {
			t.Errorf("ObjectPath(%q, %q) = %q, not absolute", tc.bucket, tc.key, got)
		}
	}
}

func TestObjectPath_EmptyKey(t *testing.T) {
	pm := NewPathMapper("/data")
	_, err := pm.ObjectPath("mybucket", "")
	if err == nil {
		t.Error("ObjectPath with empty key expected error")
	}
}

func TestObjectPath_PathTraversal(t *testing.T) {
	pm := NewPathMapper("/data")
	tests := []struct {
		bucket string
		key    string
		reason string
	}{
		{"mybucket", "../escape", "key 中的 .. 路径穿越"},
		{"mybucket", "dir/../../escape", "key 深层目录穿越"},
		{"mybucket", "..", "key 仅 .."},
		{"mybucket", "dir/../../../escape", "多层目录穿越"},
	}
	for _, tc := range tests {
		_, err := pm.ObjectPath(tc.bucket, tc.key)
		if err == nil {
			t.Errorf("ObjectPath(%q, %q) expected error (%s), got nil", tc.bucket, tc.key, tc.reason)
		}
	}
}

func TestObjectPath_InvalidBucket(t *testing.T) {
	pm := NewPathMapper("/data")
	_, err := pm.ObjectPath("", "file.txt")
	if err == nil {
		t.Error("ObjectPath with invalid bucket expected error")
	}
	_, err = pm.ObjectPath("INVALID", "file.txt")
	if err == nil {
		t.Error("ObjectPath with uppercase bucket expected error")
	}
}

func TestObjectPath_ConfinedToBucket(t *testing.T) {
	pm := NewPathMapper("/data")
	// 即使 filepath.Join/Clean 尝试逃逸，纵深防御也应阻止
	bucketPath := filepath.Join("/data", "mybucket")
	testCases := []string{
		filepath.Join("..", "otherbucket", "secret"),
	}

	for _, tc := range testCases {
		// 直接构造含 bucket 路径的 key 不太可能，但测试 ObjectPath 的前缀检查
		joined := filepath.Join(bucketPath, tc)
		if !stringsHasPrefix(filepath.Clean(joined), bucketPath+string(filepath.Separator)) {
			// 验证 ObjectPath 返回 error
			_, err := pm.ObjectPath("mybucket", tc)
			if err == nil {
				t.Errorf("ObjectPath with key %q expected error (escapes bucket)", tc)
			}
		}
	}
}

func stringsHasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func TestMetaPath(t *testing.T) {
	pm := NewPathMapper("/data")
	meta, err := pm.MetaPath("mybucket", "file.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := filepath.Join("/data", "mybucket", "file.txt") + ".meta"
	if meta != expected {
		t.Errorf("MetaPath = %q, want %q", meta, expected)
	}
}

func TestMetaPath_InvalidInput(t *testing.T) {
	pm := NewPathMapper("/data")
	_, err := pm.MetaPath("", "file.txt")
	if err == nil {
		t.Error("MetaPath with empty bucket expected error")
	}
	_, err = pm.MetaPath("mybucket", "")
	if err == nil {
		t.Error("MetaPath with empty key expected error")
	}
	_, err = pm.MetaPath("mybucket", "../escape")
	if err == nil {
		t.Error("MetaPath with traversal key expected error")
	}
}

func TestObjectPath_RealFilesystem(t *testing.T) {
	// 在真实文件系统上验证路径映射
	tmpDir := t.TempDir()
	pm := NewPathMapper(tmpDir)

	bucketPath, err := pm.BucketPath("testbucket")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.MkdirAll(bucketPath, 0755); err != nil {
		t.Fatalf("failed to create bucket dir: %v", err)
	}

	objPath, err := pm.ObjectPath("testbucket", "hello/world.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(objPath), 0755); err != nil {
		t.Fatalf("failed to create dir: %v", err)
	}
	if err := os.WriteFile(objPath, []byte("test"), 0644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	data, err := os.ReadFile(objPath)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}
	if string(data) != "test" {
		t.Fatalf("expected 'test', got %q", string(data))
	}
}
