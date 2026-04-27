package main

import (
	"flag"
	"fmt"
	"os"
)

var commands = map[string]func([]string){
	"ls":   cmdLs,
	"cp":   cmdCp,
	"rm":   cmdRm,
	"mb":   cmdMb,
	"rb":   cmdRb,
	"cat":  cmdCat,
	"stat": cmdStat,
	"config": cmdConfig,
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	name := os.Args[1]
	fn, ok := commands[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", name)
		printUsage()
		os.Exit(1)
	}

	fn(os.Args[2:])
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "Tiny Object Storage CLI")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, "  tiny-storage <command> [args...]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Commands:")
	fmt.Fprintln(os.Stderr, "  ls [bucket] [prefix]       List buckets or objects")
	fmt.Fprintln(os.Stderr, "  cp <src> <dst>              Copy (upload/download)")
	fmt.Fprintln(os.Stderr, "  mb <bucket>                 Create bucket")
	fmt.Fprintln(os.Stderr, "  rb <bucket>                 Remove bucket")
	fmt.Fprintln(os.Stderr, "  rm <bucket/key>             Delete object")
	fmt.Fprintln(os.Stderr, "  cat <bucket/key>            Print object to stdout")
	fmt.Fprintln(os.Stderr, "  stat <bucket/key>           Show object metadata")
	fmt.Fprintln(os.Stderr, "  config                      Configure credentials")
	fmt.Fprintln(os.Stderr, "  presign <bucket/key>         Generate presigned URL")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Environment variables:")
	fmt.Fprintln(os.Stderr, "  TOS_ENDPOINT, TOS_ACCESS_KEY, TOS_SECRET_KEY")
}

func cmdConfig(args []string) {
	fs := flag.NewFlagSet("config", flag.ExitOnError)
	endpoint := fs.String("endpoint", "", "Server endpoint URL")
	accessKey := fs.String("access-key", "", "Access key")
	secretKey := fs.String("secret-key", "", "Secret key")
	fs.Parse(args)

	cfg := LoadConfig()

	if *endpoint != "" {
		cfg.Endpoint = *endpoint
	}
	if *accessKey != "" {
		cfg.AccessKey = *accessKey
	}
	if *secretKey != "" {
		cfg.SecretKey = *secretKey
	}

	if *endpoint == "" && *accessKey == "" && *secretKey == "" {
		// 无参数：显示当前配置。
		fmt.Printf("Endpoint:   %s\n", cfg.Endpoint)
		fmt.Printf("Access Key: %s\n", cfg.AccessKey)
		if cfg.SecretKey != "" {
			fmt.Printf("Secret Key: ****\n")
		} else {
			fmt.Printf("Secret Key: (not set)\n")
		}
		return
	}

	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		fmt.Fprintln(os.Stderr, "error: access-key and secret-key are required")
		os.Exit(1)
	}

	if err := SaveConfig(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "error saving config: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("config saved to", configPath())
}
