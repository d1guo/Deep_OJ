package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/d1guo/deep_oj/internal/repository"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	gcResultOK      = "ok"
	gcResultSkipped = "skipped"
	gcResultError   = "error"

	gcReasonDisabled         = "disabled"
	gcReasonDryRun           = "dry_run"
	gcReasonInvalidRetention = "invalid_retention"
	gcReasonEstimateFailed   = "estimate_failed"
	gcReasonDeleteFailed     = "delete_failed"
)

func StartGcLoop(ctx context.Context, cfg ControlPlaneConfig, redisClient *repository.RedisClient, dbPool *pgxpool.Pool) {
	logger := slog.With("component", "scheduler_control_plane", "loop", "gc")
	if !cfg.GCEnabled {
		logger.Info("gc loop 已禁用", "event", "gc_disabled")
		ObserveStreamTrimResult(gcResultSkipped)
		ObserveDBGcResult(gcResultSkipped, gcReasonDisabled)
		<-ctx.Done()
		return
	}

	interval := time.Duration(cfg.GCIntervalMs) * time.Millisecond
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	logger.Info(
		"gc loop 已启动",
		"event", "gc_started",
		"interval_ms", cfg.GCIntervalMs,
		"stream_trim_maxlen", cfg.StreamTrimMaxLen,
		"db_retention_days", cfg.DBRetentionDays,
		"db_delete_enabled", cfg.DBDeleteEnabled,
	)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runGCCycle(ctx, cfg, redisClient, dbPool, logger)
		}
	}
}

func runGCCycle(ctx context.Context, cfg ControlPlaneConfig, redisClient *repository.RedisClient, dbPool *pgxpool.Pool, logger *slog.Logger) {
	trimmed, err := runStreamTrim(ctx, cfg, redisClient)
	if err != nil {
		ObserveStreamTrimResult(gcResultError)
		logger.Error("stream_trim 执行失败", "event", "stream_trim", "result", gcResultError, "error", err)
	} else {
		ObserveStreamTrimResult(gcResultOK)
		logger.Info("stream_trim 执行成功", "event", "stream_trim", "result", gcResultOK, "trimmed", trimmed, "maxlen", cfg.StreamTrimMaxLen)
	}

	if cfg.DBRetentionDays <= 0 {
		ObserveDBGcResult(gcResultSkipped, gcReasonInvalidRetention)
		logger.Info("db_gc 跳过：retention 配置无效", "event", "db_gc_plan", "result", gcResultSkipped, "reason", gcReasonInvalidRetention)
		return
	}

	cutoff := time.Now().Add(-time.Duration(cfg.DBRetentionDays) * 24 * time.Hour)
	submissionEstimate, outboxEstimate, err := estimateDBGC(ctx, dbPool, cutoff)
	if err != nil {
		ObserveDBGcResult(gcResultError, gcReasonEstimateFailed)
		logger.Error("db_gc 估算失败", "event", "db_gc_plan", "result", gcResultError, "reason", gcReasonEstimateFailed, "error", err)
		return
	}

	logger.Info(
		"db_gc 计划",
		"event", "db_gc_plan",
		"result", gcResultSkipped,
		"table", "submissions,outbox_events",
		"condition", "updated_at<cutoff",
		"cutoff", cutoff.Format(time.RFC3339),
		"estimated_submissions", submissionEstimate,
		"estimated_outbox_events", outboxEstimate,
		"db_delete_enabled", cfg.DBDeleteEnabled,
	)

	if !cfg.DBDeleteEnabled {
		ObserveDBGcResult(gcResultSkipped, gcReasonDryRun)
		return
	}

	deletedSubmissions, err := deleteOldDoneSubmissions(ctx, dbPool, cutoff, cfg.DBDeleteBatch)
	if err != nil {
		ObserveDBGcResult(gcResultError, gcReasonDeleteFailed)
		logger.Error("db_gc 删除 submissions 失败", "event", "db_gc_delete", "result", gcResultError, "reason", gcReasonDeleteFailed, "error", err)
		return
	}
	deletedOutbox, err := deleteOldOutboxEvents(ctx, dbPool, cutoff, cfg.DBDeleteBatch)
	if err != nil {
		ObserveDBGcResult(gcResultError, gcReasonDeleteFailed)
		logger.Error("db_gc 删除 outbox_events 失败", "event", "db_gc_delete", "result", gcResultError, "reason", gcReasonDeleteFailed, "error", err)
		return
	}

	ObserveDBGcResult(gcResultOK, "deleted")
	logger.Info(
		"db_gc 删除完成",
		"event", "db_gc_delete",
		"result", gcResultOK,
		"deleted_submissions", deletedSubmissions,
		"deleted_outbox_events", deletedOutbox,
		"batch_size", cfg.DBDeleteBatch,
	)
}

