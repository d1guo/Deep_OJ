package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/d1guo/deep_oj/internal/appconfig"
	"github.com/d1guo/deep_oj/internal/repository"
	"github.com/d1guo/deep_oj/internal/worker"
	"github.com/d1guo/deep_oj/pkg/observability"
	"github.com/redis/go-redis/v9"
)

func getEnvInt(key string, fallback int) int {
	if value, ok := os.LookupEnv(key); ok {
		if i, err := strconv.Atoi(value); err == nil && i > 0 {
			return i
		}
	}
	return fallback
}

func getEnvBool(key string, fallback bool) bool {
	value, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func buildRedisOptions(addr string) *redis.Options {
	opts := &redis.Options{Addr: addr}
	if strings.HasPrefix(addr, "redis://") || strings.HasPrefix(addr, "rediss://") {
		if parsed, err := redis.ParseURL(addr); err == nil {
			opts = parsed
		}
	}
	if opts.Password == "" {
		opts.Password = os.Getenv("REDIS_PASSWORD")
	}
	if opts.TLSConfig == nil && (strings.HasPrefix(addr, "rediss://") || getEnvBool("REDIS_TLS", false)) {
		opts.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	return opts
}

func main() {
	cfgFile, cfgPath, err := appconfig.Load()
	if err != nil {
		slog.Error("加载配置失败", "path", cfgPath, "error", err)
		os.Exit(1)
	}
	if cfgPath != "" {
		slog.Info("已加载配置", "path", cfgPath)
	}
	if cfgFile != nil {
		// 将配置文件值写入环境默认值，运行时仍由 LoadConfig() 读取。
		appconfig.SetEnvIfEmptyInt("REDIS_POOL_SIZE", cfgFile.Redis.PoolSize)
		appconfig.SetEnvIfEmptyInt("REDIS_MIN_IDLE_CONNS", cfgFile.Redis.MinIdleConns)
		appconfig.SetEnvIfEmptyInt("REDIS_DIAL_TIMEOUT_MS", cfgFile.Redis.DialTimeoutMs)
		appconfig.SetEnvIfEmptyInt("REDIS_READ_TIMEOUT_MS", cfgFile.Redis.ReadTimeoutMs)
		appconfig.SetEnvIfEmptyInt("REDIS_WRITE_TIMEOUT_MS", cfgFile.Redis.WriteTimeoutMs)

		wcfg := cfgFile.Worker
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
		appconfig.SetEnvIfEmpty("REDIS_URL", wcfg.RedisURL)
		appconfig.SetEnvIfEmpty("DATABASE_URL", wcfg.DatabaseURL)
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
		appconfig.SetEnvIfEmptyBool("ALLOW_HOST_CHECKER", wcfg.AllowHostChecker)
		appconfig.SetEnvIfEmptyBool("REQUIRE_CGROUPS_V2", wcfg.RequireCgroupsV2)
		appconfig.SetEnvIfEmpty("SERVICE_NAME", wcfg.Metrics.ServiceName)
		appconfig.SetEnvIfEmpty("INSTANCE_ID", wcfg.Metrics.InstanceID)
		appconfig.SetEnvIfEmptyInt("WORKER_METRICS_PORT", wcfg.MetricsPort)
		appconfig.SetEnvIfEmpty("JOB_STREAM_KEY", wcfg.Stream.StreamKey)
		appconfig.SetEnvIfEmpty("JOB_STREAM_GROUP", wcfg.Stream.Group)
		appconfig.SetEnvIfEmpty("JOB_STREAM_CONSUMER", wcfg.Stream.Consumer)
		appconfig.SetEnvIfEmptyInt("JOB_STREAM_READ_COUNT", wcfg.Stream.ReadCount)
		appconfig.SetEnvIfEmptyInt("JOB_STREAM_BLOCK_MS", wcfg.Stream.BlockMs)
		appconfig.SetEnvIfEmptyInt("JOB_LEASE_SEC", wcfg.Stream.LeaseSec)
		appconfig.SetEnvIfEmptyInt("JOB_HEARTBEAT_SEC", wcfg.Stream.HeartbeatSec)
		appconfig.SetEnvIfEmptyInt("JOB_RECLAIM_INTERVAL_SEC", wcfg.Stream.ReclaimIntervalSec)
		appconfig.SetEnvIfEmptyInt("JOB_RECLAIM_COUNT", wcfg.Stream.ReclaimCount)
		appconfig.SetEnvIfEmptyInt("JOB_RECLAIM_GRACE_SEC", wcfg.Stream.ReclaimGraceSec)
	}

	cfg := worker.LoadConfig()
	slog.Info("工作节点启动中", "id", cfg.WorkerID, "bin", cfg.JudgerBin)
	slog.Info("工作节点并发配置", "max_concurrency", cfg.PoolSize, "num_cpu", runtime.NumCPU())

	if getEnvBool("REQUIRE_CGROUPS_V2", false) {
		if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err != nil {
			slog.Error("Cgroups v2 不可用，按 REQUIRE_CGROUPS_V2=1 配置退出", "error", err)
			os.Exit(1)
		}
	}

	worker.InitMetrics()
	metricsPort := getEnvInt("WORKER_METRICS_PORT", 9092)
	observability.StartMetricsServer(fmt.Sprintf(":%d", metricsPort))

	exec := worker.NewExecutor(cfg.JudgerBin)
	slog.Info("初始化测试用例管理器...")
	tcMgr, err := worker.NewTestCaseManager(cfg)
	if err != nil {
		slog.Error("初始化测试用例管理器失败", "error", err)
		os.Exit(1)
	}

	slog.Info("正在连接 Redis", "addr", cfg.RedisURL)
	rdb := redis.NewClient(buildRedisOptions(cfg.RedisURL))
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		slog.Error("连接 Redis 失败", "error", err)
		os.Exit(1)
	}
	slog.Info("已连接 Redis")

	if strings.TrimSpace(cfg.DatabaseURL) == "" {
		slog.Error("流消费器必须设置 DATABASE_URL")
		os.Exit(1)
	}
	slog.Info("流消费器正在连接 PostgreSQL")
	db, err := repository.NewPostgresDB(context.Background(), cfg.DatabaseURL)
	if err != nil {
		slog.Error("连接 PostgreSQL 失败", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	slog.Info("已连接 PostgreSQL")

	runner := worker.NewJudgeService(cfg, exec, tcMgr, rdb)
	streamConsumer := worker.NewStreamConsumer(cfg, rdb, db, runner)
	streamCtx, cancelStream := context.WithCancel(context.Background())
	defer cancelStream()

	var streamWG sync.WaitGroup
	streamWG.Add(1)
	go func() {
		defer streamWG.Done()
		if err := streamConsumer.Run(streamCtx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("工作节点流消费器异常退出", "error", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("正在关闭...")
	cancelStream()
	streamWG.Wait()
	slog.Info("工作节点已退出")
}
