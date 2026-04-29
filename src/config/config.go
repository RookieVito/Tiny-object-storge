package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// Config 服务器配置。
type Config struct {
	Port        int               `json:"port"`
	Root        string            `json:"root"`
	AccessKey   string            `json:"access_key"`
	SecretKey   string            `json:"secret_key"`
	MaxBodySize int64             `json:"max_body_size"`
	BackendType string            `json:"backend_type"` // "local" | "ec" | "distributed"
	Region      string            `json:"region"`        // AWS region for Sig V4（默认 "us-east-1"）
	CORS        CORSConfig         `json:"cors"`         // CORS 配置
	EC          ECConfig          `json:"ec"`            // EC 配置（仅 backend_type="ec" 时使用）
	Distributed         DistributedConfig `json:"distributed"`          // 分布式配置（仅 backend_type="distributed" 时使用）
	MultipartTTLSeconds int              `json:"multipart_ttl_seconds"` // 过期 multipart upload 清理阈值（默认 86400 = 24h）
	CleanupIntervalSec  int              `json:"cleanup_interval_sec"`  // 过期上传扫描间隔（默认 3600 = 1h）
}

// CORSConfig CORS 跨域配置。
// AllowedOrigins 非空时启用 CORS；设置为 [] 或不配置均视为禁用。
type CORSConfig struct {
	AllowedOrigins   []string `json:"allowed_origins"`
	AllowedMethods   []string `json:"allowed_methods"`
	AllowedHeaders   []string `json:"allowed_headers"`
	ExposeHeaders    []string `json:"expose_headers"`
	MaxAge           int      `json:"max_age"`
	AllowCredentials bool     `json:"allow_credentials"`
}

// SetCORSDefaults 填充 CORS 零值字段为默认值。
func (cc *CORSConfig) SetCORSDefaults() {
	if cc.AllowedOrigins == nil {
		cc.AllowedOrigins = []string{"*"}
	}
	if cc.AllowedMethods == nil {
		cc.AllowedMethods = []string{"GET", "PUT", "POST", "DELETE", "HEAD", "OPTIONS"}
	}
	if cc.AllowedHeaders == nil {
		cc.AllowedHeaders = []string{"Authorization", "Content-Type", "X-Amz-Date", "X-Amz-Content-Sha256"}
	}
	if cc.ExposeHeaders == nil {
		cc.ExposeHeaders = []string{"ETag"}
	}
	if cc.MaxAge == 0 {
		cc.MaxAge = 3600
	}
}

// ECConfig 纠删码配置。
type ECConfig struct {
	Disks        []string `json:"disks"`          // N 个磁盘路径 (N >= K + M)
	DataShards   int      `json:"data_shards"`    // K（默认 4）
	ParityShards int      `json:"parity_shards"`  // M（默认 2）
	MetaRoot     string   `json:"meta_root"`      // EC 元数据存储路径
}

// DistributedConfig 分布式模式配置。
type DistributedConfig struct {
	NodeID            string   `json:"node_id"`              // 本节点 ID，默认 "host:port"
	SeedNodes         []string `json:"seed_nodes"`           // 种子节点列表
	ReplicationFactor int      `json:"replication_factor"`   // 副本数 N（默认 3）
	ReadQuorum        int      `json:"read_quorum"`          // 读仲裁 R（默认 2）
	WriteQuorum       int      `json:"write_quorum"`         // 写仲裁 W（默认 2）
	VirtualNodes      int      `json:"virtual_nodes"`        // 一致性哈希虚拟节点数（默认 500）
	GossipIntervalMs  int      `json:"gossip_interval_ms"`   // Gossip 间隔毫秒（默认 1000）
	RPCTimeoutMs      int      `json:"rpc_timeout_ms"`       // RPC 超时毫秒（默认 3000）
}

// SetDistributedDefaults 填充分布式配置零值为默认值。
func (dc *DistributedConfig) SetDistributedDefaults(port int) {
	if dc.ReplicationFactor == 0 {
		dc.ReplicationFactor = 3
	}
	if dc.ReadQuorum == 0 {
		dc.ReadQuorum = 2
	}
	if dc.WriteQuorum == 0 {
		dc.WriteQuorum = 2
	}
	if dc.VirtualNodes == 0 {
		dc.VirtualNodes = 500
	}
	if dc.GossipIntervalMs == 0 {
		dc.GossipIntervalMs = 1000
	}
	if dc.RPCTimeoutMs == 0 {
		dc.RPCTimeoutMs = 3000
	}
	if dc.NodeID == "" {
		dc.NodeID = fmt.Sprintf("localhost:%d", port)
	}
}

// SetDefaults 填充零值字段为默认值。
func (c *Config) SetDefaults() {
	if c.Port == 0 {
		c.Port = 9000
	}
	if c.Root == "" {
		c.Root = "./data"
	}
	if c.AccessKey == "" {
		c.AccessKey = "minioadmin"
	}
	if c.SecretKey == "" {
		c.SecretKey = "minioadmin"
	}
	if c.MaxBodySize == 0 {
		c.MaxBodySize = 10 << 20 // 10 MB
	}
	if c.BackendType == "" {
		c.BackendType = "local"
	}
	if c.Region == "" {
		c.Region = "us-east-1"
	}
	c.CORS.SetCORSDefaults()
	if c.MultipartTTLSeconds == 0 {
		c.MultipartTTLSeconds = 86400
	}
	if c.CleanupIntervalSec == 0 {
		c.CleanupIntervalSec = 3600
	}
}

// LoadConfig 读取 JSON 配置文件。文件不存在时返回默认值。
func LoadConfig(path string) (*Config, error) {
	cfg := &Config{}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			cfg.SetDefaults()
			return cfg, nil
		}
		return nil, err
	}

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	cfg.SetDefaults()
	return cfg, nil
}
