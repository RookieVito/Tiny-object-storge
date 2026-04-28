package main

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func init() {
	registerTest("Phase 6", testPhase6)
}

// nodeConfig 生成分布式节点的配置 JSON。
func nodeConfig(port int, nodeID string, seeds []string, root string) string {
	seedsJSON, _ := json.Marshal(seeds)
	return fmt.Sprintf(`{
  "port": %d,
  "backend_type": "distributed",
  "access_key": "minioadmin",
  "secret_key": "minioadmin",
  "max_body_size": 10485760,
  "root": "%s",
  "distributed": {
    "node_id": "%s",
    "seed_nodes": %s,
    "replication_factor": 3,
    "read_quorum": 2,
    "write_quorum": 2,
    "virtual_nodes": 500,
    "gossip_interval_ms": 200,
    "rpc_timeout_ms": 2000
  }
}`, port, root, nodeID, string(seedsJSON))
}

// startNode 启动一个分布式节点进程。
func startNode(port int, nodeID string, seeds []string, tmpDir string) (*exec.Cmd, error) {
	configFile := filepath.Join(tmpDir, fmt.Sprintf("node-%s-config.json", strings.ReplaceAll(nodeID, ":", "-")))
	rootDir := filepath.Join(tmpDir, fmt.Sprintf("data-%s", strings.ReplaceAll(nodeID, ":", "-")))
	os.MkdirAll(rootDir, 0755)

	config := nodeConfig(port, nodeID, seeds, rootDir)
	if err := os.WriteFile(configFile, []byte(config), 0644); err != nil {
		return nil, fmt.Errorf("write config: %w", err)
	}

	logFile, _ := os.Create(filepath.Join(tmpDir, fmt.Sprintf("node-%s.log", strings.ReplaceAll(nodeID, ":", "-"))))

	binPath := filepath.Join(tmpDir, "server")
	cmd := exec.Command(binPath, "--config", configFile)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Dir = "/home/vito/workspace/tiny-object-storge"

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start node %s: %w", nodeID, err)
	}
	return cmd, nil
}

