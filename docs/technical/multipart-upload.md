<!-- tags: multipart, api, s3 -->
# Multipart Upload 技术设计

## 1. 概述

Multipart Upload 允许客户端将大文件拆分为多个 part 并行上传，突破 `MaxBodySize`（默认 10MB）限制，同时支持断点续传。遵循 AWS S3 Multipart Upload 协议。

## 2. 接口设计

### MultipartStorage 接口

独立于 `StorageBackend` 的可选扩展接口，通过 type assertion 检测后端是否支持：

```go
type MultipartStorage interface {
    InitiateUpload(bucket, key, contentType string, userMeta map[string]string) (*UploadInfo, error)
    UploadPart(bucket, key, uploadId string, partNumber int, data []byte) (*PartInfo, error)
    CompleteUpload(bucket, key, uploadId string, parts []PartInfo) (string, error)
    AbortUpload(bucket, key, uploadId string) error
    ListParts(bucket, key, uploadId string) ([]PartInfo, error)
    ListUploads(bucket, prefix, keyMarker string, maxUploads int) ([]UploadInfo, string, bool, error)
    GetUploadInfo(bucket, key, uploadId string) (*UploadInfo, error)
}
```

**设计选择**：不将 multipart 方法加入 `StorageBackend` 接口，避免 EC/Distributed 后端必须立即实现。

### 数据类型

```go
type PartInfo struct {
    PartNumber   int
    Size         int64
    ETag         string    // quoted MD5 hex: "\"abcd1234\""
    LastModified time.Time
}

type UploadInfo struct {
    UploadId     string            `json:"upload_id"`
    Bucket       string            `json:"bucket"`
    Key          string            `json:"key"`
    ContentType  string            `json:"content_type,omitempty"`
    UserMetadata map[string]string `json:"user_metadata,omitempty"`
    Initiated    time.Time         `json:"initiated"`
}
```

## 3. 磁盘布局

```
{root}/{bucket}/
    .uploads/                          # multipart 临时目录（ListObjects 跳过）
        {uploadId}/                    # UUID v4
            info.json                  # UploadInfo 元数据
            part-0001.bin              # Part 数据
            part-0001.bin.meta         # PartInfo 元数据
            part-0002.bin
            part-0002.bin.meta
    hello.txt                          # 正常对象数据
    hello.txt.meta                     # 正常对象元数据
```

**隔离机制**：
- `ListObjects` 的 `WalkDir` 遇到 `.uploads/` 目录时返回 `filepath.SkipDir`
- `Metrics` 的 `scanFilesystem` 同样跳过 `.uploads/`
- `DeleteBucket` 通过 `DirEmpty` 检测，含 `.uploads/` 的 bucket 不可删除（正确语义）

## 4. Upload 生命周期

```
InitiateMultipartUpload
        │
        ▼
  ┌─────────────┐
  │  In-Progress │ ← UploadPart (可重复覆盖同一 partNumber)
  │  (可 Abort)  │ ← ListParts
  └──────┬───────┘
         │
    CompleteMultipartUpload
         │
         ▼
  ┌─────────────┐
  │  Completed   │ → 最终对象写入 {bucket}/{key}
  │  (已清理)    │   .uploads/{uploadId} 目录已删除
  └─────────────┘
```

## 5. ETag 计算

### 单 part ETag

```go
etag := fmt.Sprintf(`"%x"`, md5.Sum(partData))
```

标准 quoted MD5 hex，与 PutObject ETag 格式一致。

### 最终对象 ETag（S3 标准）

```go
// 拼接各 part 的 16 字节 MD5 摘要
var concatHash []byte
for _, h := range md5Hashes {
    concatHash = append(concatHash, h[:]...)
}
// 最终 ETag = MD5(concat) + "-N"
finalETag := fmt.Sprintf(`"%x-%d"`, md5.Sum(concatHash), partCount)
```

示例：`"a1b2c3d4e5f6-3"` — 3 个 part 组成的对象。

## 6. 路由分发

Multipart 端点复用现有 S3 路由模式，通过 query param 区分：

| 基础路由 | Query Param | 目标 Handler | 原始 Handler |
|---------|-------------|-------------|-------------|
| `POST /{bucket}/{key...}` | `?uploads` | InitiateMultipartUpload | — |
| `POST /{bucket}/{key...}` | `?uploadId=X` | CompleteMultipartUpload | — |
| `PUT /{bucket}/{key...}` | `?uploadId=X` | UploadPart | PutObject |
| `GET /{bucket}/{key...}` | `?uploadId=X` | ListParts | GetObject |
| `DELETE /{bucket}/{key...}` | `?uploadId=X` | AbortMultipartUpload | DeleteObject |
| `GET /{bucket}` | `?uploads` | ListMultipartUploads | ListObjects |

实现方式：闭包包装，在 `authWrap` 内部按 query param 分发。

## 7. 并发模型

```
UploadPart ← 无锁（并发安全）
    ├── 不同 partNumber：可并行上传
    └── 同一 partNumber：原子 rename 覆盖（service.WriteFile）

CompleteUpload ← bucket 锁
    └── 与 PutObject 互斥，防止并发写入同一 key

AbortUpload ← bucket 锁
    └── 防止与 Complete 并发竞争

InitiateUpload ← 无锁
    └── 仅创建新目录，无冲突

ListParts / ListUploads ← 无锁
    └── 只读操作
```

## 8. 数据完整性验证

CompleteMultipartUpload 时执行以下验证：

1. **Part 存在性**：每个请求的 part 必须有对应的 `part-NNNN.bin` 文件
2. **ETag 匹配**：客户端提供的 ETag 必须与服务器计算的 MD5 一致
3. **Part 大小**：非最后一个 part 必须 ≥ 5MB（S3 标准限制）
4. **Part 顺序**：请求的 parts 必须 按 PartNumber 升序排列

## 9. Complete 流程

```
1. 验证 parts 存在 + ETag 匹配 + 大小限制
2. 按顺序读取所有 part 数据
3. 拼接到临时文件（assemble-*.tmp）
4. 计算最终 ETag = MD5(concat_MD5s)-N
5. 构造 ObjectMeta
6. service.EnsureDir → os.Rename(tmp, dataPath)
7. service.WriteMeta → 写入 .meta
8. os.RemoveAll(.uploads/{uploadId})
```

## 10. 配置限制

| 参数 | 默认值 | 描述 |
|------|--------|------|
| maxBodySize | 10 MB | 单个 part 的最大大小（复用现有配置） |
| partNumber 范围 | 1-10000 | S3 标准限制 |
| 最小 part 大小 | 5 MB | 非 final part 的最小大小（Complete 时验证） |
