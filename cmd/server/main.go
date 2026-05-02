package main

import (
	"context"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"tiny-object-storage/src/config"
	"tiny-object-storage/src/handler"
	"tiny-object-storage/src/metrics"
	"tiny-object-storage/src/storage"
)

func main() {
	configPath := flag.String("config", "./config.json", "Path to config file")
	port := flag.Int("port", 0, "HTTP listen port (overrides config)")
	root := flag.String("root", "", "Storage root directory (overrides config)")
	flag.Parse()

	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	// CLI 参数覆盖配置文件值。
	if *port != 0 {
		cfg.Port = *port
	}
	if *root != "" {
		cfg.Root = *root
	}

	absRoot, err := filepath.Abs(cfg.Root)
	if err != nil {
		log.Fatalf("failed to resolve root path: %v", err)
	}

	if err := os.MkdirAll(absRoot, 0755); err != nil {
		log.Fatalf("failed to create storage root: %v", err)
	}

	// 初始化结构化 JSON 日志。
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	// 构造存储后端。
	var backend storage.StorageBackend
	var ecBackend *storage.ECBackend
	switch cfg.BackendType {
	case "local":
		backend = storage.NewLocalBackend(absRoot)
	case "ec":
		k := cfg.EC.DataShards
		m := cfg.EC.ParityShards
		if k == 0 {
			k = 4
		}
		if m == 0 {
			m = 2
		}
		metaRoot := cfg.EC.MetaRoot
		if metaRoot == "" {
			metaRoot = filepath.Join(absRoot, ".ec-meta")
		}
		disks := cfg.EC.Disks
		if len(disks) == 0 {
			log.Fatalf("EC backend requires at least %d disks (configured via ec.disks)", k+m)
		}
		// 将磁盘路径转为绝对路径。
		absDisks := make([]string, len(disks))
		for i, d := range disks {
			abs, err := filepath.Abs(d)
			if err != nil {
				log.Fatalf("failed to resolve disk path %q: %v", d, err)
			}
			absDisks[i] = abs
		}
		absMetaRoot, err := filepath.Abs(metaRoot)
		if err != nil {
			log.Fatalf("failed to resolve meta root: %v", err)
		}
		// 创建磁盘目录。
		for _, d := range absDisks {
			os.MkdirAll(d, 0755)
		}
		os.MkdirAll(absMetaRoot, 0755)
		ecBackend, err = storage.NewECBackend(absDisks, absMetaRoot, k, m)
		if err != nil {
			log.Fatalf("failed to create EC backend: %v", err)
		}
		backend = ecBackend
		slog.Info("EC backend initialized", "data_shards", k, "parity_shards", m, "disks", len(absDisks))
	case "distributed":
		dc := cfg.Distributed
		dc.SetDistributedDefaults(cfg.Port)
		distCfg := &storage.DistributedConfig{
			NodeID:            dc.NodeID,
			Addr:             dc.NodeID,
			SeedNodes:        dc.SeedNodes,
			ReplicationFactor: dc.ReplicationFactor,
			ReadQuorum:        dc.ReadQuorum,
			WriteQuorum:       dc.WriteQuorum,
			VirtualNodes:      dc.VirtualNodes,
			GossipInterval:    time.Duration(dc.GossipIntervalMs) * time.Millisecond,
			RPCTimeout:        time.Duration(dc.RPCTimeoutMs) * time.Millisecond,
		}
		distBackend, err := storage.NewDistributedBackend(distCfg, absRoot)
		if err != nil {
			log.Fatalf("failed to create distributed backend: %v", err)
		}
		if err := distBackend.Start(); err != nil {
			slog.Warn("failed to join cluster, running standalone", "err", err)
		}
		backend = distBackend
		slog.Info("distributed backend initialized",
			"node_id", dc.NodeID,
			"replication_factor", dc.ReplicationFactor,
			"read_quorum", dc.ReadQuorum,
			"write_quorum", dc.WriteQuorum,
			"seeds", dc.SeedNodes)
	case "ec_distributed":
		dc := cfg.Distributed
		dc.SetDistributedDefaults(cfg.Port)
		k := cfg.EC.DataShards
		m := cfg.EC.ParityShards
		if k == 0 {
			k = 4
		}
		if m == 0 {
			m = 2
		}
		ecDistCfg := &storage.ECDistributedConfig{
			NodeID:            dc.NodeID,
			Addr:              dc.NodeID,
			SeedNodes:         dc.SeedNodes,
			DataShards:        k,
			ParityShards:      m,
			ReplicationFactor: dc.ReplicationFactor,
			VirtualNodes:      dc.VirtualNodes,
			GossipInterval:    time.Duration(dc.GossipIntervalMs) * time.Millisecond,
			RPCTimeout:        time.Duration(dc.RPCTimeoutMs) * time.Millisecond,
		}
		ecDistBackend, err := storage.NewECDistributedBackend(ecDistCfg, absRoot)
		if err != nil {
			log.Fatalf("failed to create ec_distributed backend: %v", err)
		}
		if err := ecDistBackend.Start(); err != nil {
			slog.Warn("failed to join cluster, running standalone", "err", err)
		}
		backend = ecDistBackend
		slog.Info("ec_distributed backend initialized",
			"node_id", dc.NodeID,
			"data_shards", k,
			"parity_shards", m,
			"meta_replication", dc.ReplicationFactor,
			"seeds", dc.SeedNodes)
	default:
		log.Fatalf("unknown backend type: %s", cfg.BackendType)
	}

	metricsRoot := absRoot
	if cfg.BackendType == "ec" {
		metricsRoot = cfg.EC.MetaRoot
		if metricsRoot == "" {
			metricsRoot = filepath.Join(absRoot, ".ec-meta")
		}
	}
	m := metrics.NewMetrics(metricsRoot)

	// 启动 multipart upload TTL 清理器。
	cleanupCtx, cleanupCancel := context.WithCancel(context.Background())
	ttl := time.Duration(cfg.MultipartTTLSeconds) * time.Second
	interval := time.Duration(cfg.CleanupIntervalSec) * time.Second
	cleaner := storage.NewTTLCleaner(backend, ttl, interval, func(count int64) {
		m.MultipartCleanups.Add(count)
	})
	if cleaner != nil {
		cleaner.Start(cleanupCtx)
		slog.Info("TTL cleaner started", "ttl_seconds", cfg.MultipartTTLSeconds, "interval_seconds", cfg.CleanupIntervalSec)
	}

	// 启动 EC 磁盘健康检查和 Rebalancer。
	if ecBackend != nil {
		healthInterval := time.Duration(cfg.EC.HealthCheckIntervalSec) * time.Second
		if healthInterval == 0 {
			healthInterval = 60 * time.Second
		}
		rebalancer := storage.NewRebalancer(ecBackend, func(count int64) {
			m.RebalancedObjects.Add(count)
		})
		healthChecker := storage.NewDiskHealthChecker(ecBackend, healthInterval, func() {
			m.DiskHealthChecks.Add(1)
		}, func(diskIndex int, alive bool) {
			slog.Info("disk state changed", "disk", ecBackend.DiskPath(diskIndex), "alive", alive)
			if alive {
				go rebalancer.Rebalance()
			}
		})
		healthChecker.Start(cleanupCtx)
		slog.Info("disk health checker started", "interval", healthInterval)
	}

	// 保存原始后端引用（VersionedBackend 包装前），用于关闭和集群 handler。
	rawBackend := backend

	// 分布式模式：获取集群 HTTP handler（必须在 VersionedBackend 包装之前）。
	var clusterHandler http.Handler
	switch cfg.BackendType {
	case "distributed":
		if db, ok := rawBackend.(*storage.DistributedBackend); ok {
			clusterHandler = db.MembershipHandler()
		}
	case "ec_distributed":
		if db, ok := rawBackend.(*storage.ECDistributedBackend); ok {
			clusterHandler = db.MembershipHandler()
		}
	}

	// 包装 VersionedBackend 装饰器，为所有后端添加对象版本控制。
	backend = storage.NewVersionedBackend(backend)

	// Web UI 静态文件 handler（如果嵌入资源存在）。
	var uiHandler http.Handler
	if uiSub, err := fs.Sub(uiFS, "static/dist"); err == nil {
		uiHandler = spaHandler{fs: http.FS(uiSub)}
	}

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Port),
		Handler: handler.NewRouter(backend, cfg, m, clusterHandler, uiHandler),
	}

	// 优雅关闭。
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh

		slog.Info("shutting down...")
		cleanupCancel()
		// 分布式模式：先离开集群。
		switch cfg.BackendType {
		case "distributed":
			if db, ok := rawBackend.(*storage.DistributedBackend); ok {
				db.Stop()
			}
		case "ec_distributed":
			if db, ok := rawBackend.(*storage.ECDistributedBackend); ok {
				db.Stop()
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			slog.Error("shutdown error", "err", err)
		}
	}()

	slog.Info("server started", "port", cfg.Port, "backend", cfg.BackendType, "root", absRoot)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
	slog.Info("server stopped")
}
