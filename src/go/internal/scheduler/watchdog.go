package scheduler // Changed package to scheduler to align with the directory

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/d1guo/deep_oj/internal/repository"
	"github.com/d1guo/deep_oj/pkg/common"
	redisv8 "github.com/go-redis/redis/v8"
)

const (
	// TaskAssignmentPrefix Key for manual task assignment: task:worker:{jobID} -> workerID
	TaskAssignmentPrefix = common.TaskAssignmentPrefix
	// TaskProcessingQueue Redis List for processing tasks
	TaskProcessingQueue = common.QueueProcessing
	// TaskPendingQueue Redis List for pending tasks
	TaskPendingQueue = common.QueuePending
)

// Watchdog monitors the processing queue for zombie tasks
type Watchdog struct {
	redisClient *repository.RedisClient
	discovery   *EtcdDiscovery
	db          *repository.PostgresDB
	interval    time.Duration
}

// NewWatchdog creates a new Watchdog instance
func NewWatchdog(redisClient *repository.RedisClient, discovery *EtcdDiscovery, db *repository.PostgresDB, interval time.Duration) *Watchdog {
	return &Watchdog{
		redisClient: redisClient,
		discovery:   discovery,
		db:          db,
		interval:    interval,
	}
}

// Start runs the watchdog loop
func (w *Watchdog) Start(ctx context.Context) {
	slog.Info("🐶 Watchdog starting", "interval", w.interval)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("🐶 Watchdog stopping")
			return
		case <-ticker.C:
			if err := w.scanAndReclaim(ctx); err != nil {
				slog.Error("⚠️ Watchdog scan failed", "error", err)
			}
		}
	}
}

// scanAndReclaim checks tasks in the processing queue
func (w *Watchdog) scanAndReclaim(ctx context.Context) error {
	// 1. Get tasks in processing ZSET (older than interval)
	cutoff := time.Now().Add(-w.interval).UnixMilli()
	tasks, err := w.redisClient.ZRangeByScore(ctx, common.TaskProcessingZSet, &redisv8.ZRangeBy{
		Min: "-inf",
		Max: fmt.Sprintf("%d", cutoff),
	})
	if err != nil {
		return fmt.Errorf("failed to list processing tasks: %w", err)
	}

	if len(tasks) == 0 {
		return nil
	}

	for _, jobID := range tasks {
		// 3. Check TaskAssignment in Redis
		assignmentKey := TaskAssignmentPrefix + jobID
		workerID, err := w.redisClient.Get(ctx, assignmentKey)

		if err != nil {
			// Case A: Assignment Key Missing
			// Logically, if the task is in processing but has no assignment for a long time, it's a zombie.
			// But we'll skip for now to be safe, assuming short-lived inconsistencies.
			continue
		}

		// 4. Check Discovery Cache for this WorkerID
		if !w.discovery.IsWorkerActive(workerID) {
			slog.Warn("☠️ Watchdog: Worker dead, requeuing task", "worker_id", workerID, "job_id", jobID)

			err := w.requeueTask(ctx, jobID, assignmentKey)
			if err != nil {
				slog.Error("❌ Watchdog: Failed to requeue task", "job_id", jobID, "error", err)
			} else {
				slog.Info("🔄 Watchdog: Task successfully requeued", "job_id", jobID)
			}
		}
	}

	return nil
}

// requeueTask atomically moves task back to pending and cleans up assignment
func (w *Watchdog) requeueTask(ctx context.Context, jobID, assignmentKey string) error {
	// We need to:
	// 1. LREM processing 1 taskData
	// 2. LPUSH pending taskData
	// 3. DEL assignmentKey (to prevent double check)

	// Since RedisClient.RequeueTask does the list move, we can use that,
	// but we also need to delete the assignment key.
	// Best to use a pipeline or simply extend RequeueTask logic.

	payloadKey := common.TaskPayloadPrefix + jobID
	taskData, err := w.redisClient.Get(ctx, payloadKey)
	if err != nil {
		return err
	}
	if err := HandleRetry(ctx, w.redisClient, w.db, jobID, taskData, "watchdog requeue"); err != nil {
		return err
	}

	// Clean up assignment so next scan doesn't process it (though it's out of processing queue now)
	if err := w.redisClient.Del(ctx, assignmentKey, payloadKey, common.TaskProcessingStartPrefix+jobID); err != nil {
		return err
	}
	return w.redisClient.ZRem(ctx, common.TaskProcessingZSet, jobID)
}
