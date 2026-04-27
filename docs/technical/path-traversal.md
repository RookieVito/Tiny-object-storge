<!-- tags: security, path-validation -->
# 路径遍历防护

## 概述

路径遍历（Path Traversal）是对象存储最基本的安全威胁：攻击者试图通过特殊构造的 key
（如 `../../etc/passwd`）读写存储根目录之外的文件。本项目采用三层纵深防御（defense in depth）
来阻止路径遍历。

## 1. 攻击向量

```
正常请求: PUT /mybucket/photos/cat.jpg
攻击请求: PUT /mybucket/../../../etc/passwd
```

如果防护不到位，攻击者可以：
- 读取服务器上的任意文件
- 覆写服务器上的关键文件（如 `/etc/shadow`）
- 越权访问其他 bucket 的数据

## 2. 三层防御体系

### 第 1 层：Go net/http ServeMux 路径清理

Go 的 HTTP 路由器在匹配前会自动对 URL 路径执行 `cleanPath`（等价于 `filepath.Clean`）：

```
请求路径: /mybucket/../../../etc/passwd
清理后:   /etc/passwd
```

Go ServeMux 将 `../../etc/passwd` 规范化为 `/etc/passwd`，这意味着：
- 该请求会匹配 `PUT /{bucket}`（bucket="etc"），而不是 `PUT /{bucket}/{key...}`
- **攻击者无法通过 URL 在 bucket 和 key 之间注入 `..`**

**局限性**：这层防护由 Go 框架自动完成，不可禁用。但它只处理 URL 中
字面形式的 `..`，不能作为唯一的安全依赖。

### 第 2 层：PathMapper 子串拒绝

在 `LocalBackend`（实现 `StorageBackend` 接口）中，通过 PathMapper.ObjectPath() 显式检查 key 是否包含 `..`：

```go
if key == "" || strings.Contains(key, "..") {
    return "", ErrInvalidKey
}
```

- 即使请求以某种方式绕过了 ServeMux 的 clean，key 中的 `..` 会被直接拒绝
- 返回 400 Bad Request + `InvalidKey` 错误

### 第 3 层：路径前缀验证

即使前两层都被绕过，还有最后一道防线：

```go
joined := filepath.Join(bucketPath, key)
cleaned := filepath.Clean(joined)

prefix := bucketPath + string(filepath.Separator)
if !strings.HasPrefix(cleaned, prefix) && cleaned != bucketPath {
    return "", ErrInvalidKey
}
```

验证步骤：
1. `filepath.Join(root, bucket, key)`：拼接路径
2. `filepath.Clean()`：规范化（解析 `..`、去除多余 `/`、`.` 等）
3. 检查结果是否以 bucket 目录路径为前缀

**为什么需要这层**：理论上不可能被绕过，但作为 defense in depth，
即使前两层存在未知的边界情况，这层仍然能保证安全。

## 3. 三层防御的具体场景分析

| 攻击方式 | 第 1 层 (ServeMux) | 第 2 层 (reject) | 第 3 层 (prefix) | 结果 |
|---------|----------------|----------------|----------------|------|
| `/b/../../etc/passwd` | 清理为 `/etc/passwd` | 不触发（key 中无 `..`） | N/A | 创建 `etc` bucket |
| URL 编码 `%2e%2e` | Go HTTP 库解码为 `..`，清理 | 触发拒绝 | N/A | 返回 400 |
| 双重编码 `%252e` | 不解码，存为字面路径 `..` | 触发拒绝 | N/A | 返回 400 |
| Unicode 替代 `..` (如 `\u002e\u002e`) | 不等于 `..`，不清理 | 不触发 | N/A | 取决于路径 |
| 嵌套 bucket: `/b/../../other/key` | 清理为 `/other/key` | 不触发 | 验证前缀 | key 不在 bucket 内 |

### 3.1 关于第一种情况的说明

`/b/../../etc/passwd` 被 ServeMux 清理后变为 `/etc/passwd`，匹配 `PUT /{bucket}`。
这不是安全漏洞——`/etc/passwd` 被当作 bucket 名，数据仍然存储在 `{root}/etc/passwd/` 下，
**没有逃出存储根目录**。PathMapper 的 bucket 名称校验（正则 `[a-z0-9.\-]`）会拒绝
"etc" 这个非法 bucket 名，但由于请求路由在 PathMapper 之前，CreateBucket handler 会被调用。

**实际行为**：ServeMux 清理后路由到 `PUT /{bucket}` → `bucket="etc"` →
LocalBackend 中 PathMapper.BucketPath 验证名称 → 正则不匹配 → 返回 `InvalidBucketName`（400）。

## 4. Bucket 名称校验

LocalBackend 通过 PathMapper 对 bucket 名称进行正则校验：

```go
var bucketNameRe = regexp.MustCompile(
    `^[a-z0-9][a-z0-9.\-]{1,61}[a-z0-9]$|^([a-z0-9][a-z0-9.\-]{0,61}[a-z0-9])$`)
```

规则：
- 只允许小写字母 `a-z`、数字 `0-9`、点号 `.`、连字符 `-`
- 长度 3~63 个字符
- 以字母或数字开头和结尾

这层校验确保 bucket 名称不包含任何特殊字符，从根源上杜绝了名称注入。

> **Phase 4 架构变更**：PathMapper 不再被 handler 层直接调用，而是被封装在 `LocalBackend` 内部（实现 `StorageBackend` 接口）。三层防御逻辑完全保留，只是调用路径从 `handler → pathmapper` 变为 `handler → StorageBackend → LocalBackend → pathmapper`。

## 5. 防御纵深原则

每一层防护都独立工作，不依赖其他层：

- 第 1 层失效 → 第 2 层仍然有效
- 第 2 层失效 → 第 3 层仍然有效
- 三层同时失效 → 理论上不可能（Go 框架 + 显式检查 + 数学验证）

这种设计 philosophy 是安全系统的基本原则：**永远不要假设单一防线足够**。

## 对应实现

| 文件 | 说明 |
|------|------|
| `src/pathmapper/pathmapper.go` | PathMapper 路径映射与安全校验 |

**关键类型：** `PathMapper`
**关键函数：** `NewPathMapper()`、`BucketPath()`、`ObjectPath()`、`MetaPath()`
