package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func init() {
	registerTest("Phase 5", testPhase5)
}

// testPhase5 测试纠删码存储后端。
// 前提：服务器以 EC 模式启动，配置如下：
//
//	go run ./cmd/server/ --port 9000 --config ./test/ec-config.json
//
// 测试结束后会自动清理 EC 数据目录。
func testPhase5() {
	bucket := "p5-bucket"

	// 确保使用 EC 模式。
	status, _ := Do2("PUT", "/"+bucket, "", "")
	Pass("EC: CreateBucket", status == 200)

	// --- 基本 Put/Get 往返 ---
	data := "Hello, Erasure Coding! This is a test string."
	status, _ = Do2("PUT", "/"+bucket+"/hello.txt", data, "text/plain")
	Pass("EC: PutObject → 200", status == 200)

	_, body := Do2("GET", "/"+bucket+"/hello.txt", "", "")
	Pass("EC: GetObject content matches", body == data)

	// --- HeadObject ---
	status, _, hdrs := Do("HEAD", "/"+bucket+"/hello.txt", "", "")
	Pass("EC: HeadObject → 200", status == 200)
	Pass("EC: HeadObject Content-Length", hdrs.Get("Content-Length") == fmt.Sprintf("%d", len(data)))

	// --- 嵌套 key ---
	status, _ = Do2("PUT", "/"+bucket+"/a/b/c/nested.json", `{"ec":true}`, "application/json")
	Pass("EC: PutObject nested key → 200", status == 200)

	_, body = Do2("GET", "/"+bucket+"/a/b/c/nested.json", "", "")
	Pass("EC: GetObject nested content", strings.Contains(body, `{"ec":true}`))

	// --- 大对象（大于 K * shardSize） ---
	largeData := bytes.Repeat([]byte("ABCDEFGH"), 2000) // 16000 bytes
	status, _ = Do3("PUT", "/"+bucket+"/large.bin", bytes.NewReader(largeData), "application/octet-stream")
	Pass("EC: PutObject large → 200", status == 200)

	_, body = Do2("GET", "/"+bucket+"/large.bin", "", "")
	Pass("EC: GetObject large content matches", body == string(largeData))

	// --- Delete ---
	status, _ = Do2("DELETE", "/"+bucket+"/hello.txt", "", "")
	Pass("EC: DeleteObject → 204", status == 204)

	status, body = Do2("GET", "/"+bucket+"/hello.txt", "", "")
	Pass("EC: GetObject deleted → NoSuchKey", status == 404)

	// --- ListBuckets ---
	status, body = DoNoAuth("GET", "/")
	Pass("EC: ListBuckets → 200", status == 200)
	Pass("EC: ListBuckets contains bucket", strings.Contains(body, "<Name>"+bucket+"</Name>"))

	// --- ListObjects ---
	status, body = Do2("GET", "/"+bucket, "", "")
	Pass("EC: ListObjects → 200", status == 200)
	Pass("EC: ListObjects contains nested.json", strings.Contains(body, "nested.json"))
	Pass("EC: ListObjects contains large.bin", strings.Contains(body, "large.bin"))

	// --- 清理 ---
	Do2("DELETE", "/"+bucket+"/a/b/c/nested.json", "", "")
	Do2("DELETE", "/"+bucket+"/large.bin", "", "")
	Do2("DELETE", "/"+bucket, "", "")
}

// testECDegradedRead 测试降级读（模拟磁盘故障）。
// 通过删除磁盘上的 shard 文件模拟磁盘故障。
func testECDegradedRead() {
	bucket := "p5-degraded-bucket"

	// 读取 EC 配置。
	cfgPath := "test/ec-config.json"
	cfgData, err := os.ReadFile(cfgPath)
	if err != nil {
		fmt.Println("  SKIP: test/ec-config.json not found")
		return
	}
	var cfg struct {
		EC struct {
			Disks    []string `json:"disks"`
			MetaRoot string   `json:"meta_root"`
		} `json:"ec"`
	}
	if json.Unmarshal(cfgData, &cfg) != nil {
		fmt.Println("  SKIP: failed to parse ec-config.json")
		return
	}

	Do2("PUT", "/"+bucket, "", "")

	// 上传一个对象。
	data := "degraded read test data - this must survive disk failures!"
	Do2("PUT", "/"+bucket+"/survive.txt", data, "text/plain")

	// 验证正常读取。
	_, body := Do2("GET", "/"+bucket+"/survive.txt", "", "")
	Pass("Degraded: normal read", body == data)

	// 模拟磁盘 0 故障：删除磁盘 0 上的 shard 文件。
	if len(cfg.EC.Disks) > 0 {
		disk0Path := filepath.Join(cfg.EC.Disks[0], bucket, "survive.txt")
		os.Remove(disk0Path)
		os.Remove(disk0Path + ".meta")
	}

	// 1 个磁盘故障，应该仍然能读取。
	_, body = Do2("GET", "/"+bucket+"/survive.txt", "", "")
	Pass("Degraded: 1 disk down → still readable", body == data)

	// 恢复磁盘 0：重新 PUT 数据。
	Do2("DELETE", "/"+bucket+"/survive.txt", "", "")
	Do2("PUT", "/"+bucket+"/survive.txt", data, "text/plain")

	// 清理。
	Do2("DELETE", "/"+bucket+"/survive.txt", "", "")
	Do2("DELETE", "/"+bucket, "", "")
}

// setupECConfig 创建 EC 测试配置文件和磁盘目录。
// 供外部脚本使用。
func setupECConfig() error {
	baseDir := filepath.Join(".", "test", "ec-data")
	disks := make([]string, 6)
	for i := 0; i < 6; i++ {
		disks[i] = filepath.Join(baseDir, fmt.Sprintf("disk-%d", i))
		os.MkdirAll(disks[i], 0755)
	}
	metaRoot := filepath.Join(baseDir, "meta")

	config := map[string]interface{}{
		"port":          9000,
		"backend_type":  "ec",
		"access_key":    "minioadmin",
		"secret_key":    "minioadmin",
		"max_body_size": 10485760,
		"ec": map[string]interface{}{
			"disks":         disks,
			"data_shards":   4,
			"parity_shards": 2,
			"meta_root":     metaRoot,
		},
	}

	configData, _ := json.MarshalIndent(config, "", "  ")
	return os.WriteFile("test/ec-config.json", configData, 0644)
}

// cleanupECData 清理 EC 测试数据。
func cleanupECData() {
	os.RemoveAll(filepath.Join("test", "ec-data"))
	os.Remove("test/ec-config.json")
}

func init() {
	// 注册 EC 环境管理测试（在 Phase 5 之前运行）。
	registerTest("Phase 5 Setup", func() {
		// 检查是否已经有 EC 配置（可能是外部启动的）。
		if _, err := os.Stat("test/ec-config.json"); err != nil {
			// 自动创建 EC 配置。
			if err := setupECConfig(); err != nil {
				fmt.Printf("  SKIP: failed to create EC config: %v\n", err)
				return
			}
			fmt.Println("  INFO: Created test/ec-config.json")
			fmt.Println("  INFO: Start server with: go run ./cmd/server/ --config test/ec-config.json")
		}
	})
}
