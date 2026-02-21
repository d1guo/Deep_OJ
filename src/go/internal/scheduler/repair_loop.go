package scheduler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/d1guo/deep_oj/internal/repository"
	"github.com/d1guo/deep_oj/pkg/common"
	pb "github.com/d1guo/deep_oj/pkg/proto"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/proto"
)

const (
	repairResultOK      = "ok"
	repairResultSkipped = "skipped"
	repairResultError   = "error"

	repairReasonDisabled      = "disabled"
	repairReasonNoCandidate   = "no_candidate"
	repairReasonDedup         = "dedup"
	repairReasonPendingStale  = "pending_stale"
	repairReasonQueryFailed   = "query_failed"
	repairReasonScanFailed    = "scan_failed"
	repairReasonMarshalFailed = "marshal_failed"
	repairReasonSetFailed     = "payload_set_failed"
	repairReasonXAddFailed    = "xadd_failed"
)

type repairCandidate struct {
	JobID       string
	ProblemID   int
	Code        string
	Language    int
	TimeLimit   int
	MemoryLimit int
	AttemptID   int64
	CreatedAt   time.Time
}

type payloadEnvelopeV1 struct {
	SchemaVersion int    `json:"schema_version"`
	TaskDataB64   string `json:"task_data_b64"`
	Encoding      string `json:"encoding"`
	ContentType   string `json:"content_type"`
}

func StartRepairLoop(ctx context.Context, cfg ControlPlaneConfig, redisClient *repository.RedisClient, dbPool *pgxpool.Pool) {
	logger := slog.With("component", "scheduler_control_plane", "loop", "repair")
	if !cfg.RepairEnabled {
		logger.Info("repair loop 已禁用", "event", "repair_disabled")
		ObserveRepairResult(repairResultSkipped, repairReasonDisabled)
		<-ctx.Done()
		return
	}

	interval := time.Duration(cfg.RepairIntervalMs) * time.Millisecond
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	logger.Info(
		"repair loop 已启动",
		"event", "repair_started",
		"interval_ms", cfg.RepairIntervalMs,
		"batch_size", cfg.RepairBatchSize,
		"min_age_sec", cfg.RepairMinAgeSec,
	)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runRepairCycle(ctx, cfg, redisClient, dbPool, logger)
		}
	}
}

func runRepairCycle(ctx context.Context, cfg ControlPlaneConfig, redisClient *repository.RedisClient, dbPool *pgxpool.Pool, logger *slog.Logger) {
	minAge := time.Duration(cfg.RepairMinAgeSec) * time.Second
	candidates, err := fetchRepairCandidates(ctx, dbPool, minAge, cfg.RepairBatchSize)
	if err != nil {
		ObserveRepairResult(repairResultError, repairReasonQueryFailed)
		logger.Error("repair 查询候选任务失败", "event", "repair_cycle_error", "reason", repairReasonQueryFailed, "error", err)
		return
	}

	if len(candidates) == 0 {
		ObserveRepairResult(repairResultSkipped, repairReasonNoCandidate)
		logger.Info("repair 无候选任务", "event", "repair_cycle_end", "reason", repairReasonNoCandidate, "count", 0)
		return
	}

	seen := make(map[string]struct{}, len(candidates))
	repaired := 0
	skipped := 0
	failed := 0
	for _, candidate := range candidates {
		if _, exists := seen[candidate.JobID]; exists {
			skipped++
			ObserveRepairResult(repairResultSkipped, repairReasonDedup)
			continue
		}
		seen[candidate.JobID] = struct{}{}

		if err := enqueueRepairedJob(ctx, cfg, redisClient, candidate); err != nil {
			failed++
			reason := repairReasonXAddFailed
			if err == errRepairMarshal {
				reason = repairReasonMarshalFailed
			} else if err == errRepairSetPayload {
				reason = repairReasonSetFailed
			}
			ObserveRepairResult(repairResultError, reason)
			logger.Error(
				"repair_xadd 失败",
				"event", "repair_xadd",
				"job_id", candidate.JobID,
				"attempt_id", candidate.AttemptID,
				"reason", reason,
				"count", 1,
			)
			continue
		}

		repaired++
		ObserveRepairResult(repairResultOK, repairReasonPendingStale)
		logger.Info(
			"repair_xadd 成功",
			"event", "repair_xadd",
			"job_id", candidate.JobID,
			"attempt_id", candidate.AttemptID,
			"reason", repairReasonPendingStale,
			"count", 1,
		)
	}

	logger.Info(
		"repair 周期结束",
		"event", "repair_cycle_end",
		"candidate_count", len(candidates),
		"repaired_count", repaired,
		"skipped_count", skipped,
		"error_count", failed,
	)
}

