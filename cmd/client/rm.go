package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// cmdRm 处理 rm 命令，删除对象。
func cmdRm(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: tiny-storage rm <s3://bucket/key>")
		os.Exit(1)
	}

	cfg := LoadConfig()
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		fmt.Fprintln(os.Stderr, "error: not configured. run: tiny-storage config --access-key KEY --secret-key SECRET")
		os.Exit(1)
	}

	key := strings.TrimPrefix(args[0], "s3://")
	path := "/" + strings.TrimPrefix(key, "/")

	req, _ := http.NewRequest("DELETE", cfg.Endpoint+path, nil)
	SignRequest(req, cfg.AccessKey, cfg.SecretKey, cfg.Endpoint)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "error: %s\n", string(body))
		os.Exit(1)
	}

	fmt.Printf("Object '%s' deleted.\n", key)
}
