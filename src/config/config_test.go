package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSetDefaults_Full(t *testing.T) {
	cfg := &Config{}
	cfg.SetDefaults()

	if cfg.Port != 9000 {
		t.Errorf("expected Port 9000, got %d", cfg.Port)
	}
	if cfg.Root != "./data" {
		t.Errorf("expected Root ./data, got %s", cfg.Root)
	}
	if cfg.AccessKey != "minioadmin" {
		t.Errorf("expected AccessKey minioadmin, got %s", cfg.AccessKey)
	}
	if cfg.SecretKey != "minioadmin" {
		t.Errorf("expected SecretKey minioadmin, got %s", cfg.SecretKey)
	}
	if cfg.MaxBodySize != 10<<20 {
		t.Errorf("expected MaxBodySize %d, got %d", 10<<20, cfg.MaxBodySize)
	}
	if cfg.BackendType != "local" {
		t.Errorf("expected BackendType local, got %s", cfg.BackendType)
	}
	if cfg.Region != "us-east-1" {
		t.Errorf("expected Region us-east-1, got %s", cfg.Region)
	}
	if cfg.MultipartTTLSeconds != 86400 {
		t.Errorf("expected MultipartTTLSeconds 86400, got %d", cfg.MultipartTTLSeconds)
	}
	if cfg.CleanupIntervalSec != 3600 {
		t.Errorf("expected CleanupIntervalSec 3600, got %d", cfg.CleanupIntervalSec)
	}
}

func TestSetDefaults_Partial(t *testing.T) {
	cfg := &Config{Port: 8080, Root: "/custom"}
	cfg.SetDefaults()

	if cfg.Port != 8080 {
		t.Errorf("existing Port should not be overwritten, got %d", cfg.Port)
	}
	if cfg.Root != "/custom" {
		t.Errorf("existing Root should not be overwritten, got %s", cfg.Root)
	}
	// 未指定的字段应填充默认值
	if cfg.AccessKey != "minioadmin" {
		t.Errorf("expected AccessKey minioadmin, got %s", cfg.AccessKey)
	}
	if cfg.BackendType != "local" {
		t.Errorf("expected BackendType local, got %s", cfg.BackendType)
	}
}

func TestSetCORSDefaults(t *testing.T) {
	cc := &CORSConfig{}
	cc.SetCORSDefaults()

	if len(cc.AllowedOrigins) != 1 || cc.AllowedOrigins[0] != "*" {
		t.Errorf("expected ['*'], got %v", cc.AllowedOrigins)
	}
	if len(cc.AllowedMethods) != 6 {
		t.Errorf("expected 6 allowed methods, got %d", len(cc.AllowedMethods))
	}
	if len(cc.AllowedHeaders) != 4 {
		t.Errorf("expected 4 allowed headers, got %d", len(cc.AllowedHeaders))
	}
	if len(cc.ExposeHeaders) != 1 || cc.ExposeHeaders[0] != "ETag" {
		t.Errorf("expected ['ETag'], got %v", cc.ExposeHeaders)
	}
	if cc.MaxAge != 3600 {
		t.Errorf("expected MaxAge 3600, got %d", cc.MaxAge)
	}
}

func TestSetCORSDefaults_Partial(t *testing.T) {
	cc := &CORSConfig{AllowedOrigins: []string{"https://example.com"}, MaxAge: 7200}
	cc.SetCORSDefaults()

	if cc.AllowedOrigins[0] != "https://example.com" {
		t.Errorf("existing AllowedOrigins should not be overwritten")
	}
	if cc.MaxAge != 7200 {
		t.Errorf("existing MaxAge should not be overwritten, got %d", cc.MaxAge)
	}
	// 未指定的字段应填充默认值
	if len(cc.AllowedMethods) != 6 {
		t.Errorf("expected 6 methods, got %d", len(cc.AllowedMethods))
	}
}

func TestSetDistributedDefaults_Full(t *testing.T) {
	dc := &DistributedConfig{}
	dc.SetDistributedDefaults(9000)

	if dc.ReplicationFactor != 3 {
		t.Errorf("expected 3, got %d", dc.ReplicationFactor)
	}
	if dc.ReadQuorum != 2 {
		t.Errorf("expected 2, got %d", dc.ReadQuorum)
	}
	if dc.WriteQuorum != 2 {
		t.Errorf("expected 2, got %d", dc.WriteQuorum)
	}
	if dc.VirtualNodes != 500 {
		t.Errorf("expected 500, got %d", dc.VirtualNodes)
	}
	if dc.GossipIntervalMs != 1000 {
		t.Errorf("expected 1000, got %d", dc.GossipIntervalMs)
	}
	if dc.RPCTimeoutMs != 3000 {
		t.Errorf("expected 3000, got %d", dc.RPCTimeoutMs)
	}
	if dc.NodeID != "localhost:9000" {
		t.Errorf("expected localhost:9000, got %s", dc.NodeID)
	}
}

func TestSetDistributedDefaults_Partial(t *testing.T) {
	dc := &DistributedConfig{ReplicationFactor: 5, NodeID: "node1:8080"}
	dc.SetDistributedDefaults(9000)

	if dc.ReplicationFactor != 5 {
		t.Errorf("existing ReplicationFactor should not be overwritten, got %d", dc.ReplicationFactor)
	}
	if dc.NodeID != "node1:8080" {
		t.Errorf("existing NodeID should not be overwritten, got %s", dc.NodeID)
	}
	// 未指定的字段应填充默认值
	if dc.ReadQuorum != 2 {
		t.Errorf("expected 2, got %d", dc.ReadQuorum)
	}
}

