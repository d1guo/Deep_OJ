package api

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/d1guo/deep_oj/internal/repository"
	"github.com/d1guo/deep_oj/pkg/common"
)

const (
	defaultOutboxDispatchInterval = 1 * time.Second
	defaultOutboxDispatchBatch    = 32
	defaultOutboxRetryBase        = 500 * time.Millisecond
	defaultOutboxRetryMax         = 30 * time.Second
)

type outboxStore interface {
	ClaimPendingOutboxEvents(ctx context.Context, limit int, baseBackoff, maxBackoff time.Duration) ([]repository.OutboxEvent, error)
	MarkOutboxDispatched(ctx context.Context, id int64, streamEntryID string) error
	MarkOutboxDispatchError(ctx context.Context, id int64, lastError string) error
	CountOutboxPending(ctx context.Context) (int64, error)
}

type outboxDispatcher struct {
	store     outboxStore
	redis     streamEnqueueClient
	logger    *slog.Logger
	interval  time.Duration
	batch     int
	retryBase time.Duration
	retryMax  time.Duration
}

func NewOutboxDispatcher(store outboxStore, redisClient streamEnqueueClient, logger *slog.Logger) *outboxDispatcher {
	if logger == nil {
		logger = slog.Default()
	}

	intervalMs := getEnvInt("OUTBOX_DISPATCH_INTERVAL_MS", int(defaultOutboxDispatchInterval/time.Millisecond))
	if intervalMs <= 0 {
		intervalMs = int(defaultOutboxDispatchInterval / time.Millisecond)
	}

	batch := getEnvInt("OUTBOX_DISPATCH_BATCH_SIZE", defaultOutboxDispatchBatch)
	if batch <= 0 {
		batch = defaultOutboxDispatchBatch
	}

	retryBaseMs := getEnvInt("OUTBOX_RETRY_BASE_MS", int(defaultOutboxRetryBase/time.Millisecond))
	if retryBaseMs <= 0 {
		retryBaseMs = int(defaultOutboxRetryBase / time.Millisecond)
	}
	retryMaxMs := getEnvInt("OUTBOX_RETRY_MAX_MS", int(defaultOutboxRetryMax/time.Millisecond))
	if retryMaxMs <= 0 {
		retryMaxMs = int(defaultOutboxRetryMax / time.Millisecond)
	}
	if retryMaxMs < retryBaseMs {
		retryMaxMs = retryBaseMs
	}

	return &outboxDispatcher{
		store:     store,
		redis:     redisClient,
		logger:    logger.With("component", "api_outbox_dispatcher"),
		interval:  time.Duration(intervalMs) * time.Millisecond,
		batch:     batch,
		retryBase: time.Duration(retryBaseMs) * time.Millisecond,
		retryMax:  time.Duration(retryMaxMs) * time.Millisecond,
	}
}

func (d *outboxDispatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()

	d.DispatchOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.DispatchOnce(ctx)
		}
	}
}

func (d *outboxDispatcher) DispatchOnce(ctx context.Context) {
	start := time.Now()
	defer func() {
		apiOutboxDispatchLatencySeconds.Observe(time.Since(start).Seconds())
		d.updatePendingGauge(ctx)
	}()

	events, err := d.store.ClaimPendingOutboxEvents(ctx, d.batch, d.retryBase, d.retryMax)
	if err != nil {
		apiOutboxDispatchTotal.WithLabelValues("error", "db_error").Inc()
		d.logger.Error("Claim outbox failed", "reason", "db_error", "error", err)
		return
	}

	for _, evt := range events {
		d.dispatchOne(ctx, evt)
	}
}

func (d *outboxDispatcher) dispatchOne(ctx context.Context, evt repository.OutboxEvent) {
	logger := d.logger.With(
		"job_id", evt.JobID,
		"trace_id", evt.JobID,
		"outbox_id", evt.ID,
		"attempt", evt.Attempts,
		"next_retry_at", evt.NextAttemptAt,
	)

	payloadEnvelope, err := buildPayloadEnvelope(evt.Payload)
	if err != nil {
		d.markDispatchError(ctx, logger, evt, "payload_encode_error", err)
		return
	}

	payloadTTL := time.Duration(getEnvInt("JOB_PAYLOAD_TTL_SEC", defaultJobPayloadTTLSec)) * time.Second
	if payloadTTL <= 0 {
		payloadTTL = time.Duration(defaultJobPayloadTTLSec) * time.Second
	}

	payloadRef := common.TaskPayloadPrefix + evt.JobID
	if err := d.redis.Set(ctx, payloadRef, payloadEnvelope, payloadTTL); err != nil {
		d.markDispatchError(ctx, logger, evt, "redis_set_error", err)
		return
	}

	streamKey := strings.TrimSpace(evt.StreamKey)
	if streamKey == "" {
		streamKey = getEnvString("JOB_STREAM_KEY", defaultJobStreamKey)
	}
	streamMaxLen := getEnvInt64("JOB_STREAM_MAXLEN", defaultJobStreamMaxLen)

	xaddArgs := buildStreamXAddArgs(streamKey, streamMaxLen, map[string]interface{}{
		"job_id":      evt.JobID,
		"enqueue_ts":  evt.EnqueueTS,
		"payload_ref": payloadRef,
		"priority":    evt.Priority,
		"event_id":    evt.ID,
	})
	entryID, err := d.redis.XAdd(ctx, xaddArgs)
	if err != nil {
		_ = d.redis.Del(ctx, payloadRef)
		d.markDispatchError(ctx, logger, evt, "redis_xadd_error", err)
		return
	}

	if err := d.store.MarkOutboxDispatched(ctx, evt.ID, entryID); err != nil {
		apiOutboxDispatchTotal.WithLabelValues("error", "db_error").Inc()
		d.logger.Error(
			"Mark outbox delivered failed",
			"job_id", evt.JobID,
			"trace_id", evt.JobID,
			"outbox_id", evt.ID,
			"reason", "db_error",
			"error", err,
		)
		return
	}

	apiOutboxDispatchTotal.WithLabelValues("ok", "ok").Inc()
	logger.Info("Outbox dispatched", "stream_entry_id", entryID, "reason", "ok")
}

func (d *outboxDispatcher) markDispatchError(
	ctx context.Context,
	logger *slog.Logger,
	evt repository.OutboxEvent,
	reason string,
	err error,
) {
	apiOutboxDispatchTotal.WithLabelValues("error", reason).Inc()
	logger.Warn("Outbox dispatch failed", "reason", reason, "error", err)
	if derr := d.store.MarkOutboxDispatchError(ctx, evt.ID, truncateErr(err)); derr != nil {
		apiOutboxDispatchTotal.WithLabelValues("error", "db_error").Inc()
		logger.Error("Update outbox dispatch error failed", "reason", "db_error", "error", derr)
	}
}

func (d *outboxDispatcher) updatePendingGauge(ctx context.Context) {
	pending, err := d.store.CountOutboxPending(ctx)
	if err != nil {
		d.logger.Warn("Count outbox pending failed", "reason", "db_error", "error", err)
		return
	}
	apiOutboxPending.Set(float64(pending))
}

func truncateErr(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if len(msg) <= 512 {
		return msg
	}
	return fmt.Sprintf("%s...", msg[:509])
}
