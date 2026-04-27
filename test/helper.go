package main

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
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
		return 0, fmt.Sprintf("error: %v", err), nil
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
		return 0, fmt.Sprintf("error: %v", err)
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
		return 0, fmt.Sprintf("error: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// DoWithHeaders 发送带认证和自定义 header 的请求，返回 status、body、headers。
func DoWithHeaders(method, path, body, contentType string, extraHeaders map[string]string) (int, string, http.Header) {
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
	for k, v := range extraHeaders {
		h[k] = v
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
		return 0, fmt.Sprintf("error: %v", err), nil
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
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
		return 0, fmt.Sprintf("error: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// --- Sig V4 helpers ---

const (
	v4Alg    = "AWS4-HMAC-SHA256"
	v4Svc    = "s3"
	v4Req    = "aws4_request"
	v4Region = "us-east-1"
)

func v4HmacSHA256(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

func v4DeriveKey(secret, dateStamp, region string) []byte {
	k := []byte("AWS4" + secret)
	k = v4HmacSHA256(k, []byte(dateStamp))
	k = v4HmacSHA256(k, []byte(region))
	k = v4HmacSHA256(k, []byte(v4Svc))
	return v4HmacSHA256(k, []byte(v4Req))
}

func v4HexSHA256(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func v4URI(path string) string {
	segs := strings.Split(path, "/")
	for i, s := range segs {
		segs[i] = url.QueryEscape(s)
	}
	return strings.Join(segs, "/")
}

// SigV4 生成 Sig V4 Authorization 头和所需的 X-Amz-* 头。
func SigV4(method, path, contentType string) map[string]string {
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	scope := fmt.Sprintf("%s/%s/%s/%s", dateStamp, v4Region, v4Svc, v4Req)
	payloadHash := "UNSIGNED-PAYLOAD"

	// 从 path 中提取 resource 和 query string。
	resource := path
	queryString := ""
	if idx := strings.Index(path, "?"); idx >= 0 {
		resource = path[:idx]
		queryString = path[idx+1:]
	}

	// canonical headers
	hdrs := map[string]string{
		"host":                  "localhost:9000",
		"x-amz-content-sha256":  payloadHash,
		"x-amz-date":            amzDate,
	}
	if contentType != "" {
		hdrs["content-type"] = contentType
	}
	keys := make([]string, 0, len(hdrs))
	for k := range hdrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var chBuilder strings.Builder
	shParts := make([]string, len(keys))
	for i, k := range keys {
		chBuilder.WriteString(k)
		chBuilder.WriteByte(':')
		chBuilder.WriteString(hdrs[k])
		chBuilder.WriteByte('\n')
		shParts[i] = k
	}
	signedHeaders := strings.Join(shParts, ";")

	// canonical query string
	canonicalQS := ""
	if queryString != "" {
		qs := url.Values{}
		for _, pair := range strings.Split(queryString, "&") {
			kv := strings.SplitN(pair, "=", 2)
			if len(kv) == 2 {
				qs.Set(kv[0], kv[1])
			} else if kv[0] != "" {
				qs.Set(kv[0], "")
			}
		}
		qsKeys := make([]string, 0, len(qs))
		for k := range qs {
			qsKeys = append(qsKeys, k)
		}
		sort.Strings(qsKeys)
		parts := make([]string, 0, len(qsKeys))
		for _, k := range qsKeys {
			for _, v := range qs[k] {
				parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
			}
		}
		canonicalQS = strings.Join(parts, "&")
	}

	canonicalRequest := strings.Join([]string{
		method,
		v4URI(resource),
		canonicalQS,
		chBuilder.String(),
		"",
		signedHeaders,
		payloadHash,
	}, "\n")

	stringToSign := strings.Join([]string{
		v4Alg,
		amzDate,
		scope,
		v4HexSHA256([]byte(canonicalRequest)),
	}, "\n")

	signingKey := v4DeriveKey(SecretKey, dateStamp, v4Region)
	sig := hex.EncodeToString(v4HmacSHA256(signingKey, []byte(stringToSign)))
	cred := fmt.Sprintf("%s/%s", AccessKey, scope)
	auth := fmt.Sprintf("%s Credential=%s, SignedHeaders=%s, Signature=%s", v4Alg, cred, signedHeaders, sig)

	return map[string]string{
		"Authorization":        auth,
		"X-Amz-Date":           amzDate,
		"X-Amz-Content-Sha256": payloadHash,
	}
}

// DoV4 sends a Sig V4 authenticated request and returns status, body, headers.
func DoV4(method, path, body, contentType string) (int, string, http.Header) {
	h := SigV4(method, path, contentType)
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
		return 0, fmt.Sprintf("error: %v", err), nil
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

// DoV4WithHeaders sends a Sig V4 authenticated request with extra headers.
func DoV4WithHeaders(method, path, body, contentType string, extra map[string]string) (int, string, http.Header) {
	h := SigV4(method, path, contentType)
	if contentType != "" {
		h["Content-Type"] = contentType
	}
	for k, v := range extra {
		h[k] = v
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
		return 0, fmt.Sprintf("error: %v", err), nil
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

// --- Presign helpers ---

// presignURLAtTime 使用指定时间生成预签名 URL。
func presignURLAtTime(method, path string, expires int64, t time.Time) string {
	amzDate := t.UTC().Format("20060102T150405Z")
	dateStamp := t.UTC().Format("20060102")
	scope := fmt.Sprintf("%s/%s/%s/%s", dateStamp, v4Region, v4Svc, v4Req)
	credential := fmt.Sprintf("%s/%s", AccessKey, scope)

	signedHeaders := "host"
	qs := url.Values{}
	qs.Set("X-Amz-Algorithm", v4Alg)
	qs.Set("X-Amz-Credential", credential)
	qs.Set("X-Amz-Date", amzDate)
	qs.Set("X-Amz-Expires", fmt.Sprintf("%d", expires))
	qs.Set("X-Amz-SignedHeaders", signedHeaders)

	keys := make([]string, 0, len(qs))
	for k := range qs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	qsParts := make([]string, 0, len(keys))
	for _, k := range keys {
		for _, v := range qs[k] {
			qsParts = append(qsParts, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
	}
	canonicalQS := strings.Join(qsParts, "&")

	canonicalHeaders := "host:localhost:9000\n"
	canonicalURI := v4URI(path)

	canonicalRequest := strings.Join([]string{
		method,
		canonicalURI,
		canonicalQS,
		canonicalHeaders,
		"",
		signedHeaders,
		"UNSIGNED-PAYLOAD",
	}, "\n")

	stringToSign := strings.Join([]string{
		v4Alg,
		amzDate,
		scope,
		v4HexSHA256([]byte(canonicalRequest)),
	}, "\n")

	signingKey := v4DeriveKey(SecretKey, dateStamp, v4Region)
	signature := hex.EncodeToString(v4HmacSHA256(signingKey, []byte(stringToSign)))

	qs.Set("X-Amz-Signature", signature)
	return fmt.Sprintf("%s%s?%s", BaseURL, canonicalURI, qs.Encode())
}

// PresignURL 生成 Sig V4 预签名 URL（测试用）。
func PresignURL(method, path string, expires int64) string {
	return presignURLAtTime(method, path, expires, time.Now())
}

// DoPresigned 发送请求到预签名 URL（不带 Authorization 头）。
func DoPresigned(method, presignedURL string, body, contentType string) (int, string, http.Header) {
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, presignedURL, bodyReader)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Sprintf("error: %v", err), nil
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}
