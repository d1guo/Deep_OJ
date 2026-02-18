package worker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/d1guo/deep_oj/internal/repository"
	"github.com/d1guo/deep_oj/pkg/common"
	pb "github.com/d1guo/deep_oj/pkg/proto"
	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

const (
	streamConsumeStatusOK    = "ok"
	streamConsumeStatusError = "error"

	streamClaimStatusOK     = "ok"
	streamClaimStatusReject = "reject"
	streamClaimStatusError  = "error"

	streamHeartbeatStatusOK    = "ok"
	streamHeartbeatStatusLost  = "lost"
	streamHeartbeatStatusError = "error"

	streamReasonOK                 = "ok"
	streamReasonMissingField       = "missing_field"
	streamReasonPayloadInvalid     = "payload_missing_or_invalid"
	streamReasonDBClaimReject      = "db_claim_reject"
	streamReasonDBError            = "db_error"
	streamReasonXAckError          = "xack_error"
	streamReasonXReadGroupError    = "xreadgroup_error"
	streamReasonGroupCreateError   = "group_create_error"
	streamReasonLeaseLost          = "lease_lost"
	streamReasonDBErrorHeartbeat   = "db_error_heartbeat"
	streamReasonStaleAttempt       = "stale_attempt"
	streamReasonAttemptMismatch    = "attempt_mismatch"
	streamReasonPayloadDecodeError = "payload_decode_error"
	streamReasonLeaseStillValid    = "lease_still_valid"
	streamReasonNotFound           = "not_found"
	streamReasonXAutoClaimError    = "xautoclaim_error"
	streamReasonReclaimClaimed     = "reclaim_claimed"
	streamReasonDoneOrStale        = "done_or_stale"
	streamReasonAlreadyFinished    = "already_finished"
	streamReasonNotExpectedState   = "not_in_expected_state"
	streamReasonLeaseOwnerMismatch = "lease_lost_or_owner_mismatch"

	streamEnvelopeSchemaVersionV1    = 1
	streamEnvelopeEncodingBase64     = "base64"
	streamEnvelopeContentTypeJSON    = "application/json"
	streamEnvelopeContentTypeProto   = "application/x-protobuf"
	streamEnvelopeContentTypeProtoV2 = "application/protobuf"

	maxHeartbeatErrorRetries = 3

	reclaimStatusOK     = "ok"
	reclaimStatusReject = "reject"
	reclaimStatusError  = "error"

	reclaimDecisionClaimed         = "claimed"
	reclaimDecisionDoneOrStale     = "done_or_stale"
	reclaimDecisionLeaseStillValid = "lease_still_valid"
	reclaimDecisionNotFound        = "not_found"

	finalizeStatusOK       = "ok"
	finalizeStatusRejected = "rejected"
	finalizeStatusError    = "error"

	defaultWorkerLogThrottleSec = 5
)

type streamEnvelopeV1 struct {
	SchemaVersion int    `json:"schema_version"`
	TaskDataB64   string `json:"task_data_b64"`
	Encoding      string `json:"encoding,omitempty"`
	ContentType   string `json:"content_type,omitempty"`
}

type streamProcessResult struct {
	status string
	reason string
}

type heartbeatResult struct {
	leaseLost bool
	reason    string
}

type streamRedisClient interface {
	EnsureGroup(ctx context.Context, stream, group string) error
	ReadNew(ctx context.Context, stream, group, consumer string, count int64, block time.Duration) ([]redis.XStream, error)
	AutoClaim(ctx context.Context, stream, group, consumer string, minIdle time.Duration, start string, count int64) ([]redis.XMessage, string, error)
	Ack(ctx context.Context, stream, group string, ids ...string) (int64, error)
	Get(ctx context.Context, key string) (string, error)
}

type streamDB interface {
	ClaimSubmissionForRun(ctx context.Context, jobID, leaseOwner string, leaseSec int) (int64, bool, error)
	ReclaimSubmissionForRun(ctx context.Context, jobID, leaseOwner string, leaseSec int) (int64, string, error)
	RefreshSubmissionLease(ctx context.Context, jobID string, attemptID int64, leaseOwner string, leaseSec int) (bool, error)
	FinalizeSubmissionWithFence(ctx context.Context, jobID string, attemptID int64, leaseOwner string, status string, result any) (repository.FinalizeFenceResult, error)
	MarkSubmissionPoison(ctx context.Context, jobID, errorCode, errorMessage string) (bool, error)
}

type streamJudgeRunner interface {
	ExecuteTask(ctx context.Context, req *pb.TaskRequest) (*pb.TaskResponse, error)
}

type redisStreamClient struct {
	client *redis.Client
}

func newRedisStreamClient(client *redis.Client) *redisStreamClient {
	return &redisStreamClient{client: client}
}

func (c *redisStreamClient) EnsureGroup(ctx context.Context, stream, group string) error {
	err := c.client.XGroupCreateMkStream(ctx, stream, group, "$").Err()
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "BUSYGROUP") {
		return nil
	}
	return err
}

func (c *redisStreamClient) ReadNew(ctx context.Context, stream, group, consumer string, count int64, block time.Duration) ([]redis.XStream, error) {
	return c.client.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    group,
		Consumer: consumer,
		Streams:  []string{stream, ">"},
		Count:    count,
		Block:    block,
	}).Result()
}

func (c *redisStreamClient) AutoClaim(ctx context.Context, stream, group, consumer string, minIdle time.Duration, start string, count int64) ([]redis.XMessage, string, error) {
	return c.client.XAutoClaim(ctx, &redis.XAutoClaimArgs{
		Stream:   stream,
		Group:    group,
		Consumer: consumer,
		MinIdle:  minIdle,
		Start:    start,
		Count:    count,
	}).Result()
}

