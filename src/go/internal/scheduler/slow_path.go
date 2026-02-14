package scheduler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	"github.com/d1guo/deep_oj/internal/model"
	"github.com/d1guo/deep_oj/internal/repository"
	"github.com/d1guo/deep_oj/pkg/common"
	pb "github.com/d1guo/deep_oj/pkg/proto"
	redisv8 "github.com/go-redis/redis/v8"
	"google.golang.org/protobuf/proto"
)

// StartSlowPath 启动慢路径兜底 (处理超时任务)
func StartSlowPath(ctx context.Context, redis *repository.RedisClient, db *repository.PostgresDB) {
	slog.Info("启动 Slow Path 监控 (双写一致性保障)...")
	tickSec := getEnvInt("SLOW_PATH_TICK_SEC", 60)
	ticker := time.NewTicker(time.Duration(tickSec) * time.Second) // DB Scan cost is higher, run less frequently
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// 1. 核心: 扫描 DB 防止任务丢失 (Dual-Write Consistency)
			checkLostTasks(ctx, redis, db)

			// 2. 辅助: 扫描 Redis 防止处理超时 (Worker Crash)
			// 注意: Watchdog 已经处理了 Worker Crash，这里作为二道防线
			checkTimeoutTasks(ctx, redis, db)
		}
	}
}

// checkLostTasks 扫描 DB 中长期 Pending 的任务，确认是否在 Redis 中
func checkLostTasks(ctx context.Context, redis *repository.RedisClient, db *repository.PostgresDB) {
	pendingStaleSec := getEnvInt("PENDING_STALE_SEC", 60)
	before := time.Now().Add(-time.Duration(pendingStaleSec) * time.Second)
	dbScanLimit := getEnvInt("SLOW_PATH_DB_SCAN_LIMIT", 100)
	submissions, err := db.GetPendingSubmissions(ctx, before, dbScanLimit)
	if err != nil {
		slog.Error("SlowPath: DB Scan failed", "error", err)
		return
	}

	if len(submissions) == 0 {
		return
	}

	slog.Info("SlowPath: Found pending tasks in DB", "count", len(submissions))

	for _, sub := range submissions {
		// 检查 Redis 中是否存在 (Pending 或 Processing)
		// 注意:这开销较大，所以 SlowPath 频率不能太高
		// 优化: 可以只 check pending 队列，或者直接无脑 re-enqueue (Redis Set 去重? List 不去重)
		// 这里的策略是: 生成 TaskRequest -> Serialize -> LPush
		// 即使重复也无所谓，Worker 幂等处理 (TaskAssignment check)?
		// 目前 Worker 主要是评测，重复评测问题不大，只要由 Result cache 兜底

		slog.Info("SlowPath: Recovering task", "job_id", sub.JobID)

		cacheKey := buildRecoveredCacheKey(sub)

		task := &pb.TaskRequest{
			JobId:       sub.JobID,
			Code:        []byte(sub.Code),
			Language:    pb.Language(sub.Language),
			TimeLimit:   int32(sub.TimeLimit),
			MemoryLimit: int32(sub.MemoryLimit),
			CacheKey:    cacheKey,
			ProblemId:   uint32(sub.ProblemID),
			SubmitTime:  sub.CreatedAt.UnixMilli(),
			TraceId:     sub.JobID,
		}

		taskData, _ := proto.Marshal(task)

		// 重新推入 Redis Pending 队列
		if err := redis.LPush(ctx, common.QueuePending, string(taskData)); err != nil {
			slog.Error("SlowPath: Failed to requeue", "job_id", sub.JobID, "error", err)
		} else {
			slog.Info("SlowPath: Successfully requeued task", "job_id", sub.JobID)
		}
	}
}

func buildRecoveredCacheKey(sub *model.Submission) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d|%d|%d", sub.Code, sub.Language, sub.TimeLimit, sub.MemoryLimit, sub.ProblemID)))
	return common.CacheKeyPrefix + hex.EncodeToString(sum[:])
}

func checkTimeoutTasks(ctx context.Context, redis *repository.RedisClient, db *repository.PostgresDB) {
	// 1. 获取超时的 processing 任务 (基于 processing_start)
	cutoffSec := getEnvInt("SLOW_PATH_PROCESSING_CUTOFF_SEC", 30)
	cutoff := time.Now().Add(-time.Duration(cutoffSec) * time.Second).UnixMilli()
	jobIDs, err := redis.ZRangeByScore(ctx, common.TaskProcessingZSet, &redisv8.ZRangeBy{
		Min: "-inf",
		Max: fmt.Sprintf("%d", cutoff),
	})
	if err != nil {
		slog.Error("SlowPath: Failed to query processing zset", "error", err)
		return
	}
	if len(jobIDs) == 0 {
		return
	}

	recoveredCount := 0
	for _, jobID := range jobIDs {
		payloadKey := common.TaskPayloadPrefix + jobID
		taskData, err := redis.Get(ctx, payloadKey)
		if err != nil {
			continue
		}

		slog.Warn("SlowPath: Task timed out in processing zset", "job_id", jobID)
		if err := HandleRetry(ctx, redis, db, jobID, taskData, "slowpath timeout"); err != nil {
			slog.Error("SlowPath: Failed to requeue timed out task", "job_id", jobID, "error", err)
			continue
		}

		_ = redis.Del(ctx, common.TaskAssignmentPrefix+jobID, payloadKey, common.TaskProcessingStartPrefix+jobID)
		_ = redis.ZRem(ctx, common.TaskProcessingZSet, jobID)
		recoveredCount++
	}

	if recoveredCount > 0 {
		slog.Info("SlowPath: Recovered timed out tasks from Redis", "count", recoveredCount)
	}
}
