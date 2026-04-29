<!-- tags: versioning, storage, api -->

# 对象版本控制 (Object Versioning)

## 概述

Phase 15 实现 S3 兼容的对象版本控制。启用版本控制的 bucket 中，每次 `PutObject` 创建新版本，`DeleteObject` 创建 delete marker（不真正删除数据），通过 `?versionId=X` 访问历史版本。

## 设计决策

### 装饰器模式

`VersionedBackend` 包装任意 `StorageBackend`，版本控制逻辑全部在装饰器中，不修改现有后端实现。

```go
// cmd/server/main.go
backend = storage.NewVersionedBackend(backend)
```

优点：
- 后端无关：LocalBackend、ECBackend、DistributedBackend 均可被包装
- 零侵入：现有后端代码无需任何修改
- 可测试：装饰器可独立测试

### 后端无关的版本存储

归档版本作为普通对象存储在内部后端（key 前缀 `.versions/{safeKey}/{versionId}`），通过 inner backend 的标准接口操作。

### safeKey 编码

key 中的 `/` 替换为 `%2F`，避免 `.versions/` 下深层嵌套。

```go
func safeKey(key string) string { return strings.ReplaceAll(key, "/", "%2F") }
```

选择 `%2F` 而非 `_SLASH_` 的原因：`_SLASH_` 可能与真实 key 内容冲突（key 中包含 `_SLASH_` 字符串时会导致歧义），`%2F` 是 URL 编码标准，不太可能出现在 S3 key 中。

### Delete Marker

- 零字节数据 + meta 中 `IsDeleteMarker: true`
- 路径前缀 `.dm-{versionId}` 实现 O(1) 识别
- `.current-delete-marker` 哨兵对象标记当前最新版本是 delete marker

### UUID v4 版本 ID

使用 `crypto/rand` 生成，符合 S3 版本 ID 格式。

### Bucket 级元数据

版本控制状态存储为 `.bucket-meta` 对象（通过 inner backend 存储），支持 Unversioned / Enabled / Suspended 三种状态。

## 接口定义

```go
// src/storage/backend.go
type VersionedStorage interface {
    PutBucketVersioning(bucket, status string) error
    GetBucketVersioning(bucket string) (string, error)
    GetObjectVersion(bucket, key, versionId string) ([]byte, *service.ObjectMeta, error)
    HeadObjectVersion(bucket, key, versionId string) (*service.ObjectMeta, error)
    DeleteObjectVersion(bucket, key, versionId string) error
    ListObjectVersions(bucket, prefix, delimiter, keyMarker, versionIdMarker string, maxKeys int) (
        versions, deleteMarkers []VersionEntry, commonPrefixes []string,
        nextKeyMarker, nextVersionIdMarker string, isTruncated bool, err error,
    )
}
```

## 核心流程

### PutObject

```
版本化 bucket?
  ├─ 否 → 直接委托 inner.PutObject
  └─ 是 → archiveCurrentVersion() → 生成 versionId → 设置 meta.VersionId/IsLatest → inner.PutObject
```

归档当前版本前，检查 `.current-delete-marker` 哨兵并清理。

### DeleteObject

```
版本化 bucket?
  ├─ 否 → 直接委托 inner.DeleteObject
  └─ 是 → 检查 .current-delete-marker（幂等）
           → 当前版本存在? → 归档 → 删除当前
           → 写入 .dm-{versionId}（零字节 delete marker）
           → 写入 .current-delete-marker 哨兵
```

### GetObject / HeadObject

```
版本化 bucket?
  ├─ 否 → 直接委托 inner
  └─ 是 → 检查 .current-delete-marker → 存在则返回 404
           → inner.GetObject/HeadObject
           → 无 versionId 的旧对象设 versionId="null"（兼容）
```

### GetObjectVersion / HeadObjectVersion

查找顺序：
1. `.versions/{safeKey}/{versionId}` — 归档的普通版本
2. `.versions/{safeKey}/.dm-{versionId}` — 归档的 delete marker
3. 当前 key — 与 meta.VersionId 匹配则返回

都不匹配 → `ErrNoSuchVersion` (404)

### DeleteObjectVersion

查找目标版本（归档普通版本 / 归档 delete marker / 当前版本），三种情况：

1. **归档版本** → 直接删除
2. **归档 delete marker** → 删除后检查是否为当前 delete marker（哨兵匹配）→ 是则提升前一版本
3. **当前版本** → 先归档再删除 → 归档后按情况 1/2 处理

