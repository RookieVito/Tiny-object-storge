package main

import (
	"bytes"
	"encoding/json"
	"strings"
)

func init() {
	registerTest("Phase 4", testPhase4)
}

func testPhase4() {
	testStorageBackendRoundTrip()
	testNestedKeys()
	testListObjectsV2PrefixDelimiter()
	testListObjectsV2Pagination()
	testPhase4BodySizeLimit()
	testMetricsEndpoint()
}

// --- 存储后端抽象回归测试 ---

func testStorageBackendRoundTrip() {
	bucket := "p4-rt-bucket"

	// Bucket CRUD。
	status, _ := Do2("PUT", "/"+bucket, "", "")
	Pass("RoundTrip: CreateBucket → 200", status == 200)

	status, _ = Do2("PUT", "/"+bucket, "", "")
	Pass("RoundTrip: CreateBucket duplicate → 409", status == 409)

	status, _, hdrs := Do("HEAD", "/"+bucket, "", "")
	Pass("RoundTrip: HeadBucket → 200", status == 200)
	Pass("RoundTrip: HeadBucket headers", hdrs.Get("Server") == "tiny-object-storage/0.1")

	status, _ = Do2("HEAD", "/p4-nonexistent-bucket", "", "")
	Pass("RoundTrip: HeadBucket not found → 404", status == 404)

	// Object CRUD。
	status, _ = Do2("PUT", "/"+bucket+"/test.txt", "backend round trip", "text/plain")
	Pass("RoundTrip: PutObject → 200", status == 200)

	_, body := Do2("GET", "/"+bucket+"/test.txt", "", "")
	Pass("RoundTrip: GetObject content matches", body == "backend round trip")

	status, _, hdrs = Do("HEAD", "/"+bucket+"/test.txt", "", "")
	Pass("RoundTrip: HeadObject → 200", status == 200)
	Pass("RoundTrip: HeadObject Content-Length", hdrs.Get("Content-Length") == "18")
	Pass("RoundTrip: HeadObject Content-Type", hdrs.Get("Content-Type") == "text/plain")

	// 再上传一个对象，用于 DeleteBucket not empty 测试。
	Do2("PUT", "/"+bucket+"/keep.txt", "keep", "text/plain")

	// Delete idempotent。
	status, _ = Do2("DELETE", "/"+bucket+"/test.txt", "", "")
	Pass("RoundTrip: DeleteObject → 204", status == 204)

	status, _ = Do2("DELETE", "/"+bucket+"/test.txt", "", "")
	Pass("RoundTrip: DeleteObject idempotent → 204", status == 204)

	status, body = Do2("GET", "/"+bucket+"/test.txt", "", "")
	Pass("RoundTrip: GetObject deleted → NoSuchKey", status == 404 && strings.Contains(body, "NoSuchKey"))

	// DeleteBucket not empty。
	status, _ = Do2("DELETE", "/"+bucket, "", "")
	Pass("RoundTrip: DeleteBucket not empty → 409", status == 409)

	// 删除剩余对象后，bucket 为空。
	Do2("DELETE", "/"+bucket+"/keep.txt", "", "")
	status, _ = Do2("DELETE", "/"+bucket, "", "")
	Pass("RoundTrip: DeleteBucket empty → 204", status == 204)
}

func testNestedKeys() {
	bucket := "p4-nested-bucket"
	Do2("PUT", "/"+bucket, "", "")

	// 嵌套 key。
	status, _ := Do2("PUT", "/"+bucket+"/a/b/c/deep-file.json", `{"nested":true}`, "application/json")
	Pass("NestedKey: PutObject → 200", status == 200)

	_, body := Do2("GET", "/"+bucket+"/a/b/c/deep-file.json", "", "")
	Pass("NestedKey: GetObject content", strings.Contains(body, `{"nested":true}`))

	// 中间路径的删除。
	status, _ = Do2("DELETE", "/"+bucket+"/a/b/c/deep-file.json", "", "")
	Pass("NestedKey: DeleteObject → 204", status == 204)

	Do2("DELETE", "/"+bucket, "", "")
}