func fetchRepairCandidates(ctx context.Context, dbPool *pgxpool.Pool, minAge time.Duration, limit int) ([]repairCandidate, error) {
	if limit <= 0 {
		limit = defaultRepairBatchSize
	}
	before := time.Now().Add(-minAge)
	query := `
		SELECT job_id,
		       COALESCE(problem_id, 0),
		       code,
		       language,
		       time_limit,
		       memory_limit,
		       COALESCE(attempt_id, 0),
		       created_at
		FROM submissions
		WHERE state = 'pending'
		  AND created_at < $1
		ORDER BY created_at ASC
		LIMIT $2
	`
	rows, err := dbPool.Query(ctx, query, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	candidates := make([]repairCandidate, 0, limit)
	for rows.Next() {
		var candidate repairCandidate
		if err := rows.Scan(
			&candidate.JobID,
			&candidate.ProblemID,
			&candidate.Code,
			&candidate.Language,
			&candidate.TimeLimit,
			&candidate.MemoryLimit,
			&candidate.AttemptID,
			&candidate.CreatedAt,
		); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	if rows.Err() != nil {
		return nil, rows.Err()
	}
	return candidates, nil
}

var (
	errRepairMarshal    = fmt.Errorf("repair_marshal")
	errRepairSetPayload = fmt.Errorf("repair_set_payload")
)

func enqueueRepairedJob(ctx context.Context, cfg ControlPlaneConfig, redisClient *repository.RedisClient, candidate repairCandidate) error {
	taskReq := &pb.TaskRequest{
		JobId:       candidate.JobID,
		Code:        []byte(candidate.Code),
		Language:    pb.Language(candidate.Language),
		TimeLimit:   int32(candidate.TimeLimit),
		MemoryLimit: int32(candidate.MemoryLimit),
		ProblemId:   uint32(candidate.ProblemID),
		SubmitTime:  candidate.CreatedAt.UnixMilli(),
		TraceId:     fmt.Sprintf("repair-%s", candidate.JobID),
	}
	taskData, err := proto.Marshal(taskReq)
	if err != nil {
		return errRepairMarshal
	}

	envelopeBytes, err := json.Marshal(payloadEnvelopeV1{
		SchemaVersion: 1,
		TaskDataB64:   base64.StdEncoding.EncodeToString(taskData),
		Encoding:      "base64",
		ContentType:   "application/json",
	})
	if err != nil {
		return errRepairMarshal
	}

	payloadRef := common.TaskPayloadPrefix + candidate.JobID
	payloadTTL := time.Duration(cfg.JobPayloadTTLSec) * time.Second
	if payloadTTL <= 0 {
		payloadTTL = time.Duration(defaultJobPayloadTTLSec) * time.Second
	}
	if err := redisClient.Set(ctx, payloadRef, string(envelopeBytes), payloadTTL); err != nil {
		return errRepairSetPayload
	}

	xaddArgs := &redis.XAddArgs{
		Stream: cfg.JobStreamKey,
		Values: map[string]interface{}{
			"job_id":      candidate.JobID,
			"enqueue_ts":  time.Now().UnixMilli(),
			"payload_ref": payloadRef,
			"priority":    0,
		},
	}
	if cfg.JobStreamMaxLen > 0 {
		xaddArgs.MaxLen = cfg.JobStreamMaxLen
		xaddArgs.Approx = true
	}
	if _, err := redisClient.XAdd(ctx, xaddArgs); err != nil {
		return err
	}
	return nil
}