func (c *redisStreamClient) Ack(ctx context.Context, stream, group string, ids ...string) (int64, error) {
	return c.client.XAck(ctx, stream, group, ids...).Result()
}

func (c *redisStreamClient) Get(ctx context.Context, key string) (string, error) {
	return c.client.Get(ctx, key).Result()
}

type StreamConsumer struct {
	config             *Config
	redis              streamRedisClient
	db                 streamDB
	runner             streamJudgeRunner
	logger             *slog.Logger
	reclaimCursorMu    sync.Mutex
	reclaimNextStartID string
	logThrottleMu      sync.Mutex
	logThrottleSeen    map[string]time.Time
	logThrottleWindow  time.Duration
}

func NewStreamConsumer(cfg *Config, redisClient *redis.Client, db streamDB, runner streamJudgeRunner) *StreamConsumer {
	logThrottleSec := getEnvInt("WORKER_LOG_THROTTLE_SEC", defaultWorkerLogThrottleSec)
	if logThrottleSec <= 0 {
		logThrottleSec = defaultWorkerLogThrottleSec
	}
	return &StreamConsumer{
		config:             cfg,
		redis:              newRedisStreamClient(redisClient),
		db:                 db,
		runner:             runner,
		logger:             slog.With("component", "worker_stream_consumer"),
		reclaimNextStartID: "0-0",
		logThrottleSeen:    make(map[string]time.Time),
		logThrottleWindow:  time.Duration(logThrottleSec) * time.Second,
	}
}

func newStreamConsumerForTest(cfg *Config, redisClient streamRedisClient, db streamDB, runner streamJudgeRunner, logger *slog.Logger) *StreamConsumer {
	if logger == nil {
		logger = slog.Default()
	}
	return &StreamConsumer{
		config:             cfg,
		redis:              redisClient,
		db:                 db,
		runner:             runner,
		logger:             logger,
		reclaimNextStartID: "0-0",
		logThrottleSeen:    make(map[string]time.Time),
		logThrottleWindow:  defaultWorkerLogThrottleSec * time.Second,
	}
}

func (c *StreamConsumer) Run(ctx context.Context) error {
	if c.runner == nil {
		return errors.New("stream consumer runner is nil")
	}

	if err := c.redis.EnsureGroup(ctx, c.config.JobStreamKey, c.config.JobStreamGroup); err != nil {
		workerStreamConsumeTotal.WithLabelValues(streamConsumeStatusError, streamReasonGroupCreateError).Inc()
		c.logger.Error(
			"XGROUP CREATE failed",
			"trace_id", uuid.NewString(),
			"job_id", "",
			"attempt_id", int64(0),
			"stream_entry_id", "",
			"consumer", c.config.JobStreamConsumer,
			"group", c.config.JobStreamGroup,
			"reason", streamReasonGroupCreateError,
			"error", err,
		)
		return fmt.Errorf("ensure stream group: %w", err)
	}
	c.logger.Info(
		"Worker stream consumer started",
		"trace_id", uuid.NewString(),
		"job_id", "",
		"attempt_id", int64(0),
		"stream_entry_id", "",
		"consumer", c.config.JobStreamConsumer,
		"group", c.config.JobStreamGroup,
		"reason", "started",
		"stream_key", c.config.JobStreamKey,
	)

	readCount := int64(c.config.JobStreamReadCount)
	if readCount <= 0 {
		readCount = 16
	}
	blockMs := c.config.JobStreamBlockMs
	if blockMs <= 0 {
		blockMs = 2000
	}
	block := time.Duration(blockMs) * time.Millisecond
	go c.runReclaimLoop(ctx)

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		streams, err := c.redis.ReadNew(ctx, c.config.JobStreamKey, c.config.JobStreamGroup, c.config.JobStreamConsumer, readCount, block)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return ctx.Err()
			}
			if errors.Is(err, redis.Nil) {
				continue
			}
			workerStreamConsumeTotal.WithLabelValues(streamConsumeStatusError, streamReasonXReadGroupError).Inc()
			if c.shouldLog(streamReasonXReadGroupError) {
				c.logger.Error(
					"XREADGROUP failed",
					"event", "claim",
					"trace_id", uuid.NewString(),
					"job_id", "",
					"attempt_id", int64(0),
					"stream_entry_id", "",
					"consumer", c.config.JobStreamConsumer,
					"group", c.config.JobStreamGroup,
					"reason", streamReasonXReadGroupError,
					"error", err,
				)
			}
			continue
		}
		for _, xstream := range streams {
			for _, msg := range xstream.Messages {
				c.handleMessage(ctx, msg)
			}
		}
	}
}

func (c *StreamConsumer) runReclaimLoop(ctx context.Context) {
	intervalSec := c.config.JobReclaimIntervalSec
	if intervalSec <= 0 {
		intervalSec = 5
	}
	ticker := time.NewTicker(time.Duration(intervalSec) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.reclaimOnce(ctx)
		}
	}
}