func testListObjectsV2PrefixDelimiter() {
	bucket := "p4-list-bucket"
	Do2("PUT", "/"+bucket, "", "")

	// 上传多个对象。
	objects := []struct {
		key string
		ct  string
	}{
		{"photos/2024/cat.jpg", "image/jpeg"},
		{"photos/2024/dog.jpg", "image/jpeg"},
		{"photos/2025/sunset.jpg", "image/jpeg"},
		{"docs/readme.txt", "text/plain"},
		{"docs/notes/note.txt", "text/plain"},
	}
	for _, o := range objects {
		Do2("PUT", "/"+bucket+"/"+o.key, "data", o.ct)
	}

	// List with prefix=photos/。
	_, body := Do2("GET", "/"+bucket+"?prefix=photos/", "", "")
	Pass("ListV2: prefix=photos/ contains cat.jpg", strings.Contains(body, "<Key>photos/2024/cat.jpg</Key>"))
	Pass("ListV2: prefix=photos/ no docs", !strings.Contains(body, "docs"))

	// List with delimiter=/。
	_, body = Do2("GET", "/"+bucket+"?delimiter=/", "", "")
	Pass("ListV2: delimiter=/ CommonPrefix docs/", strings.Contains(body, "<Prefix>docs/</Prefix>"))
	Pass("ListV2: delimiter=/ CommonPrefix photos/", strings.Contains(body, "<Prefix>photos/</Prefix>"))

	// List with prefix=photos/2024/ delimiter=/。
	_, body = Do2("GET", "/"+bucket+"?prefix=photos/2024/&delimiter=/", "", "")
	Pass("ListV2: prefix+delimiter shows contents", strings.Contains(body, "<Key>photos/2024/cat.jpg</Key>"))
	Pass("ListV2: prefix+delimiter no CommonPrefix", !strings.Contains(body, "CommonPrefixes><Prefix>"))

	// 清理。
	for _, o := range objects {
		Do2("DELETE", "/"+bucket+"/"+o.key, "", "")
	}
	Do2("DELETE", "/"+bucket, "", "")
}

func testListObjectsV2Pagination() {
	bucket := "p4-page-bucket"
	Do2("PUT", "/"+bucket, "", "")

	// 上传 5 个对象（key 长度按字母序）。
	for i := 0; i < 5; i++ {
		key := string([]byte{'a' + byte(i)})
		Do2("PUT", "/"+bucket+"/"+key, "data", "")
	}

	// max-keys=2，第一页。
	_, body := Do2("GET", "/"+bucket+"?max-keys=2", "", "")
	Pass("Pagination: page1 KeyCount=2", strings.Contains(body, "<KeyCount>2</KeyCount>"))
	Pass("Pagination: page1 IsTruncated=true", strings.Contains(body, "<IsTruncated>true</IsTruncated>"))
	Pass("Pagination: page1 has token", strings.Contains(body, "<NextContinuationToken>"))

	// 提取 continuation token。
	token := ""
	if idx := strings.Index(body, "<NextContinuationToken>"); idx >= 0 {
		rest := body[idx+len("<NextContinuationToken>"):]
		if end := strings.Index(rest, "</NextContinuationToken>"); end >= 0 {
			token = rest[:end]
		}
	}

	// max-keys=2，第二页（使用 token）。
	_, body = Do2("GET", "/"+bucket+"?max-keys=2&continuation-token="+token, "", "")
	Pass("Pagination: page2 has ContinuationToken", strings.Contains(body, "<ContinuationToken>"+token+"</ContinuationToken>"))

	// 清理。
	for i := 0; i < 5; i++ {
		key := string([]byte{'a' + byte(i)})
		Do2("DELETE", "/"+bucket+"/"+key, "", "")
	}
	Do2("DELETE", "/"+bucket, "", "")
}

func testPhase4BodySizeLimit() {
	bucket := "p4-size-bucket"
	Do2("PUT", "/"+bucket, "", "")

	// 超过 10MB 的请求体。
	largeBody := bytes.NewReader(bytes.Repeat([]byte("x"), 11*1024*1024))
	status, body := Do3("PUT", "/"+bucket+"/large.bin", largeBody, "application/octet-stream")
	Pass("BodySize: >10MB → 413", status == 413)
	Pass("BodySize: 413 error XML", strings.Contains(body, "RequestEntityTooLarge"))

	// 正常大小。
	status, _ = Do2("PUT", "/"+bucket+"/small.txt", "ok", "text/plain")
	Pass("BodySize: normal body → 200", status == 200)

	Do2("DELETE", "/"+bucket+"/small.txt", "", "")
	Do2("DELETE", "/"+bucket, "", "")
}

func testMetricsEndpoint() {
	status, body := DoNoAuth("GET", "/_metrics")
	Pass("Metrics: endpoint → 200", status == 200)
	Pass("Metrics: valid JSON", json.Valid([]byte(body)))

	var m metricsJSON
	json.Unmarshal([]byte(body), &m)
	Pass("Metrics: total_requests > 0", m.TotalRequests > 0)
}
