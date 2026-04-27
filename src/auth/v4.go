package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"tiny-object-storage/src/s3error"
)

const (
	v4Algorithm = "AWS4-HMAC-SHA256"
	v4Service   = "s3"
	v4Request   = "aws4_request"
	maxTimeSkew = 15 * time.Minute
)

// getV4DateTime 从请求中提取 ISO8601 basic 格式的日期时间。
func getV4DateTime(r *http.Request) (string, string, *s3error.S3APIError) {
	amzDate := r.Header.Get("X-Amz-Date")
	if amzDate == "" {
		return "", "", s3error.ErrMissingSecurityHeader
	}
	if len(amzDate) < 8 {
		return "", "", s3error.ErrRequestTimeTooSkewed
	}
	return amzDate[:8], amzDate, nil
}

// deriveSigningKey 派生 Sig V4 签名密钥。
func deriveSigningKey(secretKey, dateStamp, region string) []byte {
	kSecret := []byte("AWS4" + secretKey)
	kDate := hmacSHA256(kSecret, []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(v4Service))
	kSigning := hmacSHA256(kService, []byte(v4Request))
	return kSigning
}

// buildCanonicalRequest 构建 Sig V4 Canonical Request。
// signedHeadersList 是从 Authorization 头解析出的签名头列表。
func buildCanonicalRequest(r *http.Request, signedHeadersList []string, payloadHash string) string {
	canonicalURI := getCanonicalURI(r.URL.Path)
	canonicalQueryString := getCanonicalQueryString(r.URL.Query())
	canonicalHeaders := buildCanonicalHeaders(r, signedHeadersList)

	return strings.Join([]string{
		r.Method,
		canonicalURI,
		canonicalQueryString,
		canonicalHeaders,
		"",
		strings.Join(signedHeadersList, ";"),
		payloadHash,
	}, "\n")
}

// buildCanonicalHeaders 根据 SignedHeaders 列表构建规范头字符串。
// 每个头格式为 "lowercase-name:value\n"，按 signedHeadersList 顺序排列。
func buildCanonicalHeaders(r *http.Request, signedHeadersList []string) string {
	var sb strings.Builder
	for _, k := range signedHeadersList {
		sb.WriteString(k)
		sb.WriteByte(':')
		switch k {
		case "host":
			host := r.Host
			if host == "" {
				host = r.URL.Host
			}
			sb.WriteString(host)
		default:
			// r.Header 使用 canonical MIME 头编码（首字母大写），
			// 但 Go 的 Header.Get 对大小写不敏感。
			if vals := r.Header.Values(k); len(vals) > 0 {
				sb.WriteString(strings.Join(vals, ","))
			}
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

// buildStringToSign 构建 Sig V4 String to Sign。
func buildStringToSign(amzDate, scope, canonicalRequest string) string {
	return strings.Join([]string{
		v4Algorithm,
		amzDate,
		scope,
		hexSHA256([]byte(canonicalRequest)),
	}, "\n")
}

// getCanonicalURI 对 URI 路径进行编码（S3 规范：每个路径段独立编码）。
func getCanonicalURI(path string) string {
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

// getCanonicalQueryString 构建排序后的查询字符串。
func getCanonicalQueryString(qs url.Values) string {
	if len(qs) == 0 {
		return ""
	}
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

// parseV4AuthHeader 解析 V4 Authorization 头。
// 格式：AWS4-HMAC-SHA256 Credential=AKID/scope, SignedHeaders=headers, Signature=sig
func parseV4AuthHeader(authHeader string) (credential, signedHeaders, signature string, ok bool) {
	if !strings.HasPrefix(authHeader, v4Algorithm+" ") {
		return
	}
	rest := strings.TrimPrefix(authHeader, v4Algorithm+" ")

	parts := strings.Split(rest, ", ")
	if len(parts) < 3 {
		return
	}

	for _, p := range parts {
		if strings.HasPrefix(p, "Credential=") {
			credential = strings.TrimPrefix(p, "Credential=")
		} else if strings.HasPrefix(p, "SignedHeaders=") {
			signedHeaders = strings.TrimPrefix(p, "SignedHeaders=")
		} else if strings.HasPrefix(p, "Signature=") {
			signature = strings.TrimPrefix(p, "Signature=")
		}
	}

	if credential == "" || signedHeaders == "" || signature == "" {
		return
	}
	ok = true
	return
}

// parseCredential 解析 Credential 字段。
// 格式：AKID/YYYYMMDD/region/service/aws4_request
func parseCredential(credential string) (accessKey, dateStamp, region string, valid bool) {
	parts := strings.Split(credential, "/")
	if len(parts) != 5 {
		return
	}
	if parts[3] != v4Service || parts[4] != v4Request {
		return
	}
	accessKey = parts[0]
	dateStamp = parts[1]
	region = parts[2]
	valid = true
	return
}

// authenticateV4 验证 Sig V4 请求。
func (a *Authenticator) authenticateV4(r *http.Request, bucket, key string) *s3error.S3APIError {
	authHeader := r.Header.Get("Authorization")

	credential, signedHeaders, providedSig, ok := parseV4AuthHeader(authHeader)
	if !ok {
		return s3error.ErrSignatureDoesNotMatch
	}

	accessKey, dateStamp, region, valid := parseCredential(credential)
	if !valid {
		return s3error.ErrSignatureDoesNotMatch
	}

	if accessKey != a.accessKey {
		return s3error.ErrAccessDenied
	}

	_, amzDate, err := getV4DateTime(r)
	if err != nil {
		return err
	}

	// 时间偏移检查。
	reqTime, parseErr := time.Parse("20060102T150405Z", amzDate)
	if parseErr != nil {
		return s3error.ErrRequestTimeTooSkewed
	}
	if time.Since(reqTime).Abs() > maxTimeSkew {
		return s3error.ErrRequestTimeTooSkewed
	}

	// 从 SignedHeaders 解析签名头列表，用于构建 canonical request。
	signedHeadersList := strings.Split(signedHeaders, ";")

	scope := fmt.Sprintf("%s/%s/%s/%s", dateStamp, region, v4Service, v4Request)
	canonicalRequest := buildCanonicalRequest(r, signedHeadersList, "UNSIGNED-PAYLOAD")
	stringToSign := buildStringToSign(amzDate, scope, canonicalRequest)
	signingKey := deriveSigningKey(a.secretKey, dateStamp, region)
	expectedSig := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	if !hmac.Equal([]byte(expectedSig), []byte(providedSig)) {
		return s3error.ErrSignatureDoesNotMatch
	}

	return nil
}

// hmacSHA256 计算 HMAC-SHA256。
func hmacSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

// hexSHA256 计算数据的 SHA-256 十六进制摘要。
func hexSHA256(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