func (c *StreamConsumer) reclaimOnce(ctx context.Context) {
	start := time.Now()
	workerReclaimInflight.Inc()
	defer func() {
		workerReclaimInflight.Dec()
		workerReclaimLatencyMs.Observe(float64(time.Since(start).Milliseconds()))
	}()

	count := int64(c.config.JobReclaimCount)
	if count <= 0 {
		count = 16
	}
	leaseSec := c.config.JobLeaseSec
	if leaseSec <= 0 {
		leaseSec = 60
	}
	graceSec := c.config.JobReclaimGraceSec
	if graceSec < 0 {
		graceSec = 0
	}
	minIdle := time.Duration(leaseSec+graceSec) * time.Second
	startID := c.getReclaimNextStartID()
	if strings.TrimSpace(startID) == "" {
		startID = "0-0"
	}
	for {
		msgs, nextID, err := c.redis.AutoClaim(
			ctx,
			c.config.JobStreamKey,
			c.config.JobStreamGroup,
			c.config.JobStreamConsumer,
			minIdle,
			startID,
			count,
		)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return
			}
			if errors.Is(err, redis.Nil) {
				return
			}
			workerReclaimTotal.WithLabelValues(reclaimStatusError, streamReasonXAutoClaimError).Inc()
			reclaimTotal.WithLabelValues(streamReasonXAutoClaimError, "xautoclaim").Inc()
			xautoclaimErrorsTotal.WithLabelValues(streamReasonXAutoClaimError).Inc()
			if c.shouldLog(streamReasonXAutoClaimError) {
				c.logger.Error(
					"XAUTOCLAIM failed",
					"event", "reclaim_skipped",
					"trace_id", uuid.NewString(),
					"job_id", "",
					"attempt_id", int64(0),
					"stream_entry_id", "",
					"group", c.config.JobStreamGroup,
					"consumer", c.config.JobStreamConsumer,
					"reason", streamReasonXAutoClaimError,
					"error", err,
				)
			}
			return
		}
		if len(msgs) == 0 {
			if nextID != "" {
				c.setReclaimNextStartID(nextID)
			}
			return
		}
		for _, msg := range msgs {
			c.handleReclaimedMessage(ctx, msg)
		}
		if nextID != "" {
			c.setReclaimNextStartID(nextID)
		}
		if nextID == "" || nextID == "0-0" || nextID == startID {
			return
		}
		startID = nextID
		if int64(len(msgs)) < count {
			return
		}
	}
}

func (c *StreamConsumer) getReclaimNextStartID() string {
	c.reclaimCursorMu.Lock()
	defer c.reclaimCursorMu.Unlock()
	return c.reclaimNextStartID
}

func (c *StreamConsumer) setReclaimNextStartID(next string) {
	c.reclaimCursorMu.Lock()
	c.reclaimNextStartID = next
	c.reclaimCursorMu.Unlock()
}

func (c *StreamConsumer) shouldLog(event string) bool {
	if c == nil {
		return true
	}
	key := strings.TrimSpace(event)
	if key == "" {
		return true
	}
	window := c.logThrottleWindow
	if window <= 0 {
		window = defaultWorkerLogThrottleSec * time.Second
	}
	now := time.Now()
	c.logThrottleMu.Lock()
	defer c.logThrottleMu.Unlock()
	last, ok := c.logThrottleSeen[key]
	if ok && now.Sub(last) < window {
		return false
	}
	c.logThrottleSeen[key] = now
	return true
}