func runStreamTrim(ctx context.Context, cfg ControlPlaneConfig, redisClient *repository.RedisClient) (int64, error) {
	if cfg.StreamTrimMaxLen <= 0 {
		return 0, nil
	}
	result, err := redisClient.Eval(ctx,
		"return redis.call('XTRIM', KEYS[1], 'MAXLEN', '~', ARGV[1])",
		[]string{cfg.JobStreamKey},
		cfg.StreamTrimMaxLen,
	)
	if err != nil {
		return 0, err
	}
	return parseEvalInt(result)
}

func parseEvalInt(value interface{}) (int64, error) {
	switch v := value.(type) {
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	case uint64:
		return int64(v), nil
	case string:
		parsed, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0, err
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("unsupported eval integer type: %T", value)
	}
}

func estimateDBGC(ctx context.Context, dbPool *pgxpool.Pool, cutoff time.Time) (int64, int64, error) {
	var submissionCount int64
	if err := dbPool.QueryRow(
		ctx,
		`SELECT COUNT(1) FROM submissions WHERE state = 'done' AND updated_at < $1`,
		cutoff,
	).Scan(&submissionCount); err != nil {
		return 0, 0, err
	}

	var outboxCount int64
	if err := dbPool.QueryRow(
		ctx,
		`SELECT COUNT(1) FROM outbox_events WHERE status = 'delivered' AND updated_at < $1`,
		cutoff,
	).Scan(&outboxCount); err != nil {
		return 0, 0, err
	}

	return submissionCount, outboxCount, nil
}

func deleteOldDoneSubmissions(ctx context.Context, dbPool *pgxpool.Pool, cutoff time.Time, batchSize int) (int64, error) {
	if batchSize <= 0 {
		batchSize = defaultDBDeleteBatchSize
	}
	var totalDeleted int64
	for {
		cmd, err := dbPool.Exec(ctx, `
			WITH targets AS (
				SELECT id
				FROM submissions
				WHERE state = 'done' AND updated_at < $1
				ORDER BY updated_at ASC
				LIMIT $2
			)
			DELETE FROM submissions s
			USING targets t
			WHERE s.id = t.id
		`, cutoff, batchSize)
		if err != nil {
			return totalDeleted, err
		}
		deleted := cmd.RowsAffected()
		totalDeleted += deleted
		if deleted < int64(batchSize) {
			break
		}
	}
	return totalDeleted, nil
}

func deleteOldOutboxEvents(ctx context.Context, dbPool *pgxpool.Pool, cutoff time.Time, batchSize int) (int64, error) {
	if batchSize <= 0 {
		batchSize = defaultDBDeleteBatchSize
	}
	var totalDeleted int64
	for {
		cmd, err := dbPool.Exec(ctx, `
			WITH targets AS (
				SELECT id
				FROM outbox_events
				WHERE status = 'delivered' AND updated_at < $1
				ORDER BY updated_at ASC
				LIMIT $2
			)
			DELETE FROM outbox_events o
			USING targets t
			WHERE o.id = t.id
		`, cutoff, batchSize)
		if err != nil {
			return totalDeleted, err
		}
		deleted := cmd.RowsAffected()
		totalDeleted += deleted
		if deleted < int64(batchSize) {
			break
		}
	}
	return totalDeleted, nil
}
