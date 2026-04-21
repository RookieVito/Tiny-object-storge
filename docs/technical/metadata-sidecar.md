# 元数据侧边文件设计

## 概述

对象存储需要为每个对象保存元数据（大小、类型、修改时间、自定义属性等）。
本项目采用 MinIO 风格的**侧边文件（sidecar file）** 方案：每个数据文件旁存放一个
`.meta` JSON 文件存储其元数据。

## 1. 设计方案对比

| 方案 | 优点 | 缺点 | 采用者 |
|------|------|------|--------|
| **侧边文件** (本项目) | 零外部依赖，直观易调试 | 列举时需额外 I/O | MinIO |
| SQLite 索引 | 查询性能好 | 需要额外依赖 | Garage, SeaweedFS |
| 扩展属性 (xattr) | 无额外文件 | Linux 特有，调试困难 | Ceph RADOS |
| 内存缓存 | 最快 | 重启丢失，不适合持久化 | - |

侧边文件方案是对象存储领域的主流选择，适合 MVP 阶段。

## 2. 磁盘布局

```
{storage_root}/
└── my-bucket/
    ├── hello.txt              # 数据文件（原始字节）
    ├── hello.txt.meta         # 元数据文件（JSON）
    └── photos/
        └── 2024/
            ├── cat.jpg
            └── cat.jpg.meta
```

命名规则：`{数据文件路径}.meta`

### 2.1 命名约定

- 后缀 `.meta` 便于与数据文件区分
- 临时写入使用 `.tmp-*.meta` 前缀，便于识别和清理
- `filepath.WalkDir` 遍历时通过 `strings.HasSuffix(path, ".meta")` 跳过

## 3. 元数据结构

```json
{
  "key": "photos/2024/cat.jpg",
  "size": 102400,
  "etag": "\"d41d8cd98f00b204e9800998ecf8427e\"",
  "content_type": "image/jpeg",
  "last_modified": "2024-01-15T10:30:00Z",
  "user_metadata": {
    "X-Amz-Meta-Author": "vito"
  }
}
```

| 字段 | 类型 | 来源 |
|------|------|------|
| `key` | string | 请求路径中的 S3 key |
| `size` | int64 | `len(body)` |
| `etag` | string | MD5 哈希（带引号） |
| `content_type` | string | 请求头 Content-Type 或自动检测 |
| `last_modified` | string | ISO 8601 UTC 时间戳 |
| `user_metadata` | map | 以 `X-Amz-Meta-` 为前缀的请求头 |

## 4. 写入时机

### PutObject 流程

```go
func PutObject(w http.ResponseWriter, r *http.Request) {
    body := io.ReadAll(r.Body)

    // 1. 构建元数据
    meta := buildMetaFromRequest(key, body, r)
    //   - ETag = md5(body)
    //   - Content-Type = 请求头 或 http.DetectContentType(body)
    //   - UserMetadata = 提取 X-Amz-Meta-* 请求头

    // 2. 原子写入数据文件
    writeFile(dataPath, body)

    // 3. 原子写入元数据文件
    writeMeta(metaPath, meta)

    // 4. 返回 ETag
    w.Header().Set("ETag", meta.ETag)
}
```

### 读取流程

```go
func GetObject(w http.ResponseWriter, r *http.Request) {
    // 1. 读取元数据
    meta := readMeta(metaPath)

    // 2. 设置响应头（从元数据中读取）
    w.Header().Set("Content-Type", meta.ContentType)
    w.Header().Set("Content-Length", meta.Size)
    w.Header().Set("ETag", meta.ETag)
    w.Header().Set("Last-Modified", meta.LastModified)

    // 3. 流式返回数据文件
    io.Copy(w, os.Open(dataPath))
}
```

## 5. Content-Type 自动检测

当客户端未提供 Content-Type 时，使用 Go 的 `http.DetectContentType` 嗅探：

```go
contentType := r.Header.Get("Content-Type")
if contentType == "" {
    contentType = http.DetectContentType(body)
}
```

`DetectContentType` 的工作原理：
- 取请求体的前 512 字节
- 检查已知文件格式的 magic byte 序列
- 常见识别结果：

| Magic Bytes | 类型 | Content-Type |
|------------|------|-------------|
| `<!DOCTYPE html>` 或 `<html` | HTML | `text/html; charset=utf-8` |
| `{"` 开头 | JSON | `application/json` |
| `\x89PNG\r\n\x1a\n` | PNG | `image/png` |
| `\xff\xd8\xff` | JPEG | `image/jpeg` |
| `%PDF` | PDF | `application/pdf` |
| 无法识别 | 二进制 | `application/octet-stream` |

自动检测结果存储在 `.meta` 文件中，后续 GET/HEAD 请求原样返回，
确保客户端收到的 Content-Type 与上传时一致。

## 6. 删除的一致性

DeleteObject 需要同时删除数据文件和元数据文件：

```go
os.Remove(dataPath)    // 忽略不存在（幂等）
os.Remove(metaPath)    // 忽略不存在（幂等）
```

两步操作不是原子的——如果删除了数据文件但 `.meta` 还在，ListObjectsV2 遍历时会
跳过该条目（因为没有对应的 `.meta` 文件），不会返回错误条目。这是可接受的行为。

## 7. 元数据损坏的处理

读取 `.meta` 文件时的容错：

```go
func readMeta(metaPath string) (*ObjectMeta, error) {
    data, err := os.ReadFile(metaPath)
    if os.IsNotExist(err) {
        return nil, ErrNoSuchKey
    }
    if err := json.Unmarshal(data, &meta); err != nil {
        return nil, fmt.Errorf("corrupt metadata: %w", err)
    }
    return &meta, nil
}
```

- 文件不存在 → 返回 NoSuchKey（对象不存在）
- JSON 解析失败 → 返回内部错误
- ListObjectsV2 遍历时跳过无法解析的元数据条目，不中断列举