func (c *StreamConsumer) handleMessage(ctx context.Context, msg redis.XMessage) streamProcessResult {
	start := time.Now()
	workerStreamInflight.Inc()
	status := streamConsumeStatusOK
	reason := streamReasonOK
	defer func() {
		workerStreamInflight.Dec()
		workerStreamConsumeLatencyMs.Observe(float64(time.Since(start).Milliseconds()))
		workerStreamConsumeTotal.WithLabelValues(status, reason).Inc()
	}()

	jobID, missingJobID := requiredStreamField(msg.Values, "job_id")
	_, missingEnqueueTS := requiredStreamField(msg.Values, "enqueue_ts")
	payloadRef, missingPayloadRef := requiredStreamField(msg.Values, "payload_ref")
	_, missingPriority := requiredStreamField(msg.Values, "priority")

	missing := make([]string, 0, 4)
	if missingJobID {
		missing = append(missing, "job_id")
	}
	if missingEnqueueTS {
		missing = append(missing, "enqueue_ts")
	}
	if missingPayloadRef {
		missing = append(missing, "payload_ref")
	}
	if missingPriority {
		missing = append(missing, "priority")
	}

	traceID := uuid.NewString()
	logger := c.newMessageLogger(traceID, jobID, 0, msg.ID)

	if len(missing) > 0 {
		status = streamConsumeStatusError
		reason = streamReasonMissingField
		if err := c.markPoisonBeforeAck(ctx, logger, jobID, reason, "missing stream fields: "+strings.Join(missing, ",")); err != nil {
			status = streamConsumeStatusError
			reason = streamReasonDBError
			logger.Error("Failed to persist poison state", "reason", reason, "error", err)
			return streamProcessResult{status: status, reason: reason}
		}
		if err := c.xackWithRetry(ctx, msg.ID); err != nil {
			status = streamConsumeStatusError
			reason = streamReasonXAckError
			logger.Error("XACK failed", "reason", reason, "error", err)
			return streamProcessResult{status: status, reason: reason}
		}
		logger.Warn("Rejected poison stream message", "reason", streamReasonMissingField)
		return streamProcessResult{status: status, reason: reason}
	}

	payloadRaw, err := c.redis.Get(ctx, payloadRef)
	if err != nil || strings.TrimSpace(payloadRaw) == "" {
		status = streamConsumeStatusError
		reason = streamReasonPayloadInvalid
		if perr := c.markPoisonBeforeAck(ctx, logger, jobID, reason, "payload missing: "+payloadRef); perr != nil {
			status = streamConsumeStatusError
			reason = streamReasonDBError
			logger.Error("Failed to persist poison state", "reason", reason, "error", perr)
			return streamProcessResult{status: status, reason: reason}
		}
		if ackErr := c.xackWithRetry(ctx, msg.ID); ackErr != nil {
			status = streamConsumeStatusError
			reason = streamReasonXAckError
			logger.Error("XACK failed", "reason", reason, "error", ackErr)
			return streamProcessResult{status: status, reason: reason}
		}
		logger.Warn("Rejected stream message: payload missing", "reason", streamReasonPayloadInvalid, "payload_ref", payloadRef, "error", err)
		return streamProcessResult{status: status, reason: reason}
	}

	taskReq, derr := decodeStreamTaskPayload(payloadRaw)
	if derr != nil {
		status = streamConsumeStatusError
		reason = streamReasonPayloadInvalid
		if perr := c.markPoisonBeforeAck(ctx, logger, jobID, streamReasonPayloadDecodeError, derr.Error()); perr != nil {
			status = streamConsumeStatusError
			reason = streamReasonDBError
			logger.Error("Failed to persist poison state", "reason", reason, "error", perr)
			return streamProcessResult{status: status, reason: reason}
		}
		if ackErr := c.xackWithRetry(ctx, msg.ID); ackErr != nil {
			status = streamConsumeStatusError
			reason = streamReasonXAckError
			logger.Error("XACK failed", "reason", reason, "error", ackErr)
			return streamProcessResult{status: status, reason: reason}
		}
		logger.Warn("Rejected stream message: payload invalid", "reason", streamReasonPayloadInvalid, "payload_ref", payloadRef, "error", derr)
		return streamProcessResult{status: status, reason: reason}
	}

	if strings.TrimSpace(taskReq.GetJobId()) == "" {
		taskReq.JobId = jobID
	}
	if taskReq.GetJobId() != jobID {
		status = streamConsumeStatusError
		reason = streamReasonPayloadInvalid
		if perr := c.markPoisonBeforeAck(ctx, logger, jobID, reason, "payload job_id mismatch"); perr != nil {
			status = streamConsumeStatusError
			reason = streamReasonDBError
			logger.Error("Failed to persist poison state", "reason", reason, "error", perr)
			return streamProcessResult{status: status, reason: reason}
		}
		if ackErr := c.xackWithRetry(ctx, msg.ID); ackErr != nil {
			status = streamConsumeStatusError
			reason = streamReasonXAckError
			logger.Error("XACK failed", "reason", reason, "error", ackErr)
			return streamProcessResult{status: status, reason: reason}
		}
		logger.Warn(
			"Rejected stream message: payload job_id mismatch",
			"reason", streamReasonPayloadInvalid,
			"payload_job_id", taskReq.GetJobId(),
		)
		return streamProcessResult{status: status, reason: reason}
	}

	if strings.TrimSpace(taskReq.TraceId) != "" {
		traceID = strings.TrimSpace(taskReq.TraceId)
		logger = c.newMessageLogger(traceID, jobID, 0, msg.ID)
	} else {
		taskReq.TraceId = traceID
	}

	attemptID, claimed, err := c.db.ClaimSubmissionForRun(ctx, jobID, c.config.JobStreamConsumer, c.config.JobLeaseSec)
	if err != nil {
		workerClaimTotal.WithLabelValues(streamClaimStatusError, streamReasonDBError).Inc()
		status = streamConsumeStatusError
		reason = streamReasonDBError
		logger.Error("DB claim failed", "reason", reason, "error", err)
		return streamProcessResult{status: status, reason: reason}
	}
	if !claimed {
		workerClaimTotal.WithLabelValues(streamClaimStatusReject, streamReasonDBClaimReject).Inc()
		status = streamConsumeStatusError
		reason = streamReasonDBClaimReject
		if ackErr := c.xackWithRetry(ctx, msg.ID); ackErr != nil {
			reason = streamReasonXAckError
			logger.Error("XACK failed", "attempt_id", int64(0), "reason", reason, "error", ackErr)
			return streamProcessResult{status: status, reason: reason}
		}
		logger.Warn("DB claim rejected, XACK message", "attempt_id", int64(0), "reason", streamReasonDBClaimReject)
		return streamProcessResult{status: status, reason: reason}
	}
	workerClaimTotal.WithLabelValues(streamClaimStatusOK, streamReasonOK).Inc()
	logger = c.newMessageLogger(traceID, jobID, attemptID, msg.ID)
	logger.Info("DB claim success", "event", "claim", "reason", streamReasonOK)
	return c.runClaimedExecution(ctx, msg, taskReq, jobID, traceID, attemptID, logger)
}

