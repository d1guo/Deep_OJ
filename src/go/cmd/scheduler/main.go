/**
 * @file main.go
 * @brief Go Scheduler 入口
 *
 * 架构定位: 任务调度层
 * 技术选型: Etcd (服务发现) + gRPC (Worker 通信) + Redis (任务队列)
 *
 * ===========================================================================
 * 面试八股知识点
 * ===========================================================================
 *
 * 1. Etcd 服务发现 vs 传统配置:
 *    - 传统: 硬编码 Worker 地址，重启才能更新
 *    - Etcd: Worker 动态注册，实时感知变化
 *    - Lease 机制: Worker 定期续约，超时自动注销
 *
 * 2. 负载均衡策略:
 *    - Round-Robin: 简单轮询，适合同构服务
 *    - Weighted: 加权轮询，根据 Worker 能力分配
 *    - Least-Connections: 最少连接优先
 *    - Consistent-Hashing: 一致性哈希，适合缓存场景
 *
 * 3. gRPC 优势:
 *    - HTTP/2: 多路复用，头部压缩
 *    - Protobuf: 紧凑的二进制序列化
 *    - 流式传输: 双向流支持
 *    - 代码生成: 强类型接口
 *
 * 4. 可靠性设计:
 *    - ACK 机制: 任务确认后才从队列移除
 *    - 超时检测: 处理中任务超时后重新入队
 *    - 重试策略: 指数退避 (Exponential Backoff)
 */
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/d1guo/deep_oj/internal/appconfig"
	"github.com/d1guo/deep_oj/internal/repository"
	"github.com/d1guo/deep_oj/internal/scheduler"
	"github.com/d1guo/deep_oj/pkg/common"
	"github.com/d1guo/deep_oj/pkg/observability"
	pb "github.com/d1guo/deep_oj/pkg/proto"
	"github.com/go-redis/redis/v8"
	"google.golang.org/protobuf/proto"
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
		slog.Error("Failed to load config", "path", cfgPath, "error", err)
		os.Exit(1)
	}
	if cfgPath != "" {
		slog.Info("Loaded config", "path", cfgPath)
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

		appconfig.SetEnvIfEmptySlice("ETCD_ENDPOINTS", cfg.Scheduler.EtcdEndpoints)
		appconfig.SetEnvIfEmpty("REDIS_URL", cfg.Scheduler.RedisURL)
		appconfig.SetEnvIfEmpty("DATABASE_URL", cfg.Scheduler.DatabaseURL)
		appconfig.SetEnvIfEmptyInt("WORKER_CAPACITY", cfg.Scheduler.WorkerCapacity)
		appconfig.SetEnvIfEmptyInt("MAX_RETRY", cfg.Scheduler.MaxRetry)
		appconfig.SetEnvIfEmptyInt("RETRY_TTL_SEC", cfg.Scheduler.RetryTTLSec)
		appconfig.SetEnvIfEmpty("SCHEDULER_ID", cfg.Scheduler.SchedulerID)
		appconfig.SetEnvIfEmptyInt("SCHEDULER_METRICS_PORT", cfg.Scheduler.MetricsPort)
		appconfig.SetEnvIfEmptyInt("SCHEDULER_METRICS_POLL_INTERVAL_MS", cfg.Scheduler.MetricsPollMs)
		appconfig.SetEnvIfEmptyInt("ETCD_DIAL_TIMEOUT_MS", cfg.Scheduler.EtcdDialTimeoutMs)
		appconfig.SetEnvIfEmptyInt("QUEUE_BRPOP_TIMEOUT_SEC", cfg.Scheduler.Queue.BRPopTimeoutSec)
		appconfig.SetEnvIfEmptyInt("NO_WORKER_SLEEP_MS", cfg.Scheduler.Queue.NoWorkerSleepMs)
		appconfig.SetEnvIfEmptyInt("ASSIGNMENT_TTL_SEC", cfg.Scheduler.Queue.AssignmentTTLSec)
		appconfig.SetEnvIfEmptyInt("PAYLOAD_TTL_SEC", cfg.Scheduler.Queue.PayloadTTLSec)
		appconfig.SetEnvIfEmptyInt("PROCESSING_START_TTL_SEC", cfg.Scheduler.Queue.ProcessingStartTTLSec)
		appconfig.SetEnvIfEmptyInt("INFLIGHT_TTL_SEC", cfg.Scheduler.Queue.InflightTTLSec)
		appconfig.SetEnvIfEmptyInt("ACK_PENDING_COUNT", cfg.Scheduler.AckListener.PendingCount)
		appconfig.SetEnvIfEmptyInt("ACK_PENDING_BLOCK_MS", cfg.Scheduler.AckListener.PendingBlockMs)
		appconfig.SetEnvIfEmptyInt("ACK_NEW_COUNT", cfg.Scheduler.AckListener.NewCount)
		appconfig.SetEnvIfEmptyInt("ACK_NEW_BLOCK_MS", cfg.Scheduler.AckListener.NewBlockMs)
		appconfig.SetEnvIfEmptyInt("SLOW_PATH_TICK_SEC", cfg.Scheduler.SlowPath.TickSec)
		appconfig.SetEnvIfEmptyInt("SLOW_PATH_PROCESSING_CUTOFF_SEC", cfg.Scheduler.SlowPath.ProcessingCutoffSec)
		appconfig.SetEnvIfEmptyInt("PENDING_STALE_SEC", cfg.Scheduler.SlowPath.PendingStaleSec)
		appconfig.SetEnvIfEmptyInt("SLOW_PATH_DB_SCAN_LIMIT", cfg.Scheduler.SlowPath.DBScanLimit)
		appconfig.SetEnvIfEmptyInt("WATCHDOG_INTERVAL_SEC", cfg.Scheduler.Watchdog.IntervalSec)
		appconfig.SetEnvIfEmptyInt("DISPATCH_CONN_TIMEOUT_MS", cfg.Scheduler.Dispatch.ConnTimeoutMs)
		appconfig.SetEnvIfEmptyInt("DISPATCH_RPC_TIMEOUT_MS", cfg.Scheduler.Dispatch.RPCTimeoutMs)
		appconfig.SetEnvIfEmptyInt("DISPATCH_MAX_RETRIES", cfg.Scheduler.Dispatch.MaxRetries)
		appconfig.SetEnvIfEmptyInt("DISPATCH_BACKOFF_BASE_MS", cfg.Scheduler.Dispatch.BackoffBaseMs)
		appconfig.SetEnvIfEmpty("GRPC_TLS_CERT", cfg.Scheduler.GRPCTLS.Cert)
		appconfig.SetEnvIfEmpty("GRPC_TLS_KEY", cfg.Scheduler.GRPCTLS.Key)
		appconfig.SetEnvIfEmpty("GRPC_TLS_CA", cfg.Scheduler.GRPCTLS.CA)
		appconfig.SetEnvIfEmpty("SERVICE_NAME", cfg.Scheduler.Metrics.ServiceName)
		appconfig.SetEnvIfEmpty("INSTANCE_ID", cfg.Scheduler.Metrics.InstanceID)
	}

	// =========================================================================
	// 1. 读取配置
	// =========================================================================
	etcdEndpoints := os.Getenv("ETCD_ENDPOINTS")
	if etcdEndpoints == "" {
		etcdEndpoints = "localhost:2379"
	}

	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		redisURL = "localhost:6379"
	}
	workerCapacity := 4
	if v := os.Getenv("WORKER_CAPACITY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			workerCapacity = n
		}
	}

	// =========================================================================
	// 2. 初始化 Context (支持优雅关闭)
	// =========================================================================
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 监听中断信号
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		slog.Info("🛑 Received shutdown signal...")
		cancel()
	}()

	// =========================================================================
	// 3. 初始化 Etcd 服务发现
	// =========================================================================
	endpoints := strings.Split(etcdEndpoints, ",")
	discovery, err := scheduler.NewEtcdDiscovery(endpoints)
	if err != nil {
		slog.Error("❌ Failed to connect to Etcd", "error", err)
		os.Exit(1)
	}
	defer discovery.Close()
	slog.Info("✅ Connected to Etcd")

	// 启动 Worker 监听
	go discovery.WatchWorkers(ctx)

	// =========================================================================
	// 4. 初始化 Redis 客户端
	// =========================================================================
	redisClient := repository.NewRedisClient(redisURL)
	if err := redisClient.Ping(ctx); err != nil {
		slog.Error("❌ Failed to connect to Redis", "error", err)
		os.Exit(1)
	}
	slog.Info("✅ Connected to Redis")

	// =========================================================================
	// 4.5 初始化 PostgreSQL (用于 ACK 回调更新状态)
	// =========================================================================
	postgresURL := os.Getenv("DATABASE_URL")
	if postgresURL == "" {
		slog.Error("❌ DATABASE_URL must be set")
		os.Exit(1)
	}
	db, err := repository.NewPostgresDB(ctx, postgresURL)
	if err != nil {
		slog.Error("❌ Failed to connect to PostgreSQL", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	slog.Info("✅ Connected to PostgreSQL")

	// 启动 ACK 监听器
	go scheduler.StartAckListener(ctx, redisClient, db)

	// =========================================================================
	// 6. [Task 3.3] 启动监控 (Probes & Metrics)
	// =========================================================================

	// 6.1 启动 Metrics Poller (Redis/Etcd 状态)
	go scheduler.StartMetricsPoller(ctx, redisClient, discovery)

	// 6.2 暴露 Prometheus Metrics endpoint
	metricsPort := getEnvInt("SCHEDULER_METRICS_PORT", 9091)
	observability.StartMetricsServer(fmt.Sprintf(":%d", metricsPort))

	// 启动慢路径兜底 (Slow Path)
	go scheduler.StartSlowPath(ctx, redisClient, db)

	// 启动 Watchdog (防止 Worker 宕机导致的任务泄漏)
	watchdogInterval := time.Duration(getEnvInt("WATCHDOG_INTERVAL_SEC", 5)) * time.Second
	watchdog := scheduler.NewWatchdog(redisClient, discovery, db, watchdogInterval)
	go watchdog.Start(ctx)

	// =========================================================================
	// 5. 启动任务分发循环
	// =========================================================================	// 5. 启动任务分发循环
	slog.Info("🚀 Scheduler started, waiting for tasks...")

	var wg sync.WaitGroup

	for {
		select {
		case <-ctx.Done():
			slog.Info("👋 Scheduler stopping... waiting for active tasks")
			wg.Wait()
			slog.Info("👋 Scheduler exited")
			return
		default:
		}

		// 阻塞等待任务 (5 秒超时)
		brpopTimeout := time.Duration(getEnvInt("QUEUE_BRPOP_TIMEOUT_SEC", 5)) * time.Second
		result, err := redisClient.BRPopLPush(ctx, common.QueuePending, common.QueueProcessing, brpopTimeout)
		if err != nil || result == "" {
			continue
		}

		// 解析任务 (Protobuf)
			task := &pb.TaskRequest{}
			if err := proto.Unmarshal([]byte(result), task); err != nil {
				slog.Warn("⚠️ Failed to parse task", "error", err)
				_ = redisClient.LRem(ctx, common.QueueProcessing, 1, result)
				_ = redisClient.LPush(ctx, common.QueueDead, result)
				continue
			}

		jobID := task.JobId
		slog.Info("📦 Received task", "job_id", jobID)
		processingStart := time.Now().UnixMilli()

		// 获取可用 Worker
		workerID, workerAddr, ok := selectWorker(ctx, redisClient, discovery, workerCapacity)
		if !ok {
			slog.Warn("⚠️ No workers available, task will retry later", "job_id", jobID)
			if err := redisClient.RequeueTask(ctx, common.QueueProcessing, common.QueuePending, result); err != nil {
				slog.Error("Failed to requeue task", "job_id", jobID, "error", err)
			}
			noWorkerSleep := time.Duration(getEnvInt("NO_WORKER_SLEEP_MS", 1000)) * time.Millisecond
			time.Sleep(noWorkerSleep)
			continue
		}

		// 【关键】记录任务分配关系 (Job -> Worker)
		// TTL 设置为 10 分钟 (假设判题不会超过 10 分钟)
		assignmentKey := common.TaskAssignmentPrefix + jobID
		assignmentTTL := time.Duration(getEnvInt("ASSIGNMENT_TTL_SEC", 600)) * time.Second
		if err := redisClient.Set(ctx, assignmentKey, workerID, assignmentTTL); err != nil {
			slog.Error("❌ Failed to set assignment", "job_id", jobID, "error", err)
			// 即使失败也尝试继续，或者选择回滚
		}

		// 记录任务 payload 便于 O(1) 清理
		payloadKey := common.TaskPayloadPrefix + jobID
		payloadTTL := time.Duration(getEnvInt("PAYLOAD_TTL_SEC", 1800)) * time.Second
		if err := redisClient.Set(ctx, payloadKey, result, payloadTTL); err != nil {
			slog.Error("❌ Failed to set payload", "job_id", jobID, "error", err)
		}

		// 记录 processing_start 与 ZSET
		startKey := common.TaskProcessingStartPrefix + jobID
		processingTTL := time.Duration(getEnvInt("PROCESSING_START_TTL_SEC", 1800)) * time.Second
		if err := redisClient.Set(ctx, startKey, fmt.Sprintf("%d", processingStart), processingTTL); err != nil {
			slog.Error("❌ Failed to set processing_start", "job_id", jobID, "error", err)
		}
		if err := redisClient.ZAdd(ctx, common.TaskProcessingZSet, &redis.Z{Score: float64(processingStart), Member: jobID}); err != nil {
			slog.Error("❌ Failed to zadd processing", "job_id", jobID, "error", err)
		}

		// 更新 DB 状态为 processing
		if err := db.UpdateSubmissionState(ctx, jobID, "processing"); err != nil {
			slog.Error("❌ Failed to update submission state", "job_id", jobID, "error", err)
		}

		// 增加 inflight 计数
		inflightKey := common.WorkerInflightPrefix + workerID
		_, _ = redisClient.Incr(ctx, inflightKey)
		inflightTTL := time.Duration(getEnvInt("INFLIGHT_TTL_SEC", 1800)) * time.Second
		_, _ = redisClient.Expire(ctx, inflightKey, inflightTTL)

		// 异步分发任务
		wg.Add(1)
		go func(addr string, taskData []byte) {
			defer wg.Done()
			if err := scheduler.DispatchTask(ctx, addr, taskData, redisClient); err != nil {
				slog.Error("❌ Failed to dispatch task", "job_id", jobID, "error", err)

				if errors.Is(err, common.ErrNonRetryable) {
					slog.Error("🛑 Discarding non-retryable task", "job_id", jobID)
					// Poison Pill: 进入死信队列并标记失败
					_ = redisClient.LPush(ctx, common.QueueDead, string(taskData))
					_ = db.UpdateSubmissionState(ctx, jobID, "failed")
					_, _, _ = db.UpdateSubmissionResultIfNotDone(ctx, jobID, "System Error", map[string]any{
						"status":        "System Error",
						"error_message": "non-retryable dispatch error",
					})
				} else {
					if err := scheduler.HandleRetry(ctx, redisClient, db, jobID, string(taskData), "dispatch retry exceeded"); err != nil {
						slog.Error("HandleRetry failed", "job_id", jobID, "error", err)
					}
				}

				redisClient.Del(ctx, assignmentKey)
				redisClient.Del(ctx, payloadKey, startKey)
				_ = redisClient.ZRem(ctx, common.TaskProcessingZSet, jobID)
				// 回滚 inflight
				_, _ = redisClient.Decr(ctx, inflightKey)
			} else {
				// 成功时不移除！等待 ACK Listener 移除
				slog.Debug("Dispatch success", "job_id", jobID)
			}
		}(workerAddr, []byte(result))
	}
}

func selectWorker(ctx context.Context, redisClient *repository.RedisClient, discovery *scheduler.EtcdDiscovery, capacity int) (string, string, bool) {
	total := discovery.GetWorkerCount()
	if total == 0 {
		return "", "", false
	}
	for i := 0; i < total; i++ {
		workerID, workerAddr, ok := discovery.GetNextWorker()
		if !ok {
			return "", "", false
		}
		inflightKey := common.WorkerInflightPrefix + workerID
		inflightStr, err := redisClient.Get(ctx, inflightKey)
		if err != nil || inflightStr == "" {
			return workerID, workerAddr, true
		}
		n, err := strconv.Atoi(inflightStr)
		if err != nil {
			return workerID, workerAddr, true
		}
		if n < capacity {
			return workerID, workerAddr, true
		}
	}
	return "", "", false
}
