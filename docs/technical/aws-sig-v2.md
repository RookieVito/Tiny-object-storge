<!-- tags: authentication, sig-v2, sig-v4, presign, security, hmac -->
# AWS 签名认证（Sig V2 + V4 + Presigned URL）

## 概述

AWS Signature V2 是 S3 API 的请求认证机制，确保请求确实由持有合法凭证的客户端发出。
本项目实现了 Sig V2 的核心子集，使用 HMAC-SHA1 签名。

## 1. 认证流程

```
客户端                                    服务端
  │                                        │
  ├─ 1. 构造 StringToSign                 │
  ├─ 2. HMAC-SHA1(SecretKey, STS) → Base64  │
  ├─ 3. 发送 Authorization 头              │
  │    Authorization: AWS {AK}:{Sig}       │
  │                                        ├─ 4. 解析 Authorization 头
  │                                        ├─ 5. 校验 AccessKey
  │                                        ├─ 6. 用相同算法重算签名
  │                                        ├─ 7. 比较签名
  │                                        └─ 8. 返回结果
```

## 2. Authorization 头格式

```
Authorization: AWS {AccessKey}:{Signature}
```

- `AWS`：固定前缀，标识 Sig V2 协议
- `AccessKey`：明文的访问密钥
- `Signature`：Base64 编码的 HMAC-SHA1 签名

示例：
```
Authorization: AWS minioadmin:JBmHPjiovatUtj/ZzagYxntWWN0=
```

## 3. StringToSign 构造

StringToSign 由 5 个字段用换行符 `\n` 拼接而成：

```
HTTP-VERB
Content-MD5
Content-Type
Date
CanonicalizedResource
```

各字段的取值规则：

| 字段 | 说明 | 本项目取值 |
|------|------|-----------|
| HTTP-VERB | HTTP 方法（大写） | `PUT`, `GET`, `DELETE`, `HEAD` |
| Content-MD5 | 请求体的 MD5 哈希（Base64） | 客户端通常不发送，为空字符串 |
| Content-Type | 请求体的 MIME 类型 | 客户端发送什么就用什么，未发送为空 |
| Date | 请求时间（RFC 1123 格式） | 客户端必须设置此头 |
| CanonicalizedResource | 规范化的资源路径 | `/{bucket}` 或 `/{bucket}/{key}` |

### CanonicalizedResource

CanonicalizedResource 标识请求操作的目标资源：

- Bucket 操作：`/{bucket}`（如 `/mybucket`）
- Object 操作：`/{bucket}/{key}`（如 `/mybucket/hello.txt`）
- 注意：query string 不包含在 CanonicalizedResource 中（MVP 简化）

### 示例

上传对象 `hello.txt` 到 `mybucket`：

```
PUT
                            ← Content-MD5 为空
application/octet-stream      ← Content-Type
Mon, 01 Jan 2024 00:00:00 GMT  ← Date
/mybucket/hello.txt           ← CanonicalizedResource
```

## 4. 签名计算

```python
import hmac, hashlib, base64

secret_key = b"minioadmin"
string_to_sign = "PUT\n\napplication/octet-stream\nMon, 01 Jan 2024 00:00:00 GMT\n/mybucket/hello.txt"

signature = base64.b64encode(
    hmac.new(secret_key, string_to_sign.encode(), hashlib.sha1).digest()
).decode()
```

Go 实现等价代码：

```go
mac := hmac.New(sha1.New, []byte(secretKey))
mac.Write([]byte(stringToSign))
signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))
```

## 5. 签名验证中的安全注意事项

### 5.1 常量时间比较

本项目使用 `hmac.Equal()` 而非 `==` 进行签名比较：

```go
hmac.Equal([]byte(expected), []byte(provided))
```

**为什么**：字符串逐字节比较的时间会泄露签名长度信息（timing attack）。
`hmac.Equal` 无论内容是否匹配，耗时恒定。

### 5.2 curl 的 Content-Type 陷阱

curl 使用 `-d` 参数发送数据时，会自动添加 `Content-Type: application/x-www-form-urlencoded` 头。
如果签名计算时没有包含这个 Content-Type，验证会失败。

```bash
# 错误：签名未包含 curl 自动添加的 Content-Type
curl -X PUT -d "hello" -H "Authorization: ..." http://server/bucket/key

# 正确：签名中包含实际的 Content-Type
curl -X PUT -d "hello" -H "Content-Type: application/x-www-form-urlencoded" -H "Authorization: ..." http://server/bucket/key
```

## 6. 认证体系演进

本项目已实现三层认证机制：

| 认证方式 | 文件 | 说明 |
|----------|------|------|
| Sig V2 | `src/auth/auth.go` | `Authorization: AWS {AK}:{Sig}`，HMAC-SHA1 |
| Sig V4 | `src/auth/v4.go` | `Authorization: AWS4-HMAC-SHA256 ...`，HMAC-SHA256 |
| Presigned URL | `src/auth/presign.go` | `X-Amz-Algorithm=AWS4-HMAC-SHA256` query params |

认证分发逻辑（`Authenticate()`）：
- Authorization 头以 `AWS4-HMAC-SHA256` 开头 → V4 认证
- Authorization 头以 `AWS ` 开头 → V2 认证
- 无 Authorization 头但 query 中有 `X-Amz-Algorithm` → Presigned URL 认证

### V4 签名要点（详见 `src/auth/v4.go`）

- Signing Key 派生：`HMAC(HMAC(HMAC(HMAC("AWS4"+SecretKey, DateStamp), Region), "s3"), "aws4_request")`
- String to Sign = `Algorithm + "\n" + AmzDate + "\n" + Scope + "\n" + SHA256(CanonicalRequest)`
- 时间偏移检查：`|X-Amz-Date - server time| > 15min` → 403

### Presigned URL 要点（详见 `src/auth/presign.go`）

- 签名嵌入 query params：`X-Amz-Algorithm`、`X-Amz-Credential`、`X-Amz-Signature`、`X-Amz-Date`、`X-Amz-Expires`、`X-Amz-SignedHeaders`
- 最大有效期 7 天（604800 秒）
- 支持 GET/PUT 方法

### 通用约束

- Content-MD5 字段为空（客户端未发送时）
- 单对固定凭据，从配置文件读取
- ListBuckets (`GET /`) 不需要认证（便于开发调试）

## 对应实现

| 文件 | 说明 |
|------|------|
| `src/auth/auth.go` | Authenticator 认证分发（V4/V2/Presign） |
| `src/auth/v4.go` | Sig V4 HMAC-SHA256 签名计算与验证 |
| `src/auth/presign.go` | Presigned URL 生成与验证 |

**关键类型：** `Authenticator`
**关键函数：** `NewAuthenticatorWithRegion()`、`Authenticate()`、`authenticatePresigned()`、`PresignV4()`