func (c *StreamConsumer) handleReclaimedMessage(ctx context.Context, msg redis.XMessage) streamProcessResult {
	status := reclaimStatusOK
	reason := streamReasonOK
	source := "stream_reclaim"
	defer func() {
		workerReclaimTotal.WithLabelValues(status, reason).Inc()
		reclaimTotal.WithLabelValues(reason, source).Inc()
	}()

	jobID, missingJobID := requiredStreamField(msg.Values, "job_id")
	_, missingEnqueueTS := requiredStreamField(msg.Values, "enqueue_ts")
	payloadRef, missingPayloadRef := requiredStreamField(msg.Values, "payload_ref")
	_, missingPriority := requiredStreamField(msg.Values, "priority")

	missing := make([]string, 0, 4)
	if missingJobID {
		missing = append(missing, "job_id")
	}
	if missingEnqueueTS {
		missing = append(missing, "enqueue_ts")
	}
	if missingPayloadRef {
		missing = append(missing, "payload_ref")
	}
	if missingPriority {
		missing = append(missing, "priority")
	}

	traceID := uuid.NewString()
	logger := c.newMessageLogger(traceID, jobID, 0, msg.ID)
	if len(missing) > 0 {
		status = reclaimStatusReject
		reason = streamReasonMissingField
		if err := c.markPoisonBeforeAck(ctx, logger, jobID, reason, "missing stream fields: "+strings.Join(missing, ",")); err != nil {
			status = reclaimStatusError
			reason = streamReasonDBError
			logger.Error("Failed to persist poison state for reclaimed message", "reason", reason, "error", err)
			return streamProcessResult{status: streamConsumeStatusError, reason: reason}
		}
		if err := c.xackWithRetry(ctx, msg.ID); err != nil {
			status = reclaimStatusError
			reason = streamReasonXAckError
			logger.Error("XACK failed for reclaimed message", "reason", reason, "error", err)
			return streamProcessResult{status: streamConsumeStatusError, reason: reason}
		}
		return streamProcessResult{status: streamConsumeStatusError, reason: streamReasonMissingField}
	}

	payloadRaw, err := c.redis.Get(ctx, payloadRef)
	if err != nil || strings.TrimSpace(payloadRaw) == "" {
		status = reclaimStatusReject
		reason = streamReasonPayloadInvalid
		if perr := c.markPoisonBeforeAck(ctx, logger, jobID, reason, "payload missing: "+payloadRef); perr != nil {
			status = reclaimStatusError
			reason = streamReasonDBError
			logger.Error("Failed to persist poison state for reclaimed message", "reason", reason, "error", perr)
			return streamProcessResult{status: streamConsumeStatusError, reason: reason}
		}
		if ackErr := c.xackWithRetry(ctx, msg.ID); ackErr != nil {
			status = reclaimStatusError
			reason = streamReasonXAckError
			logger.Error("XACK failed for reclaimed message", "reason", reason, "error", ackErr)
			return streamProcessResult{status: streamConsumeStatusError, reason: reason}
		}
		return streamProcessResult{status: streamConsumeStatusError, reason: streamReasonPayloadInvalid}
	}

	taskReq, derr := decodeStreamTaskPayload(payloadRaw)
	if derr != nil {
		status = reclaimStatusReject
		reason = streamReasonPayloadInvalid
		if perr := c.markPoisonBeforeAck(ctx, logger, jobID, streamReasonPayloadDecodeError, derr.Error()); perr != nil {
			status = reclaimStatusError
			reason = streamReasonDBError
			logger.Error("Failed to persist poison state for reclaimed message", "reason", reason, "error", perr)
			return streamProcessResult{status: streamConsumeStatusError, reason: reason}
		}
		if ackErr := c.xackWithRetry(ctx, msg.ID); ackErr != nil {
			status = reclaimStatusError
			reason = streamReasonXAckError
			logger.Error("XACK failed for reclaimed message", "reason", reason, "error", ackErr)
			return streamProcessResult{status: streamConsumeStatusError, reason: reason}
		}
		return streamProcessResult{status: streamConsumeStatusError, reason: streamReasonPayloadInvalid}
	}

	if strings.TrimSpace(taskReq.GetJobId()) == "" {
		taskReq.JobId = jobID
	}
	if taskReq.GetJobId() != jobID {
		status = reclaimStatusReject
		reason = streamReasonPayloadInvalid
		if perr := c.markPoisonBeforeAck(ctx, logger, jobID, reason, "payload job_id mismatch"); perr != nil {
			status = reclaimStatusError
			reason = streamReasonDBError
			logger.Error("Failed to persist poison state for reclaimed message", "reason", reason, "error", perr)
			return streamProcessResult{status: streamConsumeStatusError, reason: reason}
		}
		if ackErr := c.xackWithRetry(ctx, msg.ID); ackErr != nil {
			status = reclaimStatusError
			reason = streamReasonXAckError
			logger.Error("XACK failed for reclaimed message", "reason", reason, "error", ackErr)
			return streamProcessResult{status: streamConsumeStatusError, reason: reason}
		}
		return streamProcessResult{status: streamConsumeStatusError, reason: streamReasonPayloadInvalid}
	}

	if strings.TrimSpace(taskReq.TraceId) != "" {
		traceID = strings.TrimSpace(taskReq.TraceId)
	} else {
		taskReq.TraceId = traceID
	}
	logger = c.newMessageLogger(traceID, jobID, 0, msg.ID)

	attemptID, decision, rerr := c.db.ReclaimSubmissionForRun(ctx, jobID, c.config.JobStreamConsumer, c.config.JobLeaseSec)
	if rerr != nil {
		status = reclaimStatusError
		reason = streamReasonDBError
		logger.Error("DB reclaim failed", "reason", reason, "error", rerr)
		return streamProcessResult{status: streamConsumeStatusError, reason: streamReasonDBError}
	}
	logger = c.newMessageLogger(traceID, jobID, attemptID, msg.ID)

	switch decision {
	case reclaimDecisionClaimed:
		status = reclaimStatusOK
		reason = streamReasonReclaimClaimed
		logger = c.newMessageLogger(traceID, jobID, attemptID, msg.ID)
		logger.Info("DB reclaim success", "event", "reclaim_claimed", "reason", streamReasonReclaimClaimed)
		res := c.runClaimedExecution(ctx, msg, taskReq, jobID, traceID, attemptID, logger)
		if res.reason != streamReasonOK {
			status = reclaimStatusError
			reason = res.reason
		} else {
			status = reclaimStatusOK
			reason = streamReasonReclaimClaimed
		}
		return res
	case reclaimDecisionLeaseStillValid:
		status = reclaimStatusReject
		reason = streamReasonLeaseStillValid
		logger.Warn("Reclaim skipped: lease still valid", "event", "reclaim_skipped", "reason", reason)
		logger.Info("Skip XACK on reclaim entry", "event", "xack_skip", "reason", reason)
		return streamProcessResult{status: streamConsumeStatusError, reason: reason}
	case reclaimDecisionDoneOrStale:
		status = reclaimStatusReject
		reason = streamReasonDoneOrStale
		if err := c.xackWithRetry(ctx, msg.ID); err != nil {
			status = reclaimStatusError
			reason = streamReasonXAckError
			logger.Error("XACK failed for done/stale reclaimed entry", "reason", reason, "error", err)
			return streamProcessResult{status: streamConsumeStatusError, reason: reason}
		}
		logger.Info("Reclaimed entry dropped as done/stale and ACKed", "event", "reclaim_skipped", "reason", streamReasonDoneOrStale)
		return streamProcessResult{status: streamConsumeStatusError, reason: streamReasonDoneOrStale}
	case reclaimDecisionNotFound:
		status = reclaimStatusReject
		reason = streamReasonNotFound
		if err := c.xackWithRetry(ctx, msg.ID); err != nil {
			status = reclaimStatusError
			reason = streamReasonXAckError
			logger.Error("XACK failed for not_found reclaimed entry", "reason", reason, "error", err)
			return streamProcessResult{status: streamConsumeStatusError, reason: reason}
		}
		logger.Info("Reclaimed entry dropped as not_found and ACKed", "event", "reclaim_skipped", "reason", streamReasonNotFound)
		return streamProcessResult{status: streamConsumeStatusError, reason: streamReasonNotFound}
	default:
		status = reclaimStatusError
		reason = streamReasonDBError
		logger.Error("Unknown DB reclaim decision", "reason", decision)
		return streamProcessResult{status: streamConsumeStatusError, reason: streamReasonDBError}
	}
}

