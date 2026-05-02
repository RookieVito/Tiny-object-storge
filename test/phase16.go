package main

import (
	"encoding/json"
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
	registerTest("Phase 16", testPhase16)
}

const p16Port = "19502"

func p16Do(baseURL, method, path, body, contentType string) (int, string, http.Header) {
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

func p16BuildServer(tmpDir string) (binPath string, baseURL string, cleanup func(), diskPaths []string, err error) {
	baseURL = "http://localhost:" + p16Port
	binPath = filepath.Join(tmpDir, "server")

	if out, buildErr := exec.Command("go", "build", "-o", binPath, "./cmd/server/").CombinedOutput(); buildErr != nil {
		return "", "", nil, nil, fmt.Errorf("build: %v: %s", buildErr, string(out))
	}

	for i := 0; i < 10; i++ {
		conn, dialErr := net.DialTimeout("tcp", "localhost:"+p16Port, 100*time.Millisecond)
		if dialErr != nil {
			break
		}
		conn.Close()
		time.Sleep(500 * time.Millisecond)
	}
	if conn, _ := net.DialTimeout("tcp", "localhost:"+p16Port, 100*time.Millisecond); conn != nil {
		conn.Close()
		return "", "", nil, nil, fmt.Errorf("port %s is still in use", p16Port)
	}

	dataDir := filepath.Join(tmpDir, "ec-data")
	metaDir := filepath.Join(dataDir, "meta")
	diskPaths = make([]string, 6)
	for i := 0; i < 6; i++ {
		diskPaths[i] = filepath.Join(dataDir, fmt.Sprintf("disk-%d", i))
		os.MkdirAll(diskPaths[i], 0755)
	}
	os.MkdirAll(metaDir, 0755)

	disksJSON, _ := json.Marshal(diskPaths)
	cfg := map[string]interface{}{
		"port":         9000,
		"backend_type": "ec",
		"access_key":   AccessKey,
		"secret_key":   SecretKey,
		"max_body_size": 10485760,
		"ec": map[string]interface{}{
			"disks":                    json.RawMessage(disksJSON),
			"data_shards":              4,
			"parity_shards":            2,
			"meta_root":                metaDir,
			"health_check_interval_sec": 2,
		},
	}
	cfgData, _ := json.Marshal(cfg)
	cfgPath := filepath.Join(tmpDir, "config.json")
	os.WriteFile(cfgPath, cfgData, 0644)

	cmd := exec.Command(binPath, "--config", cfgPath, "--port", p16Port)
	logFile, _ := os.Create(filepath.Join(tmpDir, "server.log"))
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if startErr := cmd.Start(); startErr != nil {
		return "", "", nil, nil, fmt.Errorf("start server: %v", startErr)
	}

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
	return binPath, baseURL, cleanup, diskPaths, nil
}

func p16Metrics(baseURL string) map[string]interface{} {
	_, b, _ := p16Do(baseURL, "GET", "/_metrics", "", "")
	var m map[string]interface{}
	json.Unmarshal([]byte(b), &m)
	return m
}

func testPhase16() {
	tmpDir, err := os.MkdirTemp("", "phase16-*")
	if err != nil {
		Pass("HC: create temp dir", false)
		return
	}
	defer os.RemoveAll(tmpDir)

	_, baseURL, cleanup, diskPaths, err := p16BuildServer(tmpDir)
	if err != nil {
		Pass("HC: build server", false)
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		return
	}
	Pass("HC: build server", true)
	defer cleanup()

	ct := "application/octet-stream"

	// 等待首次健康检查完成。
	time.Sleep(3 * time.Second)

	// ============================================================
	// 1. 健康检查基本功能 — metrics 递增
	// ============================================================
	m := p16Metrics(baseURL)
	Pass("HC: metrics endpoint", m != nil && m["disk_health_checks"] != nil)
	hc, _ := m["disk_health_checks"].(float64)
	Pass("HC: disk_health_checks >= 1", hc >= 1)

	// ============================================================
	// 2. EC 降级读 + 自修复
	// ============================================================
	{
		bucket := "p16-bucket"
		p16Do(baseURL, "PUT", "/"+bucket, "", "")

		s, _, _ := p16Do(baseURL, "PUT", "/"+bucket+"/test.txt", "hello-ec-world", ct)
		Pass("HC: PutObject", s == 200)

		s, b, _ := p16Do(baseURL, "GET", "/"+bucket+"/test.txt", "", "")
		Pass("HC: GetObject", s == 200 && b == "hello-ec-world")

		shardFile := filepath.Join(diskPaths[5], bucket, "test.txt")
		os.Remove(shardFile)
		Pass("HC: shard removed from disk 5", true)

		s, b, _ = p16Do(baseURL, "GET", "/"+bucket+"/test.txt", "", "")
		Pass("HC: GetObject after shard loss", s == 200 && b == "hello-ec-world")

		time.Sleep(500 * time.Millisecond)
		_, statErr := os.Stat(shardFile)
		Pass("HC: shard self-repaired on read", statErr == nil)

		p16Do(baseURL, "DELETE", "/"+bucket+"/test.txt", "", "")
		p16Do(baseURL, "DELETE", "/"+bucket, "", "")
	}

	// ============================================================
	// 3. 磁盘故障检测
	// ============================================================
	{
		bucket := "p16-detect"
		p16Do(baseURL, "PUT", "/"+bucket, "", "")
		p16Do(baseURL, "PUT", "/"+bucket+"/obj1.txt", "data-for-detect", ct)

		os.RemoveAll(diskPaths[5])
		Pass("HC: disk 5 removed", true)

		time.Sleep(3 * time.Second)

		m := p16Metrics(baseURL)
		hc, _ := m["disk_health_checks"].(float64)
		Pass("HC: disk_health_checks incremented after failure", hc >= 2)

		s, b, _ := p16Do(baseURL, "GET", "/"+bucket+"/obj1.txt", "", "")
		Pass("HC: read works with 1 disk down", s == 200 && b == "data-for-detect")

		s, _, _ = p16Do(baseURL, "PUT", "/"+bucket+"/obj2.txt", "new-data", ct)
		Pass("HC: write with 1 disk down", s == 200)

		p16Do(baseURL, "DELETE", "/"+bucket+"/obj1.txt", "", "")
		p16Do(baseURL, "DELETE", "/"+bucket+"/obj2.txt", "", "")
		p16Do(baseURL, "DELETE", "/"+bucket, "", "")
	}

	// ============================================================
	// 4. 磁盘恢复 + Rebalance
	// ============================================================
	{
		bucket := "p16-recover"
		p16Do(baseURL, "PUT", "/"+bucket, "", "")
		p16Do(baseURL, "PUT", "/"+bucket+"/r1.txt", "rebalance-obj-1", ct)
		p16Do(baseURL, "PUT", "/"+bucket+"/r2.txt", "rebalance-obj-2", ct)

		os.Remove(filepath.Join(diskPaths[4], bucket, "r1.txt"))
		os.Remove(filepath.Join(diskPaths[4], bucket, "r2.txt"))
		Pass("RB: shards removed from disk 4", true)

		_, err1 := os.Stat(filepath.Join(diskPaths[4], bucket, "r1.txt"))
		Pass("RB: shard r1 absent", err1 != nil)

		s, b, _ := p16Do(baseURL, "GET", "/"+bucket+"/r1.txt", "", "")
		Pass("RB: GetObject r1 after shard loss", s == 200 && b == "rebalance-obj-1")

		time.Sleep(500 * time.Millisecond)
		_, err1 = os.Stat(filepath.Join(diskPaths[4], bucket, "r1.txt"))
		Pass("RB: r1 shard self-repaired", err1 == nil)

		_, err2 := os.Stat(filepath.Join(diskPaths[4], bucket, "r2.txt"))
		Pass("RB: r2 shard still absent", err2 != nil)

		// 删除磁盘 4 目录 → 等检测 → 恢复 → rebalance。
		os.RemoveAll(diskPaths[4])
		time.Sleep(3 * time.Second)

		os.MkdirAll(diskPaths[4], 0755)
		time.Sleep(3 * time.Second)

		_, err2 = os.Stat(filepath.Join(diskPaths[4], bucket, "r2.txt"))
		Pass("RB: r2 shard rebalanced after disk recovery", err2 == nil)

		s, b, _ = p16Do(baseURL, "GET", "/"+bucket+"/r2.txt", "", "")
		Pass("RB: GetObject r2 after rebalance", s == 200 && b == "rebalance-obj-2")

		m := p16Metrics(baseURL)
		rb, _ := m["rebalanced_objects"].(float64)
		Pass("RB: rebalanced_objects >= 1", rb >= 1)

		p16Do(baseURL, "DELETE", "/"+bucket+"/r1.txt", "", "")
		p16Do(baseURL, "DELETE", "/"+bucket+"/r2.txt", "", "")
		p16Do(baseURL, "DELETE", "/"+bucket, "", "")
	}

	// ============================================================
	// 5. 多磁盘故障容忍
	// ============================================================
	{
		bucket := "p16-multi"
		p16Do(baseURL, "PUT", "/"+bucket, "", "")
		p16Do(baseURL, "PUT", "/"+bucket+"/multi.txt", "multi-failure-test", ct)

		os.RemoveAll(diskPaths[4])
		os.RemoveAll(diskPaths[5])

		s, b, _ := p16Do(baseURL, "GET", "/"+bucket+"/multi.txt", "", "")
		Pass("MF: read with 2 disks down", s == 200 && b == "multi-failure-test")

		os.MkdirAll(diskPaths[4], 0755)
		os.MkdirAll(diskPaths[5], 0755)
		time.Sleep(3 * time.Second)

		s, b, _ = p16Do(baseURL, "GET", "/"+bucket+"/multi.txt", "", "")
		Pass("MF: read after disk recovery", s == 200 && b == "multi-failure-test")

		p16Do(baseURL, "DELETE", "/"+bucket+"/multi.txt", "", "")
		p16Do(baseURL, "DELETE", "/"+bucket, "", "")
	}

	P("INFO: Phase 16 Disk Health & Rebalance complete")
}
