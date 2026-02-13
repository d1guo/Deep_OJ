package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/d1guo/deep_oj/internal/appconfig"
	"github.com/d1guo/deep_oj/internal/worker"
	"github.com/d1guo/deep_oj/pkg/observability"
	pb "github.com/d1guo/deep_oj/pkg/proto"
	"github.com/go-redis/redis/v8"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

func getEnvInt(key string, fallback int) int {
	if value, ok := os.LookupEnv(key); ok {
		if i, err := strconv.Atoi(value); err == nil && i > 0 {
			return i
		}
	}
	return fallback
}

func main() {
	cfgFile, cfgPath, err := appconfig.Load()
	if err != nil {
		slog.Error("Failed to load config", "path", cfgPath, "error", err)
		os.Exit(1)
	}
	if cfgPath != "" {
		slog.Info("Loaded config", "path", cfgPath)
	}
	if cfgFile != nil {
		appconfig.SetEnvIfEmptyInt("REDIS_POOL_SIZE", cfgFile.Redis.PoolSize)
		appconfig.SetEnvIfEmptyInt("REDIS_MIN_IDLE_CONNS", cfgFile.Redis.MinIdleConns)
		appconfig.SetEnvIfEmptyInt("REDIS_DIAL_TIMEOUT_MS", cfgFile.Redis.DialTimeoutMs)
		appconfig.SetEnvIfEmptyInt("REDIS_READ_TIMEOUT_MS", cfgFile.Redis.ReadTimeoutMs)
		appconfig.SetEnvIfEmptyInt("REDIS_WRITE_TIMEOUT_MS", cfgFile.Redis.WriteTimeoutMs)

		wcfg := cfgFile.Worker
		// Backward-compatible fallback to server/path sections
		if wcfg.Port == 0 && cfgFile.Server.Port > 0 {
			wcfg.Port = cfgFile.Server.Port
		}
		if wcfg.PoolSize == 0 && cfgFile.Server.PoolSize > 0 {
			wcfg.PoolSize = cfgFile.Server.PoolSize
		}
		if wcfg.Workspace == "" && cfgFile.Path.WorkspaceRoot != "" {
			wcfg.Workspace = cfgFile.Path.WorkspaceRoot
		}

		appconfig.SetEnvIfEmpty("WORKER_ID", wcfg.ID)
		appconfig.SetEnvIfEmpty("WORKER_ADDR", wcfg.Addr)
		appconfig.SetEnvIfEmptyInt("WORKER_PORT", wcfg.Port)
		appconfig.SetEnvIfEmptySlice("ETCD_ENDPOINTS", wcfg.EtcdEndpoints)
		appconfig.SetEnvIfEmpty("REDIS_URL", wcfg.RedisURL)
		appconfig.SetEnvIfEmpty("MINIO_ENDPOINT", wcfg.MinIO.Endpoint)
		appconfig.SetEnvIfEmpty("MINIO_ACCESS_KEY", wcfg.MinIO.AccessKey)
		appconfig.SetEnvIfEmpty("MINIO_SECRET_KEY", wcfg.MinIO.SecretKey)
		appconfig.SetEnvIfEmpty("MINIO_BUCKET", wcfg.MinIO.Bucket)
		appconfig.SetEnvIfEmpty("WORKSPACE", wcfg.Workspace)
		appconfig.SetEnvIfEmpty("JUDGER_BIN", wcfg.JudgerBin)
		appconfig.SetEnvIfEmpty("JUDGER_CONFIG", wcfg.JudgerConfig)
		appconfig.SetEnvIfEmptyInt("WORKER_POOL_SIZE", wcfg.PoolSize)
		appconfig.SetEnvIfEmptyBool("KEEP_WORKDIR", wcfg.KeepWorkdir)
		appconfig.SetEnvIfEmptyInt("COMPILE_TIMEOUT_MS", wcfg.Timeouts.CompileMs)
		appconfig.SetEnvIfEmptyInt("EXEC_TIMEOUT_BUFFER_MS", wcfg.Timeouts.ExecBufferMs)
		appconfig.SetEnvIfEmptyInt("DOWNLOAD_TIMEOUT_MS", wcfg.Timeouts.DownloadMs)
		appconfig.SetEnvIfEmptyInt("UNZIP_TIMEOUT_MS", wcfg.Timeouts.UnzipMs)
		appconfig.SetEnvIfEmptyInt("TESTCASE_CACHE_MAX", wcfg.TestcaseCache.Max)
		appconfig.SetEnvIfEmptyInt("TESTCASE_CACHE_TTL_SEC", wcfg.TestcaseCache.TTLSec)
		appconfig.SetEnvIfEmptyInt64("UNZIP_MAX_BYTES", wcfg.UnzipLimits.MaxBytes)
		appconfig.SetEnvIfEmptyInt("UNZIP_MAX_FILES", wcfg.UnzipLimits.MaxFiles)
		appconfig.SetEnvIfEmptyInt64("UNZIP_MAX_FILE_BYTES", wcfg.UnzipLimits.MaxFileBytes)
		appconfig.SetEnvIfEmptyInt("RESULT_TTL_SEC", wcfg.ResultTTLSec)
		appconfig.SetEnvIfEmptyInt("CHECKER_TIMEOUT_MS", wcfg.CheckerTimeoutMs)
		appconfig.SetEnvIfEmptyInt("CLEANUP_TIMEOUT_SEC", wcfg.CleanupTimeoutSec)
		appconfig.SetEnvIfEmptyInt("RESULT_STREAM_MAX_RETRIES", wcfg.ResultStreamMaxRetries)
		appconfig.SetEnvIfEmptyInt("RESULT_STREAM_BACKOFF_MS", wcfg.ResultStreamBackoffMs)
		appconfig.SetEnvIfEmptyInt("ETCD_DIAL_TIMEOUT_MS", wcfg.EtcdDialTimeoutMs)
		appconfig.SetEnvIfEmptyInt("ETCD_LEASE_TTL_SEC", wcfg.EtcdLeaseTTLSec)
		appconfig.SetEnvIfEmptyBool("ALLOW_HOST_CHECKER", wcfg.AllowHostChecker)
		appconfig.SetEnvIfEmptyBool("REQUIRE_CGROUPS_V2", wcfg.RequireCgroupsV2)
		appconfig.SetEnvIfEmpty("GRPC_TLS_CERT", wcfg.GRPCTLS.Cert)
		appconfig.SetEnvIfEmpty("GRPC_TLS_KEY", wcfg.GRPCTLS.Key)
		appconfig.SetEnvIfEmpty("GRPC_TLS_CA", wcfg.GRPCTLS.CA)
		appconfig.SetEnvIfEmpty("SERVICE_NAME", wcfg.Metrics.ServiceName)
		appconfig.SetEnvIfEmpty("INSTANCE_ID", wcfg.Metrics.InstanceID)
		appconfig.SetEnvIfEmptyInt("WORKER_METRICS_PORT", wcfg.MetricsPort)
	}

	// 1. Config
	cfg := worker.LoadConfig()
	slog.Info("Worker starting", "id", cfg.WorkerID, "addr", cfg.WorkerAddr, "bin", cfg.JudgerBin)

	if os.Getenv("REQUIRE_CGROUPS_V2") == "1" {
		if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err != nil {
			slog.Error("Cgroups v2 not available, aborting (REQUIRE_CGROUPS_V2=1)", "error", err)
			os.Exit(1)
		}
	}

	// 1.5 Start Metrics Server (Worker uses 9092)
	worker.InitMetrics()
	metricsPort := getEnvInt("WORKER_METRICS_PORT", 9092)
	observability.StartMetricsServer(fmt.Sprintf(":%d", metricsPort))

	// 2. Dependencies
	exec := worker.NewExecutor(cfg.JudgerBin)
	slog.Info("Initializing TestCaseManager...")
	tcMgr, err := worker.NewTestCaseManager(cfg)
	if err != nil {
		slog.Error("Failed to init tcMgr", "error", err)
		os.Exit(1)
	}

	slog.Info("Connecting to Redis", "addr", cfg.RedisURL)
	rdb := redis.NewClient(&redis.Options{
		Addr: cfg.RedisURL,
	})
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		slog.Error("Failed to connect to Redis", "error", err)
		os.Exit(1)
	}
	slog.Info("Connected to Redis")

	// 3. gRPC Server
	lis, err := net.Listen("tcp", cfg.WorkerAddr)
	if err != nil {
		slog.Error("Failed to listen", "error", err)
		os.Exit(1)
	}

	var grpcServer *grpc.Server
	if opt, err := loadServerTLS(); err != nil {
		slog.Error("Failed to load TLS", "error", err)
		os.Exit(1)
	} else if opt != nil {
		grpcServer = grpc.NewServer(opt)
	} else {
		grpcServer = grpc.NewServer()
	}
	svc := worker.NewJudgeService(cfg, exec, tcMgr, rdb)
	pb.RegisterJudgeServiceServer(grpcServer, svc)

	// 4. Etcd Registration
	dialMs := getEnvInt("ETCD_DIAL_TIMEOUT_MS", 5000)
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   cfg.EtcdEndpoints,
		DialTimeout: time.Duration(dialMs) * time.Millisecond,
	})
	if err != nil {
		slog.Error("Failed to connect to etcd", "error", err)
		os.Exit(1)
	}
	defer cli.Close()

	// Register with lease
	leaseTTL := int64(getEnvInt("ETCD_LEASE_TTL_SEC", 10))
	lease, err := cli.Grant(context.Background(), leaseTTL)
	if err != nil {
		slog.Error("Etcd grant failed", "error", err)
		os.Exit(1)
	}

	key := fmt.Sprintf("/deep-oj/workers/%s", cfg.WorkerID)
	val := cfg.WorkerAddr

	_, err = cli.Put(context.Background(), key, val, clientv3.WithLease(lease.ID))
	if err != nil {
		slog.Error("Etcd put failed", "error", err)
		os.Exit(1)
	}

	// KeepAlive
	ch, err := cli.KeepAlive(context.Background(), lease.ID)
	if err != nil {
		slog.Error("KeepAlive failed", "error", err)
		os.Exit(1)
	}
	go func() {
		for range ch {
			// consume keepalive response
		}
		slog.Warn("Etcd keepalive channel closed")
	}()

	slog.Info("Worker registered", "key", key)

	// 5. Start
	go func() {
		slog.Info("gRPC server listening", "addr", lis.Addr())
		if err := grpcServer.Serve(lis); err != nil {
			slog.Error("failed to serve", "error", err)
			os.Exit(1)
		}
	}()

	// Graceful Shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("Shutting down...")
	grpcServer.GracefulStop()
	cli.Revoke(context.Background(), lease.ID)
	slog.Info("Worker exited")
}

func loadServerTLS() (grpc.ServerOption, error) {
	certFile := os.Getenv("GRPC_TLS_CERT")
	keyFile := os.Getenv("GRPC_TLS_KEY")
	caFile := os.Getenv("GRPC_TLS_CA")
	if certFile == "" || keyFile == "" || caFile == "" {
		return nil, nil
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	caData, err := os.ReadFile(caFile)
	if err != nil {
		return nil, err
	}
	certPool := x509.NewCertPool()
	certPool.AppendCertsFromPEM(caData)

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    certPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS12,
	}
	return grpc.Creds(credentials.NewTLS(tlsConfig)), nil
}
