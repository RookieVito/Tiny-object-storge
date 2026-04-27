package main

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	v4Algorithm = "AWS4-HMAC-SHA256"
	v4Service   = "s3"
	v4Request   = "aws4_request"
)

// SignV2 计算 AWS Signature V2，返回签名值和用于请求的 Date 头。
func SignV2(method, resource, contentType, secretKey string) (signature, date string) {
	date = time.Now().UTC().Format(time.RFC1123)
	sts := strings.Join([]string{method, "", contentType, date, resource}, "\n")
	mac := hmac.New(sha1.New, []byte(secretKey))
	mac.Write([]byte(sts))
	signature = base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return
}

// SignV4 计算 AWS Signature V4，返回签名相关的所有请求头。
// endpoint 用于提取 host 进行签名（格式 http://host:port）。
// queryString 是 URL 的原始查询字符串（不含 ?）。
func SignV4(method, resource, contentType, accessKey, secretKey, region, endpoint, queryString string) map[string]string {
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	scope := fmt.Sprintf("%s/%s/%s/%s", dateStamp, region, v4Service, v4Request)

	payloadHash := "UNSIGNED-PAYLOAD"
	canonicalURI := canonicalURIPath(resource)

	// 构建 canonical query string。
	canonicalQS := ""
	if queryString != "" {
		pairs := strings.Split(queryString, "&")
		sort.Strings(pairs)
		var parts []string
		for _, p := range pairs {
			kv := strings.SplitN(p, "=", 2)
			if len(kv) == 2 {
				parts = append(parts, url.QueryEscape(kv[0])+"="+url.QueryEscape(kv[1]))
			} else {
				parts = append(parts, url.QueryEscape(p))
			}
		}
		canonicalQS = strings.Join(parts, "&")
	}

	// 从 endpoint 提取 host。
	host := endpoint
	if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
		host = u.Host
	}

	// 收集 canonical headers。
	headers := map[string]string{
		"host":                  host,
		"x-amz-date":            amzDate,
		"x-amz-content-sha256":  payloadHash,
	}
	if contentType != "" {
		headers["content-type"] = contentType
	}

	var headerKeys []string
	for k := range headers {
		headerKeys = append(headerKeys, k)
	}
	sort.Strings(headerKeys)

	var canonicalHeaders strings.Builder
	signedHeaderParts := make([]string, len(headerKeys))
	for i, k := range headerKeys {
		canonicalHeaders.WriteString(k)
		canonicalHeaders.WriteByte(':')
		canonicalHeaders.WriteString(headers[k])
		canonicalHeaders.WriteByte('\n')
		signedHeaderParts[i] = k
	}
	signedHeaders := strings.Join(signedHeaderParts, ";")

	canonicalRequest := strings.Join([]string{
		method,
		canonicalURI,
		canonicalQS,
		canonicalHeaders.String(),
		"",
		signedHeaders,
		payloadHash,
	}, "\n")

	stringToSign := strings.Join([]string{
		v4Algorithm,
		amzDate,
		scope,
		hexSHA256([]byte(canonicalRequest)),
	}, "\n")

	signingKey := deriveSigningKey(secretKey, dateStamp, region)
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	credential := fmt.Sprintf("%s/%s", accessKey, scope)
	authHeader := fmt.Sprintf("%s Credential=%s, SignedHeaders=%s, Signature=%s",
		v4Algorithm, credential, signedHeaders, signature)

	result := map[string]string{
		"Authorization":          authHeader,
		"X-Amz-Date":             amzDate,
		"X-Amz-Content-Sha256":   payloadHash,
	}
	return result
}

// PresignV4 生成 Sig V4 预签名 URL。
func PresignV4(method, resource, accessKey, secretKey, region, host string, expires int64) string {
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	scope := fmt.Sprintf("%s/%s/%s/%s", dateStamp, region, v4Service, v4Request)
	credential := fmt.Sprintf("%s/%s", accessKey, scope)

	canonicalURI := canonicalURIPath(resource)

	signedHeaders := "host"
	qs := url.Values{}
	qs.Set("X-Amz-Algorithm", v4Algorithm)
	qs.Set("X-Amz-Credential", credential)
	qs.Set("X-Amz-Date", amzDate)
	qs.Set("X-Amz-Expires", fmt.Sprintf("%d", expires))
	qs.Set("X-Amz-SignedHeaders", signedHeaders)
	canonicalQS := buildCanonicalQueryString(qs)

	canonicalHeaders := "host:" + host + "\n"

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
		v4Algorithm,
		amzDate,
		scope,
		hexSHA256([]byte(canonicalRequest)),
	}, "\n")

	signingKey := deriveSigningKey(secretKey, dateStamp, region)
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	qs.Set("X-Amz-Signature", signature)
	return fmt.Sprintf("http://%s%s?%s", host, canonicalURI, qs.Encode())
}

// buildCanonicalQueryString 构建排序后的查询字符串（用于 presign）。
func buildCanonicalQueryString(qs url.Values) string {
	keys := make([]string, 0, len(qs))
	for k := range qs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		for _, v := range qs[k] {
			parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
	}
	return strings.Join(parts, "&")
}

// SignRequest 对 HTTP 请求添加 AWS Sig V4 Authorization 头和相关头。
func SignRequest(req *http.Request, accessKey, secretKey, endpoint string) {
	contentType := req.Header.Get("Content-Type")
	queryString := req.URL.RawQuery
	v4Headers := SignV4(req.Method, req.URL.Path, contentType, accessKey, secretKey, "us-east-1", endpoint, queryString)
	for k, v := range v4Headers {
		req.Header.Set(k, v)
	}
}

func canonicalURIPath(path string) string {
	if path == "" {
		return "/"
	}
	segments := strings.Split(path, "/")
	encoded := make([]string, len(segments))
	for i, seg := range segments {
		encoded[i] = url.QueryEscape(seg)
	}
	return strings.Join(encoded, "/")
}

func deriveSigningKey(secretKey, dateStamp, region string) []byte {
	kSecret := []byte("AWS4" + secretKey)
	kDate := hmacSHA256(kSecret, []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(v4Service))
	kSigning := hmacSHA256(kService, []byte(v4Request))
	return kSigning
}

func hmacSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

func hexSHA256(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
