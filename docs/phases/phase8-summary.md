<!-- tags: phase-summary -->
# Phase 8 完成总结

## 1. 完成状态：全部完成

Phase 8 新增 3 个文件（multipart.go、multipart.go、phase8.go），修改 8 个文件（backend.go、error.go、local.go、ec.go、distributed.go、router.go、metrics.go、CLAUDE.md），新增 32 个集成测试全部通过，Phase 1-8 全量回归无新增失败。

---

## 2. Phase 8 实现内容

### 2.1 MultipartStorage 接口（storage/backend.go）

独立于 `StorageBackend` 的可选扩展接口，后端通过 type assertion 检测支持：

- `InitiateUpload` — 创建 multipart upload，返回 UploadId
- `UploadPart` — 上传单个 part 数据，返回 ETag
- `CompleteUpload` — 合并所有 part 为最终对象，返回 ETag
- `AbortUpload` — 取消并清理 upload 数据
- `ListParts` — 列出指定 upload 的已上传 part
- `ListUploads` — 列出 bucket 中进行中的 upload
- `GetUploadInfo` — 读取 upload 元数据

新增数据类型：

- `PartInfo` — PartNumber、Size、ETag（quoted MD5）、LastModified
- `UploadInfo` — UploadId、Bucket、Key、ContentType、UserMetadata、Initiated

### 2.2 LocalBackend 实现（storage/multipart.go）

在 `*LocalBackend` 上完整实现 `MultipartStorage` 接口：

- **UploadId**：UUID v4，`crypto/rand` 生成
- **InitiateUpload**：验证 bucket 存在 → 生成 uploadId → 创建目录 → 原子写入 `info.json`
- **UploadPart**：验证 upload 存在 → 验证 partNumber 1-10000 → `service.WriteFile` 原子写入 → 计算 MD5 ETag
- **CompleteUpload**：验证 parts 存在且 ETag 匹配 → 验证非最后 part ≥ 5MB → 拼接到临时文件 → 原子 rename → 清理 upload 目录
- **AbortUpload**：删除 `.uploads/{uploadId}` 目录
- **ListParts**：扫描 upload 目录下 `part-*.bin.meta` 文件，按 PartNumber 排序
- **ListUploads**：扫描 `.uploads/` 子目录，prefix 过滤 + keyMarker 分页
- **GetUploadInfo**：读取 `info.json`，验证 bucket/key 匹配

### 2.3 MultipartManager（handler/multipart.go）

6 个 S3 multipart HTTP 端点：

| 端点 | HTTP | Query Param | 描述 |
|------|------|-------------|------|
| InitiateMultipartUpload | POST | `?uploads` | 创建 upload，返回 UploadId |
| UploadPart | PUT | `?partNumber=N&uploadId=X` | 上传 part，返回 ETag header |
| CompleteMultipartUpload | POST | `?uploadId=X` | 合并 parts，XML 请求体 |
| AbortMultipartUpload | DELETE | `?uploadId=X` | 取消 upload |
| ListParts | GET | `?uploadId=X` | 列出已上传 parts |
| ListMultipartUploads | GET | `?uploads` | 列出进行中的 uploads |

### 2.4 路由分发（handler/router.go）

通过 query param 分发，复用现有路由模式：

```
POST /{bucket}/{key...}  →  ?uploads → Initiate / ?uploadId → Complete
PUT  /{bucket}/{key...}  →  ?uploadId → UploadPart / else → PutObject
GET  /{bucket}/{key...}  →  ?uploadId → ListParts / else → GetObject
DELETE /{bucket}/{key...} → ?uploadId → Abort / else → DeleteObject
GET  /{bucket}           →  ?uploads → ListUploads / else → ListObjects
```

### 2.5 ETag 算法

- **单 part ETag**：`fmt.Sprintf("\"%x\"", md5.Sum(partData))` — 标准 quoted MD5 hex
- **最终对象 ETag**：`fmt.Sprintf("\"%x-%d\"", md5.Sum(concatMD5s), partCount)` — S3 标准格式，`concatMD5s` 为各 part 的 16 字节 MD5 摘要拼接

