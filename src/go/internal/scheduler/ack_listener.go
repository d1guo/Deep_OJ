/**
 * @file ack_listener.go
 * @brief 任务结果确认监听器
 */
package scheduler

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"time"

	"github.com/d1guo/deep_oj/internal/repository"
	"github.com/d1guo/deep_oj/pkg/common"
	redisv8 "github.com/go-redis/redis/v8"
)

// StartAckListener 启动 ACK 监听
func StartAckListener(ctx context.Context, redis *repository.RedisClient, db *repository.PostgresDB) {
	slog.Info("启动结果流 ACK 监听器...")

	pendingCount := getEnvInt("ACK_PENDING_COUNT", 20)
	pendingBlock := time.Duration(getEnvInt("ACK_PENDING_BLOCK_MS", 1000)) * time.Millisecond
	newCount := getEnvInt("ACK_NEW_COUNT", 10)
	newBlock := time.Duration(getEnvInt("ACK_NEW_BLOCK_MS", 5000)) * time.Millisecond

	// 创建消费组（幂等）
	if err := redis.XGroupCreateMkStream(ctx, common.ResultStream, common.ResultStreamGroup, "0"); err != nil {
		slog.Error("创建流消费组失败", "error", err)
	}
	consumer := os.Getenv("SCHEDULER_ID")
	if consumer == "" {
		consumer = common.ResultStreamConsumer
	}

	// 尝试处理遗留的 Pending 消息
	if streams, err := redis.XReadGroup(ctx, &redisv8.XReadGroupArgs{
		Group:    common.ResultStreamGroup,
		Consumer: consumer,
		Streams:  []string{common.ResultStream, "0"},
		Count:    int64(pendingCount),
		Block:    pendingBlock,
	}); err == nil {
		for _, s := range streams {
			for _, msg := range s.Messages {
				jobID, _ := msg.Values["job_id"].(string)
				resultJSON, _ := msg.Values["result"].(string)
				if jobID == "" {
					_ = redis.XAck(ctx, common.ResultStream, common.ResultStreamGroup, msg.ID)
					continue
				}
				if handleTaskCompletion(ctx, jobID, resultJSON, redis, db) {
					_ = redis.XAck(ctx, common.ResultStream, common.ResultStreamGroup, msg.ID)
				} else {
					slog.Warn("结果处理失败，保留在待确认队列", "job_id", jobID, "id", msg.ID)
				}
			}
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		streams, err := redis.XReadGroup(ctx, &redisv8.XReadGroupArgs{
			Group:    common.ResultStreamGroup,
			Consumer: consumer,
			Streams:  []string{common.ResultStream, ">"},
			Count:    int64(newCount),
			Block:    newBlock,
		})
		if err != nil {
			slog.Error("XReadGroup 失败", "error", err)
			continue
		}
		if len(streams) == 0 {
			continue
		}

		for _, s := range streams {
			for _, msg := range s.Messages {
				jobID, _ := msg.Values["job_id"].(string)
				resultJSON, _ := msg.Values["result"].(string)
				if jobID == "" {
					slog.Warn("流消息缺少 job_id", "id", msg.ID)
					_ = redis.XAck(ctx, common.ResultStream, common.ResultStreamGroup, msg.ID)
					continue
				}
				slog.Debug("收到任务结果", "job_id", jobID)

				if handleTaskCompletion(ctx, jobID, resultJSON, redis, db) {
					if err := redis.XAck(ctx, common.ResultStream, common.ResultStreamGroup, msg.ID); err != nil {
						slog.Error("XAck 失败", "job_id", jobID, "error", err)
					}
				} else {
					slog.Warn("结果处理失败，保留在待确认队列", "job_id", jobID, "id", msg.ID)
				}
			}
		}
	}
}

func handleTaskCompletion(ctx context.Context, jobID, resultJSON string, redis *repository.RedisClient, db *repository.PostgresDB) bool {
	payloadKey := common.TaskPayloadPrefix + jobID
	rawItem, err := redis.Get(ctx, payloadKey)
	if err != nil {
		rawItem = ""
	}

	// 1. 处理结果 (同步到 DB + 更新指标)
	if !processResult(ctx, jobID, resultJSON, redis, db) {
		return false
	}

	// 2. 从队列移除
	if rawItem != "" {
		if err := redis.LRem(ctx, common.QueueProcessing, 1, rawItem); err != nil {
			slog.Error("从 processing 队列移除任务失败", "job_id", jobID, "error", err)
		} else {
			slog.Debug("已从 processing 队列移除任务", "job_id", jobID)
		}
	}

	// 3. 清理辅助键并回收 inflight
	assignmentKey := common.TaskAssignmentPrefix + jobID
	workerID, _ := redis.Get(ctx, assignmentKey)
	if workerID != "" {
		_, _ = redis.Decr(ctx, common.WorkerInflightPrefix+workerID)
	}
	_ = redis.Del(ctx,
		payloadKey,
		assignmentKey,
		common.TaskProcessingStartPrefix+jobID,
	)
	_ = redis.ZRem(ctx, common.TaskProcessingZSet, jobID)
	return true
}

func processResult(ctx context.Context, jobID string, resultJSON string, redis *repository.RedisClient, db *repository.PostgresDB) bool {
	// 1. 从 Redis 读取结果（当流中 result 为空时回退）。
	if resultJSON == "" {
		resultKey := common.ResultKeyPrefix + jobID
		val, err := redis.Get(ctx, resultKey)
		if err != nil || val == "" {
			slog.Error("在 Redis 中未找到结果", "job_id", jobID, "error", err)
			return false
		}
		resultJSON = val
	}

	// 2. 解析结果
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
		slog.Error("解析结果 JSON 失败", "job_id", jobID, "error", err)
		return false
	}

	status, _ := result["status"].(string)     // 例如 "Accepted"、"Wrong Answer"
	language, _ := result["language"].(string) // 从工作节点结果读取语言
	if language == "" {
		language = "未知"
	}

	traceID, _ := result["trace_id"].(string)
	cacheKey, _ := result["cache_key"].(string)

	// 更新业务指标
	submissionResultTotal.WithLabelValues(status, language).Inc()

	// 3. 更新 PostgreSQL (幂等: 仅在未完成时更新)
	latency, updated, err := db.UpdateSubmissionResultIfNotDone(ctx, jobID, status, result)
	if err != nil {
		slog.Error("更新数据库提交结果失败", "job_id", jobID, "error", err)
		return false
	}
	if !updated {
		slog.Warn("重复结果已忽略（已完成）", "job_id", jobID)
		return true
	}

	// 记录耗时指标
	schedulerJobLatency.Observe(latency)

	slog.Info("任务完成并已同步到数据库",
		"job_id", jobID,
		"status", status,
		"lang", language,
		"latency_s", latency,
		"trace_id", traceID,
	)

	if cacheKey != "" {
		_ = redis.Del(ctx, common.InFlightKeyPrefix+cacheKey)
	}
	return true
}
