package main

import (
	"fmt"
	"strings"
)

func init() {
	registerTest("Phase 9", testPhase9)
}

func testPhase9() {
	const bucket = "range-test-bucket"
	const key = "range-test-object"
	const content = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"

	// 清理环境
	Do2("DELETE", "/"+bucket+"/"+key, "", "")
	Do2("DELETE", "/"+bucket, "", "")

	// 创建 bucket
	s, _ := Do2("PUT", "/"+bucket, "", "")
	Pass("CreateBucket", s == 200 || s == 409)

	// 上传测试对象（36 字节）
	s, _ = Do2("PUT", "/"+bucket+"/"+key, content, "text/plain")
	Pass("PutObject", s == 200)

	// --- 基本 Range GET 测试 ---

	// 无 Range → 200 全量返回
	s, b, h := DoWithHeaders("GET", "/"+bucket+"/"+key, "", "", nil)
	Pass("GET without Range → 200", s == 200)
	Pass("GET without Range → full body", b == content)
	Pass("GET without Range → Accept-Ranges header", h.Get("Accept-Ranges") == "bytes")

	// bytes=0-4 → 前 5 字节
	s, b, h = DoWithHeaders("GET", "/"+bucket+"/"+key, "", "", map[string]string{"Range": "bytes=0-4"})
	Pass("Range bytes=0-4 → 206", s == 206)
	Pass("Range bytes=0-4 → body '01234'", b == "01234")
	Pass("Range bytes=0-4 → Content-Range", h.Get("Content-Range") == "bytes 0-4/36")
	Pass("Range bytes=0-4 → Content-Length '5'", h.Get("Content-Length") == "5")
	Pass("Range bytes=0-4 → ETag present", h.Get("ETag") != "")
	Pass("Range bytes=0-4 → Last-Modified present", h.Get("Last-Modified") != "")

	// bytes=10-19 → 中间 10 字节
	s, b, h = DoWithHeaders("GET", "/"+bucket+"/"+key, "", "", map[string]string{"Range": "bytes=10-19"})
	Pass("Range bytes=10-19 → 206", s == 206)
	Pass("Range bytes=10-19 → body 'ABCDEFGHIJ'", b == "ABCDEFGHIJ")
	Pass("Range bytes=10-19 → Content-Range", h.Get("Content-Range") == "bytes 10-19/36")

	// bytes=30- → 从偏移 30 到末尾（6 字节）
	s, b, h = DoWithHeaders("GET", "/"+bucket+"/"+key, "", "", map[string]string{"Range": "bytes=30-"})
	Pass("Range bytes=30- → 206", s == 206)
	Pass("Range bytes=30- → body 'UVWXYZ'", b == "UVWXYZ")
	Pass("Range bytes=30- → Content-Range", h.Get("Content-Range") == "bytes 30-35/36")

	// bytes=-5 → 最后 5 字节
	s, b, h = DoWithHeaders("GET", "/"+bucket+"/"+key, "", "", map[string]string{"Range": "bytes=-5"})
	Pass("Range bytes=-5 → 206", s == 206)
	Pass("Range bytes=-5 → body 'VWXYZ'", b == "VWXYZ")
	Pass("Range bytes=-5 → Content-Range", h.Get("Content-Range") == "bytes 31-35/36")

	// bytes=-100 → 超过文件大小，从开头开始
	s, b, h = DoWithHeaders("GET", "/"+bucket+"/"+key, "", "", map[string]string{"Range": "bytes=-100"})
	Pass("Range bytes=-100 (overflow) → 206", s == 206)
	Pass("Range bytes=-100 → full body", b == content)
	Pass("Range bytes=-100 → Content-Range", h.Get("Content-Range") == "bytes 0-35/36")

	// bytes=0-0 → 单字节
	s, b, h = DoWithHeaders("GET", "/"+bucket+"/"+key, "", "", map[string]string{"Range": "bytes=0-0"})
	Pass("Range bytes=0-0 → 206", s == 206)
	Pass("Range bytes=0-0 → body '0'", b == "0")
	Pass("Range bytes=0-0 → Content-Length '1'", h.Get("Content-Length") == "1")

	// bytes=35-35 → 最后单字节
	s, b, _ = DoWithHeaders("GET", "/"+bucket+"/"+key, "", "", map[string]string{"Range": "bytes=35-35"})
	Pass("Range bytes=35-35 → 206", s == 206)
	Pass("Range bytes=35-35 → body 'Z'", b == "Z")

	// --- 错误 Range 测试 ---

	// bytes=100-200 → 超出范围 → 416
	s, b, h = DoWithHeaders("GET", "/"+bucket+"/"+key, "", "", map[string]string{"Range": "bytes=100-200"})
	Pass("Range bytes=100-200 → 416", s == 416)
	Pass("Range 416 → Content-Range */36", h.Get("Content-Range") == "bytes */36")
	Pass("Range 416 → InvalidRange code", strings.Contains(b, "InvalidRange"))

	// bytes=50-49 → start > end → 416
	s, _, _ = DoWithHeaders("GET", "/"+bucket+"/"+key, "", "", map[string]string{"Range": "bytes=50-49"})
	Pass("Range bytes=50-49 (inverted) → 416", s == 416)

	// bytes=36- → start == size → 416
	s, _, _ = DoWithHeaders("GET", "/"+bucket+"/"+key, "", "", map[string]string{"Range": "bytes=36-"})
	Pass("Range bytes=36- (start=size) → 416", s == 416)

	// 无效格式 bytes=abc → 回退 200
	s, b, _ = DoWithHeaders("GET", "/"+bucket+"/"+key, "", "", map[string]string{"Range": "bytes=abc"})
	Pass("Range bytes=abc (invalid) → 200 fallback", s == 200)
	Pass("Range bytes=abc → full body", b == content)

	// 多 range bytes=0-4,10-14 → 回退 200
	s, b, _ = DoWithHeaders("GET", "/"+bucket+"/"+key, "", "", map[string]string{"Range": "bytes=0-4,10-14"})
	Pass("Multi-range → 200 fallback", s == 200)
	Pass("Multi-range → full body", b == content)

	// bytes=0-35 恰好等于文件大小 → 206
	s, b, h = DoWithHeaders("GET", "/"+bucket+"/"+key, "", "", map[string]string{"Range": "bytes=0-35"})
	Pass("Range bytes=0-35 (exact) → 206", s == 206)
	Pass("Range bytes=0-35 → full body", b == content)
	Pass("Range bytes=0-35 → Content-Range", h.Get("Content-Range") == "bytes 0-35/36")

	// --- HEAD Range 测试 ---

	// HEAD with Range → 206 + 正确 headers，无 body
	s, b, h = DoWithHeaders("HEAD", "/"+bucket+"/"+key, "", "", map[string]string{"Range": "bytes=5-9"})
	Pass("HEAD Range bytes=5-9 → 206", s == 206)
	Pass("HEAD Range → no body", b == "")
	Pass("HEAD Range → Content-Range", h.Get("Content-Range") == "bytes 5-9/36")
	Pass("HEAD Range → Content-Length '5'", h.Get("Content-Length") == "5")

	// HEAD with invalid Range → 416
	s, _, h = DoWithHeaders("HEAD", "/"+bucket+"/"+key, "", "", map[string]string{"Range": "bytes=100-200"})
	Pass("HEAD Range invalid → 416", s == 416)
	Pass("HEAD Range 416 → Content-Range */36", h.Get("Content-Range") == "bytes */36")

	// HEAD without Range → 200 + Accept-Ranges
	s, _, h = DoWithHeaders("HEAD", "/"+bucket+"/"+key, "", "", nil)
	Pass("HEAD without Range → 200", s == 200)
	Pass("HEAD without Range → Accept-Ranges", h.Get("Accept-Ranges") == "bytes")

	// --- Range on non-existent object ---

	s, _, _ = DoWithHeaders("GET", "/"+bucket+"/no-such-key", "", "", map[string]string{"Range": "bytes=0-9"})
	Pass("Range on missing key → 404", s == 404)

	// --- 较大对象测试 ---

	bigContent := strings.Repeat("ABCDEFGHIJ", 1000) // 10000 字节
	s, _ = Do2("PUT", "/"+bucket+"/big-object", bigContent, "text/plain")
	Pass("Put big object (10KB)", s == 200)

	s, b, h = DoWithHeaders("GET", "/"+bucket+"/big-object", "", "", map[string]string{"Range": "bytes=9990-9999"})
	Pass("Big object Range last 10 bytes → 206", s == 206)
	Pass("Big object Range → correct content", b == "ABCDEFGHIJ")
	Pass("Big object Range → Content-Length '10'", h.Get("Content-Length") == "10")
	Pass("Big object Range → Content-Range", h.Get("Content-Range") == "bytes 9990-9999/10000")

	// suffix range on big object
	s, b, h = DoWithHeaders("GET", "/"+bucket+"/big-object", "", "", map[string]string{"Range": "bytes=-5"})
	Pass("Big object suffix -5 → 206", s == 206)
	Pass("Big object suffix -5 → 'FGHIJ'", b == "FGHIJ")

	// 清理
	Do2("DELETE", "/"+bucket+"/big-object", "", "")
	Do2("DELETE", "/"+bucket+"/"+key, "", "")
	Do2("DELETE", "/"+bucket, "", "")

	fmt.Printf("  INFO: Phase 9 Range requests complete\n")
}
