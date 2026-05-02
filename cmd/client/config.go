package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config 存储 CLI 客户端配置。
type Config struct {
	Endpoint  string `json:"endpoint"`
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
}

// configDir 返回配置目录路径。
func configDir() string {
	dir, err := os.UserHomeDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, ".tiny-storage")
}

// configPath 返回配置文件路径。
func configPath() string {
	return filepath.Join(configDir(), "config.json")
}

// LoadConfig 从配置文件加载，缺失字段回退到环境变量和默认值。
func LoadConfig() *Config {
	cfg := &Config{}

	// 从文件加载。
	data, err := os.ReadFile(configPath())
	if err == nil {
		_ = json.Unmarshal(data, cfg)
	}

	// 环境变量回退。
	if v := os.Getenv("TOS_ENDPOINT"); v != "" {
		cfg.Endpoint = v
	}
	if v := os.Getenv("TOS_ACCESS_KEY"); v != "" {
		cfg.AccessKey = v
	}
	if v := os.Getenv("TOS_SECRET_KEY"); v != "" {
		cfg.SecretKey = v
	}

	// 默认值。
	if cfg.Endpoint == "" {
		cfg.Endpoint = "http://localhost:9000"
	}
	return cfg
}

// SaveConfig 将配置写入文件。
func SaveConfig(cfg *Config) error {
	dir := configDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(), data, 0600)
}
