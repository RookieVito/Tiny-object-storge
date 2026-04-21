package pathmapper

import (
	"path/filepath"
	"regexp"
	"strings"

	"tiny-object-storage/src/s3error"
)

// PathMapper 将 (bucket, key) 安全转换为文件系统路径。
// 三层遍历防护：bucket 名称正则校验、key 拒绝 ".." 子串、后缀前缀验证。
type PathMapper struct {
	root string
}

// bucketNameRe 校验 bucket 名称（简化的 S3 命名规则）。
var bucketNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.\-]{1,61}[a-z0-9]$|^([a-z0-9][a-z0-9.\-]{0,61}[a-z0-9])$`)

// NewPathMapper 创建 PathMapper 实例。
func NewPathMapper(root string) *PathMapper {
	return &PathMapper{root: root}
}

// Root 返回存储根目录路径。
func (pm *PathMapper) Root() string {
	return pm.root
}

// BucketPath 返回 bucket 的文件系统路径。
func (pm *PathMapper) BucketPath(bucket string) (string, error) {
	if bucket == "" || !bucketNameRe.MatchString(bucket) || strings.Contains(bucket, "..") {
		return "", s3error.ErrInvalidBucketName
	}
	return filepath.Join(pm.root, bucket), nil
}

// ObjectPath 返回对象数据文件的文件系统路径。
func (pm *PathMapper) ObjectPath(bucket string, key string) (string, error) {
	bucketPath, err := pm.BucketPath(bucket)
	if err != nil {
		return "", err
	}

	if key == "" || strings.Contains(key, "..") {
		return "", s3error.ErrInvalidKey
	}

	joined := filepath.Join(bucketPath, key)
	cleaned := filepath.Clean(joined)

	// 纵深防御：确保结果在 bucket 目录内。
	prefix := bucketPath + string(filepath.Separator)
	if !strings.HasPrefix(cleaned, prefix) && cleaned != bucketPath {
		return "", s3error.ErrInvalidKey
	}

	return cleaned, nil
}

// MetaPath 返回 .meta 侧边文件的文件系统路径。
func (pm *PathMapper) MetaPath(bucket string, key string) (string, error) {
	dataPath, err := pm.ObjectPath(bucket, key)
	if err != nil {
		return "", err
	}
	return dataPath + ".meta", nil
}
