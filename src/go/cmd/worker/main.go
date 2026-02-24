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
	appconfig.ApplyRuntimeEnvForWorker(cfgFile)

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
