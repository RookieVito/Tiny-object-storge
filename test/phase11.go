package main

import (
	"strings"
	"time"
)

func init() {
	registerTest("Phase 11", testPhase11)
}

func testPhase11() {
	const bucket = "presign-test-bucket"
	const key = "presign-test-object"

	// 清理。
	Do2("DELETE", "/"+bucket+"/"+key, "", "")
	Do2("DELETE", "/"+bucket, "", "")

	// 准备：创建 bucket 和对象。
	Do2("PUT", "/"+bucket, "", "")
	Do2("PUT", "/"+bucket+"/"+key, "presigned content", "text/plain")

	// --- Presign GET 基本测试 ---

	presignedURL := PresignURL("GET", "/"+bucket+"/"+key, 3600)
	s, b, _ := DoPresigned("GET", presignedURL, "", "")
	Pass("Presign GET existing object", s == 200)
	Pass("Presign GET correct content", b == "presigned content")

	// 预签名 GET URL 不存在的对象。
	presignedURL = PresignURL("GET", "/"+bucket+"/nonexist", 3600)
	s, _, _ = DoPresigned("GET", presignedURL, "", "")
	Pass("Presign GET non-existing key → 404", s == 404)

	// --- Presign PUT 测试 ---

	presignedURL = PresignURL("PUT", "/"+bucket+"/presign-put-key", 3600)
	s, _, _ = DoPresigned("PUT", presignedURL, "uploaded via presign", "text/plain")
	Pass("Presign PUT object", s == 200)

	s, b, _ = DoV4("GET", "/"+bucket+"/presign-put-key", "", "")
	Pass("Presign PUT verify content", s == 200 && b == "uploaded via presign")

	// --- 过期测试 ---

	// 构造一个已过期的预签名 URL：24 小时前的时间戳 + 1 秒过期。
	expiredURL := presignURLAtTime("GET", "/"+bucket+"/"+key, 1, time.Now().Add(-24*time.Hour))
	s, _, _ = DoPresigned("GET", expiredURL, "", "")
	Pass("Expired presign → 403", s == 403)

	// --- 签名篡改测试 ---

	presignedURL = PresignURL("GET", "/"+bucket+"/"+key, 3600)
	tamperedURL := strings.Replace(presignedURL, "X-Amz-Signature=", "X-Amz-Signature=deadbeef", 1)
	s, _, _ = DoPresigned("GET", tamperedURL, "", "")
	Pass("Tampered signature → 403", s == 403)

	// --- 方法不匹配测试 ---

	presignedURL = PresignURL("GET", "/"+bucket+"/"+key, 3600)
	s, _, _ = DoPresigned("PUT", presignedURL, "wrong method", "text/plain")
	Pass("Method mismatch → 403", s == 403)

	// --- V4 header 认证不受影响 ---

	s, b, _ = DoV4("GET", "/"+bucket+"/"+key, "", "")
	Pass("V4 header auth still works after presign", s == 200 && b == "presigned content")

	s, _ = Do2("GET", "/"+bucket+"/"+key, "", "")
	Pass("V2 auth still works after presign", s == 200)

	// --- 无认证无 presign 参数 ---

	s, _ = DoNoAuth("GET", "/"+bucket+"/"+key)
	Pass("No auth, no presign params → 403", s == 403)

	// --- Presign 有效期边界 ---

	presignedURL = PresignURL("GET", "/"+bucket+"/"+key, 604800)
	s, _, _ = DoPresigned("GET", presignedURL, "", "")
	Pass("Presign max expires (7 days) → 200", s == 200)

	// --- Bucket 操作 presign ---

	presignedURL = PresignURL("GET", "/"+bucket, 3600)
	s, _, _ = DoPresigned("GET", presignedURL, "", "")
	Pass("Presign GET bucket (list objects)", s == 200)

	// --- 预签名 URL 格式验证 ---

	presignedURL = PresignURL("GET", "/"+bucket+"/"+key, 3600)
	Pass("Presign URL contains X-Amz-Algorithm", strings.Contains(presignedURL, "X-Amz-Algorithm=AWS4-HMAC-SHA256"))
	Pass("Presign URL contains X-Amz-Expires", strings.Contains(presignedURL, "X-Amz-Expires=3600"))
	Pass("Presign URL contains X-Amz-SignedHeaders", strings.Contains(presignedURL, "X-Amz-SignedHeaders=host"))
	Pass("Presign URL contains X-Amz-Signature", strings.Contains(presignedURL, "X-Amz-Signature="))
	Pass("Presign URL contains X-Amz-Credential", strings.Contains(presignedURL, "X-Amz-Credential="))
	Pass("Presign URL contains X-Amz-Date", strings.Contains(presignedURL, "X-Amz-Date="))

	// 清理。
	Do2("DELETE", "/"+bucket+"/presign-put-key", "", "")
	Do2("DELETE", "/"+bucket+"/"+key, "", "")
	Do2("DELETE", "/"+bucket, "", "")

	P("INFO: Phase 11 Presigned URL complete")
}
