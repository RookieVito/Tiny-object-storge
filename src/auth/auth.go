package auth

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"net/http"
	"strings"

	"tiny-object-storage/src/s3error"
)

// Authenticator 验证 AWS Signature V2/V4 请求。
type Authenticator struct {
	accessKey string
	secretKey string
	region    string
}

// NewAuthenticator 使用给定凭证创建 Authenticator。
func NewAuthenticator(accessKey, secretKey string) *Authenticator {
	return &Authenticator{accessKey: accessKey, secretKey: secretKey, region: "us-east-1"}
}

// NewAuthenticatorWithRegion 使用给定凭证和 region 创建 Authenticator。
func NewAuthenticatorWithRegion(accessKey, secretKey, region string) *Authenticator {
	return &Authenticator{accessKey: accessKey, secretKey: secretKey, region: region}
}

// Authenticate 验证 Authorization 头，按前缀分发 V4 或 V2。
// 成功返回 nil，失败返回 *s3error.S3APIError。
func (a *Authenticator) Authenticate(r *http.Request, bucket, key string) *s3error.S3APIError {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return s3error.ErrAccessDenied
	}

	// Sig V4
	if strings.HasPrefix(authHeader, "AWS4-HMAC-SHA256") {
		return a.authenticateV4(r, bucket, key)
	}

	// Sig V2 fallback
	if !strings.HasPrefix(authHeader, "AWS ") {
		return s3error.ErrAccessDenied
	}

	rest := strings.TrimPrefix(authHeader, "AWS ")
	idx := strings.Index(rest, ":")
	if idx < 0 {
		return s3error.ErrSignatureDoesNotMatch
	}

	providedAccessKey := rest[:idx]
	providedSignature := rest[idx+1:]

	if providedAccessKey != a.accessKey {
		return s3error.ErrAccessDenied
	}

	// 构建 StringToSign。
	verb := r.Method
	contentMD5 := r.Header.Get("Content-MD5")
	contentType := r.Header.Get("Content-Type")
	date := r.Header.Get("Date")
	if date == "" {
		date = r.Header.Get("X-Amz-Date")
	}

	canonicalResource := "/" + bucket
	if key != "" {
		canonicalResource += "/" + key
	}

	stringToSign := strings.Join([]string{verb, contentMD5, contentType, date, canonicalResource}, "\n")

	// 计算期望签名。
	mac := hmac.New(sha1.New, []byte(a.secretKey))
	mac.Write([]byte(stringToSign))
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(expected), []byte(providedSignature)) {
		return s3error.ErrSignatureDoesNotMatch
	}

	return nil
}
