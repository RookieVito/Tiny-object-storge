package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
)

// cmdMb 处理 mb 命令，创建 bucket。
func cmdMb(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: tiny-storage mb <bucket>")
		os.Exit(1)
	}

	cfg := LoadConfig()
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		fmt.Fprintln(os.Stderr, "error: not configured. run: tiny-storage config --access-key KEY --secret-key SECRET")
		os.Exit(1)
	}

	bucket := args[0]
	path := "/" + bucket

	req, _ := http.NewRequest("PUT", cfg.Endpoint+path, nil)
	SignRequest(req, cfg.AccessKey, cfg.SecretKey, cfg.Endpoint)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "error: %s\n", string(body))
		os.Exit(1)
	}

	fmt.Printf("Bucket '%s' created.\n", bucket)
}
