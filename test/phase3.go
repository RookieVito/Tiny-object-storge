package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
)

func init() {
	registerTest("Phase 3", testPhase3)
}

func testPhase3() {
	testBodySizeLimit()
	testConcurrentSafety()
	testMetrics()
}

// --- F1: 请求体大小限制 ---

func testBodySizeLimit() {
	bucket := "p3-size-bucket"

	status, _ := Do2("PUT", "/"+bucket, "", "")
	Pass("BodySize: CreateBucket", status == 200)

	// 发送超过 10MB 的请求体，期望 413。
	largeBody := bytes.NewReader(bytes.Repeat([]byte("x"), 11*1024*1024))
	status, body := Do3("PUT", "/"+bucket+"/large.bin", largeBody, "application/octet-stream")
	Pass("BodySize: >10MB → 413", status == 413)
	Pass("BodySize: 413 error XML", strings.Contains(body, "RequestEntityTooLarge"))

	// 发送正常大小的请求体，期望 200。
	status, _ = Do2("PUT", "/"+bucket+"/small.txt", "hello", "text/plain")
	Pass("BodySize: small body → 200", status == 200)

	// 清理。
	Do2("DELETE", "/"+bucket+"/small.txt", "", "")
	Do2("DELETE", "/"+bucket, "", "")
}

// --- F2: 并发安全 ---

func testConcurrentSafety() {
	bucket := "p3-concurrent-bucket"
	Do2("PUT", "/"+bucket, "", "")

	var wg sync.WaitGroup
	errCh := make(chan string, 20)

	// 并发 PutObject 同一 bucket 内的不同 key。
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			k := string([]byte{'o', 'b', 'j', '-', '0' + byte(idx)})
			status, _ := Do2("PUT", "/"+bucket+"/"+k, "data", "application/octet-stream")
			if status != 200 {
				errCh <- "put:" + k
			}
		}(i)
	}

	// 并发 DeleteObject 同一 bucket 内的不同 key。
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			k := string([]byte{'o', 'b', 'j', '-', '0' + byte(idx)})
			status, _ := Do2("DELETE", "/"+bucket+"/"+k, "", "")
			if status != 204 {
				errCh <- "delete:" + k
			}
		}(i)
	}

	wg.Wait()
	close(errCh)

	errCount := 0
	for range errCh {
		errCount++
	}
	Pass("Concurrent: 20 parallel ops no errors", errCount == 0)

	Do2("DELETE", "/"+bucket, "", "")
}

// --- F3+F4: Metrics 端点 ---

type metricsJSON struct {
	TotalRequests int64 `json:"total_requests"`
	TotalErrors   int64 `json:"total_errors"`
	BucketCount   int   `json:"bucket_count"`
	StorageBytes  int64 `json:"storage_bytes"`
}

func fetchMetrics() metricsJSON {
	_, body := DoNoAuth("GET", "/_metrics")
	var m metricsJSON
	json.Unmarshal([]byte(body), &m)
	return m
}

func testMetrics() {
	bucket := "p3-metrics-bucket"

	// 记录基线（fetchMetrics 本身也会产生 1 个请求）。
	before := fetchMetrics()

	// 执行 5 个已知操作。
	Do2("PUT", "/"+bucket, "", "")
	Do2("PUT", "/"+bucket+"/a.txt", "hello metrics", "text/plain")
	Do2("GET", "/"+bucket, "", "")
	DoNoAuth("GET", "/"+bucket+"/a.txt")     // 403 error
	DoNoAuth("GET", "/_metrics")             // 200

	// after = fetchMetrics() 自身产生 1 个请求，所以 delta = 5 + 1 = 6。
	after := fetchMetrics()

	expectedDelta := int64(6)
	actualDelta := after.TotalRequests - before.TotalRequests
	Pass("Metrics: request count delta=6", actualDelta == expectedDelta)
	Pass("Metrics: error count increased", after.TotalErrors > before.TotalErrors)
	Pass("Metrics: bucket_count > 0", after.BucketCount > 0)

	// 验证 storage_bytes 反映了上传的 "hello metrics"（13 bytes）。
	Pass("Metrics: storage_bytes >= 13", after.StorageBytes >= 13)

	// 验证 /_metrics 返回合法 JSON。
	status, body := DoNoAuth("GET", "/_metrics")
	Pass("Metrics: endpoint 200", status == 200)
	Pass("Metrics: valid JSON", json.Valid([]byte(body)))

	// POST /_metrics 应该返回 405。
	status, _ = DoRaw("POST", "/_metrics", nil)
	Pass("Metrics: POST → 405", status == 405)

	// 清理。
	Do2("DELETE", "/"+bucket+"/a.txt", "", "")
	Do2("DELETE", "/"+bucket, "", "")
}
