package auth

import (
	"crypto/hmac"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"tiny-object-storage/src/s3error"
)

const maxPresignExpires = 604800 // 7 天（秒）

// PresignResult 包含预签名 URL 及其组成信息。
type PresignResult struct {
	URL            string
	AmzDate        string
	ExpiresSeconds int64
}

// PresignV4 生成 Sig V4 预签名 URL。
// host 是服务器外部地址（如 "localhost:9000"），不含 scheme。
func (a *Authenticator) PresignV4(method, bucket, key string, expires time.Duration, host string) (*PresignResult, error) {
	expiresSec := int64(expires.Seconds())
	if expiresSec < 1 || expiresSec > maxPresignExpires {
		return nil, fmt.Errorf("X-Amz-Expires must be between 1 and %d", maxPresignExpires)
	}

	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	scope := fmt.Sprintf("%s/%s/%s/%s", dateStamp, a.region, v4Service, v4Request)
	credential := fmt.Sprintf("%s/%s", a.accessKey, scope)

	// Canonical URI。
	canonicalURI := "/" + bucket
	if key != "" {
		canonicalURI += "/" + key
	}
	canonicalURI = getCanonicalURI(canonicalURI)

	// Canonical query string：包含所有 X-Amz-* 参数（不含 Signature），按字母排序。
	signedHeaders := "host"
	qs := url.Values{}
	qs.Set("X-Amz-Algorithm", v4Algorithm)
	qs.Set("X-Amz-Credential", credential)
	qs.Set("X-Amz-Date", amzDate)
	qs.Set("X-Amz-Expires", strconv.FormatInt(expiresSec, 10))
	qs.Set("X-Amz-SignedHeaders", signedHeaders)
	canonicalQS := getCanonicalQueryString(qs)

	// Canonical headers：presigned URL 只签名 host。
	canonicalHeaders := "host:" + host + "\n"

	// Canonical request。
	canonicalRequest := fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n%s\n%s",
		method, canonicalURI, canonicalQS, canonicalHeaders, "",
		signedHeaders, "UNSIGNED-PAYLOAD")

	// String to sign。
	stringToSign := fmt.Sprintf("%s\n%s\n%s\n%s",
		v4Algorithm, amzDate, scope, hexSHA256([]byte(canonicalRequest)))

	// Signature。
	signingKey := deriveSigningKey(a.secretKey, dateStamp, a.region)
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	// 构建完整 URL。
	qs.Set("X-Amz-Signature", signature)
	fullURL := fmt.Sprintf("http://%s%s?%s", host, canonicalURI, qs.Encode())

	return &PresignResult{
		URL:            fullURL,
		AmzDate:        amzDate,
		ExpiresSeconds: expiresSec,
	}, nil
}

// authenticatePresigned 验证预签名 URL 请求。
func (a *Authenticator) authenticatePresigned(r *http.Request, bucket, key string) *s3error.S3APIError {
	qs := r.URL.Query()

	amzDate := qs.Get("X-Amz-Date")
	if amzDate == "" {
		return s3error.ErrMissingSecurityHeader
	}

	expiresStr := qs.Get("X-Amz-Expires")
	expires, err := strconv.ParseInt(expiresStr, 10, 64)
	if err != nil || expires < 1 || expires > maxPresignExpires {
		return s3error.ErrInvalidExpires
	}

	// 过期检查：server time > X-Amz-Date + X-Amz-Expires。
	reqTime, err := time.Parse("20060102T150405Z", amzDate)
	if err != nil {
		return s3error.ErrRequestTimeTooSkewed
	}
	if time.Now().UTC().After(reqTime.Add(time.Duration(expires) * time.Second)) {
		return s3error.ErrExpiredPresign
	}

	// 时间偏移检查。
	if time.Since(reqTime).Abs() > maxTimeSkew {
		return s3error.ErrRequestTimeTooSkewed
	}

	// 解析 Credential。
	credential := qs.Get("X-Amz-Credential")
	accessKey, dateStamp, region, valid := parseCredential(credential)
	if !valid {
		return s3error.ErrSignatureDoesNotMatch
	}
	if accessKey != a.accessKey {
		return s3error.ErrAccessDenied
	}

	// 解析 SignedHeaders。
	signedHeaders := qs.Get("X-Amz-SignedHeaders")
	signedHeadersList := splitSignedHeaders(signedHeaders)

	// 提供的签名。
	providedSig := qs.Get("X-Amz-Signature")

	// 构建 canonical request：从 query params 中去掉 X-Amz-Signature。
	canonicalQS := getPresignCanonicalQueryString(r)

	canonicalURI := getCanonicalURI(r.URL.Path)

	// Canonical headers。
	canonicalHeaders := buildCanonicalHeaders(r, signedHeadersList)

	// Canonical request。
	canonicalRequest := fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n%s\n%s",
		r.Method, canonicalURI, canonicalQS, canonicalHeaders, "",
		signedHeaders, "UNSIGNED-PAYLOAD")

	scope := fmt.Sprintf("%s/%s/%s/%s", dateStamp, region, v4Service, v4Request)
	stringToSign := fmt.Sprintf("%s\n%s\n%s\n%s",
		v4Algorithm, amzDate, scope, hexSHA256([]byte(canonicalRequest)))

	signingKey := deriveSigningKey(a.secretKey, dateStamp, region)
	expectedSig := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	if !hmac.Equal([]byte(expectedSig), []byte(providedSig)) {
		return s3error.ErrSignatureDoesNotMatch
	}

	return nil
}

// getPresignCanonicalQueryString 构建预签名 URL 的 canonical query string。
// 从 query params 中去掉 X-Amz-Signature（签名不参与签名计算）。
func getPresignCanonicalQueryString(r *http.Request) string {
	qs := r.URL.Query()
	qs.Del("X-Amz-Signature")
	return getCanonicalQueryString(qs)
}

// splitSignedHeaders 将分号分隔的签名头列表拆分为字符串切片。
func splitSignedHeaders(s string) []string {
	if s == "" {
		return nil
	}
	parts := []string{}
	for _, p := range splitString(s, ';') {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return parts
}

// splitString 按分隔符拆分字符串。
func splitString(s string, sep byte) []string {
	var parts []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	parts = append(parts, s[start:])
	return parts
}
