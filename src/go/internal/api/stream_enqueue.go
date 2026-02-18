package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/d1guo/deep_oj/pkg/common"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
)

const (
	defaultJobStreamKey      = "deepoj:jobs"
	defaultJobStreamMaxLen   = int64(200000)
	defaultJobPayloadTTLSec  = 24 * 60 * 60
	jobStreamSchemaVersionV1 = 1
)

type streamEnqueueClient interface {
	Set(ctx context.Context, key, value string, expiration time.Duration) error
	XAdd(ctx context.Context, args *redis.XAddArgs) (string, error)
	Del(ctx context.Context, keys ...string) error
}

type payloadEnvelopeV1 struct {
	SchemaVersion int    `json:"schema_version"`
	TaskDataB64   string `json:"task_data_b64"`
	Encoding      string `json:"encoding"`
	ContentType   string `json:"content_type"`
}

func enqueueJobToStream(
	ctx context.Context,
	client streamEnqueueClient,
	logger *slog.Logger,
	jobID string,
	traceID string,
	taskData []byte,
) (string, error) {
	start := time.Now()
	observe := func(status string) {
		apiStreamEnqueueTotal.WithLabelValues(status).Inc()
		apiStreamEnqueueLatencyMs.Observe(float64(time.Since(start).Milliseconds()))
	}

	streamKey := getEnvString("JOB_STREAM_KEY", defaultJobStreamKey)
	streamMaxLen := getEnvInt64("JOB_STREAM_MAXLEN", defaultJobStreamMaxLen)
	payloadTTLSec := getEnvInt("JOB_PAYLOAD_TTL_SEC", defaultJobPayloadTTLSec)
	if payloadTTLSec <= 0 {
		payloadTTLSec = defaultJobPayloadTTLSec
	}
	payloadTTL := time.Duration(payloadTTLSec) * time.Second
	enqueueTS := time.Now().UnixMilli()
	payloadRef := common.TaskPayloadPrefix + jobID

	payloadBytes, err := json.Marshal(payloadEnvelopeV1{
		SchemaVersion: jobStreamSchemaVersionV1,
		TaskDataB64:   base64.StdEncoding.EncodeToString(taskData),
		Encoding:      "base64",
		ContentType:   "application/json",
	})
	if err != nil {
		observe("error")
		return "", fmt.Errorf("marshal payload envelope: %w", err)
	}

	if err := client.Set(ctx, payloadRef, string(payloadBytes), payloadTTL); err != nil {
		logger.Error(
			"Redis payload set failed",
			"component", "api_stream_enqueue",
			"event", "enqueue",
			"job_id", jobID,
			"attempt_id", int64(0),
			"trace_id", traceID,
			"reason", "payload_set_failed",
			"payload_ref", payloadRef,
			"error", err,
		)
		observe("error")
		return "", fmt.Errorf("set payload: %w", err)
	}

	xaddArgs := buildStreamXAddArgs(streamKey, streamMaxLen, map[string]interface{}{
		"job_id":      jobID,
		"enqueue_ts":  enqueueTS,
		"payload_ref": payloadRef,
		"priority":    0,
	})

	entryID, err := client.XAdd(ctx, xaddArgs)
	if err != nil {
		if delErr := client.Del(ctx, payloadRef); delErr != nil {
			logger.Warn(
				"Redis payload cleanup failed after XADD error",
				"component", "api_stream_enqueue",
				"event", "enqueue",
				"job_id", jobID,
				"attempt_id", int64(0),
				"trace_id", traceID,
				"reason", "payload_cleanup_failed",
				"payload_ref", payloadRef,
				"error", delErr,
			)
		}
		logger.Error(
			"Redis stream enqueue failed",
			"component", "api_stream_enqueue",
			"event", "enqueue",
			"job_id", jobID,
			"attempt_id", int64(0),
			"trace_id", traceID,
			"reason", "xadd_failed",
			"stream", streamKey,
			"payload_ref", payloadRef,
			"error", err,
		)
		observe("error")
		return "", fmt.Errorf("stream xadd: %w", err)
	}

	logger.Info(
		"Stream enqueue success",
		"component", "api_stream_enqueue",
		"event", "enqueue",
		"job_id", jobID,
		"attempt_id", int64(0),
		"trace_id", traceID,
		"stream", streamKey,
		"stream_entry_id", entryID,
	)
	observe("ok")
	return entryID, nil
}

func buildPayloadEnvelope(taskData []byte) (string, error) {
	payloadBytes, err := json.Marshal(payloadEnvelopeV1{
		SchemaVersion: jobStreamSchemaVersionV1,
		TaskDataB64:   base64.StdEncoding.EncodeToString(taskData),
		Encoding:      "base64",
		ContentType:   "application/json",
	})
	if err != nil {
		return "", fmt.Errorf("marshal payload envelope: %w", err)
	}
	return string(payloadBytes), nil
}

func buildStreamXAddArgs(streamKey string, streamMaxLen int64, values map[string]interface{}) *redis.XAddArgs {
	args := &redis.XAddArgs{
		Stream: streamKey,
		Values: values,
	}
	if streamMaxLen > 0 {
		// Approximate trim keeps enqueue path O(1) on average while bounding stream memory.
		args.MaxLen = streamMaxLen
		args.Approx = true
	}
	return args
}

func enqueueJobToStreamOrReply5xx(
	c *gin.Context,
	client streamEnqueueClient,
	logger *slog.Logger,
	jobID string,
	traceID string,
	inflightKey string,
	taskData []byte,
) (string, bool) {
	streamEntryID, err := enqueueJobToStream(c.Request.Context(), client, logger, jobID, traceID, taskData)
	if err != nil {
		_ = client.Del(c.Request.Context(), inflightKey)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "队列错误",
			"code":  "QUEUE_ERROR",
		})
		return "", false
	}
	return streamEntryID, true
}
