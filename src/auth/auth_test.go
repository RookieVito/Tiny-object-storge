package auth

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	testAccessKey = "testkey"
	testSecretKey = "testsecret"
	testRegion    = "us-east-1"
)

// --- Sig V2 签名辅助函数 ---

func signV2(method, bucket, key, date, accessKey, secretKey string) string {
	canonicalResource := "/" + bucket
	if key != "" {
		canonicalResource += "/" + key
	}
	stringToSign := strings.Join([]string{method, "", "", date, canonicalResource}, "\n")
	mac := hmac.New(sha1.New, []byte(secretKey))
	mac.Write([]byte(stringToSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func makeV2Request(method, bucket, key, date string) *http.Request {
	var path string
	if key != "" {
		path = "/" + bucket + "/" + key
	} else {
		path = "/" + bucket
	}
	r, _ := http.NewRequest(method, path, nil)
	r.Header.Set("Date", date)
	r.Header.Set("Authorization", "AWS "+testAccessKey+":"+signV2(method, bucket, key, date, testAccessKey, testSecretKey))
	return r
}

// --- Sig V2 测试 ---

func TestAuthenticate_V2_BasicBucket(t *testing.T) {
	auth := NewAuthenticator(testAccessKey, testSecretKey)
	r := makeV2Request("GET", "mybucket", "", "Mon, 01 Jan 2024 00:00:00 GMT")
	if err := auth.Authenticate(r, "mybucket", ""); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestAuthenticate_V2_BasicObject(t *testing.T) {
	auth := NewAuthenticator(testAccessKey, testSecretKey)
	r := makeV2Request("PUT", "mybucket", "mykey", "Mon, 01 Jan 2024 00:00:00 GMT")
	if err := auth.Authenticate(r, "mybucket", "mykey"); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestAuthenticate_V2_WrongAccessKey(t *testing.T) {
	auth := NewAuthenticator(testAccessKey, testSecretKey)
	r := makeV2Request("GET", "mybucket", "", "Mon, 01 Jan 2024 00:00:00 GMT")
	// 篡改 access key
	r.Header.Set("Authorization", "AWS wrongkey:"+signV2("GET", "mybucket", "", "Mon, 01 Jan 2024 00:00:00 GMT", testAccessKey, testSecretKey))
	err := auth.Authenticate(r, "mybucket", "")
	if err == nil || err.Code != "AccessDenied" {
		t.Fatalf("expected AccessDenied, got %v", err)
	}
}

func TestAuthenticate_V2_WrongSignature(t *testing.T) {
	auth := NewAuthenticator(testAccessKey, testSecretKey)
	r, _ := http.NewRequest("GET", "/mybucket", nil)
	r.Header.Set("Date", "Mon, 01 Jan 2024 00:00:00 GMT")
	r.Header.Set("Authorization", "AWS "+testAccessKey+":invalidsignature")
	err := auth.Authenticate(r, "mybucket", "")
	if err == nil || err.Code != "SignatureDoesNotMatch" {
		t.Fatalf("expected SignatureDoesNotMatch, got %v", err)
	}
}

func TestAuthenticate_V2_NoAuthorization(t *testing.T) {
	auth := NewAuthenticator(testAccessKey, testSecretKey)
	r, _ := http.NewRequest("GET", "/mybucket", nil)
	err := auth.Authenticate(r, "mybucket", "")
	if err == nil || err.Code != "AccessDenied" {
		t.Fatalf("expected AccessDenied, got %v", err)
	}
}

func TestAuthenticate_V2_InvalidFormat(t *testing.T) {
	auth := NewAuthenticator(testAccessKey, testSecretKey)
	r, _ := http.NewRequest("GET", "/mybucket", nil)
	r.Header.Set("Authorization", "AWS nosig") // 缺少冒号
	err := auth.Authenticate(r, "mybucket", "")
	if err == nil || err.Code != "SignatureDoesNotMatch" {
		t.Fatalf("expected SignatureDoesNotMatch, got %v", err)
	}
}

func TestAuthenticate_V2_UnknownPrefix(t *testing.T) {
	auth := NewAuthenticator(testAccessKey, testSecretKey)
	r, _ := http.NewRequest("GET", "/mybucket", nil)
	r.Header.Set("Authorization", "Bearer some-token")
	err := auth.Authenticate(r, "mybucket", "")
	if err == nil || err.Code != "AccessDenied" {
		t.Fatalf("expected AccessDenied, got %v", err)
	}
}

func TestAuthenticate_V2_WithContentMD5(t *testing.T) {
	auth := NewAuthenticator(testAccessKey, testSecretKey)
	date := "Mon, 01 Jan 2024 00:00:00 GMT"
	contentMD5 := "d41d8cd98f00b204e9800998ecf8427e"
	canonicalResource := "/mybucket/mykey"
	stringToSign := strings.Join([]string{"PUT", contentMD5, "", date, canonicalResource}, "\n")
	mac := hmac.New(sha1.New, []byte(testSecretKey))
	mac.Write([]byte(stringToSign))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	r, _ := http.NewRequest("PUT", "/mybucket/mykey", nil)
	r.Header.Set("Date", date)
	r.Header.Set("Content-MD5", contentMD5)
	r.Header.Set("Authorization", "AWS "+testAccessKey+":"+sig)
	if err := auth.Authenticate(r, "mybucket", "mykey"); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestAuthenticate_V2_UseXAmzDate(t *testing.T) {
	auth := NewAuthenticator(testAccessKey, testSecretKey)
	// Date 为空时 fallback 到 X-Amz-Date
	date := "20240101T000000Z"
	canonicalResource := "/mybucket/mykey"
	stringToSign := strings.Join([]string{"GET", "", "", date, canonicalResource}, "\n")
	mac := hmac.New(sha1.New, []byte(testSecretKey))
	mac.Write([]byte(stringToSign))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	r, _ := http.NewRequest("GET", "/mybucket/mykey", nil)
	r.Header.Set("X-Amz-Date", date)
	r.Header.Set("Authorization", "AWS "+testAccessKey+":"+sig)
	if err := auth.Authenticate(r, "mybucket", "mykey"); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

// --- Sig V4 辅助函数 ---

func signV4(method, bucket, key string, payloadHash string, r *http.Request, accessKey, secretKey, region string) string {
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	signedHeaders := "host;x-amz-date"

	host := "localhost:9000"
	r.Host = host
	r.Header.Set("X-Amz-Date", amzDate)

	canonicalURI := "/" + bucket
	if key != "" {
		canonicalURI += "/" + url.QueryEscape(key)
	}
	canonicalQS := ""
	canonicalHeaders := "host:" + host + "\n" + "x-amz-date:" + amzDate + "\n"

	canonicalRequest := strings.Join([]string{
		method, canonicalURI, canonicalQS, canonicalHeaders, "",
		signedHeaders, payloadHash,
	}, "\n")

	scope := dateStamp + "/" + region + "/s3/aws4_request"
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256", amzDate, scope, hexSHA256([]byte(canonicalRequest)),
	}, "\n")

	signingKey := deriveSigningKey(secretKey, dateStamp, region)
	return hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))
}

func makeV4Request(method, bucket, key string) *http.Request {
	var path string
	if key != "" {
		path = "/" + bucket + "/" + key
	} else {
		path = "/" + bucket
	}
	r, _ := http.NewRequest(method, path, nil)
	sig := signV4(method, bucket, key, "UNSIGNED-PAYLOAD", r, testAccessKey, testSecretKey, testRegion)
	amzDate := r.Header.Get("X-Amz-Date")
	dateStamp := amzDate[:8]
	credential := testAccessKey + "/" + dateStamp + "/" + testRegion + "/s3/aws4_request"
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+credential+", SignedHeaders=host;x-amz-date, Signature="+sig)
	return r
}

// --- Sig V4 测试 ---

func TestAuthenticate_V4_BasicBucket(t *testing.T) {
	auth := NewAuthenticatorWithRegion(testAccessKey, testSecretKey, testRegion)
	r := makeV4Request("GET", "mybucket", "")
	if err := auth.Authenticate(r, "mybucket", ""); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestAuthenticate_V4_BasicObject(t *testing.T) {
	auth := NewAuthenticatorWithRegion(testAccessKey, testSecretKey, testRegion)
	r := makeV4Request("PUT", "mybucket", "mykey")
	if err := auth.Authenticate(r, "mybucket", "mykey"); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestAuthenticate_V4_WrongAccessKey(t *testing.T) {
	auth := NewAuthenticatorWithRegion(testAccessKey, testSecretKey, testRegion)
	r := makeV4Request("GET", "mybucket", "")
	// 修改 Authorization 中的 access key
	authHeader := r.Header.Get("Authorization")
	wrongAuth := strings.Replace(authHeader, testAccessKey, "wrongkey", 1)
	r.Header.Set("Authorization", wrongAuth)
	err := auth.Authenticate(r, "mybucket", "")
	if err == nil || err.Code != "AccessDenied" {
		t.Fatalf("expected AccessDenied, got %v", err)
	}
}

func TestAuthenticate_V4_WrongSignature(t *testing.T) {
	auth := NewAuthenticatorWithRegion(testAccessKey, testSecretKey, testRegion)
	r := makeV4Request("GET", "mybucket", "")
	authHeader := r.Header.Get("Authorization")
	wrongAuth := strings.Replace(authHeader, "Signature=", "Signature=badsig", 1)
	r.Header.Set("Authorization", wrongAuth)
	err := auth.Authenticate(r, "mybucket", "")
	if err == nil || err.Code != "SignatureDoesNotMatch" {
		t.Fatalf("expected SignatureDoesNotMatch, got %v", err)
	}
}

func TestAuthenticate_V4_MissingAmzDate(t *testing.T) {
	auth := NewAuthenticatorWithRegion(testAccessKey, testSecretKey, testRegion)
	r, _ := http.NewRequest("GET", "/mybucket", nil)
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=testkey/20240101/us-east-1/s3/aws4_request, SignedHeaders=host, Signature=abc")
	err := auth.Authenticate(r, "mybucket", "")
	if err == nil || err.Code != "MissingSecurityHeader" {
		t.Fatalf("expected MissingSecurityHeader, got %v", err)
	}
}

func TestAuthenticate_V4_InvalidAuthFormat(t *testing.T) {
	auth := NewAuthenticatorWithRegion(testAccessKey, testSecretKey, testRegion)
	r, _ := http.NewRequest("GET", "/mybucket", nil)
	r.Header.Set("X-Amz-Date", "20240101T000000Z")
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 incomplete")
	err := auth.Authenticate(r, "mybucket", "")
	if err == nil || err.Code != "SignatureDoesNotMatch" {
		t.Fatalf("expected SignatureDoesNotMatch, got %v", err)
	}
}

func TestAuthenticate_V4_InvalidCredential(t *testing.T) {
	auth := NewAuthenticatorWithRegion(testAccessKey, testSecretKey, testRegion)
	r, _ := http.NewRequest("GET", "/mybucket", nil)
	r.Header.Set("X-Amz-Date", "20240101T000000Z")
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=badformat, SignedHeaders=host, Signature=abc")
	err := auth.Authenticate(r, "mybucket", "")
	if err == nil || err.Code != "SignatureDoesNotMatch" {
		t.Fatalf("expected SignatureDoesNotMatch, got %v", err)
	}
}

func TestAuthenticate_V4_WrongRegion(t *testing.T) {
	// V4 签名验证使用请求 credential 中的 region 派生 signing key，
	// authenticator 的 region 不影响验证（仅用于 presign 生成）。
	// 这里验证 authenticator 用不同 region 仍能通过 us-east-1 的签名。
	auth := NewAuthenticatorWithRegion(testAccessKey, testSecretKey, "ap-southeast-1")
	r := makeV4Request("GET", "mybucket", "")
	// makeV4Request 用 testRegion (us-east-1) 签名，authenticator 是 ap-southeast-1
	// 但 authenticateV4 从 credential 解析 region 并用它派生 key → 应该通过
	err := auth.Authenticate(r, "mybucket", "")
	if err != nil {
		t.Fatalf("V4 uses request credential region for key derivation, got %v", err)
	}
}

// --- Presigned URL 测试 ---

func TestPresignV4_ValidURL(t *testing.T) {
	auth := NewAuthenticatorWithRegion(testAccessKey, testSecretKey, testRegion)
	result, err := auth.PresignV4("GET", "mybucket", "mykey", 3600*time.Second, "localhost:9000")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.URL == "" {
		t.Fatal("expected non-empty URL")
	}
	if result.ExpiresSeconds != 3600 {
		t.Fatalf("expected 3600, got %d", result.ExpiresSeconds)
	}
	// 验证 URL 包含必要参数
	if !strings.Contains(result.URL, "X-Amz-Algorithm=AWS4-HMAC-SHA256") {
		t.Fatal("missing algorithm in presigned URL")
	}
	if !strings.Contains(result.URL, "X-Amz-Signature=") {
		t.Fatal("missing signature in presigned URL")
	}
	if !strings.Contains(result.URL, "X-Amz-Credential=") {
		t.Fatal("missing credential in presigned URL")
	}
	if !strings.Contains(result.URL, "X-Amz-Expires=3600") {
		t.Fatal("missing expires in presigned URL")
	}
}

func TestPresignV4_ExpiresTooLong(t *testing.T) {
	auth := NewAuthenticatorWithRegion(testAccessKey, testSecretKey, testRegion)
	_, err := auth.PresignV4("GET", "mybucket", "mykey", 86400*8*time.Second, "localhost:9000")
	if err == nil {
		t.Fatal("expected error for expires > 7 days")
	}
}

func TestPresignV4_ExpiresZero(t *testing.T) {
	auth := NewAuthenticatorWithRegion(testAccessKey, testSecretKey, testRegion)
	_, err := auth.PresignV4("GET", "mybucket", "mykey", 0, "localhost:9000")
	if err == nil {
		t.Fatal("expected error for expires = 0")
	}
}

func TestPresignV4_CustomScheme(t *testing.T) {
	auth := NewAuthenticatorWithRegion(testAccessKey, testSecretKey, testRegion)
	result, err := auth.PresignV4("GET", "mybucket", "mykey", 3600*time.Second, "localhost:9000", "https")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(result.URL, "https://") {
		t.Fatalf("expected https scheme, got: %s", result.URL[:10])
	}
}

func TestAuthenticate_Presigned_Valid(t *testing.T) {
	auth := NewAuthenticatorWithRegion(testAccessKey, testSecretKey, testRegion)
	result, err := auth.PresignV4("GET", "mybucket", "mykey", 3600*time.Second, "localhost:9000")
	if err != nil {
		t.Fatalf("unexpected presign error: %v", err)
	}

	// 用生成的 presigned URL 构造请求
	u, _ := url.Parse(result.URL)
	r, _ := http.NewRequest("GET", u.RequestURI(), nil)
	r.Host = "localhost:9000"
	qs := r.URL.Query()
	for k, vv := range u.Query() {
		for _, v := range vv {
			qs.Set(k, v)
		}
	}
	r.URL.RawQuery = qs.Encode()

	if err := auth.Authenticate(r, "mybucket", "mykey"); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestAuthenticate_Presigned_Expired(t *testing.T) {
	auth := NewAuthenticatorWithRegion(testAccessKey, testSecretKey, testRegion)

	// 手动构造一个已过期的 presigned URL
	qs := url.Values{}
	qs.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	qs.Set("X-Amz-Credential", testAccessKey+"/20230101T000000Z/us-east-1/s3/aws4_request")
	qs.Set("X-Amz-Date", "20230101T000000Z")
	qs.Set("X-Amz-Expires", "1")
	qs.Set("X-Amz-SignedHeaders", "host")
	qs.Set("X-Amz-Signature", "abc")

	r, _ := http.NewRequest("GET", "/mybucket/mykey?"+qs.Encode(), nil)
	r.Host = "localhost:9000"
	err := auth.Authenticate(r, "mybucket", "mykey")
	if err == nil || err.Code != "AccessDenied" {
		t.Fatalf("expected AccessDenied (expired), got %v", err)
	}
}

func TestAuthenticate_Presigned_InvalidExpires(t *testing.T) {
	auth := NewAuthenticatorWithRegion(testAccessKey, testSecretKey, testRegion)

	qs := url.Values{}
	qs.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	qs.Set("X-Amz-Credential", testAccessKey+"/20240101/us-east-1/s3/aws4_request")
	qs.Set("X-Amz-Date", "20240101T000000Z")
	qs.Set("X-Amz-Expires", "0")
	qs.Set("X-Amz-SignedHeaders", "host")
	qs.Set("X-Amz-Signature", "abc")

	r, _ := http.NewRequest("GET", "/mybucket/mykey?"+qs.Encode(), nil)
	err := auth.Authenticate(r, "mybucket", "mykey")
	if err == nil || err.Code != "InvalidArgument" {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestAuthenticate_Presigned_MissingSignature(t *testing.T) {
	auth := NewAuthenticatorWithRegion(testAccessKey, testSecretKey, testRegion)

	// 使用当前时间避免过期
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	qs := url.Values{}
	qs.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	qs.Set("X-Amz-Credential", testAccessKey+"/"+dateStamp+"/us-east-1/s3/aws4_request")
	qs.Set("X-Amz-Date", amzDate)
	qs.Set("X-Amz-Expires", strconv.FormatInt(maxPresignExpires, 10))
	qs.Set("X-Amz-SignedHeaders", "host")
	// 缺少 X-Amz-Signature

	r, _ := http.NewRequest("GET", "/mybucket/mykey?"+qs.Encode(), nil)
	r.Host = "localhost:9000"
	err := auth.Authenticate(r, "mybucket", "mykey")
	if err == nil || err.Code != "SignatureDoesNotMatch" {
		t.Fatalf("expected SignatureDoesNotMatch, got %v", err)
	}
}

// --- V4 纯函数测试 ---

func TestHexSHA256(t *testing.T) {
	// 空 SHA-256 的已知值
	expected := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	got := hexSHA256([]byte{})
	if got != expected {
		t.Fatalf("expected %s, got %s", expected, got)
	}
}

func TestHexSHA256_Hello(t *testing.T) {
	h := sha256.Sum256([]byte("hello"))
	expected := hex.EncodeToString(h[:])
	got := hexSHA256([]byte("hello"))
	if got != expected {
		t.Fatalf("expected %s, got %s", expected, got)
	}
}

func TestDeriveSigningKey(t *testing.T) {
	key := deriveSigningKey("secret", "20240101", "us-east-1")
	if len(key) != 32 {
		t.Fatalf("expected 32-byte key, got %d", len(key))
	}
	// 同样输入应产生相同结果
	key2 := deriveSigningKey("secret", "20240101", "us-east-1")
	if hex.EncodeToString(key) != hex.EncodeToString(key2) {
		t.Fatal("deterministic key derivation failed")
	}
}

func TestGetCanonicalURI(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"/mybucket/mykey", "/mybucket/mykey"},
		{"/mybucket/path with spaces/key", "/mybucket/path+with+spaces/key"},
		{"", "/"},
		{"/bucket/key%2Fwith%2Fslash", "/bucket/key%252Fwith%252Fslash"},
	}
	for _, tc := range tests {
		got := getCanonicalURI(tc.input)
		if got != tc.expected {
			t.Errorf("getCanonicalURI(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestGetCanonicalQueryString(t *testing.T) {
	qs := url.Values{}
	qs.Set("prefix", "photos/")
	qs.Set("max-keys", "100")
	qs.Set("delimiter", "/")

	got := getCanonicalQueryString(qs)
	// 应按字母排序：delimiter, max-keys, prefix
	if !strings.HasPrefix(got, "delimiter=") {
		t.Errorf("expected sorted query string, got: %s", got)
	}
}

func TestParseV4AuthHeader(t *testing.T) {
	tests := []struct {
		input   string
		ok      bool
		credLen int // credential 中 / 的数量应为 4
	}{
		{"AWS4-HMAC-SHA256 Credential=AKID/date/region/s3/aws4_request, SignedHeaders=host, Signature=sig", true, 4},
		{"AWS4-HMAC-SHA256 incomplete", false, 0},
		{"invalid-prefix Credential=AKID/date/region/s3/aws4_request, SignedHeaders=host, Signature=sig", false, 0},
		{"AWS4-HMAC-SHA256 Credential=, SignedHeaders=, Signature=", false, 0},
	}
	for _, tc := range tests {
		_, _, _, ok := parseV4AuthHeader(tc.input)
		if ok != tc.ok {
			t.Errorf("parseV4AuthHeader(%q) ok=%v, want %v", tc.input, ok, tc.ok)
		}
		if ok && tc.credLen > 0 {
			cred, _, _, _ := parseV4AuthHeader(tc.input)
			if strings.Count(cred, "/") != tc.credLen {
				t.Errorf("parseV4AuthHeader(%q) credential parts=%d, want %d", tc.input, strings.Count(cred, "/"), tc.credLen)
			}
		}
	}
}

func TestParseCredential(t *testing.T) {
	tests := []struct {
		input     string
		valid     bool
		accessKey string
		region    string
	}{
		{"AKID/20240101/us-east-1/s3/aws4_request", true, "AKID", "us-east-1"},
		{"AKID/20240101/ap-southeast-1/s3/aws4_request", true, "AKID", "ap-southeast-1"},
		{"invalid-format", false, "", ""},
		{"AKID/20240101/region/wrong/aws4_request", false, "", ""},
		{"AKID/20240101/region/s3/wrong", false, "", ""},
	}
	for _, tc := range tests {
		ak, _, region, valid := parseCredential(tc.input)
		if valid != tc.valid {
			t.Errorf("parseCredential(%q) valid=%v, want %v", tc.input, valid, tc.valid)
		}
		if valid && (ak != tc.accessKey || region != tc.region) {
			t.Errorf("parseCredential(%q) ak=%s region=%s, want ak=%s region=%s", tc.input, ak, region, tc.accessKey, tc.region)
		}
	}
}

func TestGetV4DateTime(t *testing.T) {
	r, _ := http.NewRequest("GET", "/", nil)
	r.Header.Set("X-Amz-Date", "20240101T120000Z")
	ds, full, err := getV4DateTime(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ds != "20240101" {
		t.Errorf("expected datestamp 20240101, got %s", ds)
	}
	if full != "20240101T120000Z" {
		t.Errorf("expected full 20240101T120000Z, got %s", full)
	}
}

func TestGetV4DateTime_Missing(t *testing.T) {
	r, _ := http.NewRequest("GET", "/", nil)
	_, _, err := getV4DateTime(r)
	if err == nil || err.Code != "MissingSecurityHeader" {
		t.Fatalf("expected MissingSecurityHeader, got %v", err)
	}
}

func TestSplitSignedHeaders(t *testing.T) {
	tests := []struct {
		input string
		count int
	}{
		{"host;x-amz-date", 2},
		{"host", 1},
		{"", 0},
		{"host;;x-amz-date", 2},
	}
	for _, tc := range tests {
		got := splitSignedHeaders(tc.input)
		if len(got) != tc.count {
			t.Errorf("splitSignedHeaders(%q) count=%d, want %d", tc.input, len(got), tc.count)
		}
	}
}

func TestBuildCanonicalHeaders(t *testing.T) {
	r, _ := http.NewRequest("GET", "/", nil)
	r.Host = "example.com"
	r.Header.Set("Content-Type", "application/xml")
	r.Header.Set("X-Amz-Date", "20240101T000000Z")

	headers := buildCanonicalHeaders(r, []string{"content-type", "host", "x-amz-date"})
	// 应按字母排序
	if !strings.HasPrefix(headers, "content-type:") {
		t.Errorf("expected sorted headers, got: %s", headers)
	}
	if !strings.Contains(headers, "host:example.com") {
		t.Errorf("missing host header, got: %s", headers)
	}
}

// --- NewAuthenticator 默认 region ---

func TestNewAuthenticator_DefaultRegion(t *testing.T) {
	auth := NewAuthenticator("key", "secret")
	if auth.region != "us-east-1" {
		t.Fatalf("expected default region us-east-1, got %s", auth.region)
	}
}

func TestNewAuthenticatorWithRegion_Custom(t *testing.T) {
	auth := NewAuthenticatorWithRegion("key", "secret", "ap-northeast-1")
	if auth.region != "ap-northeast-1" {
		t.Fatalf("expected ap-northeast-1, got %s", auth.region)
	}
}
