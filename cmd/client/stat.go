package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"
)

// cmdStat 处理 stat 命令，显示对象元数据。
func cmdStat(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: tiny-storage stat <s3://bucket/key>")
		os.Exit(1)
	}

	cfg := LoadConfig()
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		fmt.Fprintln(os.Stderr, "error: not configured. run: tiny-storage config --access-key KEY --secret-key SECRET")
		os.Exit(1)
	}

	key := strings.TrimPrefix(args[0], "s3://")
	path := "/" + strings.TrimPrefix(key, "/")

	req, _ := http.NewRequest("HEAD", cfg.Endpoint+path, nil)
	SignRequest(req, cfg.AccessKey, cfg.SecretKey, cfg.Endpoint)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "error: %s\n", resp.Status)
		os.Exit(1)
	}

	fmt.Printf("Key:          %s\n", key)
	fmt.Printf("Content-Type: %s\n", resp.Header.Get("Content-Type"))
	fmt.Printf("Content-Length: %s\n", resp.Header.Get("Content-Length"))
	fmt.Printf("ETag:         %s\n", resp.Header.Get("ETag"))
	fmt.Printf("Last-Modified: %s\n", resp.Header.Get("Last-Modified"))
}
