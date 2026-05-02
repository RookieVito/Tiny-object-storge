package main

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// cmdLs 处理 ls 命令：无参列出 bucket，有 bucket 参数列出对象。
func cmdLs(args []string) {
	cfg := LoadConfig()
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		fmt.Fprintln(os.Stderr, "error: not configured. run: tiny-storage config --access-key KEY --secret-key SECRET")
		os.Exit(1)
	}

	if len(args) == 0 {
		listBuckets(cfg)
		return
	}

	bucket := args[0]
	prefix := ""
	if len(args) > 1 {
		prefix = args[1]
	}
	listObjects(cfg, bucket, prefix)
}

func listBuckets(cfg *Config) {
	req, _ := http.NewRequest("GET", cfg.Endpoint+"/", nil)
	SignRequest(req, cfg.AccessKey, cfg.SecretKey, cfg.Endpoint)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "error: %s\n", body)
		os.Exit(1)
	}

	var result struct {
		Buckets []struct {
			Name         string `xml:"Name"`
			CreationDate string `xml:"CreationDate"`
		} `xml:"Buckets>Bucket"`
	}
	if err := xml.NewDecoder(resp.Body).Decode(&result); err != nil {
		fmt.Fprintf(os.Stderr, "error parsing response: %v\n", err)
		os.Exit(1)
	}

	for _, b := range result.Buckets {
		fmt.Printf("%-40s %s\n", b.Name, b.CreationDate)
	}
}

func listObjects(cfg *Config, bucket, prefix string) {
	path := fmt.Sprintf("/%s?delimiter=/&max-keys=1000", bucket)
	if prefix != "" {
		path += "&prefix=" + prefix
	}

	req, _ := http.NewRequest("GET", cfg.Endpoint+path, nil)
	SignRequest(req, cfg.AccessKey, cfg.SecretKey, cfg.Endpoint)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "error: %s\n", body)
		os.Exit(1)
	}

	var result struct {
		CommonPrefixes []struct {
			Prefix string `xml:"Prefix"`
		} `xml:"CommonPrefixes>Prefix"`
		Contents []struct {
			Key          string `xml:"Key"`
			Size         int64  `xml:"Size"`
			LastModified string `xml:"LastModified"`
		} `xml:"Contents"`
	}
	if err := xml.NewDecoder(resp.Body).Decode(&result); err != nil {
		fmt.Fprintf(os.Stderr, "error parsing response: %v\n", err)
		os.Exit(1)
	}

	for _, cp := range result.CommonPrefixes {
		name := strings.TrimSuffix(cp.Prefix, "/")
		fmt.Printf("DIR  %-50s\n", name+"/")
	}

	for _, obj := range result.Contents {
		size := formatSize(obj.Size)
		fmt.Printf("OBJ  %-50s %8s  %s\n", obj.Key, size, obj.LastModified)
	}
}

func formatSize(n int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	switch {
	case n >= GB:
		return fmt.Sprintf("%.1fG", float64(n)/float64(GB))
	case n >= MB:
		return fmt.Sprintf("%.1fM", float64(n)/float64(MB))
	case n >= KB:
		return fmt.Sprintf("%.1fK", float64(n)/float64(KB))
	default:
		return fmt.Sprintf("%dB", n)
	}
}
