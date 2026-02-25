// 调度器入口：仅保留控制面能力，不执行任何 legacy 数据面动作。
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
	appconfig.ApplyRuntimeEnvForScheduler(cfg)

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
	schedulerID := os.Getenv("SCHEDULER_ID")

	scheduler.SetControlPlaneOnly(true)
	scheduler.SetLegacyLoopsStarted(0)
	go scheduler.StartMetricsPoller(ctx, discovery)
	go scheduler.StartRepairLoop(ctx, controlPlaneCfg, redisClient, dbPool)
	go scheduler.StartGcLoop(ctx, controlPlaneCfg, redisClient, dbPool)

	metricsPort := getEnvInt("SCHEDULER_METRICS_PORT", 9091)
	observability.StartMetricsServer(fmt.Sprintf(":%d", metricsPort))

	slog.Info(
		"scheduler 控制面已启动",
		"scheduler_id", schedulerID,
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
