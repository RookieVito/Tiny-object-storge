<!-- tags: api, s3, listing, pagination -->
# ListObjectsV2 实现原理

## 概述

ListObjectsV2 是 S3 中最复杂的列举接口。它不是简单的目录列表——而是一个支持
前缀过滤、分隔符分组、分页的强大查询工具。本项目在 `cmd/server/bucket.go` 中完整实现了该接口。

## 1. 接口定义

### 请求

```
GET /{bucket}?list-type=2&prefix=X&delimiter=D&max-keys=N&continuation-token=T&start-after=S
```

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `list-type` | string | 无 | 设为 `2` 启用 V2 格式（本项目默认行为） |
| `prefix` | string | `""` | 只返回以此前缀开头的 key |
| `delimiter` | string | `""` | 分组分隔符，通常为 `/` |
| `max-keys` | int | `1000` | 单次返回的最大条目数 |
| `continuation-token` | string | `""` | 上一页返回的分页令牌 |
| `start-after` | string | `""` | 从此 key 之后开始列举 |

### 响应 XML

```xml
<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Name>mybucket</Name>
  <Prefix>photos/</Prefix>
  <KeyCount>3</KeyCount>
  <MaxKeys>1000</MaxKeys>
  <Delimiter>/</Delimiter>
  <IsTruncated>false</IsTruncated>
  <NextContinuationToken></NextContinuationToken>
  <Contents>
    <Key>photos/cat.jpg</Key>
    <LastModified>2024-01-15T10:30:00.000Z</LastModified>
    <ETag>"d41d8cd98f00b204e9800998ecf8427e"</ETag>
    <Size>102400</Size>
    <StorageClass>STANDARD</StorageClass>
  </Contents>
  <CommonPrefixes>
    <Prefix>photos/2024/</Prefix>
  </CommonPrefixes>
</ListBucketResult>
```

## 2. 核心算法：Delimiter 分组

Delimiter 是 ListObjectsV2 最关键也最复杂的特性。它将共享前缀的 key 聚合为"公共前缀"，
模拟类似目录浏览的行为。

### 2.1 算法步骤

对于每个经过 prefix 过滤的 key：

1. 截取 prefix 之后的剩余部分：`remainder = key[len(prefix):]`
2. 在 remainder 中查找 delimiter 的位置：`delimIdx = strings.Index(remainder, delimiter)`
3. **未找到 delimiter**（`delimIdx < 0`）：该 key 是一个普通条目，加入 `Contents[]`
4. **找到 delimiter**：提取公共前缀 `prefix + remainder[:delimIdx + len(delimiter)]`
   - 去重后加入 `CommonPrefixes[]`
   - 已见过的公共前缀不重复计数

### 2.2 完整示例

假设 bucket 中有以下 key：

```
a/b/c
a/b/d
a/e
f
photos/2024/cat.jpg
photos/2024/dog.jpg
photos/2024/bird.png
```

**请求 1**：`prefix="" delimiter="/"`

处理过程：

| Key | remainder | delimiter 位置 | 结果 |
|-----|-----------|--------------|------|
| `a/b/c` | `a/b/c` | 1（`a/` 后） | CommonPrefix: `a/`（新增） |
| `a/b/d` | `a/b/d` | 1 | CommonPrefix: `a/`（已见，跳过） |
| `a/e` | `a/e` | 1 | CommonPrefix: `a/`（已见，跳过） |
| `f` | `f` | -1 | Contents: `{Key: "f"}` |
| `photos/2024/cat.jpg` | `photos/2024/cat.jpg` | 6 | CommonPrefix: `photos/`（新增） |
| `photos/2024/dog.jpg` | `photos/2024/dog.jpg` | 6 | CommonPrefix: `photos/`（已见，跳过） |
| `photos/2024/bird.png` | `photos/2024/bird.png` | 6 | CommonPrefix: `photos/`（已见，跳过） |

结果：
- `Contents`: `[{Key: "f"}]`
- `CommonPrefixes`: `[{Prefix: "a/"}, {Prefix: "photos/"}]`
- `KeyCount`: 3

**请求 2**：`prefix="a/" delimiter="/"`

处理过程：

| Key | remainder | delimiter 位置 | 结果 |
|-----|-----------|--------------|------|
| `a/b/c` | `b/c` | 1 | CommonPrefix: `a/b/`（新增） |
| `a/b/d` | `b/d` | 1 | CommonPrefix: `a/b/`（已见，跳过） |
| `a/e` | `e` | -1 | Contents: `{Key: "a/e"}` |

结果：
- `Contents`: `[{Key: "a/e"}]`
- `CommonPrefixes`: `[{Prefix: "a/b/"}]`
- `KeyCount`: 2

## 3. 分页机制

### 3.1 Continuation Token

当结果被 `max-keys` 截断时，响应包含：
- `IsTruncated`: `true`
- `NextContinuationToken`: Base64 编码的"本页最后一个条目的 key"

本项目的 token 生成方式：

```go
nextContinuationToken = base64.StdEncoding.EncodeToString([]byte(lastKey))
```

### 3.2 客户端翻页流程

```
第一页：
  请求: GET /bucket?max-keys=2
  响应: Contents[key1, key2], IsTruncated=true, NextContinuationToken="a2V5"

第二页：
  请求: GET /bucket?max-keys=2&continuation-token=a2V5
  服务端解码得到 startAfter="key2"，跳过已返回的条目
  响应: Contents[key3, key4], IsTruncated=false
```

### 3.3 start-after 跳过已见条目的实现

```go
idx := sort.Search(len(filtered), func(i int) bool {
    return filtered[i].key > startAfter
})
filtered = filtered[idx:]
```

`sort.Search` 返回第一个 `key > startAfter` 的索引，直接切片跳过。

## 4. 本项目实现流程

```
1. filepath.WalkDir(bucketPath)
   ├─ 跳过 bucket 根目录自身
   ├─ 跳过所有子目录（只处理文件）
   └─ 跳过 .meta 侧边文件

2. 从文件路径推导 S3 key
   filepath.Rel(bucketPath, path) → filepath.ToSlash(rel)

3. 读取每个 .meta 文件获取 size/etag/last-modified

4. 字典序排序所有 key

5. 应用 prefix 过滤

6. 解码 continuation-token → startAfter → 跳过已返回条目

7. 应用 delimiter 分组，收集到 max-keys 条目

8. 构造 XML 响应（含 IsTruncated + NextContinuationToken）
```

## 5. 性能考量

- 每次列举都需要遍历整个 bucket 目录并读取所有 .meta 文件
- 对于大 bucket（数万对象），这会产生大量磁盘 I/O
- 未来优化方向：
  - 内存缓存元数据索引
  - 将元数据合并存储（单个 bucket 索引文件）
  - 支持 OS 级 `getdents` 批量读取目录项
