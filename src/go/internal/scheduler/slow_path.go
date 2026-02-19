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

// StartSlowPath 启动慢路径兜底（处理超时任务）。
func StartSlowPath(ctx context.Context, redis *repository.RedisClient, db *repository.PostgresDB) {
	slog.Info("启动慢路径监控（保障双写一致性）...")
	tickSec := getEnvInt("SLOW_PATH_TICK_SEC", 60)
	ticker := time.NewTicker(time.Duration(tickSec) * time.Second) // 数据库扫描成本较高，频率不宜过高。
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// 1. 核心：扫描数据库，防止任务丢失（双写一致性）。
			checkLostTasks(ctx, redis, db)

			// 2. 辅助：扫描 Redis，防止处理超时（工作节点崩溃）。
			// 注意：看门狗已覆盖该场景，这里作为第二道防线。
			checkTimeoutTasks(ctx, redis, db)
		}
	}
}

// checkLostTasks 扫描数据库中长期 Pending 的任务，并确认是否仍在 Redis 中。
func checkLostTasks(ctx context.Context, redis *repository.RedisClient, db *repository.PostgresDB) {
	pendingStaleSec := getEnvInt("PENDING_STALE_SEC", 60)
	before := time.Now().Add(-time.Duration(pendingStaleSec) * time.Second)
	dbScanLimit := getEnvInt("SLOW_PATH_DB_SCAN_LIMIT", 100)
	submissions, err := db.GetPendingSubmissions(ctx, before, dbScanLimit)
	if err != nil {
		slog.Error("慢路径：数据库扫描失败", "error", err)
		return
	}

	if len(submissions) == 0 {
		return
	}

	slog.Info("慢路径：在数据库发现待处理任务", "count", len(submissions))

	for _, sub := range submissions {
		// 检查 Redis 中是否存在 (Pending 或 Processing)
		// 注意：该检查开销较大，慢路径频率不能太高。
		// 目前策略：直接构造 TaskRequest 并重新入队，依赖工作节点幂等与结果缓存兜底。

		slog.Info("慢路径：正在恢复任务", "job_id", sub.JobID)

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
			slog.Error("慢路径：重新入队失败", "job_id", sub.JobID, "error", err)
		} else {
			slog.Info("慢路径：任务已重新入队", "job_id", sub.JobID)
		}
	}
}

func buildRecoveredCacheKey(sub *model.Submission) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d|%d|%d", sub.Code, sub.Language, sub.TimeLimit, sub.MemoryLimit, sub.ProblemID)))
	return common.CacheKeyPrefix + hex.EncodeToString(sum[:])
}

func checkTimeoutTasks(ctx context.Context, redis *repository.RedisClient, db *repository.PostgresDB) {
	// 1. 获取超时的 processing 任务（基于 processing_start）。
	cutoffSec := getEnvInt("SLOW_PATH_PROCESSING_CUTOFF_SEC", 30)
	cutoff := time.Now().Add(-time.Duration(cutoffSec) * time.Second).UnixMilli()
	jobIDs, err := redis.ZRangeByScore(ctx, common.TaskProcessingZSet, &redisv8.ZRangeBy{
		Min: "-inf",
		Max: fmt.Sprintf("%d", cutoff),
	})
	if err != nil {
		slog.Error("慢路径：查询 processing zset 失败", "error", err)
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

		slog.Warn("慢路径：processing zset 中任务超时", "job_id", jobID)
		if err := HandleRetry(ctx, redis, db, jobID, taskData, "慢路径超时回收"); err != nil {
			slog.Error("慢路径：超时任务重新入队失败", "job_id", jobID, "error", err)
			continue
		}

		_ = redis.Del(ctx, common.TaskAssignmentPrefix+jobID, payloadKey, common.TaskProcessingStartPrefix+jobID)
		_ = redis.ZRem(ctx, common.TaskProcessingZSet, jobID)
		recoveredCount++
	}

	if recoveredCount > 0 {
		slog.Info("慢路径：已从 Redis 恢复超时任务", "count", recoveredCount)
	}
}
