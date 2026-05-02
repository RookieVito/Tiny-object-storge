package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// cmdCp 处理 cp 命令，支持上传（local→s3）和下载（s3→local）。
func cmdCp(args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: tiny-storage cp <src> <dst>")
		fmt.Fprintln(os.Stderr, "  tiny-storage cp localfile s3://bucket/key   # upload")
		fmt.Fprintln(os.Stderr, "  tiny-storage cp s3://bucket/key localfile   # download")
		os.Exit(1)
	}

	cfg := LoadConfig()
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		fmt.Fprintln(os.Stderr, "error: not configured. run: tiny-storage config --access-key KEY --secret-key SECRET")
		os.Exit(1)
	}

	src, dst := args[0], args[1]

	if strings.HasPrefix(src, "s3://") && !strings.HasPrefix(dst, "s3://") {
		// s3 → local（下载）。
		s3Path := strings.TrimPrefix(src, "s3://")
		downloadFile(cfg, s3Path, dst)
	} else if !strings.HasPrefix(src, "s3://") && strings.HasPrefix(dst, "s3://") {
		// local → s3（上传）。
		s3Path := strings.TrimPrefix(dst, "s3://")
		uploadFile(cfg, src, s3Path)
	} else {
		fmt.Fprintln(os.Stderr, "error: one source must be local and the other must be s3://")
		os.Exit(1)
	}
}

func uploadFile(cfg *Config, localPath, s3Path string) {
	f, err := os.Open(localPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error opening file: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	path := "/" + strings.TrimPrefix(s3Path, "/")
	req, _ := http.NewRequest("PUT", cfg.Endpoint+path, f)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = stat.Size()
	SignRequest(req, cfg.AccessKey, cfg.SecretKey, cfg.Endpoint)

	fmt.Fprintf(os.Stderr, "Uploading %s → s3://%s\n", localPath, s3Path)
	pw := NewProgressWriter(stat.Size())

	// 使用 httputil 代理跟踪进度。
	// 因为 body 已经是 *os.File，我们需要包装 response body 来跟踪已发送字节。
	// 更简单的方案：读取整个文件并跟踪写入。
	// 但对于大文件，使用 tee reader。
	f.Seek(0, io.SeekStart)
	tee := io.TeeReader(f, pw)
	req.Body = io.NopCloser(tee)
	req.GetBody = func() (io.ReadCloser, error) {
		f2, err := os.Open(localPath)
		if err != nil {
			return nil, err
		}
		return io.NopCloser(io.TeeReader(f2, pw)), nil
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		pw.Finish()
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	pw.Current.Store(stat.Size())
	pw.Finish()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "error: %s\n", string(body))
		os.Exit(1)
	}

	fmt.Printf("Upload complete: s3://%s\n", s3Path)
}

func downloadFile(cfg *Config, s3Path, localPath string) {
	path := "/" + strings.TrimPrefix(s3Path, "/")

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
		fmt.Fprintf(os.Stderr, "error: %s\n", string(body))
		os.Exit(1)
	}

	// 确保目标目录存在。
	if dir := filepath.Dir(localPath); dir != "." {
		os.MkdirAll(dir, 0755)
	}

	out, err := os.Create(localPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error creating file: %v\n", err)
		os.Exit(1)
	}
	defer out.Close()

	total := resp.ContentLength
	if total < 0 {
		total = 0 // 未知大小
	}

	fmt.Fprintf(os.Stderr, "Downloading s3://%s → %s\n", s3Path, localPath)
	pw := NewProgressWriter(total)
	tee := io.TeeReader(resp.Body, pw)
	io.Copy(out, tee)
	pw.Current.Store(total)
	pw.Finish()

	fmt.Printf("Download complete: %s\n", localPath)
}