// httpGet 发送 HTTP GET 请求。
func httpGet(url string) (int, string) {
	resp, err := http.Get(url)
	if err != nil {
		return 0, ""
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// distDo 发送带 AWS Sig V2 认证的请求到指定完整 URL。
func distDo(method, fullURL, data, contentType string) (int, string, http.Header) {
	date := time.Now().UTC().Format(time.RFC1123)
	path := fullURL[len("http://"):]
	if idx := strings.Index(path, "/"); idx >= 0 {
		path = path[idx:]
	}
	// Sig V2 resource 不包含 query string。
	resource := path
	if idx := strings.Index(resource, "?"); idx >= 0 {
		resource = resource[:idx]
	}

	sts := method + "\n" + "" + "\n" + contentType + "\n" + date + "\n" + resource
	mac := hmac.New(sha1.New, []byte(SecretKey))
	mac.Write([]byte(sts))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	var bodyReader io.Reader
	if data != "" {
		bodyReader = strings.NewReader(data)
	}
	req, _ := http.NewRequest(method, fullURL, bodyReader)
	req.Header.Set("Authorization", fmt.Sprintf("AWS %s:%s", AccessKey, sig))
	req.Header.Set("Date", date)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Sprintf("error: %v", nil), nil
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

// testPhase6 测试分布式存储后端。
// 自动启动 3 个节点进程，测试完成后清理。
func testPhase6() {
	tmpDir, err := os.MkdirTemp("", "phase6-*")
	if err != nil {
		Pass("Phase 6: create temp dir", false)
		return
	}
	defer os.RemoveAll(tmpDir)

	ports := []int{19101, 19102, 19103}
	nodeIDs := []string{
		fmt.Sprintf("localhost:%d", ports[0]),
		fmt.Sprintf("localhost:%d", ports[1]),
		fmt.Sprintf("localhost:%d", ports[2]),
	}
	baseURLs := []string{
		fmt.Sprintf("http://localhost:%d", ports[0]),
		fmt.Sprintf("http://localhost:%d", ports[1]),
		fmt.Sprintf("http://localhost:%d", ports[2]),
	}

	// 预编译服务器二进制。
	binPath := filepath.Join(tmpDir, "server")
	buildCmd := exec.Command("go", "build", "-o", binPath, "./cmd/server/")
	buildCmd.Dir = "/home/vito/workspace/tiny-object-storge"
	if out, err := buildCmd.CombinedOutput(); err != nil {
		Pass("Phase 6: build server binary", false)
		fmt.Fprintf(os.Stderr, "build error: %s\n%s\n", err, string(out))
		os.RemoveAll(tmpDir)
		return
	}
	Pass("Phase 6: build server binary", true)

	// 等待端口可用。
	waitForPort := func(port int) {
		for i := 0; i < 10; i++ {
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), 100*time.Millisecond)
			if err != nil {
				time.Sleep(500 * time.Millisecond)
				continue
				}
			conn.Close()
			return // 端口被占用
		}
	}
	for _, p := range ports {
		waitForPort(p)
	}

	// 启动 3 个节点（串行启动，避免同时启动导致 Join 失败）。
	// node1 先启动，node2 和 node3 以 node1 为 seed 加入。
	cmds := make([]*exec.Cmd, 3)
	for i := 0; i < 3; i++ {
		seeds := make([]string, 0, 2)
		for j := 0; j < 3; j++ {
			if j != i {
				seeds = append(seeds, nodeIDs[j])
			}
		}
		cmd, err := startNode(ports[i], nodeIDs[i], seeds, tmpDir)
		if err != nil {
			Pass(fmt.Sprintf("Phase 6: start node %d", i+1), false)
			cleanupNodes(cmds)
			return
		}
		cmds[i] = cmd
			// 每个节点启动后等待一小段时间让 HTTP 服务器就绪。
		time.Sleep(500 * time.Millisecond)
	}

	// 等待节点启动并完成 gossip 收敛。
	timeout := time.After(30 * time.Second)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

loop:
	for {
		select {
		case <-ticker.C:
			alive := 0
			for _, url := range baseURLs {
				status, body := httpGet(url + "/_cluster/members")
				if status == 200 {
					var ms []map[string]interface{}
					json.Unmarshal([]byte(body), &ms)
					for _, m := range ms {
						if s, ok := m["state"].(float64); ok && s == 0 {
							alive++
						}
					}
				}
			}
			if alive >= 3 {
				break loop
			}
		case <-timeout:
			break loop
		}
	}

	defer cleanupNodes(cmds)

	// 调试：打印节点日志中 ring_size 相关信息。
	for i := 0; i < 3; i++ {
		logPath := filepath.Join(tmpDir, fmt.Sprintf("node-localhost-%d.log", ports[i]))
		data, _ := os.ReadFile(logPath)
		if data != nil {
			lines := strings.Split(string(data), "\n")
			for _, line := range lines {
				if strings.Contains(line, "ring") || strings.Contains(line, "CreateBucket") || strings.Contains(line, "added to hash") {
					fmt.Printf("  [node%d] %s\n", i+1, line)
				}
			}
		}
	}

	// --- 1. 成员发现 ---
	status, body := httpGet(baseURLs[0] + "/_cluster/members")
	Pass("Phase 6: GET /_cluster/members → 200", status == 200)

	var members []map[string]interface{}
	json.Unmarshal([]byte(body), &members)
	Pass("Phase 6: 3 members in cluster", len(members) == 3)

	aliveCount := 0
	for _, m := range members {
		if state, ok := m["state"].(float64); ok && state == 0 {
			aliveCount++
		}
	}
	Pass("Phase 6: all 3 members alive", aliveCount == 3)

	// --- 2. 基本 Put/Get 往返（通过 node1） ---
	bucket := "p6-bucket"
	status, _, _ = distDo("PUT", baseURLs[0]+"/"+bucket, "", "")
	Pass("Phase 6: CreateBucket → 200", status == 200)

	data := "Hello, Distributed Storage!"
	status, _, _ = distDo("PUT", baseURLs[0]+"/"+bucket+"/hello.txt", data, "text/plain")
	Pass("Phase 6: PutObject → 200", status == 200)

	_, body, _ = distDo("GET", baseURLs[0]+"/"+bucket+"/hello.txt", "", "")
	Pass("Phase 6: GetObject content matches (node1)", strings.Contains(body, data))

	// --- 3. 从其他节点读取（验证副本复制） ---
	time.Sleep(1 * time.Second)

	_, body, _ = distDo("GET", baseURLs[1]+"/"+bucket+"/hello.txt", "", "")
	Pass("Phase 6: GetObject content matches (node2)", strings.Contains(body, data))

	_, body, _ = distDo("GET", baseURLs[2]+"/"+bucket+"/hello.txt", "", "")
	Pass("Phase 6: GetObject content matches (node3)", strings.Contains(body, data))

	// --- 4. HeadObject ---
	status, _, hdrs := distDo("HEAD", baseURLs[0]+"/"+bucket+"/hello.txt", "", "")
	Pass("Phase 6: HeadObject → 200", status == 200)
	Pass("Phase 6: HeadObject Content-Length", hdrs.Get("Content-Length") == fmt.Sprintf("%d", len(data)))

	// --- 5. 通过 node2 写入，从 node1/node3 读取 ---
	status, _, _ = distDo("PUT", baseURLs[1]+"/"+bucket+"/from-node2.txt", "data-from-node2", "text/plain")
	Pass("Phase 6: PutObject via node2 → 200", status == 200)
	time.Sleep(500 * time.Millisecond)

	_, body, _ = distDo("GET", baseURLs[0]+"/"+bucket+"/from-node2.txt", "", "")
	Pass("Phase 6: GetObject on node1 from node2 write", strings.Contains(body, "data-from-node2"))

	_, body, _ = distDo("GET", baseURLs[2]+"/"+bucket+"/from-node2.txt", "", "")
	Pass("Phase 6: GetObject on node3 from node2 write", strings.Contains(body, "data-from-node2"))

	// --- 6. DeleteObject ---
	status, _, _ = distDo("DELETE", baseURLs[0]+"/"+bucket+"/hello.txt", "", "")
	Pass("Phase 6: DeleteObject → 204", status == 204)
	time.Sleep(500 * time.Millisecond)

	status, _, _ = distDo("GET", baseURLs[1]+"/"+bucket+"/hello.txt", "", "")
	Pass("Phase 6: GetObject deleted → 404", status == 404)

	// --- 7. ListBuckets 跨节点合并 ---
	status, body, _ = distDo("GET", baseURLs[2]+"/", "", "")
	Pass("Phase 6: ListBuckets → 200", status == 200)
	Pass("Phase 6: ListBuckets contains bucket", strings.Contains(body, bucket))

	// --- 8. 节点故障容忍 ---
	if cmds[2] != nil && cmds[2].Process != nil {
		cmds[2].Process.Kill()
		cmds[2].Wait()
		cmds[2] = nil
	}
	// 等待 gossip 检测故障。
	time.Sleep(3 * time.Second)

	status, _, _ = distDo("PUT", baseURLs[0]+"/"+bucket+"/after-failure.txt", "survived", "text/plain")
	Pass("Phase 6: PutObject after node3 failure → 200", status == 200)

	_, body, _ = distDo("GET", baseURLs[1]+"/"+bucket+"/after-failure.txt", "", "")
	Pass("Phase 6: GetObject after failure", strings.Contains(body, "survived"))

	_, body, _ = distDo("GET", baseURLs[0]+"/"+bucket+"/from-node2.txt", "", "")
	Pass("Phase 6: GetObject existing data after failure", strings.Contains(body, "data-from-node2"))
}

// cleanupNodes 清理所有节点进程。
// go run 会创建编译子进程，Kill 父进程不够，需要 kill 进程组。
func cleanupNodes(cmds []*exec.Cmd) {
	for _, cmd := range cmds {
		if cmd != nil && cmd.Process != nil {
			// 先尝试发送 SIGTERM，再 SIGKILL。
			cmd.Process.Signal(syscall.SIGTERM)
			time.Sleep(200 * time.Millisecond)
			cmd.Process.Kill()
			cmd.Wait()
		}
	}
	// 额外清理：确保端口释放。
	for _, cmd := range cmds {
		if cmd != nil && cmd.Process != nil {
			pgid, err := syscall.Getpgid(cmd.Process.Pid)
			if err == nil {
				syscall.Kill(-pgid, syscall.SIGKILL)
			}
		}
	}
}
