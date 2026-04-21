package storage

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"tiny-object-storage/src/pathmapper"
	"tiny-object-storage/src/service"
	"tiny-object-storage/src/s3error"
)

// LocalBackend 基于本地文件系统的存储后端实现。
type LocalBackend struct {
	pm *pathmapper.PathMapper
}

// NewLocalBackend 创建 LocalBackend。
func NewLocalBackend(root string) *LocalBackend {
	return &LocalBackend{pm: pathmapper.NewPathMapper(root)}
}

// Root 返回存储根目录路径（供外部使用，如 metrics 扫描）。
func (lb *LocalBackend) Root() string {
	return lb.pm.Root()
}

func (lb *LocalBackend) CreateBucket(bucket string) error {
	path, err := lb.pm.BucketPath(bucket)
	if err != nil {
		return err
	}
	if err := os.Mkdir(path, 0755); err != nil {
		if os.IsExist(err) {
			return s3error.ErrBucketAlreadyExists
		}
		return err
	}
	return nil
}

func (lb *LocalBackend) DeleteBucket(bucket string) error {
	path, err := lb.pm.BucketPath(bucket)
	if err != nil {
		return err
	}
	empty, err := service.DirEmpty(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s3error.ErrNoSuchBucket
		}
		return err
	}
	if !empty {
		return s3error.ErrBucketNotEmpty
	}
	return os.Remove(path)
}

func (lb *LocalBackend) BucketExists(bucket string) (bool, error) {
	path, err := lb.pm.BucketPath(bucket)
	if err != nil {
		return false, err
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, s3error.ErrNoSuchBucket
		}
		return false, err
	}
	if !info.IsDir() {
		return false, s3error.ErrNoSuchBucket
	}
	return true, nil
}

func (lb *LocalBackend) ListBuckets() ([]BucketInfo, error) {
	entries, err := os.ReadDir(lb.pm.Root())
	if err != nil {
		return nil, err
	}
	buckets := make([]BucketInfo, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		buckets = append(buckets, BucketInfo{
			Name:         e.Name(),
			CreationDate: info.ModTime().UTC().Truncate(time.Second),
		})
	}
	return buckets, nil
}

func (lb *LocalBackend) PutObject(bucket, key string, data []byte, meta *service.ObjectMeta) error {
	dataPath, err := lb.pm.ObjectPath(bucket, key)
	if err != nil {
		return err
	}
	metaPath, err := lb.pm.MetaPath(bucket, key)
	if err != nil {
		return err
	}
	if err := service.WriteFile(dataPath, data); err != nil {
		return err
	}
	if err := service.WriteMeta(metaPath, meta); err != nil {
		return err
	}
	return nil
}

func (lb *LocalBackend) GetObject(bucket, key string) ([]byte, *service.ObjectMeta, error) {
	dataPath, err := lb.pm.ObjectPath(bucket, key)
	if err != nil {
		return nil, nil, err
	}
	metaPath, err := lb.pm.MetaPath(bucket, key)
	if err != nil {
		return nil, nil, err
	}
	meta, err := service.ReadMeta(metaPath)
	if err != nil {
		return nil, nil, err
	}
	data, err := os.ReadFile(dataPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, s3error.ErrNoSuchKey
		}
		return nil, nil, err
	}
	return data, meta, nil
}

func (lb *LocalBackend) HeadObject(bucket, key string) (*service.ObjectMeta, error) {
	metaPath, err := lb.pm.MetaPath(bucket, key)
	if err != nil {
		return nil, err
	}
	return service.ReadMeta(metaPath)
}

func (lb *LocalBackend) DeleteObject(bucket, key string) error {
	dataPath, err := lb.pm.ObjectPath(bucket, key)
	if err != nil {
		return err
	}
	metaPath, err := lb.pm.MetaPath(bucket, key)
	if err != nil {
		return err
	}
	os.Remove(dataPath)
	os.Remove(metaPath)
	removeEmptyParents(filepath.Dir(dataPath), filepath.Join(lb.pm.Root(), bucket))
	return nil
}

func (lb *LocalBackend) ListObjects(bucket, prefix, delimiter, startAfter string, maxKeys int) (
	[]ObjectEntry, []string, string, bool, error,
) {
	bucketPath, err := lb.pm.BucketPath(bucket)
	if err != nil {
		return nil, nil, "", false, err
	}

	// 检查 bucket 是否存在。
	if _, err := os.Stat(bucketPath); err != nil {
		if os.IsNotExist(err) {
			return nil, nil, "", false, s3error.ErrNoSuchBucket
		}
		return nil, nil, "", false, err
	}

	// 遍历 bucket 目录，收集常规文件。
	type rawEntry struct {
		key  string
		meta *service.ObjectMeta
	}
	var entries []rawEntry
	_ = filepath.WalkDir(bucketPath, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if path == bucketPath || d.IsDir() || strings.HasSuffix(path, ".meta") {
			return nil
		}
		rel, err := filepath.Rel(bucketPath, path)
		if err != nil {
			return nil
		}
		s3Key := filepath.ToSlash(rel)
		meta, err := service.ReadMeta(path + ".meta")
		if err != nil {
			return nil
		}
		entries = append(entries, rawEntry{key: s3Key, meta: meta})
		return nil
	})

	// 字典序排序。
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].key < entries[j].key
	})

	// prefix 过滤。
	var filtered []rawEntry
	for _, e := range entries {
		if strings.HasPrefix(e.key, prefix) {
			filtered = append(filtered, e)
		}
	}

	// start-after 分页。
	if startAfter != "" {
		idx := sort.Search(len(filtered), func(i int) bool {
			return filtered[i].key > startAfter
		})
		filtered = filtered[idx:]
	}

	// delimiter 分组，收集到 maxKeys。
	var contents []ObjectEntry
	var commonPrefixes []string
	commonPrefixSet := make(map[string]bool)
	count := 0

	for _, e := range filtered {
		if count >= maxKeys {
			break
		}
		if delimiter == "" {
			contents = append(contents, ObjectEntry{
				Key:          e.key,
				LastModified: e.meta.LastModified,
				ETag:         e.meta.ETag,
				Size:         e.meta.Size,
				StorageClass: "STANDARD",
			})
			count++
		} else {
			remainder := strings.TrimPrefix(e.key, prefix)
			delimIdx := strings.Index(remainder, delimiter)
			if delimIdx < 0 {
				contents = append(contents, ObjectEntry{
					Key:          e.key,
					LastModified: e.meta.LastModified,
					ETag:         e.meta.ETag,
					Size:         e.meta.Size,
					StorageClass: "STANDARD",
				})
				count++
			} else {
				commonPrefix := prefix + remainder[:delimIdx+len(delimiter)]
				if !commonPrefixSet[commonPrefix] {
					commonPrefixes = append(commonPrefixes, commonPrefix)
					commonPrefixSet[commonPrefix] = true
					count++
				}
			}
		}
	}

	// 分页判断。
	isTruncated := count < len(filtered)
	nextToken := ""
	if isTruncated {
		lastKey := filtered[count-1].key
		nextToken = base64.StdEncoding.EncodeToString([]byte(lastKey))
	}

	return contents, commonPrefixes, nextToken, isTruncated, nil
}

// removeEmptyParents 向上删除空父目录，直到 stopAt（不包含）。
func removeEmptyParents(dir, stopAt string) {
	for dir != stopAt && dir != filepath.Dir(dir) {
		if err := os.Remove(dir); err != nil {
			break
		}
		dir = filepath.Dir(dir)
	}
}
