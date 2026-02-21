// 调度器入口：仅保留控制面能力，不执行任何 legacy 数据面动作。
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/d1guo/deep_oj/internal/appconfig"
	"github.com/d1guo/deep_oj/internal/repository"
	"github.com/d1guo/deep_oj/internal/scheduler"
	"github.com/d1guo/deep_oj/pkg/observability"
	"gopkg.in/yaml.v3"
)

type schedulerControlPlaneConfigFile struct {
	Scheduler struct {
		ControlPlane struct {
			RepairEnabled    *bool  `yaml:"repair_enabled"`
			RepairIntervalMs *int   `yaml:"repair_interval_ms"`
			RepairBatchSize  *int   `yaml:"repair_batch_size"`
			RepairMinAgeSec  *int   `yaml:"repair_min_age_sec"`
			GCEnabled        *bool  `yaml:"gc_enabled"`
			GCIntervalMs     *int   `yaml:"gc_interval_ms"`
			StreamTrimMaxLen *int64 `yaml:"stream_trim_maxlen"`
			DBRetentionDays  *int   `yaml:"db_retention_days"`
			DBDeleteEnabled  *bool  `yaml:"db_delete_enabled"`
			DBDeleteBatch    *int   `yaml:"db_delete_batch_size"`
		} `yaml:"control_plane"`
	} `yaml:"scheduler"`
}

func getEnvInt(key string, fallback int) int {
	if value, ok := os.LookupEnv(key); ok {
		if i, err := strconv.Atoi(value); err == nil && i > 0 {
			return i
		}
	}
	return fallback
}

func setEnvIfEmptyBool(key string, value *bool) {
	if value == nil {
		return
	}
	appconfig.SetEnvIfEmpty(key, strconv.FormatBool(*value))
}

func setEnvIfEmptyIntPtr(key string, value *int) {
	if value == nil {
		return
	}
	appconfig.SetEnvIfEmpty(key, strconv.Itoa(*value))
}

func setEnvIfEmptyInt64Ptr(key string, value *int64) {
	if value == nil {
		return
	}
	appconfig.SetEnvIfEmpty(key, strconv.FormatInt(*value, 10))
}

func applyControlPlaneDefaultsFromConfig(configPath string) {
	if strings.TrimSpace(configPath) == "" {
		return
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		slog.Warn("读取配置文件失败，跳过 scheduler.control_plane 默认值回填", "path", configPath, "error", err)
		return
	}

	var cfg schedulerControlPlaneConfigFile
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		slog.Warn("解析配置文件失败，跳过 scheduler.control_plane 默认值回填", "path", configPath, "error", err)
		return
	}

	cp := cfg.Scheduler.ControlPlane
	setEnvIfEmptyBool("SCHEDULER_REPAIR_ENABLED", cp.RepairEnabled)
	setEnvIfEmptyIntPtr("SCHEDULER_REPAIR_INTERVAL_MS", cp.RepairIntervalMs)
	setEnvIfEmptyIntPtr("SCHEDULER_REPAIR_BATCH_SIZE", cp.RepairBatchSize)
	setEnvIfEmptyIntPtr("SCHEDULER_REPAIR_MIN_AGE_SEC", cp.RepairMinAgeSec)

	setEnvIfEmptyBool("SCHEDULER_GC_ENABLED", cp.GCEnabled)
	setEnvIfEmptyIntPtr("SCHEDULER_GC_INTERVAL_MS", cp.GCIntervalMs)
	setEnvIfEmptyInt64Ptr("SCHEDULER_STREAM_TRIM_MAXLEN", cp.StreamTrimMaxLen)
	setEnvIfEmptyIntPtr("SCHEDULER_DB_RETENTION_DAYS", cp.DBRetentionDays)
	setEnvIfEmptyBool("SCHEDULER_DB_DELETE_ENABLED", cp.DBDeleteEnabled)
	setEnvIfEmptyIntPtr("SCHEDULER_DB_DELETE_BATCH_SIZE", cp.DBDeleteBatch)
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
		appconfig.SetEnvIfEmpty("JOB_STREAM_KEY", cfg.API.Stream.StreamKey)
		appconfig.SetEnvIfEmptyInt64("JOB_STREAM_MAXLEN", cfg.API.Stream.StreamMaxLen)
		appconfig.SetEnvIfEmptyInt("JOB_PAYLOAD_TTL_SEC", cfg.API.Stream.JobPayloadTTLSec)
	}
	applyControlPlaneDefaultsFromConfig(cfgPath)

	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		redisURL = "localhost:6379"
	}
	postgresURL := os.Getenv("DATABASE_URL")
	if postgresURL == "" {
		slog.Error("必须设置 DATABASE_URL")
		os.Exit(1)
	}
	if err := scheduler.GuardLegacyDataPlane(); err != nil {
		slog.Error("legacy 数据面护栏拦截启动", "error", err)
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

	dbPool, err := scheduler.OpenControlPlanePool(ctx, postgresURL)
	if err != nil {
		slog.Error("连接 PostgreSQL 失败", "error", err)
		os.Exit(1)
	}
	defer dbPool.Close()

	controlPlaneCfg := scheduler.LoadControlPlaneConfig()

	scheduler.SetControlPlaneOnly(true)
	scheduler.SetLegacyLoopsStarted(0)
	go scheduler.StartMetricsPoller(ctx, discovery)
	go scheduler.StartRepairLoop(ctx, controlPlaneCfg, redisClient, dbPool)
	go scheduler.StartGcLoop(ctx, controlPlaneCfg, redisClient, dbPool)

	metricsPort := getEnvInt("SCHEDULER_METRICS_PORT", 9091)
	observability.StartMetricsServer(fmt.Sprintf(":%d", metricsPort))

	slog.Info(
		"scheduler 控制面已启动",
		"dispatch_mode", controlPlaneCfg.DispatchMode,
		"control_plane_only", true,
		"legacy_loops_started", 0,
		"control_plane_loops_started", "repair,gc",
		"worker_count", discovery.GetWorkerCount(),
		"repair_enabled", controlPlaneCfg.RepairEnabled,
		"gc_enabled", controlPlaneCfg.GCEnabled,
	)

	<-ctx.Done()
	slog.Info("调度器已退出")
}
