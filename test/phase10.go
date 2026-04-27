package main

import (
	"strings"
)

func init() {
	registerTest("Phase 10", testPhase10)
}

func testPhase10() {
	const bucket = "sigv4-test-bucket"
	const key = "sigv4-test-object"

	// 清理
	Do2("DELETE", "/"+bucket+"/"+key, "", "")
	Do2("DELETE", "/"+bucket, "", "")

	// --- Sig V4 基本认证测试 ---

	// V4 CreateBucket
	s, _, _ := DoV4("PUT", "/"+bucket, "", "")
	Pass("V4 CreateBucket", s == 200)

	// V4 PutObject
	s, _, _ = DoV4("PUT", "/"+bucket+"/"+key, "hello v4", "text/plain")
	Pass("V4 PutObject", s == 200)

	// V4 GetObject
	s, b, _ := DoV4("GET", "/"+bucket+"/"+key, "", "")
	Pass("V4 GetObject", s == 200)
	Pass("V4 GetObject content", b == "hello v4")

	// V4 HeadObject
	s, _, _ = DoV4("HEAD", "/"+bucket+"/"+key, "", "")
	Pass("V4 HeadObject", s == 200)

	// V4 DeleteObject
	s, _, _ = DoV4("DELETE", "/"+bucket+"/"+key, "", "")
	Pass("V4 DeleteObject", s == 204)

	// V4 GetObject → 404 (deleted)
	s, _, _ = DoV4("GET", "/"+bucket+"/"+key, "", "")
	Pass("V4 GetObject deleted → 404", s == 404)

	// --- V2 向后兼容 ---

	s, _ = Do2("PUT", "/"+bucket+"/v2-object", "v2 still works", "text/plain")
	Pass("V2 PutObject still works", s == 200)

	s, b, _ = Do("GET", "/"+bucket+"/v2-object", "", "")
	Pass("V2 GetObject still works", s == 200)
	Pass("V2 GetObject content", b == "v2 still works")

	// --- 认证失败测试 ---

	// 无认证头
	s, _ = DoNoAuth("GET", "/"+bucket+"/v2-object")
	Pass("No auth → 403", s == 403)

	// 无效 V4 签名
	s, _, _ = DoV4WithHeaders("GET", "/"+bucket+"/v2-object", "", "", map[string]string{
		"Authorization": "AWS4-HMAC-SHA256 Credential=minioadmin/20260101/us-east-1/s3/aws4_request, SignedHeaders=host;x-amz-date, Signature=badsignature",
		"X-Amz-Date":    "20260101T000000Z",
	})
	Pass("Invalid V4 signature → 403", s == 403)

	// 错误 AccessKey
	v4Headers := SigV4("GET", "/"+bucket+"/v2-object", "")
	badCred := strings.Replace(v4Headers["Authorization"], AccessKey, "wrongkey", 1)
	s, _, _ = DoV4WithHeaders("GET", "/"+bucket+"/v2-object", "", "", map[string]string{
		"Authorization": badCred,
	})
	Pass("Wrong V4 access key → 403", s == 403)

	// 缺少 X-Amz-Date 头
	s, _ = DoRaw("GET", "/"+bucket+"/v2-object", map[string]string{
		"Authorization": "AWS4-HMAC-SHA256 Credential=minioadmin/20260101/us-east-1/s3/aws4_request, SignedHeaders=host, Signature=abc",
	})
	Pass("Missing X-Amz-Date → 400", s == 400)

	// --- 额外 V4 测试 ---

	// V4 带查询字符串签名
	s, _, _ = DoV4("GET", "/"+bucket+"?uploads", "", "")
	Pass("V4 GET with ?uploads query string", s == 200)

	// V4 带前缀查询
	s, _, _ = DoV4("GET", "/"+bucket+"?delimiter=/&prefix=test", "", "")
	Pass("V4 GET with delimiter+prefix query", s == 200)

	// Content-Type 篾改检测：用 text/plain 签名，但发送 application/json
	s, _ = Do2("PUT", "/"+bucket+"/ct-tamper", "test", "text/plain")
	Pass("V4 PutObject for content-type tamper setup", s == 200)
	ctHeaders := SigV4("GET", "/"+bucket+"/ct-tamper", "text/plain")
	ctHeaders["Content-Type"] = "application/json" // 篡改 content-type
	s, _ = DoRaw("GET", "/"+bucket+"/ct-tamper", ctHeaders)
	Pass("Content-Type tamper → 403", s == 403)

	// Range + V4 组合
	s, b, h := DoV4WithHeaders("GET", "/"+bucket+"/v2-object", "", "", map[string]string{
		"Range": "bytes=0-4",
	})
	Pass("Range + V4 → 206", s == 206)
	Pass("Range + V4 content", b == "v2 st")
	Pass("Range + V4 Content-Range", h.Get("Content-Range") != "")

	// 清理
	Do2("DELETE", "/"+bucket+"/v2-object", "", "")
	Do2("DELETE", "/"+bucket, "", "")

	P("INFO: Phase 10 Sig V4 authentication complete")
}

func P(label string) {
	Pass(label, true)
}
