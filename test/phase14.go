package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func init() {
	registerTest("Phase 14", testPhase14)
}

// p14Do 发送 V2 签名请求到指定 base URL。
func p14Do(baseURL, method, path, body, contentType string) (int, string, http.Header) {
	resource := path
	if idx := strings.Index(path, "?"); idx >= 0 {
		resource = path[:idx]
	}

	h := map[string]string{
		"Authorization": fmt.Sprintf("AWS %s:%s", AccessKey, Sig(method, resource, contentType)),
		"Date":          time.Now().UTC().Format(time.RFC1123),
		"Host":          strings.TrimPrefix(baseURL, "http://"),
	}
	if contentType != "" {
		h["Content-Type"] = contentType
	}

	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, baseURL+path, bodyReader)
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

func testPhase14() {
	tmpDir, err := os.MkdirTemp("", "phase14-*")
	if err != nil {
		Pass("TTL: create temp dir", false)
		return
	}
	defer os.RemoveAll(tmpDir)

	port := 19301
	baseURL := fmt.Sprintf("http://localhost:%d", port)

	// 预编译服务器二进制。
	binPath := filepath.Join(tmpDir, "server")
	{
		buildCmd := exec.Command("go", "build", "-o", binPath, "./cmd/server/")
		buildCmd.Dir = "/home/vito/workspace/tiny-object-storge"
		if out, err := buildCmd.CombinedOutput(); err != nil {
			Pass("TTL: build server", false)
			fmt.Fprintf(os.Stderr, "build error: %s\n%s\n", err, string(out))
			return
		}
		Pass("TTL: build server", true)
	}

	// 确保端口未被占用。
	for i := 0; i < 10; i++ {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), 100*time.Millisecond)
		if err != nil {
			break
		}
		conn.Close()
		time.Sleep(500 * time.Millisecond)
	}

	// 创建配置文件（短 TTL: 3s，扫描间隔: 1s）。
	cfg := map[string]interface{}{
		"port":                  port,
		"backend_type":          "local",
		"access_key":            AccessKey,
		"secret_key":            SecretKey,
		"multipart_ttl_seconds": 3,
		"cleanup_interval_sec":  1,
	}
	cfgJSON, _ := json.Marshal(cfg)
	cfgPath := filepath.Join(tmpDir, "config.json")
	os.WriteFile(cfgPath, cfgJSON, 0644)

	rootDir := filepath.Join(tmpDir, "data")
	os.MkdirAll(rootDir, 0755)

	// 启动服务器。
	cmd := exec.Command(binPath, "--config", cfgPath, "--root", rootDir)
	cmd.Dir = "/home/vito/workspace/tiny-object-storge"
	logFile, _ := os.Create(filepath.Join(tmpDir, "server.log"))
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Start()
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()

	// 等待服务器启动。
	for i := 0; i < 20; i++ {
		time.Sleep(200 * time.Millisecond)
		resp, err := http.Get(baseURL + "/_metrics")
		if err == nil {
			resp.Body.Close()
			break
		}
	}

	bucket := "p14-bucket"
	ct := "application/octet-stream"

	// 创建 bucket。
	s, _, _ := p14Do(baseURL, "PUT", "/"+bucket, "", "")
	Pass("TTL: CreateBucket", s == 200)
	defer p14Do(baseURL, "DELETE", "/"+bucket, "", "")

	// ============================================================
	// 测试 1：过期 upload 被自动清理
	// ============================================================
	{
		key := "expire-test.bin"
		s, body, _ := p14Do(baseURL, "POST", "/"+bucket+"/"+key+"?uploads", "", ct)
		Pass("TTL expire: InitiateUpload → 200", s == 200)

		type initRes struct {
			UploadId string `xml:"UploadId"`
		}
		var ir initRes
		xml.Unmarshal([]byte(body), &ir)
		uploadId := ir.UploadId
		Pass("TTL expire: UploadId returned", uploadId != "")

		// 验证 upload 存在。
		_, body, _ = p14Do(baseURL, "GET", "/"+bucket+"?uploads", "", "")
		Pass("TTL expire: visible before expiry", strings.Contains(body, uploadId))

		// 等待超过 TTL（3s）+ 扫描间隔（1s）+ 缓冲。
		time.Sleep(5 * time.Second)

		// 验证 upload 被清理。
		_, body, _ = p14Do(baseURL, "GET", "/"+bucket+"?uploads", "", "")
		Pass("TTL expire: cleaned after expiry", !strings.Contains(body, uploadId))
	}

	// ============================================================
	// 测试 2：未过期 upload 不被清理
	// ============================================================
	{
		key := "active-test.bin"
		s, body, _ := p14Do(baseURL, "POST", "/"+bucket+"/"+key+"?uploads", "", ct)
		Pass("TTL active: InitiateUpload → 200", s == 200)

		type initRes struct {
			UploadId string `xml:"UploadId"`
		}
		var ir initRes
		xml.Unmarshal([]byte(body), &ir)

		// 等待 1s（低于 3s TTL）。
		time.Sleep(1 * time.Second)

		_, body, _ = p14Do(baseURL, "GET", "/"+bucket+"?uploads", "", "")
		Pass("TTL active: still visible before TTL", strings.Contains(body, ir.UploadId))

		// 手动清理。
		p14Do(baseURL, "DELETE", "/"+bucket+"/"+key+"?uploadId="+ir.UploadId, "", "")
	}

	// ============================================================
	// 测试 3：MultipartCleanups metrics 计数器
	// ============================================================
	{
		s, body, _ := p14Do(baseURL, "GET", "/_metrics", "", "")
		Pass("TTL metrics: endpoint → 200", s == 200)

		type metricsResp struct {
			MultipartCleanups int64 `json:"multipart_cleanups"`
		}
		var mr metricsResp
		json.Unmarshal([]byte(body), &mr)
		Pass("TTL metrics: multipart_cleanups > 0", mr.MultipartCleanups > 0)
	}

	// ============================================================
	// 测试 4：完成的 upload 不受 TTL 影响
	// ============================================================
	{
		key := "complete-safe.bin"
		partData := "test-complete-data"

		// 发起 upload。
		s, body, _ := p14Do(baseURL, "POST", "/"+bucket+"/"+key+"?uploads", "", ct)
		Pass("TTL complete: InitiateUpload → 200", s == 200)

		type initRes struct {
			UploadId string `xml:"UploadId"`
		}
		var ir initRes
		xml.Unmarshal([]byte(body), &ir)

		// 上传一个 part 并获取 ETag。
		s, _, hdr := p14Do(baseURL, "PUT", fmt.Sprintf("/%s/%s?partNumber=1&uploadId=%s", bucket, key, ir.UploadId), partData, ct)
		Pass("TTL complete: UploadPart → 200", s == 200)

		etag := hdr.Get("ETag")
		Pass("TTL complete: ETag returned", etag != "")

		completeXML := fmt.Sprintf(`<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>%s</ETag></Part></CompleteMultipartUpload>`, etag)
		s, _, _ = p14Do(baseURL, "POST", fmt.Sprintf("/%s/%s?uploadId=%s", bucket, key, ir.UploadId), completeXML, "application/xml")
		Pass("TTL complete: CompleteUpload → 200", s == 200)

		// 等待超过 TTL。
		time.Sleep(5 * time.Second)

		// 验证对象仍存在。
		s, body, _ = p14Do(baseURL, "GET", "/"+bucket+"/"+key, "", "")
		Pass("TTL complete: object still exists", s == 200 && body == partData)

		p14Do(baseURL, "DELETE", "/"+bucket+"/"+key, "", "")
	}

	P("INFO: Phase 14 TTL auto-cleanup complete")
}
