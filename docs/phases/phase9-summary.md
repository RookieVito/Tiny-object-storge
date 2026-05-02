<!-- tags: phase-summary -->
# Phase 9 完成总结

## 1. 完成状态：全部完成

Phase 9 新增 0 个文件，修改 4 个文件，新增 60 个集成测试全部通过。

## 2. Phase 9 实现内容

### 2.1 Range 请求解析

在 `src/handler/helpers.go` 中新增 `parseRangeHeader` 函数，支持三种 Range 格式：

- `bytes=start-end` — 显式范围（两端闭区间，超出自动截断到文件末尾）
- `bytes=start-` — 从 start 到文件末尾的开放范围
- `bytes=-suffix` — 最后 suffix 字节的后缀范围（超出文件大小时从开头开始）

无效格式（如 `bytes=abc`）返回 nil ranges（调用方回退 200 全量返回），语法正确但无法满足的 range 返回 `invalid=true`（调用方返回 416）。多 range 请求回退到 200 全量返回。

### 2.2 GetObject 206 响应

修改 `src/handler/object.go` 的 `GetObject` 方法：

- 检测 `Range` 请求头，存在时调用 `parseRangeHeader` 解析
- 单 range 且有效 → 返回 `206 Partial Content` + `Content-Range` + `Content-Length` + `ETag` + `Last-Modified`
- 无法满足 → 返回 `416 Range Not Satisfiable` + `Content-Range: bytes */size`
- 无 Range 或无效/多 range → 回退到 200 全量返回
- 提取公共逻辑到 `writeFullObject` 辅助函数

### 2.3 HeadObject 206 响应

修改 `HeadObject` 方法，支持相同的 Range 解析逻辑：

- 有效单 range → 206 + `Content-Range` + `Content-Length`（无 body）
- 无法满足 → 416
- 无 Range → 200 + `Content-Length`

### 2.4 Accept-Ranges 头

所有 GetObject、HeadObject 响应（200 和 206）均添加 `Accept-Ranges: bytes` 头，客户端可据此判断服务器支持 Range 请求。

### 2.5 S3 错误码

在 `src/s3error/error.go` 新增 `ErrInvalidRange`（HTTP 416）。

### 2.6 测试辅助函数

在 `test/helper.go` 新增 `DoWithHeaders` 函数，支持发送带认证和自定义 header 的请求，返回 status、body、response headers。

## 3. 依赖关系

```
src/handler/helpers.go (parseRangeHeader, contentRangeValue)
    ← src/handler/object.go (GetObject, HeadObject)

src/s3error/error.go (ErrInvalidRange)
    ← src/handler/object.go (GetObject, HeadObject)

test/helper.go (DoWithHeaders)
    ← test/phase9.go
```

## 4. 测试覆盖

**Phase 9 集成测试：60 个**

- GET 无 Range → 200 全量返回 + Accept-Ranges 头
- `bytes=0-4` → 206，Content-Range、Content-Length、ETag、Last-Modified 验证
- `bytes=10-19` → 206，中间范围切片
- `bytes=30-` → 206，开放范围到末尾
- `bytes=-5` → 206，后缀范围
- `bytes=-100` → 206，后缀超出文件大小从开头开始
- `bytes=0-0` → 206，单字节范围
- `bytes=35-35` → 206，最后单字节
- `bytes=100-200` → 416，超出范围
- `bytes=50-49` → 416，inverted range
- `bytes=36-` → 416，start == size
- `bytes=abc` → 200 fallback，无效格式
- 多 range → 200 fallback
- `bytes=0-35` → 206，恰好等于文件大小
- HEAD Range → 206 + 正确 headers + 无 body
- HEAD Range invalid → 416
- HEAD 无 Range → 200 + Accept-Ranges
- 不存在的 key + Range → 404
- 10KB 大对象 Range 测试
- 大对象后缀范围测试

## 5. 文件清单

| 文件 | 行数 | 功能 |
|------|------|------|
| `src/handler/object.go` | 200 | GetObject/HeadObject 支持 Range 请求，206/416 响应 |
| `src/handler/helpers.go` | 109 | parseRangeHeader、byteRange、contentRangeValue |
| `src/s3error/error.go` | 72 | 新增 ErrInvalidRange（416） |
| `test/phase9.go` | 160 | Phase 9 集成测试（60 个） |
| `test/helper.go` | 169 | 新增 DoWithHeaders 辅助函数 |

## 6. 设计决策

- **Handler 层实现**：Range 解析在 handler 层完成，不修改 StorageBackend 接口。GetObject 已经返回完整 `[]byte`，直接内存切片即可。
- **单 range only**：多 range 请求回退到 200 全量返回，与 S3/MinIO 对无法满足的多 range 行为一致，避免实现 multipart/byteranges 的复杂性。
- **Accept-Ranges 声明**：所有响应均携带，便于客户端探测服务器能力。
