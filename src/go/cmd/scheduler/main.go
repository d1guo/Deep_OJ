// 调度器入口：B2 后仅保留控制面能力，不执行任何 legacy 数据面动作。
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/d1guo/deep_oj/internal/appconfig"
	"github.com/d1guo/deep_oj/internal/repository"
	"github.com/d1guo/deep_oj/internal/scheduler"
	"github.com/d1guo/deep_oj/pkg/observability"
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
	cfg, cfgPath, err := appconfig.Load()
	if err != nil {
		slog.Error("加载配置失败", "path", cfgPath, "error", err)
		os.Exit(1)
	}
	if cfgPath != "" {
		slog.Info("已加载配置", "path", cfgPath)
	}
	if cfg != nil {
		appconfig.SetEnvIfEmptyInt("REDIS_POOL_SIZE", cfg.Redis.PoolSize)
		appconfig.SetEnvIfEmptyInt("REDIS_MIN_IDLE_CONNS", cfg.Redis.MinIdleConns)
		appconfig.SetEnvIfEmptyInt("REDIS_DIAL_TIMEOUT_MS", cfg.Redis.DialTimeoutMs)
		appconfig.SetEnvIfEmptyInt("REDIS_READ_TIMEOUT_MS", cfg.Redis.ReadTimeoutMs)
		appconfig.SetEnvIfEmptyInt("REDIS_WRITE_TIMEOUT_MS", cfg.Redis.WriteTimeoutMs)
		appconfig.SetEnvIfEmptyInt("PG_MAX_CONNS", cfg.Postgres.MaxConns)
		appconfig.SetEnvIfEmptyInt("PG_MIN_CONNS", cfg.Postgres.MinConns)
		appconfig.SetEnvIfEmptyInt("PG_MAX_CONN_LIFETIME_MIN", cfg.Postgres.MaxConnLifetimeMin)
		appconfig.SetEnvIfEmptyInt("PG_MAX_CONN_IDLE_MIN", cfg.Postgres.MaxConnIdleMin)

		appconfig.SetEnvIfEmpty("REDIS_URL", cfg.Scheduler.RedisURL)
		appconfig.SetEnvIfEmpty("DATABASE_URL", cfg.Scheduler.DatabaseURL)
		appconfig.SetEnvIfEmptyInt("SCHEDULER_METRICS_PORT", cfg.Scheduler.MetricsPort)
		appconfig.SetEnvIfEmptyInt("SCHEDULER_METRICS_POLL_INTERVAL_MS", cfg.Scheduler.MetricsPollMs)
		appconfig.SetEnvIfEmpty("SERVICE_NAME", cfg.Scheduler.Metrics.ServiceName)
		appconfig.SetEnvIfEmpty("INSTANCE_ID", cfg.Scheduler.Metrics.InstanceID)
	}

	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		redisURL = "localhost:6379"
	}
	postgresURL := os.Getenv("DATABASE_URL")
	if postgresURL == "" {
		slog.Error("必须设置 DATABASE_URL")
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		slog.Info("收到退出信号，准备关闭")
		cancel()
	}()

	discovery, err := scheduler.NewWorkerDiscovery()
	if err != nil {
		slog.Error("初始化工作节点发现失败", "error", err)
		os.Exit(1)
	}
	defer discovery.Close()
	go discovery.WatchWorkers(ctx)

	redisClient := repository.NewRedisClient(redisURL)
	if err := redisClient.Ping(ctx); err != nil {
		slog.Error("连接 Redis 失败", "error", err)
		os.Exit(1)
	}
	defer redisClient.Close()

	db, err := repository.NewPostgresDB(ctx, postgresURL)
	if err != nil {
		slog.Error("连接 PostgreSQL 失败", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	scheduler.SetControlPlaneOnly(true)
	scheduler.SetLegacyLoopsStarted(0)
	go scheduler.StartMetricsPoller(ctx, discovery)

	metricsPort := getEnvInt("SCHEDULER_METRICS_PORT", 9091)
	observability.StartMetricsServer(fmt.Sprintf(":%d", metricsPort))

	slog.Info("调度器已进入控制面模式（legacy 数据面已移除）", "worker_count", discovery.GetWorkerCount())

	<-ctx.Done()
	slog.Info("调度器已退出")
}
