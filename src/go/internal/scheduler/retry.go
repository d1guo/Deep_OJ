package scheduler

import (
	"context"
	"os"
	"strconv"
	"time"

	"github.com/d1guo/deep_oj/internal/repository"
	"github.com/d1guo/deep_oj/pkg/common"
)

func maxRetry() int64 {
	if v := os.Getenv("MAX_RETRY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return int64(n)
		}
	}
	return 3
}

// HandleRetry either requeues or moves to DLQ when retry exceeds max.
func HandleRetry(ctx context.Context, redis *repository.RedisClient, db *repository.PostgresDB, jobID, taskData, reason string) error {
	retryKey := common.TaskRetryPrefix + jobID
	retryCount, _ := redis.Incr(ctx, retryKey)
	retryTTLSec := getEnvInt("RETRY_TTL_SEC", 86400)
	_, _ = redis.Expire(ctx, retryKey, time.Duration(retryTTLSec)*time.Second)

	if retryCount > maxRetry() {
		_ = redis.LPush(ctx, common.QueueDead, taskData)
		if db != nil {
			_ = db.UpdateSubmissionState(ctx, jobID, "failed")
			_, _, _ = db.UpdateSubmissionResultIfNotDone(ctx, jobID, "System Error", map[string]any{
				"status":        "System Error",
				"error_message": reason,
				"retry":         retryCount,
			})
		}
		return nil
	}

	if db != nil {
		_ = db.UpdateSubmissionState(ctx, jobID, "retry")
	}
	return redis.RequeueTask(ctx, common.QueueProcessing, common.QueuePending, taskData)
}