func (c *StreamConsumer) runClaimedExecution(
	ctx context.Context,
	msg redis.XMessage,
	taskReq *pb.TaskRequest,
	jobID string,
	traceID string,
	attemptID int64,
	logger *slog.Logger,
) streamProcessResult {
	status := streamConsumeStatusOK
	reason := streamReasonOK

	runCtx, cancelRun := context.WithCancel(ctx)
	hbCh := make(chan heartbeatResult, 1)
	go func() {
		hbCh <- c.runLeaseHeartbeat(runCtx, cancelRun, jobID, attemptID, traceID, msg.ID)
	}()

	logger.Info("Worker execution started", "event", "start_exec")
	execCtx := withAttemptID(runCtx, attemptID)
	resp, execErr := c.runner.ExecuteTask(execCtx, taskReq)

	cancelRun()
	hbRes := <-hbCh
	if hbRes.leaseLost {
		status = streamConsumeStatusError
		reason = hbRes.reason
		logger.Warn("Lease lost, execution canceled", "event", "xack_skip", "reason", reason, "exec_error", execErr)
		return streamProcessResult{status: status, reason: reason}
	}

	finalStatus, finalResult := c.resolveFinalResult(ctx, taskReq, jobID, attemptID, traceID, resp, execErr)
	if mappedAttempt, ok := parseInt64FromAny(finalResult["attempt_id"]); ok && mappedAttempt != attemptID {
		logger.Warn(
			"Result attempt mismatch after execution, force overwrite to claimed attempt",
			"reason", streamReasonAttemptMismatch,
			"expected_attempt_id", attemptID,
			"actual_attempt_id", mappedAttempt,
		)
	}
	finalResult["attempt_id"] = attemptID

	finalizeStart := time.Now()
	finalizeResult, ferr := c.db.FinalizeSubmissionWithFence(ctx, jobID, attemptID, c.config.JobStreamConsumer, finalStatus, finalResult)
	workerFinalizeLatencyMs.Observe(float64(time.Since(finalizeStart).Milliseconds()))
	if ferr != nil {
		workerFinalizeTotal.WithLabelValues(finalizeStatusError).Inc()
		workerFinalizeErrorsTotal.Inc()
		status = streamConsumeStatusError
		reason = streamReasonDBError
		logger.Error("Fenced final write failed", "event", "db_finalize_error", "reason", reason, "error", ferr)
		return streamProcessResult{status: status, reason: reason}
	}

	if !finalizeResult.Applied {
		workerFinalizeTotal.WithLabelValues(finalizeStatusRejected).Inc()
		workerFinalizeRejectedTotal.WithLabelValues(finalizeResult.Reason).Inc()
		status = streamConsumeStatusError
		reason = mapFinalizeRejectReason(finalizeResult.Reason)
		if reason == streamReasonStaleAttempt {
			workerStaleAttemptTotal.Inc()
		}

		finalizeEvent := "db_finalize_rejected"
		if reason == streamReasonStaleAttempt {
			finalizeEvent = "db_finalize_stale"
		}
		logger.Warn(
			"Fenced final write rejected",
			"event", finalizeEvent,
			"reason", reason,
			"db_attempt_id", finalizeResult.DBAttemptID,
			"db_status", finalizeResult.DBStatus,
			"db_state", finalizeResult.DBState,
			"db_lease_owner", finalizeResult.DBLeaseOwner,
			"db_lease_until", finalizeResult.DBLeaseUntil,
		)

		if !isBusinessFinalizeRejectReason(reason) {
			workerFinalizeTotal.WithLabelValues(finalizeStatusError).Inc()
			workerFinalizeErrorsTotal.Inc()
			return streamProcessResult{status: status, reason: streamReasonDBError}
		}

		if ackErr := c.xackWithRetry(ctx, msg.ID); ackErr != nil {
			reason = streamReasonXAckError
			logger.Error("XACK failed after fenced reject", "reason", reason, "error", ackErr)
			return streamProcessResult{status: status, reason: reason}
		}
		logger.Info("XACK completed", "event", "xack_ok", "reason", "fenced_reject")
		return streamProcessResult{status: status, reason: reason}
	}
	workerFinalizeTotal.WithLabelValues(finalizeStatusOK).Inc()
	logger.Info("Fenced final write applied", "event", "db_finalize_ok", "status", finalStatus)

	if err := c.xackWithRetry(ctx, msg.ID); err != nil {
		status = streamConsumeStatusError
		reason = streamReasonXAckError
		logger.Error("XACK failed after fenced write", "reason", reason, "error", err)
		return streamProcessResult{status: status, reason: reason}
	}
	logger.Info("XACK completed", "event", "xack_ok", "reason", streamReasonOK)

	logger.Info("Stream message processed", "event", "finish_exec", "reason", streamReasonOK, "status", finalStatus)
	return streamProcessResult{status: streamConsumeStatusOK, reason: streamReasonOK}
}

