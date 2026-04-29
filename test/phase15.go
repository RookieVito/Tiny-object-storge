package main

import (
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
	registerTest("Phase 15", testPhase15)
}

// p15Do 发送 V2 签名请求到指定 base URL。
func p15Do(baseURL, method, path, body, contentType string) (int, string, http.Header) {
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

// p15BuildServer 编译服务器二进制并启动在指定端口，返回 baseURL 和 cleanup 函数。
func p15BuildServer(tmpDir string) (binPath string, baseURL string, cleanup func(), err error) {
	const port = "19501"
	baseURL = "http://localhost:" + port

	binPath = filepath.Join(tmpDir, "server")
	{
		buildCmd := exec.Command("go", "build", "-o", binPath, "./cmd/server/")
		if out, buildErr := buildCmd.CombinedOutput(); buildErr != nil {
			return "", "", nil, fmt.Errorf("build: %v: %s", buildErr, string(out))
		}
	}

	// 等待端口释放（最多 5 秒）。
	for i := 0; i < 10; i++ {
		conn, dialErr := net.DialTimeout("tcp", "localhost:"+port, 100*time.Millisecond)
		if dialErr != nil {
			break
		}
		conn.Close()
		time.Sleep(500 * time.Millisecond)
	}
	// 最终检查：端口仍被占用则报错。
	if conn, _ := net.DialTimeout("tcp", "localhost:"+port, 100*time.Millisecond); conn != nil {
		conn.Close()
		return "", "", nil, fmt.Errorf("port %s is still in use after waiting", port)
	}

	rootDir := filepath.Join(tmpDir, "data")
	os.MkdirAll(rootDir, 0755)

	cmd := exec.Command(binPath, "--root", rootDir, "--port", port)
	logFile, _ := os.Create(filepath.Join(tmpDir, "server.log"))
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if startErr := cmd.Start(); startErr != nil {
		return "", "", nil, fmt.Errorf("start server: %v", startErr)
	}

	// 等待服务器启动。
	for i := 0; i < 20; i++ {
		time.Sleep(200 * time.Millisecond)
		resp, httpErr := http.Get(baseURL + "/_metrics")
		if httpErr == nil {
			resp.Body.Close()
			break
		}
	}

	cleanup = func() {
		cmd.Process.Kill()
		cmd.Wait()
	}
	return binPath, baseURL, cleanup, nil
}

// p15Cleanup 删除 bucket 及其所有对象。
func p15Cleanup(baseURL, bucket string) {
	// 尝试删除 bucket，如果非空则忽略错误。
	p15Do(baseURL, "DELETE", "/"+bucket, "", "")
}

func testPhase15() {
	tmpDir, err := os.MkdirTemp("", "phase15-*")
	if err != nil {
		Pass("V: create temp dir", false)
		return
	}
	defer os.RemoveAll(tmpDir)

	_, baseURL, cleanup, err := p15BuildServer(tmpDir)
	if err != nil {
		Pass("V: build server", false)
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		return
	}
	Pass("V: build server", true)
	defer cleanup()

	ct := "application/octet-stream"
	var (
		s         int
		b         string
		h         http.Header
		vid1, vid2 string
	)

	// ============================================================
	// 1. 未版本化 bucket 向后兼容
	// ============================================================
	{
		bucket := "p15-compat"
		s, _, _ = p15Do(baseURL, "PUT", "/"+bucket, "", "")
		Pass("V compat: CreateBucket", s == 200)

		s, _, _ = p15Do(baseURL, "PUT", "/"+bucket+"/compat.txt", "v1", ct)
		Pass("V compat: PutObject v1", s == 200)

		s, b, _ = p15Do(baseURL, "GET", "/"+bucket+"/compat.txt", "", "")
		Pass("V compat: GetObject v1", s == 200 && b == "v1")

		// 覆盖（未版本化行为）。
		s, _, _ = p15Do(baseURL, "PUT", "/"+bucket+"/compat.txt", "v2", ct)
		Pass("V compat: PutObject v2 overwrite", s == 200)

		s, b, _ = p15Do(baseURL, "GET", "/"+bucket+"/compat.txt", "", "")
		Pass("V compat: GetObject v2 (overwritten)", s == 200 && b == "v2")

		p15Do(baseURL, "DELETE", "/"+bucket+"/compat.txt", "", "")
		s, _, _ = p15Do(baseURL, "GET", "/"+bucket+"/compat.txt", "", "")
		Pass("V compat: GetObject deleted", s == 404)

		p15Cleanup(baseURL, bucket)
	}

	// ============================================================
	// 2. 启用版本控制 + 多版本 PutObject
	// ============================================================
	{
		bucket := "p15-versioned"
		versioningXML := `<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>`

		s, _, _ = p15Do(baseURL, "PUT", "/"+bucket, "", "")
		Pass("V enable: CreateBucket", s == 200)

		s, _, _ = p15Do(baseURL, "PUT", "/"+bucket+"?versioning", versioningXML, "application/xml")
		Pass("V enable: PutBucketVersioning", s == 200)

		s, b, _ = p15Do(baseURL, "GET", "/"+bucket+"?versioning", "", "")
		Pass("V enable: GetBucketVersioning", s == 200 && strings.Contains(b, "Enabled"))

		// 再次启用 → 400
		s, _, _ = p15Do(baseURL, "PUT", "/"+bucket+"?versioning", versioningXML, "application/xml")
		Pass("V enable: duplicate → 400", s == 400)

		s, _, h = p15Do(baseURL, "PUT", "/"+bucket+"/multi.txt", "version1", ct)
		Pass("V multi: PutObject v1", s == 200)
		vid1 = h.Get("x-amz-version-id")
		Pass("V multi: v1 has version-id", vid1 != "")

		s, _, h = p15Do(baseURL, "PUT", "/"+bucket+"/multi.txt", "version2", ct)
		Pass("V multi: PutObject v2", s == 200)
		vid2 = h.Get("x-amz-version-id")
		Pass("V multi: v2 has version-id", vid2 != "")
		Pass("V multi: v1 != v2", vid1 != vid2)

		// 当前应该是 v2。
		s, b, _ = p15Do(baseURL, "GET", "/"+bucket+"/multi.txt", "", "")
		Pass("V multi: GetObject returns v2", s == 200 && b == "version2")

		// 通过 versionId 获取 v1。
		s, b, h = p15Do(baseURL, "GET", "/"+bucket+"/multi.txt?versionId="+vid1, "", "")
		Pass("V multi: GetObject ?versionId=v1", s == 200 && b == "version1")
		Pass("V multi: response version-id header", h.Get("x-amz-version-id") == vid1)

		p15Cleanup(baseURL, bucket)
	}

	// ============================================================
	// 3. Delete marker
	// ============================================================
	{
		bucket := "p15-dm"
		versioningXML := `<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>`

		p15Do(baseURL, "PUT", "/"+bucket, "", "")
		p15Do(baseURL, "PUT", "/"+bucket+"?versioning", versioningXML, "application/xml")

		s, _, h = p15Do(baseURL, "PUT", "/"+bucket+"/multi.txt", "version1", ct)
		vid1 = h.Get("x-amz-version-id")

		s, _, h = p15Do(baseURL, "PUT", "/"+bucket+"/multi.txt", "version2", ct)
		vid2 = h.Get("x-amz-version-id")

		s, _, h = p15Do(baseURL, "DELETE", "/"+bucket+"/multi.txt", "", "")
		Pass("V dm: DeleteObject", s == 204)
		Pass("V dm: x-amz-delete-marker header", h.Get("x-amz-delete-marker") == "true")

		// GetObject → 404（delete marker 激活）。
		s, _, _ = p15Do(baseURL, "GET", "/"+bucket+"/multi.txt", "", "")
		Pass("V dm: GetObject → 404", s == 404)

		// 通过 versionId 仍可访问 v2。
		s, b, _ = p15Do(baseURL, "GET", "/"+bucket+"/multi.txt?versionId="+vid2, "", "")
		Pass("V dm: GetObject v2 still accessible", s == 200 && b == "version2")

		s, b, _ = p15Do(baseURL, "GET", "/"+bucket+"/multi.txt?versionId="+vid1, "", "")
		Pass("V dm: GetObject v1 still accessible", s == 200 && b == "version1")

		// 新写入覆盖 delete marker。
		s, _, h = p15Do(baseURL, "PUT", "/"+bucket+"/multi.txt", "version3", ct)
		Pass("V dm: PutObject v3 after delete marker", s == 200)

		s, b, _ = p15Do(baseURL, "GET", "/"+bucket+"/multi.txt", "", "")
		Pass("V dm: GetObject v3 after delete", s == 200 && b == "version3")

		p15Cleanup(baseURL, bucket)
	}

	// ============================================================
	// 4. DeleteObjectVersion 永久删除
	// ============================================================
	{
		bucket := "p15-delver"
		versioningXML := `<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>`

		p15Do(baseURL, "PUT", "/"+bucket, "", "")
		p15Do(baseURL, "PUT", "/"+bucket+"?versioning", versioningXML, "application/xml")

		s, _, h = p15Do(baseURL, "PUT", "/"+bucket+"/multi.txt", "version1", ct)
		vid1 = h.Get("x-amz-version-id")

		s, _, h = p15Do(baseURL, "PUT", "/"+bucket+"/multi.txt", "version2", ct)
		vid2 = h.Get("x-amz-version-id")

		// 先永久删除 v2。
		s, _, h = p15Do(baseURL, "DELETE", "/"+bucket+"/multi.txt?versionId="+vid2, "", "")
		Pass("V delver: DeleteObjectVersion v2", s == 204)
		Pass("V delver: version-id header", h.Get("x-amz-version-id") == vid2)

		// v2 不再可访问。
		s, _, _ = p15Do(baseURL, "GET", "/"+bucket+"/multi.txt?versionId="+vid2, "", "")
		Pass("V delver: v2 gone → 404", s == 404)

		// v1 仍可访问。
		s, b, _ = p15Do(baseURL, "GET", "/"+bucket+"/multi.txt?versionId="+vid1, "", "")
		Pass("V delver: v1 still accessible", s == 200 && b == "version1")

		p15Cleanup(baseURL, bucket)
	}

	// ============================================================
	// 5. GetBucketVersioning 状态转换
	// ============================================================
	{
		bucket := "p15-vstatus"
		suspendXML := `<VersioningConfiguration><Status>Suspended</Status></VersioningConfiguration>`
		versioningXML := `<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>`

		p15Do(baseURL, "PUT", "/"+bucket, "", "")
		p15Do(baseURL, "PUT", "/"+bucket+"?versioning", versioningXML, "application/xml")

		s, _, _ = p15Do(baseURL, "PUT", "/"+bucket+"?versioning", suspendXML, "application/xml")
		Pass("V suspend: PutBucketVersioning", s == 200)

		s, _, _ = p15Do(baseURL, "PUT", "/"+bucket+"?versioning", suspendXML, "application/xml")
		Pass("V suspend: duplicate → 400", s == 400)

		// 未版本化的 bucket 查询返回空 Status。
		freshBucket := "p15-unversioned"
		p15Cleanup(baseURL, bucket)
		s, _, _ = p15Do(baseURL, "PUT", "/"+freshBucket, "", "")
		Pass("V unversioned: CreateBucket", s == 200)

		s, b, _ = p15Do(baseURL, "GET", "/"+freshBucket+"?versioning", "", "")
		Pass("V unversioned: empty status", s == 200 && !strings.Contains(b, "Status"))

		p15Cleanup(baseURL, freshBucket)
	}

	// ============================================================
	// 6. HeadObject with versionId
	// ============================================================
	{
		bucket := "p15-head"
		versioningXML := `<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>`

		p15Do(baseURL, "PUT", "/"+bucket, "", "")
		p15Do(baseURL, "PUT", "/"+bucket+"?versioning", versioningXML, "application/xml")

		s, _, h = p15Do(baseURL, "PUT", "/"+bucket+"/head-test.txt", "head-data", ct)
		Pass("V head: PutObject", s == 200)
		vid := h.Get("x-amz-version-id")

		s, _, h = p15Do(baseURL, "HEAD", "/"+bucket+"/head-test.txt?versionId="+vid, "", "")
		Pass("V head: HeadObject with versionId", s == 200)
		Pass("V head: Content-Length matches", h.Get("Content-Length") == "9")
		Pass("V head: version-id header", h.Get("x-amz-version-id") == vid)

		// HeadObject 不存在的 versionId → 404。
		s, _, _ = p15Do(baseURL, "HEAD", "/"+bucket+"/head-test.txt?versionId=nonexistent", "", "")
		Pass("V head: nonexistent versionId → 404", s == 404)

		p15Cleanup(baseURL, bucket)
	}

	// ============================================================
	// 7. ListObjectVersions
	// ============================================================
	{
		bucket := "p15-list"
		versioningXML := `<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>`

		p15Do(baseURL, "PUT", "/"+bucket, "", "")
		p15Do(baseURL, "PUT", "/"+bucket+"?versioning", versioningXML, "application/xml")

		p15Do(baseURL, "PUT", "/"+bucket+"/list-a.txt", "a1", ct)
		p15Do(baseURL, "PUT", "/"+bucket+"/list-a.txt", "a2", ct)
		p15Do(baseURL, "PUT", "/"+bucket+"/list-b.txt", "b1", ct)

		s, b, _ = p15Do(baseURL, "GET", "/"+bucket+"?versions", "", "")
		Pass("V list: ListObjectVersions → 200", s == 200)
		Pass("V list: contains list-a.txt", strings.Contains(b, "list-a.txt"))
		Pass("V list: contains list-b.txt", strings.Contains(b, "list-b.txt"))
		Pass("V list: has Version element", strings.Contains(b, "<Version>"))
		Pass("V list: has IsLatest", strings.Contains(b, "IsLatest"))

		p15Cleanup(baseURL, bucket)
	}

	// ============================================================
	// 8. ListObjectVersions with delete marker
	// ============================================================
	{
		bucket := "p15-dmlist"
		versioningXML := `<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>`

		p15Do(baseURL, "PUT", "/"+bucket, "", "")
		p15Do(baseURL, "PUT", "/"+bucket+"?versioning", versioningXML, "application/xml")

		p15Do(baseURL, "PUT", "/"+bucket+"/dm-list.txt", "dm1", ct)
		p15Do(baseURL, "DELETE", "/"+bucket+"/dm-list.txt", "", "")

		s, b, _ = p15Do(baseURL, "GET", "/"+bucket+"?versions", "", "")
		Pass("V dm-list: ListObjectVersions → 200", s == 200)
		Pass("V dm-list: contains DeleteMarker", strings.Contains(b, "<DeleteMarker"))
		Pass("V dm-list: contains dm-list.txt", strings.Contains(b, "dm-list.txt"))

		p15Cleanup(baseURL, bucket)
	}

	P("INFO: Phase 15 Object Versioning complete")
}
