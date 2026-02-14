package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/d1guo/deep_oj/internal/model"
)

const (
	OutboxStatusPending   = "pending"
	OutboxStatusDelivered = "delivered"

	OutboxEventTypeSubmissionEnqueue = "submission.enqueue"
)

const claimPendingOutboxSQL = `
	WITH picked AS (
		SELECT id
		FROM outbox_events
		WHERE status = $1
		  AND dispatched_at IS NULL
		  AND next_attempt_at <= NOW()
		ORDER BY next_attempt_at, id
		FOR UPDATE SKIP LOCKED
		LIMIT $2
	)
	UPDATE outbox_events AS o
	SET attempts = o.attempts + 1,
	    next_attempt_at = NOW() + (
	        LEAST(
	            $4::numeric,
	            GREATEST(
	                1::numeric,
	                LEAST(
	                    $3::numeric * POWER(2::numeric, LEAST(o.attempts, 30)),
	                    $4::numeric
	                ) * (0.9 + random() * 0.2)
	            )
	        )::BIGINT * interval '1 millisecond'
	    ),
	    updated_at = NOW()
	FROM picked
	WHERE o.id = picked.id
	RETURNING o.id, o.event_type, o.job_id, o.stream_key, o.enqueue_ts, o.priority,
	          o.payload::text, o.status, o.attempts, o.next_attempt_at, o.created_at, o.updated_at
`