提升前一版本逻辑：
- 查找 `.versions/{safeKey}/` 下所有剩余版本
- 按 LastModified 倒序取最新
- 如果是 delete marker → 更新哨兵
- 如果是普通版本 → 从 `.versions/` 移到当前 key

### ListObjectVersions

单次 `ListObjects(".versions/", ...)` 遍历所有归档版本 + 单次 `ListObjects("", ...)` 获取当前对象。路径解析确定版本类型（`.dm-` 前缀 = delete marker，`.current-delete-marker` = 哨兵标记）。

排序：按 key 字典序，同 key 按时间倒序。首个为 `IsLatest=true`。

## 磁盘布局

```
{root}/{bucket}/
    .bucket-meta                          — 版本控制配置 JSON
    {key}                                 — 当前版本数据（最新）
    {key}.meta                            — 当前版本元数据（含 VersionId）
    .versions/
        {safeKey}/                        — 如 "dir%2Ffile.txt"
            {versionId}                   — 归档版本数据
            {versionId}.meta              — 归档版本元数据
            .dm-{versionId}               — delete marker（零字节）
            .dm-{versionId}.meta          — meta with IsDeleteMarker=true
            .current-delete-marker        — 哨兵（最新是 delete marker 时存在）
            .current-delete-marker.meta   — 哨兵元数据（含 VersionId）
```

## ObjectMeta 扩展

```go
// src/service/metadata.go
type ObjectMeta struct {
    // ... 原有字段 ...
    VersionId      string `json:"version_id,omitempty"`       // UUID v4 或 "null"
    IsLatest       bool   `json:"is_latest,omitempty"`        // 是否为最新版本
    IsDeleteMarker bool   `json:"is_delete_marker,omitempty"` // 是否为 delete marker
}
```

## HTTP 端点

| 操作 | 方法 | 路径 | 说明 |
|------|------|------|------|
| PutBucketVersioning | PUT | `/{bucket}?versioning` | 启用/暂停版本控制 |
| GetBucketVersioning | GET | `/{bucket}?versioning` | 查询版本控制状态 |
| GetObjectVersion | GET | `/{bucket}/{key}?versionId=X` | 获取特定版本 |
| HeadObjectVersion | HEAD | `/{bucket}/{key}?versionId=X` | 获取特定版本元数据 |
| DeleteObjectVersion | DELETE | `/{bucket}/{key}?versionId=X` | 永久删除特定版本 |
| ListObjectVersions | GET | `/{bucket}?versions` | 列出所有版本 |

## 响应头

| Header | 场景 | 值 |
|--------|------|----|
| `x-amz-version-id` | PutObject/GetObject/HeadObject 成功 | 版本 ID（UUID v4） |
| `x-amz-delete-marker` | DeleteObject 创建 delete marker | `"true"` |

## 错误码

| 错误码 | HTTP 状态 | 触发条件 |
|--------|-----------|----------|
| `VersioningAlreadyEnabled` | 400 | 重复启用版本控制 |
| `VersioningAlreadySuspended` | 400 | 重复暂停版本控制 |
| `NoSuchVersion` | 404 | versionId 不存在 |

## MultipartStorage 委托

`VersionedBackend` 直接实现 `MultipartStorage` 接口，所有方法委托给 inner backend。版本化 bucket 的 multipart upload 不创建版本。

## 向后兼容性

未启用版本控制的 bucket 行为与之前完全一致：
- `PutObject` 不生成 versionId
- `DeleteObject` 直接删除数据
- `GetObject`/`HeadObject` 无 `.current-delete-marker` 检查
- `?versionId` 参数由 handler 层 type assertion 处理，后端不支持时返回 501

## 源文件

| 文件 | 职责 |
|------|------|
| `src/storage/versioning.go` | VersionedBackend 装饰器，核心版本控制逻辑 |
| `src/storage/backend.go` | VersionedStorage 接口 + VersionEntry 类型 |
| `src/handler/versioning.go` | VersioningManager，HTTP handler |
| `src/handler/object.go` | GetObject/HeadObject/DeleteObject 的 versionId 分发 |
| `src/handler/router.go` | `?versioning`/`?versions` 路由分发 |
| `src/s3error/error.go` | 版本控制错误码 |
| `src/service/metadata.go` | ObjectMeta VersionId/IsLatest/IsDeleteMarker 字段 |
| `cmd/server/main.go` | VersionedBackend 包装 |
