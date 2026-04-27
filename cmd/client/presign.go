package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

func init() {
	commands["presign"] = cmdPresign
}

func cmdPresign(args []string) {
	fs := flag.NewFlagSet("presign", flag.ExitOnError)
	method := fs.String("method", "GET", "HTTP method (GET or PUT)")
	expires := fs.Int("expires", 3600, "URL expiration in seconds (max 604800)")
	fs.Parse(args)

	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: tiny-storage presign [flags] <bucket/key>")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Flags:")
		fs.PrintDefaults()
		os.Exit(1)
	}

	cfg := LoadConfig()
	if cfg.Endpoint == "" || cfg.AccessKey == "" || cfg.SecretKey == "" {
		fmt.Fprintln(os.Stderr, "error: not configured. Run 'tiny-storage config' first.")
		os.Exit(1)
	}

	target := fs.Arg(0)
	var resource string
	if idx := strings.Index(target, "/"); idx > 0 {
		resource = "/" + target
	} else {
		resource = "/" + target + "/"
	}

	host := cfg.Endpoint
	if strings.HasPrefix(host, "http://") {
		host = host[7:]
	} else if strings.HasPrefix(host, "https://") {
		host = host[8:]
	}

	url := PresignV4(*method, resource, cfg.AccessKey, cfg.SecretKey, "us-east-1", host, int64(*expires))
	fmt.Println(url)
}
