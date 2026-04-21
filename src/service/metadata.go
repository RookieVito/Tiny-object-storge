package service

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"tiny-object-storage/src/s3error"
)

// ObjectMeta 表示存储在 .meta 侧边文件中的元数据。
type ObjectMeta struct {
	Key          string            `json:"key"`
	Size         int64             `json:"size"`
	ETag         string            `json:"etag"`
	ContentType  string            `json:"content_type"`
	LastModified time.Time         `json:"last_modified"`
	UserMetadata map[string]string `json:"user_metadata,omitempty"`
}

// WriteMeta 原子写入元数据 JSON 到 metaPath。
func WriteMeta(metaPath string, meta *ObjectMeta) error {
	dir := filepath.Dir(metaPath)
	tmp, err := os.CreateTemp(dir, ".tmp-*.meta")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, metaPath)
}

// ReadMeta 读取并解析元数据侧边文件。
func ReadMeta(metaPath string) (*ObjectMeta, error) {
	data, err := os.ReadFile(metaPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, s3error.ErrNoSuchKey
		}
		return nil, err
	}
	var meta ObjectMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("corrupt metadata: %w", err)
	}
	return &meta, nil
}

// BuildMetaFromRequest 从 HTTP PUT 请求构造 ObjectMeta。
func BuildMetaFromRequest(key string, body []byte, r *http.Request) *ObjectMeta {
	etag := fmt.Sprintf(`"%x"`, md5.Sum(body))

	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		contentType = http.DetectContentType(body)
	}

	// 提取 x-amz-meta-* headers。
	userMeta := make(map[string]string)
	for k, v := range r.Header {
		if strings.HasPrefix(strings.ToLower(k), "x-amz-meta-") {
			userMeta[k] = v[0]
		}
	}

	return &ObjectMeta{
		Key:          key,
		Size:         int64(len(body)),
		ETag:         etag,
		ContentType:  contentType,
		LastModified: time.Now().UTC(),
		UserMetadata: userMeta,
	}
}

// WriteFile 原子写入数据到 destPath。
func WriteFile(destPath string, data []byte) error {
	dir := filepath.Dir(destPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, destPath)
}

// EnsureDir 创建文件路径的所有父目录。
func EnsureDir(path string) error {
	return os.MkdirAll(filepath.Dir(path), 0755)
}

// DirEmpty 检查目录是否为空。
func DirEmpty(dirPath string) (bool, error) {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		if fs.ErrNotExist == err {
			return false, err
		}
		return false, err
	}
	return len(entries) == 0, nil
}
