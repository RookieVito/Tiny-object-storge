<!-- tags: phase-summary -->
# Phase 11 完成总结

## 1. 完成状态：全部完成

Phase 11 新增 3 个文件，修改 9 个文件，新增 20 个集成测试全部通过。

### 新增文件
- `src/auth/presign.go` — Presigned URL 生成与验证
- `cmd/client/presign.go` — CLI presign 子命令
- `test/phase11.go` — Phase 11 集成测试

### 修改文件
- `src/s3error/error.go` — 新增 ErrExpiredPresign、ErrInvalidExpires
- `src/auth/auth.go` — Authenticate() 新增 Presigned URL 分发
- `src/auth/v4.go` — 移除 buildCanonicalHeaders 冗余排序
- `cmd/client/signer.go` — 新增 PresignV4() 预签名 URL 生成函数
- `cmd/client/main.go` — 注册 presign 命令
- `test/helper.go` — 新增 PresignURL()、DoPresigned()、presignURLAtTime() 辅助函数
- `src/handler/object.go` — 修复 GetObject 416 处重复 Content-Range
- `web/src/api/signer.ts` — signV4() 增加 accessKey 参数
- `web/src/api/client.ts` — 传入 accessKey，移除 regex 替换补丁

### Phase 10 遗留修复
- 修复 object.go:84 重复 Content-Range 设置
- Web UI signV4 直接接受 accessKey 参数，消除 client.ts regex 补丁
- buildCanonicalHeaders 移除冗余 sort
- 补充 V4 查询字符串签名、content-type 篾改检测、Range+V4 组合测试

---

## 2. 实现要点

### 2.1 Presigned URL 生成

`PresignV4(method, bucket, key, expires, host)` 生成预签名 URL：

1. 计算 `now`、`amzDate`、`dateStamp`、`scope`
2. 构建 canonical query string：包含 `X-Amz-Algorithm`、`X-Amz-Credential`、`X-Amz-Date`、`X-Amz-Expires`、`X-Amz-SignedHeaders`（不含 Signature）
3. Canonical headers 只签名 `host`
4. 使用与 V4 相同的密钥派生和签名算法
5. 构建完整 URL，附加 `X-Amz-Signature`

### 2.2 Presigned URL 验证

`authenticatePresigned(r, bucket, key)` 验证预签名 URL 请求：

1. 从 query params 提取签名参数
2. 验证 `X-Amz-Expires` 范围（1-604800 秒）
3. 过期检查：`server_time > X-Amz-Date + X-Amz-Expires` → 403
4. 时间偏移检查：复用 V4 的 15 分钟 skew 检查
5. 解析 Credential 验证 accessKey
6. 构建 canonical request（去掉 X-Amz-Signature 后的 query string）
7. 计算签名并比对

### 2.3 认证分发扩展

`Authenticate()` 新增第三种分发路径：
- 无 `Authorization` 头 + query 中有 `X-Amz-Algorithm=AWS4-HMAC-SHA256` → `authenticatePresigned()`
- 无 Authorization 头 + 无 presign 参数 → `ErrAccessDenied`

### 2.4 CLI presign 命令

```
tiny-storage presign <bucket/key>                  # GET, 1 小时有效
tiny-storage presign -method PUT <bucket/key>       # PUT 预签名
tiny-storage presign -expires 86400 <bucket/key>    # 24 小时有效
```

---

## 3. 测试覆盖

### Phase 11 测试（20 个）

**Presign GET：**
- GET 已有对象 → 200 + 正确内容
- GET 不存在对象 → 404
- GET bucket（ListObjects）→ 200

**Presign PUT：**
- PUT 上传对象 → 200
- 验证上传内容 → 正确

**安全性测试：**
- 过期 URL → 403
- 签名篡改 → 403
- 方法不匹配 → 403
- 最大有效期 7 天 → 200

**兼容性测试：**
- V4 header 认证不受影响
- V2 认证不受影响
- 无认证无 presign → 403

**URL 格式验证：**
- 包含 X-Amz-Algorithm
- 包含 X-Amz-Expires
- 包含 X-Amz-SignedHeaders
- 包含 X-Amz-Signature
- 包含 X-Amz-Credential
- 包含 X-Amz-Date

### Phase 10 额外测试（7 个）
- V4 GET with ?uploads query string
- V4 GET with delimiter+prefix query
- Content-Type tamper → 403
- Range + V4 → 206
- Range + V4 content
- Range + V4 Content-Range

---

## 4. 依赖关系

```
src/auth/presign.go (PresignV4, authenticatePresigned)
    ← src/auth/auth.go (Authenticate dispatch)
    ← cmd/client/signer.go (独立实现，不依赖 src/auth)

src/auth/v4.go (deriveSigningKey, getCanonicalURI, getCanonicalQueryString, hexSHA256)
    ← src/auth/presign.go (复用签名原语)

test/helper.go (PresignURL, DoPresigned, presignURLAtTime)
    ← test/phase11.go
```

---

## 5. 文件清单

| 文件 | 行数 | 功能 |
|------|------|------|
| `src/auth/presign.go` | 165 | PresignV4 生成 + authenticatePresigned 验证 |
| `src/auth/auth.go` | 95 | Authenticate() 新增 presign 分发 |
| `src/s3error/error.go` | 52 | ErrExpiredPresign、ErrInvalidExpires |
| `cmd/client/signer.go` | 223 | PresignV4() 独立预签名函数 + buildCanonicalQueryString |
| `cmd/client/presign.go` | 55 | presign 子命令 |
| `cmd/client/main.go` | 56 | 注册 presign 命令 |
| `test/phase11.go` | 170 | 20 个集成测试 |
| `test/helper.go` | 440 | PresignURL/DoPresigned/presignURLAtTime + SigV4 query string 修复 |

---

## 6. 已知限制

- Presigned URL 只签名 `host` 头，不签名其他请求头
- Payload 使用 `UNSIGNED-PAYLOAD`，PUT presign 不验证请求体完整性
- CLI 和服务端各自独立实现 presign 生成（避免跨包依赖）
- Web UI 暂未集成 presign 功能（需 Phase 12 CORS 支持）
