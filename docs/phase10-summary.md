<!-- tags: phase-summary -->
# Phase 10 完成总结

## 1. 完成状态：全部完成

Phase 10 新增 1 个文件，修改 7 个文件，新增 15 个集成测试全部通过。

### 新增文件
- `src/auth/v4.go` — AWS Sig V4 签名计算与验证

### 修改文件
- `src/auth/auth.go` — Authenticate() 按前缀分发 V4/V2，Authenticator 新增 region 字段
- `src/config/config.go` — 新增 Region 字段（默认 `us-east-1`）
- `src/s3error/error.go` — 新增 ErrRequestTimeTooSkewed、ErrMissingSecurityHeader
- `cmd/client/signer.go` — CLI 客户端升级为 V4 签名
- `web/src/api/signer.ts` — Web UI 签名器升级为 V4（Web Crypto API）
- `web/src/api/client.ts` — 使用 V4 签名头
- `test/helper.go` — 新增 SigV4/DoV4/DoV4WithHeaders 辅助函数
- `test/phase10.go` — 15 个集成测试
- `src/handler/router.go` — 使用 NewAuthenticatorWithRegion

---

## 2. 实现要点

### 2.1 Sig V4 签名流程

```
Canonical Request → String to Sign → Signing Key 派生 → HMAC-SHA256 签名
```

1. **Canonical Request**：HTTP Method + Canonical URI + Canonical Query String + Canonical Headers + Signed Headers + Payload Hash
2. **String to Sign**：Algorithm + AmzDate + Scope + SHA256(Canonical Request)
3. **Signing Key 派生**：HMAC 层叠 `AWS4{secret}` → `kDate` → `kRegion` → `kService` → `kSigning`
4. **签名**：HMAC-SHA256(kSigning, StringToSign)

### 2.2 关键设计

- **Payload**：统一使用 `UNSIGNED-PAYLOAD`（跳过 body hash 验证，简化实现）
- **时间偏移检查**：`|X-Amz-Date - server time| > 15min` → 403 RequestTimeTooSkewed
- **V4/V2 双认证**：Authenticate() 按 Authorization 头前缀分发
  - `AWS4-HMAC-SHA256` → V4 认证
  - `AWS ` → V2 认证（向后兼容）
- **Canonical Headers**：从 Authorization 头的 SignedHeaders 字段解析，只签名声明的头
- **Region**：Config 新增 Region 字段，默认 `us-east-1`

### 2.3 客户端升级

- **CLI 客户端**（cmd/client/signer.go）：
  - SignV4() 接受 method、resource、contentType、accessKey、secretKey、region、endpoint、queryString
  - SignRequest() 自动提取 host 和 query string 进行签名
  - 解决了 host 为空字符串和 query string 未签名的问题

- **Web UI**（web/src/api/signer.ts）：
  - signV4() 使用 Web Crypto API 实现 HMAC-SHA256
  - 返回 authorization、amzDate、contentSha256 三个头
  - buildAuthHeaders() 生成 V4 认证头

---

## 3. 测试覆盖

### Phase 10 测试（15 个）

**V4 基本认证：**
- V4 CreateBucket → 200
- V4 PutObject → 200
- V4 GetObject → 200 + content 验证
- V4 HeadObject → 200
- V4 DeleteObject → 204
- V4 GetObject deleted → 404

**V2 向后兼容：**
- V2 PutObject still works → 200
- V2 GetObject still works → 200 + content 验证

**认证失败测试：**
- No auth → 403
- Invalid V4 signature → 403
- Wrong V4 access key → 403
- Missing X-Amz-Date → 400

### 全量回归测试

所有 Phase 1-10 测试 + EC + Distributed + 单元测试全部通过。

---

## 4. 已知限制

- Payload 使用 `UNSIGNED-PAYLOAD`，不验证请求体完整性（S3 的 chunked upload 暂不支持）
- Sig V2 作为 fallback 保留，未来版本可考虑移除
- 签名头列表（SignedHeaders）中暂不支持重复头（multi-value headers）
