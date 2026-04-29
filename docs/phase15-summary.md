<!-- tags: versioning, storage, api -->
# Phase 15: 对象版本控制 — 完成总结

## 实现日期

2026-04-29

## 概述

实现 S3 兼容的对象版本控制。启用版本控制的 bucket 中，每次 PutObject 创建新版本，DeleteObject 创建 delete marker（不真正删除数据），通过 `?versionId=X` 访问历史版本。

## 核心实现

### VersionedBackend 装饰器（src/storage/versioning.go）

采用装饰器模式包装任意 `StorageBackend`，版本控制逻辑全部在装饰器中：

- **PutObject**：版本化 bucket → 归档当前版本到 `.versions/` → 写新版本（带 versionId）
- **DeleteObject**：版本化 bucket → 归档当前版本 → 创建 delete marker（零字节 + 哨兵）
- **GetObject/HeadObject**：先检查 delete marker 哨兵，再委托 inner
- **GetObjectVersion/HeadObjectVersion**：查找归档版本 → 归档 delete marker → 当前版本
- **DeleteObjectVersion**：永久删除特定版本，如果是当前 delete marker 则提升前一版本
- **ListObjectVersions**：遍历 `.versions/` + 当前对象，路径解析确定版本类型，排序分页
- **MultipartStorage 委托**：直接委托给 inner，版本化 bucket 的 multipart 不创建版本

### 关键设计

- **safeKey 编码**：`/` → `%2F`（避免 `_SLASH_` 碰撞风险）
- **Delete marker**：零字节 + `.dm-{versionId}` 路径前缀 + `.current-delete-marker` 哨兵
- **UUID v4 版本 ID**：`crypto/rand` 生成
- **Bucket 级配置**：`.bucket-meta` 对象存储版本控制状态

### 版本控制 HTTP 端点

- `PUT /{bucket}?versioning` — 启用/暂停版本控制
- `GET /{bucket}?versioning` — 查询版本控制状态
- `GET /{bucket}/{key}?versionId=X` — 获取特定版本
- `HEAD /{bucket}/{key}?versionId=X` — 获取特定版本元数据
- `DELETE /{bucket}/{key}?versionId=X` — 永久删除特定版本
- `GET /{bucket}?versions` — 列出所有版本

## 修改文件

| 文件 | 类型 | 说明 |
|------|------|------|
| `src/storage/versioning.go` | 新增 | VersionedBackend 装饰器（~700 行） |
| `src/storage/backend.go` | 修改 | 新增 VersionedStorage 接口 + VersionEntry 类型 |
| `src/handler/versioning.go` | 新增 | VersioningManager HTTP handler |
| `src/handler/object.go` | 修改 | GetObject/HeadObject/DeleteObject 支持 versionId |
| `src/handler/router.go` | 修改 | `?versioning`/`?versions` 路由分发 |
| `src/s3error/error.go` | 修改 | 新增 3 个版本控制错误码 |
| `src/service/metadata.go` | 修改 | ObjectMeta 新增 3 个版本控制字段 |
| `cmd/server/main.go` | 修改 | VersionedBackend 包装 |
| `test/phase15.go` | 新增 | 集成测试（47 个） |

## 测试覆盖

47 个集成测试，覆盖 8 个场景：
1. 未版本化 bucket 向后兼容
2. 启用版本控制 + 多版本 PutObject
3. Delete marker 创建和访问
4. DeleteObjectVersion 永久删除
5. PutBucketVersioning/GetBucketVersioning 状态转换
6. HeadObject with versionId
7. ListObjectVersions 列表验证
8. ListObjectVersions with delete marker

## 回归测试

全量回归通过（Phase 1-15），未启用版本控制的 bucket 行为完全不变。

## 技术文档

- [versioning.md](technical/versioning.md) — 对象版本控制技术设计