### 2.6 并发模型

| 操作 | 加锁 | 原因 |
|------|------|------|
| InitiateUpload | 不加锁 | 仅创建新目录 |
| UploadPart | 不加锁 | 不同 part 可并发上传，同一 part 覆盖通过原子 rename 保证安全 |
| CompleteUpload | 加 bucket 锁 | 写入最终对象，需与 PutObject 互斥 |
| AbortUpload | 加 bucket 锁 | 防止与 Complete 并发 |
| ListParts / ListUploads | 不加锁 | 只读操作 |

### 2.7 磁盘布局

```
{root}/{bucket}/.uploads/{uploadId}/
    info.json                    # UploadInfo JSON
    part-0001.bin                # Part 数据
    part-0001.bin.meta           # PartInfo JSON
    part-0002.bin
    part-0002.bin.meta
```

`.uploads/` 目录通过以下方式与正常对象隔离：
- `ListObjects`（local.go）跳过 `.uploads/` 目录（`filepath.SkipDir`）
- `Metrics`（metrics.go）扫描跳过 `.uploads/` 目录

### 2.8 EC/Distributed 后端

ECBackend 和 DistributedBackend 添加了 `MultipartStorage` 的 stub 方法，全部返回 `ErrNotImplemented`（501），接口已预留供后续实现。

### 2.9 新增 S3 错误码

| 错误码 | HTTP Status | 描述 |
|--------|-------------|------|
| NoSuchUpload | 404 | 指定的 upload 不存在 |
| InvalidPart | 400 | Part 不存在或 ETag 不匹配 |
| EntityTooSmall | 400 | 非 final part 小于 5MB |
| InvalidPartOrder | 400 | Parts 未按编号升序 |
| InvalidPartNumber | 400 | PartNumber 不在 1-10000 |
| NotImplemented | 501 | 后端不支持 multipart |

---

## 3. 依赖关系

```
storage/multipart.go  ← 新增文件，依赖 storage (LocalBackend)、service、s3error
handler/multipart.go  ← 新增文件，依赖 storage (MultipartStorage)、s3error、locks
```

依赖图保持无环。`MultipartStorage` 作为可选接口不影响现有 `StorageBackend` 依赖链。

---

## 4. 测试覆盖

**Phase 8 集成测试（test/phase8.go）：32 个**

- InitiateMultipartUpload 返回 200 + UploadId
- UploadPart 返回 200 + ETag header（3 个 part）
- ListParts 返回 3 个 parts，大小和排序正确
- 进行中的 multipart 在 ListObjects 中不可见
- ListMultipartUploads 返回进行中的 upload
- CompleteMultipartUpload 返回 200 + 最终 ETag（`hash-N` 格式）
- 验证最终对象 GET 读取内容匹配拼接结果
- AbortMultipartUpload 返回 204，对象不存在
- 无效 uploadId → 404
- partNumber=0 / 10001 → 400
- 非 final part < 5MB → EntityTooSmall
- 覆盖同一 partNumber 后 Complete 使用最新版本

---

## 5. 关联修复

Phase 8 开发期间同时修复了以下问题：

### 5.1 Phase 3 POST → 405 预期失败

**原因**：`topMux.Handle("GET /_metrics", m)` 使用方法限定路由，ServeMux 对 POST 请求返回 307 重定向。
**修复**：改为 `topMux.Handle("/_metrics", m)`，handler 内部判断方法返回 405。

### 5.2 分布式模式 seed_nodes 格式

**原因**：示例配置中 `seed_nodes` 包含 `http://` 前缀，导致 RPC URL 双重前缀。
**修复**：去掉前缀，README 中注明格式要求。

### 5.3 分布式 replicas() 竞态条件

**原因**：`replicas()` 使用哈希环选节点，但节点加入哈希环是异步 goroutine，存在短暂窗口返回不完整节点列表。
**修复**：`replicas()` 先通过 `membership.AliveNodes()` 过滤，确保只选可达节点。
