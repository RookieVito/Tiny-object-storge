package main

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	BaseURL   = "http://localhost:9000"
	AccessKey = "minioadmin"
	SecretKey = "minioadmin"
)

var failed bool

// Pass records a test result. Exits with code 1 on failure.
func Pass(label string, ok bool) {
	if ok {
		fmt.Printf("  PASS: %s\n", label)
	} else {
		fmt.Printf("  FAIL: %s\n", label)
		failed = true
	}
}

// CheckFailed exits with code 1 if any test failed.
func CheckFailed() {
	if failed {
		os.Exit(1)
	}
}

// Sig computes the AWS Sig V2 signature for a request.
func Sig(method, resource, contentType string) string {
	date := time.Now().UTC().Format(time.RFC1123)
	sts := strings.Join([]string{method, "", contentType, date, resource}, "\n")
	mac := hmac.New(sha1.New, []byte(SecretKey))
	mac.Write([]byte(sts))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// Do sends an authenticated request and returns status, body, headers.
func Do(method, path, body, contentType string) (int, string, http.Header) {
	resource := path
	if idx := strings.Index(path, "?"); idx >= 0 {
		resource = path[:idx]
	}

	h := map[string]string{
		"Authorization": fmt.Sprintf("AWS %s:%s", AccessKey, Sig(method, resource, contentType)),
		"Date":          time.Now().UTC().Format(time.RFC1123),
	}
	if contentType != "" {
		h["Content-Type"] = contentType
	}

	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, BaseURL+path, bodyReader)
	for k, v := range h {
		req.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Sprintf("error: %v"), nil
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

// DoNoAuth sends a request without authentication.
func DoNoAuth(method, path string) (int, string) {
	req, _ := http.NewRequest(method, BaseURL+path, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Sprintf("error: %v")
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// DoRaw sends a request with custom headers (no auto-signing).
func DoRaw(method, path string, headers map[string]string) (int, string) {
	req, _ := http.NewRequest(method, BaseURL+path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Sprintf("error: %v")
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// Do2 is a convenience wrapper that returns only status and body.
func Do2(method, path, body, contentType string) (int, string) {
	status, b, _ := Do(method, path, body, contentType)
	return status, b
}

// Do3 发送带 io.Reader body 的认证请求，用于大文件上传测试。
func Do3(method, path string, body io.Reader, contentType string) (int, string) {
	resource := path
	if idx := strings.Index(path, "?"); idx >= 0 {
		resource = path[:idx]
	}

	h := map[string]string{
		"Authorization": fmt.Sprintf("AWS %s:%s", AccessKey, Sig(method, resource, contentType)),
		"Date":          time.Now().UTC().Format(time.RFC1123),
	}
	if contentType != "" {
		h["Content-Type"] = contentType
	}

	req, _ := http.NewRequest(method, BaseURL+path, body)
	for k, v := range h {
		req.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Sprintf("error: %v")
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}