func TestLoadConfig_NotExist(t *testing.T) {
	cfg, err := LoadConfig("/nonexistent/config.json")
	if err != nil {
		t.Fatalf("LoadConfig should return defaults for missing file: %v", err)
	}
	if cfg.Port != 9000 {
		t.Errorf("expected default port 9000, got %d", cfg.Port)
	}
}

func TestLoadConfig_ValidFile(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	input := Config{
		Port:        8080,
		Root:        "/custom",
		AccessKey:   "mykey",
		SecretKey:   "mysecret",
		BackendType: "ec",
		Region:      "ap-southeast-1",
	}
	data, _ := json.Marshal(input)
	os.WriteFile(configPath, data, 0644)

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Port != 8080 {
		t.Errorf("expected Port 8080, got %d", cfg.Port)
	}
	if cfg.BackendType != "ec" {
		t.Errorf("expected BackendType ec, got %s", cfg.BackendType)
	}
	if cfg.Region != "ap-southeast-1" {
		t.Errorf("expected Region ap-southeast-1, got %s", cfg.Region)
	}
	// 未指定的字段由 SetDefaults 填充
	if cfg.MaxBodySize != 10<<20 {
		t.Errorf("expected default MaxBodySize, got %d", cfg.MaxBodySize)
	}
}

func TestLoadConfig_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")
	os.WriteFile(configPath, []byte("not json{"), 0644)

	_, err := LoadConfig(configPath)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestLoadConfig_WithECConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	input := map[string]interface{}{
		"port":          9000,
		"backend_type":  "ec",
		"ec": map[string]interface{}{
			"data_shards":   4,
			"parity_shards": 2,
			"disks":         []string{"/d1", "/d2", "/d3", "/d4", "/d5", "/d6"},
			"meta_root":     "/meta",
		},
	}
	data, _ := json.Marshal(input)
	os.WriteFile(configPath, data, 0644)

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.EC.DataShards != 4 {
		t.Errorf("expected DataShards 4, got %d", cfg.EC.DataShards)
	}
	if cfg.EC.ParityShards != 2 {
		t.Errorf("expected ParityShards 2, got %d", cfg.EC.ParityShards)
	}
	if len(cfg.EC.Disks) != 6 {
		t.Errorf("expected 6 disks, got %d", len(cfg.EC.Disks))
	}
	if cfg.EC.MetaRoot != "/meta" {
		t.Errorf("expected MetaRoot /meta, got %s", cfg.EC.MetaRoot)
	}
}

func TestLoadConfig_WithDistributedConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	input := map[string]interface{}{
		"port":         9000,
		"backend_type": "distributed",
		"distributed": map[string]interface{}{
			"node_id":             "node1:9000",
			"seed_nodes":          []string{"node2:9000"},
			"replication_factor":  3,
			"read_quorum":         2,
			"write_quorum":        2,
		},
	}
	data, _ := json.Marshal(input)
	os.WriteFile(configPath, data, 0644)

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Distributed.NodeID != "node1:9000" {
		t.Errorf("expected NodeID node1:9000, got %s", cfg.Distributed.NodeID)
	}
	if len(cfg.Distributed.SeedNodes) != 1 {
		t.Errorf("expected 1 seed node, got %d", len(cfg.Distributed.SeedNodes))
	}
	if cfg.Distributed.ReplicationFactor != 3 {
		t.Errorf("expected ReplicationFactor 3, got %d", cfg.Distributed.ReplicationFactor)
	}
}

func TestLoadConfig_WithCORSConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	input := map[string]interface{}{
		"port": 9000,
		"cors": map[string]interface{}{
			"allowed_origins": []string{"https://example.com"},
			"allowed_methods": []string{"GET", "PUT"},
			"max_age":         7200,
		},
	}
	data, _ := json.Marshal(input)
	os.WriteFile(configPath, data, 0644)

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.CORS.AllowedOrigins[0] != "https://example.com" {
		t.Errorf("expected AllowedOrigins[0] https://example.com, got %s", cfg.CORS.AllowedOrigins[0])
	}
	if cfg.CORS.MaxAge != 7200 {
		t.Errorf("expected MaxAge 7200, got %d", cfg.CORS.MaxAge)
	}
	// SetCORSDefaults 应填充未指定的 CORS 字段
	if len(cfg.CORS.AllowedHeaders) != 4 {
		t.Errorf("expected 4 default allowed headers, got %d", len(cfg.CORS.AllowedHeaders))
	}
}

func TestLoadConfig_AccessPermissionError(t *testing.T) {
	// LoadConfig 对非 Exist 错误应返回 error
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "subdir", "config.json")
	// 不创建 subdir，让中间目录不存在
	// 但 os.ReadFile 对不存在的文件返回 IsNotExist，所以这里测试其他权限错误
	// 在 Unix 上可以通过创建一个目录代替文件来触发错误
	dirPath := configPath
	os.MkdirAll(dirPath, 0755)
	defer os.RemoveAll(dirPath)

	_, err := LoadConfig(configPath)
	// 读取目录而非文件应返回 error（非 IsNotExist）
	if err == nil {
		t.Fatal("expected error for directory path")
	}
}
