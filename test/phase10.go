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

	// 清理
	Do2("DELETE", "/"+bucket+"/v2-object", "", "")
	Do2("DELETE", "/"+bucket, "", "")

	P("INFO: Phase 10 Sig V4 authentication complete")
}

func P(label string) {
	Pass(label, true)
}