func mapFinalizeRejectReason(reason string) string {
	switch strings.TrimSpace(reason) {
	case repository.FinalizeReasonStaleAttempt:
		return streamReasonStaleAttempt
	case repository.FinalizeReasonAlreadyFinished:
		return streamReasonAlreadyFinished
	case repository.FinalizeReasonNotInExpectedState:
		return streamReasonNotExpectedState
	case repository.FinalizeReasonLeaseLostOwnerMismatch:
		return streamReasonLeaseOwnerMismatch
	default:
		return streamReasonDBError
	}
}

func isBusinessFinalizeRejectReason(reason string) bool {
	switch reason {
	case streamReasonStaleAttempt, streamReasonAlreadyFinished, streamReasonNotExpectedState, streamReasonLeaseOwnerMismatch:
		return true
	default:
		return false
	}
}

func (c *StreamConsumer) runLeaseHeartbeat(
	ctx context.Context,
	cancelRun context.CancelFunc,
	jobID string,
	attemptID int64,
	traceID string,
	streamEntryID string,
) heartbeatResult {
	heartbeatSec := c.config.JobHeartbeatSec
	if heartbeatSec <= 0 {
		heartbeatSec = 10
	}
	ticker := time.NewTicker(time.Duration(heartbeatSec) * time.Second)
	defer ticker.Stop()

	logger := c.newMessageLogger(traceID, jobID, attemptID, streamEntryID)
	consecutiveErrors := 0
	for {
		select {
		case <-ctx.Done():
			return heartbeatResult{}
		case <-ticker.C:
			ok, err := c.db.RefreshSubmissionLease(ctx, jobID, attemptID, c.config.JobStreamConsumer, c.config.JobLeaseSec)
			if err != nil {
				consecutiveErrors++
				workerLeaseHeartbeatTotal.WithLabelValues(streamHeartbeatStatusError, streamReasonDBErrorHeartbeat).Inc()
				logger.Warn(
					"Lease heartbeat db error",
					"reason", streamReasonDBErrorHeartbeat,
					"error", err,
					"retry", consecutiveErrors,
				)
				if consecutiveErrors >= maxHeartbeatErrorRetries {
					cancelRun()
					workerLeaseHeartbeatTotal.WithLabelValues(streamHeartbeatStatusLost, streamReasonDBErrorHeartbeat).Inc()
					logger.Error("Lease heartbeat exceeded retries, stopping execution", "reason", streamReasonDBErrorHeartbeat)
					return heartbeatResult{leaseLost: true, reason: streamReasonDBErrorHeartbeat}
				}
				continue
			}
			consecutiveErrors = 0
			if !ok {
				workerLeaseHeartbeatTotal.WithLabelValues(streamHeartbeatStatusLost, streamReasonLeaseLost).Inc()
				logger.Warn("Lease heartbeat lost", "reason", streamReasonLeaseLost)
				cancelRun()
				return heartbeatResult{leaseLost: true, reason: streamReasonLeaseLost}
			}
			workerLeaseHeartbeatTotal.WithLabelValues(streamHeartbeatStatusOK, streamReasonOK).Inc()
		}
	}
}

func (c *StreamConsumer) resolveFinalResult(
	ctx context.Context,
	req *pb.TaskRequest,
	jobID string,
	attemptID int64,
	traceID string,
	resp *pb.TaskResponse,
	execErr error,
) (string, map[string]interface{}) {
	resultKey := common.ResultKeyPrefix + jobID
	if raw, err := c.redis.Get(ctx, resultKey); err == nil && strings.TrimSpace(raw) != "" {
		var result map[string]interface{}
		if uerr := json.Unmarshal([]byte(raw), &result); uerr == nil {
			if _, ok := result["job_id"]; !ok {
				result["job_id"] = jobID
			}
			if _, ok := result["attempt_id"]; !ok {
				result["attempt_id"] = attemptID
			}
			if _, ok := result["trace_id"]; !ok {
				result["trace_id"] = traceID
			}
			status := deriveFinalStatusFromResult(result)
			if status == "" {
				status = deriveFallbackStatus(resp, execErr)
				result["status"] = status
			}
			return status, result
		}
	}

	status := deriveFallbackStatus(resp, execErr)
	result := map[string]interface{}{
		"job_id":     jobID,
		"attempt_id": attemptID,
		"trace_id":   traceID,
		"status":     status,
		"cache_key":  req.GetCacheKey(),
	}
	if execErr != nil {
		result["error_message"] = execErr.Error()
	}
	if resp != nil && strings.TrimSpace(resp.GetMessage()) != "" {
		result["message"] = resp.GetMessage()
	}
	return status, result
}

