# S3 协议基础

## 概述

Amazon S3 (Simple Storage Service) 定义了一套基于 HTTP REST 的对象存储 API。
本项目实现了 S3 协议的一个兼容子集，使用户可以用 aws-cli、s3cmd 等标准客户端直接访问。

## 1. 核心概念

### 1.1 Bucket（桶）

Bucket 是对象的顶层容器，类似文件系统中的根目录。

**命名规则**（本项目简化实现）：

```
^[a-z0-9][a-z0-9.\-]{0,61}[a-z0-9]$
```

- 长度 3~63 个字符
- 只允许小写字母、数字、点号、连字符
- 必须以字母或数字开头和结尾
- 不能包含 `..`（路径遍历风险）

本项目实现：每个 Bucket 对应文件系统上的一个目录。

### 1.2 Object（对象）

Object 是存储的基本单元，由三部分组成：Key、Data、Metadata。

- **Key**：对象的唯一标识符，类似文件路径。支持 `/` 分隔的嵌套结构（如 `photos/2024/cat.jpg`），但不是真正的目录——`photos/` 和 `photos` 是完全不同的两个 key。
- **Data**：对象的实际内容（字节流），本项目存为普通文件。
- **Metadata**：对象的描述信息，本项目存为 `.meta` JSON 侧边文件。

### 1.3 ETag

ETag（Entity Tag）是对象的版本标识符，用于缓存控制和条件请求。

本项目采用 **MD5 哈希**生成：

```
ETag = "<md5_hex_string>"
```

- `md5.Sum(body)` 计算请求体的 MD5（16 字节）
- 转为 32 位小写十六进制字符串
- 外层加双引号，这是 S3 单部分对象的规范格式

示例：`"65a8e27d8879283831b664bd8b7f0ad4"`

## 2. API 操作

### 2.1 本项目实现的 9 个 S3 操作

#### Bucket 操作

| 操作 | 方法 | 路径 | 语义 |
|------|------|------|------|
| CreateBucket | `PUT` | `/{bucket}` | 创建桶，已存在返回 409 |
| DeleteBucket | `DELETE` | `/{bucket}` | 删除空桶，非空返回 409 |
| HeadBucket | `HEAD` | `/{bucket}` | 检查桶是否存在，不返回 body |
| ListBuckets | `GET` | `/` | 列出所有桶 |

#### Object 操作

| 操作 | 方法 | 路径 | 语义 |
|------|------|------|------|
| PutObject | `PUT` | `/{bucket}/{key...}` | 上传对象，返回 ETag |
| GetObject | `GET` | `/{bucket}/{key...}` | 下载对象，不存在返回 404 |
| HeadObject | `HEAD` | `/{bucket}/{key...}` | 获取元数据，不返回 body |
| DeleteObject | `DELETE` | `/{bucket}/{key...}` | 删除对象，**幂等**（不存在也返回 204） |
| ListObjectsV2 | `GET` | `/{bucket}?list-type=2` | 列举对象，详见 [ListObjectsV2 文档](list-objects-v2.md) |

### 2.2 S3 幂等性语义

S3 协议中 DeleteObject 是幂等的：
- 第一次删除：返回 204 No Content
- 对同一个 key 再次删除：仍然返回 204（不是 404）

这与 GetObject 不同——Get 不存在的 key 返回 404 NoSuchKey。

## 3. 错误响应格式

S3 错误响应使用 XML 格式：

```xml
<?xml version="1.0" encoding="UTF-8"?>
<Error>
  <Code>NoSuchKey</Code>
  <Message>The specified key does not exist.</Message>
  <Resource>/mybucket/hello.txt</Resource>
  <RequestId>tiny-req-id</RequestId>
</Error>
```

### 3.1 本项目定义的 S3 错误码

| 错误码 | HTTP 状态码 | 含义 |
|--------|-----------|------|
| `NoSuchBucket` | 404 | Bucket 不存在 |
| `NoSuchKey` | 404 | Object 不存在 |
| `BucketAlreadyExists` | 409 | Bucket 已存在 |
| `BucketNotEmpty` | 409 | Bucket 非空，无法删除 |
| `InvalidBucketName` | 400 | Bucket 名称不合法 |
| `InvalidKey` | 400 | Key 不合法 |
| `AccessDenied` | 403 | 无认证信息或 AccessKey 不匹配 |
| `SignatureDoesNotMatch` | 403 | 签名验证失败 |

### 3.2 实现方式

本项目通过 `S3APIError` 类型统一错误处理：

```go
type S3APIError struct {
    Code    string   // S3 错误码
    Message string   // 人类可读描述
    Status  int      // HTTP 状态码
}
```

- 实现 `error` 接口，可以在函数返回值中传递
- 在 HTTP 层边界通过 `writeS3Err()` 序列化为 XML 响应
- Handler 函数只需 `return ErrNoSuchKey`，不需要关心 XML 序列化细节
