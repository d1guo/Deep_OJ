package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/d1guo/deep_oj/internal/model"
	"github.com/d1guo/deep_oj/internal/repository"
	"github.com/redis/go-redis/v9"
)

type mockSubmitStore struct {
	createCalls       int
	createOutboxCalls int
}

func (m *mockSubmitStore) CreateSubmission(ctx context.Context, s *model.Submission) error {
	m.createCalls++
	return nil
}

func (m *mockSubmitStore) CreateSubmissionAndOutbox(ctx context.Context, submission *model.Submission, event *repository.OutboxEvent) error {
	m.createOutboxCalls++
	submission.ID = 1
	event.ID = 1
	return nil
}

type mockOutboxStore struct {
	mu     sync.Mutex
	events map[int64]repository.OutboxEvent
}

func (m *mockOutboxStore) ClaimPendingOutboxEvents(
	ctx context.Context,
	limit int,
	baseBackoff, maxBackoff time.Duration,
) ([]repository.OutboxEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	result := make([]repository.OutboxEvent, 0, limit)
	for id, evt := range m.events {
		if evt.Status != repository.OutboxStatusPending || evt.DispatchedAt != nil {
			continue
		}
		if !evt.NextAttemptAt.IsZero() && evt.NextAttemptAt.After(now) {
			continue
		}
		evt.Attempts++
		evt.NextAttemptAt = now.Add(baseBackoff)
		evt.UpdatedAt = now
		m.events[id] = evt
		result = append(result, evt)
		if len(result) >= limit {
			break
		}
	}
	return result, nil
}

func (m *mockOutboxStore) MarkOutboxDispatched(ctx context.Context, id int64, streamEntryID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	evt, ok := m.events[id]
	if !ok {
		return errors.New("event not found")
	}
	now := time.Now()
	evt.Status = repository.OutboxStatusDelivered
	evt.StreamEntryID = streamEntryID
	evt.DispatchedAt = &now
	evt.LastError = ""
	evt.UpdatedAt = now
	m.events[id] = evt
	return nil
}

func (m *mockOutboxStore) MarkOutboxDispatchError(ctx context.Context, id int64, lastError string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	evt, ok := m.events[id]
	if !ok {
		return errors.New("event not found")
	}
	evt.Status = repository.OutboxStatusPending
	evt.LastError = lastError
	evt.UpdatedAt = time.Now()
	m.events[id] = evt
	return nil
}

func (m *mockOutboxStore) CountOutboxPending(ctx context.Context) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var count int64
	for _, evt := range m.events {
		if evt.Status == repository.OutboxStatusPending && evt.DispatchedAt == nil {
			count++
		}
	}
	return count, nil
}

func (m *mockOutboxStore) get(id int64) repository.OutboxEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.events[id]
}

type mockOutboxRedis struct {
	setErr  error
	xaddErr error

	setCalls  int
	xaddCalls int
	delCalls  int
}

func (m *mockOutboxRedis) Set(ctx context.Context, key, value string, expiration time.Duration) error {
	m.setCalls++
	return m.setErr
}

func (m *mockOutboxRedis) XAdd(ctx context.Context, args *redis.XAddArgs) (string, error) {
	m.xaddCalls++
	if m.xaddErr != nil {
		return "", m.xaddErr
	}
	return "1700000000000-0", nil
}

func (m *mockOutboxRedis) Del(ctx context.Context, keys ...string) error {
	m.delCalls++
	return nil
}

func TestOutbox_SubmitWhenRedisDown_NotLoseJob(t *testing.T) {
	store := &mockSubmitStore{}
	submission := &model.Submission{JobID: "job-outbox-submit", Status: "pending", State: "pending"}
	event := &repository.OutboxEvent{
		JobID:     "job-outbox-submit",
		StreamKey: "deepoj:jobs",
		EnqueueTS: time.Now().UnixMilli(),
		Priority:  0,
		Payload:   []byte(`{"job_id":"job-outbox-submit"}`),
		Status:    repository.OutboxStatusPending,
	}

	enqueueCalls := 0
	err := persistSubmissionWithDeliveryMode(context.Background(), store, true, submission, event, func() error {
		enqueueCalls++
		return errors.New("redis down")
	})
	if err != nil {
		t.Fatalf("expected outbox submit success when redis is down, got err=%v", err)
	}
	if store.createOutboxCalls != 1 {
		t.Fatalf("expected CreateSubmissionAndOutbox called once, got %d", store.createOutboxCalls)
	}
	if store.createCalls != 0 {
		t.Fatalf("expected CreateSubmission not called in outbox mode, got %d", store.createCalls)
	}
	if enqueueCalls != 0 {
		t.Fatalf("expected direct enqueue not called in outbox mode, got %d", enqueueCalls)
	}
}

func TestOutbox_DispatcherRetry_WhenRedisRecovers_Delivered(t *testing.T) {
	t.Setenv("OUTBOX_RETRY_BASE_MS", "1")
	t.Setenv("OUTBOX_RETRY_MAX_MS", "1")

	eventID := int64(7)
	store := &mockOutboxStore{
		events: map[int64]repository.OutboxEvent{
			eventID: {
				ID:            eventID,
				EventType:     repository.OutboxEventTypeSubmissionEnqueue,
				JobID:         "job-outbox-retry",
				StreamKey:     "deepoj:jobs",
				EnqueueTS:     time.Now().UnixMilli(),
				Priority:      0,
				Payload:       []byte(`{"job_id":"job-outbox-retry"}`),
				Status:        repository.OutboxStatusPending,
				Attempts:      0,
				NextAttemptAt: time.Now().Add(-time.Second),
			},
		},
	}
	redisClient := &mockOutboxRedis{xaddErr: errors.New("redis down")}
	dispatcher := NewOutboxDispatcher(store, redisClient, slog.New(slog.NewTextHandler(io.Discard, nil)))

	dispatcher.DispatchOnce(context.Background())
	first := store.get(eventID)
	if first.DispatchedAt != nil {
		t.Fatalf("expected event still pending on first redis failure")
	}
	if first.LastError == "" {
		t.Fatalf("expected last_error set after first dispatch failure")
	}
	if redisClient.xaddCalls == 0 {
		t.Fatalf("expected xadd called on first dispatch attempt")
	}

	redisClient.xaddErr = nil
	time.Sleep(3 * time.Millisecond)
	dispatcher.DispatchOnce(context.Background())
	second := store.get(eventID)
	if second.DispatchedAt == nil {
		t.Fatalf("expected event delivered after redis recovers")
	}
	if second.Status != repository.OutboxStatusDelivered {
		t.Fatalf("expected delivered status, got %s", second.Status)
	}
	if second.StreamEntryID == "" {
		t.Fatalf("expected stream_entry_id set after successful dispatch")
	}
}