func deriveFinalStatusFromResult(result map[string]interface{}) string {
	raw, ok := result["status"]
	if !ok || raw == nil {
		return ""
	}
	s := strings.TrimSpace(fmt.Sprint(raw))
	if s == "" || s == "<nil>" {
		return ""
	}
	return s
}

func deriveFallbackStatus(resp *pb.TaskResponse, execErr error) string {
	if execErr != nil {
		return "System Error"
	}
	if resp == nil {
		return "System Error"
	}
	msg := strings.ToLower(strings.TrimSpace(resp.GetMessage()))
	switch msg {
	case "ok":
		return "Finished"
	case "compile error":
		return "Compile Error"
	case "system error", "failed", "report failed":
		return "System Error"
	default:
		if msg == "" {
			return "System Error"
		}
		return resp.GetMessage()
	}
}

func (c *StreamConsumer) markPoisonBeforeAck(
	ctx context.Context,
	logger *slog.Logger,
	jobID string,
	errorCode string,
	errorMessage string,
) error {
	if strings.TrimSpace(jobID) == "" {
		logger.Warn("Skip poison DB mark: empty job_id", "reason", errorCode)
		return nil
	}
	ok, err := c.db.MarkSubmissionPoison(ctx, jobID, errorCode, errorMessage)
	if err != nil {
		return err
	}
	if !ok {
		logger.Warn("Poison DB mark skipped (already done or not found)", "reason", errorCode)
	}
	return nil
}

func (c *StreamConsumer) xackWithRetry(ctx context.Context, entryID string) error {
	var lastErr error
	for i := 0; i < 2; i++ {
		_, err := c.redis.Ack(ctx, c.config.JobStreamKey, c.config.JobStreamGroup, entryID)
		if err == nil {
			xackTotal.WithLabelValues("ok").Inc()
			return nil
		}
		lastErr = err
		if i == 0 {
			time.Sleep(50 * time.Millisecond)
		}
	}
	xackTotal.WithLabelValues("error").Inc()
	return lastErr
}

func (c *StreamConsumer) newMessageLogger(traceID, jobID string, attemptID int64, streamEntryID string) *slog.Logger {
	return c.logger.With(
		"trace_id", traceID,
		"job_id", jobID,
		"attempt_id", attemptID,
		"stream_entry_id", streamEntryID,
		"group", c.config.JobStreamGroup,
		"consumer", c.config.JobStreamConsumer,
	)
}

func requiredStreamField(values map[string]interface{}, key string) (string, bool) {
	raw, ok := values[key]
	if !ok || raw == nil {
		return "", true
	}
	switch v := raw.(type) {
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return "", true
		}
		return s, false
	case []byte:
		s := strings.TrimSpace(string(v))
		if s == "" {
			return "", true
		}
		return s, false
	default:
		s := strings.TrimSpace(fmt.Sprint(v))
		if s == "" || s == "<nil>" {
			return "", true
		}
		return s, false
	}
}

func decodeStreamTaskPayload(payloadRaw string) (*pb.TaskRequest, error) {
	var envelope streamEnvelopeV1
	if err := json.Unmarshal([]byte(payloadRaw), &envelope); err != nil {
		return nil, fmt.Errorf("decode payload envelope: %w", err)
	}
	if envelope.SchemaVersion != streamEnvelopeSchemaVersionV1 {
		return nil, fmt.Errorf("unsupported payload schema_version: %d", envelope.SchemaVersion)
	}
	if strings.TrimSpace(envelope.Encoding) == "" {
		envelope.Encoding = streamEnvelopeEncodingBase64
	}
	if strings.TrimSpace(envelope.ContentType) == "" {
		envelope.ContentType = streamEnvelopeContentTypeJSON
	}
	if strings.ToLower(strings.TrimSpace(envelope.Encoding)) != streamEnvelopeEncodingBase64 {
		return nil, fmt.Errorf("unsupported payload encoding: %s", envelope.Encoding)
	}
	decoded, err := base64.StdEncoding.DecodeString(envelope.TaskDataB64)
	if err != nil {
		return nil, fmt.Errorf("decode payload task_data_b64: %w", err)
	}

	contentType := strings.ToLower(strings.TrimSpace(envelope.ContentType))
	switch contentType {
	case streamEnvelopeContentTypeJSON:
		task, err := decodeJSONTaskPayload(decoded)
		if err == nil {
			return task, nil
		}
		legacy, legacyErr := decodeProtoTaskPayload(decoded)
		if legacyErr == nil {
			return legacy, nil
		}
		return nil, fmt.Errorf("decode task_data json: %w", err)
	case streamEnvelopeContentTypeProto, streamEnvelopeContentTypeProtoV2:
		return decodeProtoTaskPayload(decoded)
	default:
		return nil, fmt.Errorf("unsupported payload content_type: %s", envelope.ContentType)
	}
}

func decodeJSONTaskPayload(data []byte) (*pb.TaskRequest, error) {
	var task pb.TaskRequest
	if err := json.Unmarshal(data, &task); err != nil {
		return nil, err
	}
	return &task, nil
}

func decodeProtoTaskPayload(data []byte) (*pb.TaskRequest, error) {
	var task pb.TaskRequest
	if err := proto.Unmarshal(data, &task); err != nil {
		return nil, err
	}
	return &task, nil
}

func parseInt64FromAny(v interface{}) (int64, bool) {
	switch t := v.(type) {
	case int64:
		return t, true
	case int32:
		return int64(t), true
	case int:
		return int64(t), true
	case float64:
		return int64(t), true
	case json.Number:
		x, err := t.Int64()
		return x, err == nil
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return 0, false
		}
		var x int64
		_, err := fmt.Sscanf(s, "%d", &x)
		return x, err == nil
	default:
		return 0, false
	}
}