type OutboxEvent struct {
	ID            int64
	EventType     string
	JobID         string
	StreamKey     string
	EnqueueTS     int64
	Priority      int
	Payload       []byte
	Status        string
	Attempts      int
	NextAttemptAt time.Time
	LastError     string
	DispatchedAt  *time.Time
	StreamEntryID string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// CreateSubmissionAndOutbox persists submission and outbox event atomically.
func (db *PostgresDB) CreateSubmissionAndOutbox(
	ctx context.Context,
	submission *model.Submission,
	event *OutboxEvent,
) error {
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) // nolint:errcheck

	var problemID interface{}
	if submission.ProblemID > 0 {
		problemID = submission.ProblemID
	}
	var userID interface{}
	if submission.UserID > 0 {
		userID = submission.UserID
	}

	submissionSQL := `
		INSERT INTO submissions (job_id, problem_id, user_id, code, language, time_limit, memory_limit, status, state)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, created_at, updated_at
	`
	if err := tx.QueryRow(
		ctx,
		submissionSQL,
		submission.JobID,
		problemID,
		userID,
		submission.Code,
		submission.Language,
		submission.TimeLimit,
		submission.MemoryLimit,
		submission.Status,
		submission.State,
	).Scan(&submission.ID, &submission.CreatedAt, &submission.UpdatedAt); err != nil {
		return fmt.Errorf("insert submission: %w", err)
	}

	eventType := strings.TrimSpace(event.EventType)
	if eventType == "" {
		eventType = OutboxEventTypeSubmissionEnqueue
	}
	status := strings.TrimSpace(event.Status)
	if status == "" {
		status = OutboxStatusPending
	}

	outboxSQL := `
		INSERT INTO outbox_events (
			event_type, job_id, stream_key, enqueue_ts, priority, payload,
			status, attempts, next_attempt_at, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, 0, NOW(), NOW(), NOW())
		RETURNING id, created_at, updated_at
	`
	if err := tx.QueryRow(
		ctx,
		outboxSQL,
		eventType,
		event.JobID,
		event.StreamKey,
		event.EnqueueTS,
		event.Priority,
		string(event.Payload),
		status,
	).Scan(&event.ID, &event.CreatedAt, &event.UpdatedAt); err != nil {
		return fmt.Errorf("insert outbox event: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}

	event.EventType = eventType
	event.Status = status
	return nil
}

// ClaimPendingOutboxEvents claims pending outbox rows with row-level locking and applies retry backoff.
func (db *PostgresDB) ClaimPendingOutboxEvents(
	ctx context.Context,
	limit int,
	baseBackoff time.Duration,
	maxBackoff time.Duration,
) ([]OutboxEvent, error) {
	if limit <= 0 {
		limit = 32
	}
	baseMs := int64(baseBackoff / time.Millisecond)
	if baseMs <= 0 {
		baseMs = int64((500 * time.Millisecond) / time.Millisecond)
	}
	maxMs := int64(maxBackoff / time.Millisecond)
	if maxMs <= 0 {
		maxMs = int64((30 * time.Second) / time.Millisecond)
	}
	if maxMs < baseMs {
		maxMs = baseMs
	}

	rows, err := db.pool.Query(ctx, claimPendingOutboxSQL, OutboxStatusPending, limit, baseMs, maxMs)
	if err != nil {
		return nil, fmt.Errorf("claim outbox events: %w", err)
	}
	defer rows.Close()

	events := make([]OutboxEvent, 0, limit)
	for rows.Next() {
		var evt OutboxEvent
		var payloadText string
		if err := rows.Scan(
			&evt.ID,
			&evt.EventType,
			&evt.JobID,
			&evt.StreamKey,
			&evt.EnqueueTS,
			&evt.Priority,
			&payloadText,
			&evt.Status,
			&evt.Attempts,
			&evt.NextAttemptAt,
			&evt.CreatedAt,
			&evt.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan outbox event: %w", err)
		}
		evt.Payload = []byte(payloadText)
		events = append(events, evt)
	}
	return events, nil
}

func (db *PostgresDB) MarkOutboxDispatched(ctx context.Context, id int64, streamEntryID string) error {
	query := `
		UPDATE outbox_events
		SET status = $2,
		    last_error = NULL,
		    dispatched_at = NOW(),
		    stream_entry_id = $3,
		    updated_at = NOW()
		WHERE id = $1
	`
	if _, err := db.pool.Exec(ctx, query, id, OutboxStatusDelivered, streamEntryID); err != nil {
		return fmt.Errorf("mark outbox dispatched: %w", err)
	}
	return nil
}

func (db *PostgresDB) MarkOutboxDispatchError(ctx context.Context, id int64, lastError string) error {
	query := `
		UPDATE outbox_events
		SET status = $2,
		    last_error = $3,
		    updated_at = NOW()
		WHERE id = $1
		  AND dispatched_at IS NULL
	`
	if _, err := db.pool.Exec(ctx, query, id, OutboxStatusPending, lastError); err != nil {
		return fmt.Errorf("mark outbox dispatch error: %w", err)
	}
	return nil
}

func (db *PostgresDB) CountOutboxPending(ctx context.Context) (int64, error) {
	query := `
		SELECT COUNT(1)
		FROM outbox_events
		WHERE status = $1
		  AND dispatched_at IS NULL
	`
	var count int64
	if err := db.pool.QueryRow(ctx, query, OutboxStatusPending).Scan(&count); err != nil {
		return 0, fmt.Errorf("count outbox pending: %w", err)
	}
	return count, nil
}

func (db *PostgresDB) GetOutboxByJobID(ctx context.Context, jobID string) (*OutboxEvent, error) {
	query := `
		SELECT id, event_type, job_id, stream_key, enqueue_ts, priority,
		       payload::text, status, attempts, next_attempt_at, last_error,
		       dispatched_at, stream_entry_id, created_at, updated_at
		FROM outbox_events
		WHERE job_id = $1
	`
	var evt OutboxEvent
	var payloadText string
	if err := db.pool.QueryRow(ctx, query, jobID).Scan(
		&evt.ID,
		&evt.EventType,
		&evt.JobID,
		&evt.StreamKey,
		&evt.EnqueueTS,
		&evt.Priority,
		&payloadText,
		&evt.Status,
		&evt.Attempts,
		&evt.NextAttemptAt,
		&evt.LastError,
		&evt.DispatchedAt,
		&evt.StreamEntryID,
		&evt.CreatedAt,
		&evt.UpdatedAt,
	); err != nil {
		return nil, err
	}
	evt.Payload = []byte(payloadText)
	return &evt, nil
}
