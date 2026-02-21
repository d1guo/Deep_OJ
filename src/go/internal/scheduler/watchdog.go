package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/d1guo/deep_oj/internal/repository"
	"github.com/d1guo/deep_oj/pkg/common"
	"github.com/redis/go-redis/v9"
)

const (
	// TaskAssignmentPrefix 表示手动任务分配关系键：task:worker:{jobID} -> workerID
	TaskAssignmentPrefix = common.TaskAssignmentPrefix
	// TaskProcessingQueue 表示处理中任务的 Redis 列表
	TaskProcessingQueue = common.QueueProcessing
	// TaskPendingQueue 表示待处理任务的 Redis 列表
	TaskPendingQueue = common.QueuePending
)

// Watchdog 负责扫描处理中队列，回收僵尸任务。
type Watchdog struct {
	redisClient *repository.RedisClient
	discovery   *WorkerDiscovery
	db          *repository.PostgresDB
	interval    time.Duration
}

// NewWatchdog 创建看门狗实例。
func NewWatchdog(redisClient *repository.RedisClient, discovery *WorkerDiscovery, db *repository.PostgresDB, interval time.Duration) *Watchdog {
	return &Watchdog{
		redisClient: redisClient,
		discovery:   discovery,
		db:          db,
		interval:    interval,
	}
}

// Start 启动看门狗循环。
func (w *Watchdog) Start(ctx context.Context) {
	slog.Info("看门狗启动", "interval", w.interval)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("看门狗停止")
			return
		case <-ticker.C:
			if err := w.scanAndReclaim(ctx); err != nil {
				slog.Error("看门狗扫描失败", "error", err)
			}
		}
	}
}

// scanAndReclaim 扫描处理中队列并回收需要重试的任务。
func (w *Watchdog) scanAndReclaim(ctx context.Context) error {
	// 1. 从 processing ZSET 读取超时任务。
	cutoff := time.Now().Add(-w.interval).UnixMilli()
	tasks, err := w.redisClient.ZRangeByScore(ctx, common.TaskProcessingZSet, &redis.ZRangeBy{
		Min: "-inf",
		Max: fmt.Sprintf("%d", cutoff),
	})
	if err != nil {
		return fmt.Errorf("读取 processing 任务列表失败: %w", err)
	}

	if len(tasks) == 0 {
		return nil
	}

	for _, jobID := range tasks {
		// 2. 检查任务分配关系。
		assignmentKey := TaskAssignmentPrefix + jobID
		workerID, err := w.redisClient.Get(ctx, assignmentKey)

		if err != nil {
			// 分配键缺失：先跳过，避免短暂不一致导致误判。
			continue
		}

		// 3. 检查该工作节点是否仍在线。
		if !w.discovery.IsWorkerActive(workerID) {
			slog.Warn("看门狗：工作节点离线，任务重新入队", "worker_id", workerID, "job_id", jobID)

			err := w.requeueTask(ctx, jobID, assignmentKey)
			if err != nil {
				slog.Error("看门狗：任务重新入队失败", "job_id", jobID, "error", err)
			} else {
				slog.Info("看门狗：任务已重新入队", "job_id", jobID)
			}
		}
	}

	return nil
}

// requeueTask 将任务重新入队并清理关联键。
func (w *Watchdog) requeueTask(ctx context.Context, jobID, assignmentKey string) error {
	payloadKey := common.TaskPayloadPrefix + jobID
	taskData, err := w.redisClient.Get(ctx, payloadKey)
	if err != nil {
		return err
	}
	if err := HandleRetry(ctx, w.redisClient, w.db, jobID, taskData, "看门狗重新入队"); err != nil {
		return err
	}

	// 清理分配键与相关状态，避免下次扫描重复处理。
	if err := w.redisClient.Del(ctx, assignmentKey, payloadKey, common.TaskProcessingStartPrefix+jobID); err != nil {
		return err
	}
	return w.redisClient.ZRem(ctx, common.TaskProcessingZSet, jobID)
}
