package worker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/d1guo/deep_oj/pkg/common"
	pb "github.com/d1guo/deep_oj/pkg/proto"
	"github.com/go-redis/redis/v8"
)

type mockStreamRedis struct {
	mu            sync.Mutex
	payloadByKey  map[string]string
	getErrByKey   map[string]error
	ackErr        error
	ackCalls      int
	lastAckStream string
	lastAckGroup  string
	lastAckID     string
}

func (m *mockStreamRedis) EnsureGroup(ctx context.Context, stream, group string) error {
	return nil
}

func (m *mockStreamRedis) ReadNew(ctx context.Context, stream, group, consumer string, count int64, block time.Duration) ([]redis.XStream, error) {
	return nil, redis.Nil
}

func (m *mockStreamRedis) Ack(ctx context.Context, stream, group string, ids ...string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ackCalls++
	m.lastAckStream = stream
	m.lastAckGroup = group
	if len(ids) > 0 {
		m.lastAckID = ids[0]
	}
	if m.ackErr != nil {
		return 0, m.ackErr
	}
	return 1, nil
}

func (m *mockStreamRedis) Get(ctx context.Context, key string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err, ok := m.getErrByKey[key]; ok {
		return "", err
	}
	val, ok := m.payloadByKey[key]
	if !ok {
		return "", redis.Nil
	}
	return val, nil
}

type mockStreamDB struct {
	mu sync.Mutex

	claimAttempt int64
	claimOK      bool
	claimErr     error
	claimCalls   int

	refreshResults []bool
	refreshErrs    []error
	refreshCalls   int

	finalizeOK      bool
	finalizeErr     error
	finalizeCalls   int
	finalizeJobID   string
	finalizeAttempt int64
	finalizeStatus  string

	poisonOK      bool
	poisonErr     error
	poisonCalls   int
	poisonJobID   string
	poisonCode    string
	poisonMessage string
}

func (m *mockStreamDB) ClaimSubmissionForRun(ctx context.Context, jobID, leaseOwner string, leaseSec int) (int64, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.claimCalls++
	if m.claimErr != nil {
		return 0, false, m.claimErr
	}
	return m.claimAttempt, m.claimOK, nil
}

func (m *mockStreamDB) RefreshSubmissionLease(ctx context.Context, jobID string, attemptID int64, leaseOwner string, leaseSec int) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx := m.refreshCalls
	m.refreshCalls++
	if idx < len(m.refreshErrs) && m.refreshErrs[idx] != nil {
		return false, m.refreshErrs[idx]
	}
	if idx < len(m.refreshResults) {
		return m.refreshResults[idx], nil
	}
	return true, nil
}

func (m *mockStreamDB) FinalizeSubmissionWithFence(ctx context.Context, jobID string, attemptID int64, status string, result any) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.finalizeCalls++
	m.finalizeJobID = jobID
	m.finalizeAttempt = attemptID
	m.finalizeStatus = status
	if m.finalizeErr != nil {
		return false, m.finalizeErr
	}
	return m.finalizeOK, nil
}

func (m *mockStreamDB) MarkSubmissionPoison(ctx context.Context, jobID, errorCode, errorMessage string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.poisonCalls++
	m.poisonJobID = jobID
	m.poisonCode = errorCode
	m.poisonMessage = errorMessage
	if m.poisonErr != nil {
		return false, m.poisonErr
	}
	return m.poisonOK, nil
}

type mockStreamRunner struct {
	response      *pb.TaskResponse
	err           error
	sleep         time.Duration
	waitForCancel bool

	mu        sync.Mutex
	called    int
	cancelled bool
}

func (m *mockStreamRunner) ExecuteTask(ctx context.Context, req *pb.TaskRequest) (*pb.TaskResponse, error) {
	m.mu.Lock()
	m.called++
	m.mu.Unlock()

	if m.waitForCancel {
		<-ctx.Done()
		m.mu.Lock()
		m.cancelled = true
		m.mu.Unlock()
		return nil, ctx.Err()
	}
	if m.sleep > 0 {
		select {
		case <-time.After(m.sleep):
		case <-ctx.Done():
			m.mu.Lock()
			m.cancelled = true
			m.mu.Unlock()
			return nil, ctx.Err()
		}
	}
	return m.response, m.err
}

func (m *mockStreamRunner) wasCancelled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cancelled
}

func TestStreamConsumer_ClaimHeartbeatFinalizeAndXAck(t *testing.T) {
	jobID := "job-c3-1"
	payloadKey := "task:payload:" + jobID
	resultKey := common.ResultKeyPrefix + jobID

	cfg := &Config{
		JobStreamKey:      "deepoj:jobs",
		JobStreamGroup:    "deepoj:workers",
		JobStreamConsumer: "worker-test-1",
		JobLeaseSec:       60,
		JobHeartbeatSec:   1,
	}
	redisClient := &mockStreamRedis{
		payloadByKey: map[string]string{
			payloadKey: mustBuildPayloadEnvelopeFromTask(t, &pb.TaskRequest{
				JobId:   jobID,
				TraceId: "trace-c3-1",
			}),
			resultKey: `{"job_id":"job-c3-1","attempt_id":7,"status":"Accepted","time_used":5,"memory_used":32,"trace_id":"trace-c3-1"}`,
		},
	}
	db := &mockStreamDB{
		claimAttempt:   7,
		claimOK:        true,
		refreshResults: []bool{true},
		finalizeOK:     true,
		poisonOK:       true,
	}
	runner := &mockStreamRunner{
		response: &pb.TaskResponse{Message: "OK"},
		sleep:    1200 * time.Millisecond,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	consumer := newStreamConsumerForTest(cfg, redisClient, db, runner, logger)

	res := consumer.handleMessage(context.Background(), redis.XMessage{
		ID: "1700000000000-0",
		Values: map[string]interface{}{
			"job_id":      jobID,
			"enqueue_ts":  "1700000000000",
			"payload_ref": payloadKey,
			"priority":    "0",
		},
	})

	if res.status != streamConsumeStatusOK || res.reason != streamReasonOK {
		t.Fatalf("expected consume ok, got status=%s reason=%s", res.status, res.reason)
	}
	if db.claimCalls != 1 {
		t.Fatalf("expected one claim call, got %d", db.claimCalls)
	}
	if db.refreshCalls == 0 {
		t.Fatalf("expected heartbeat refresh called")
	}
	if db.finalizeCalls != 1 {
		t.Fatalf("expected finalize called once, got %d", db.finalizeCalls)
	}
	if db.finalizeAttempt != 7 {
		t.Fatalf("expected finalize attempt=7, got %d", db.finalizeAttempt)
	}
	if redisClient.ackCalls != 1 {
		t.Fatalf("expected xack once, got %d", redisClient.ackCalls)
	}
	if redisClient.lastAckID != "1700000000000-0" {
		t.Fatalf("unexpected ack id: %s", redisClient.lastAckID)
	}
}

func TestStreamConsumer_StaleAttemptFinalizeRejected(t *testing.T) {
	jobID := "job-c3-stale"
	payloadKey := "task:payload:" + jobID

	cfg := &Config{
		JobStreamKey:      "deepoj:jobs",
		JobStreamGroup:    "deepoj:workers",
		JobStreamConsumer: "worker-test-1",
		JobLeaseSec:       60,
		JobHeartbeatSec:   10,
	}
	redisClient := &mockStreamRedis{
		payloadByKey: map[string]string{
			payloadKey: mustBuildPayloadEnvelopeFromTask(t, &pb.TaskRequest{
				JobId:   jobID,
				TraceId: "trace-c3-stale",
			}),
		},
	}
	db := &mockStreamDB{
		claimAttempt: 3,
		claimOK:      true,
		finalizeOK:   false,
		poisonOK:     true,
	}
	runner := &mockStreamRunner{
		response: &pb.TaskResponse{Message: "OK"},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	consumer := newStreamConsumerForTest(cfg, redisClient, db, runner, logger)

	res := consumer.handleMessage(context.Background(), redis.XMessage{
		ID: "1700000000001-0",
		Values: map[string]interface{}{
			"job_id":      jobID,
			"enqueue_ts":  "1700000001000",
			"payload_ref": payloadKey,
			"priority":    "0",
		},
	})

	if res.reason != streamReasonStaleAttempt {
		t.Fatalf("expected reason=%s, got=%s", streamReasonStaleAttempt, res.reason)
	}
	if db.finalizeCalls != 1 {
		t.Fatalf("expected finalize called once, got %d", db.finalizeCalls)
	}
	if redisClient.ackCalls != 1 {
		t.Fatalf("expected stale attempt path to xack, got %d", redisClient.ackCalls)
	}
}

func TestStreamConsumer_LeaseLostStopsExecutionAndNoFinalize(t *testing.T) {
	jobID := "job-c3-lease-lost"
	payloadKey := "task:payload:" + jobID

	cfg := &Config{
		JobStreamKey:      "deepoj:jobs",
		JobStreamGroup:    "deepoj:workers",
		JobStreamConsumer: "worker-test-1",
		JobLeaseSec:       60,
		JobHeartbeatSec:   1,
	}
	redisClient := &mockStreamRedis{
		payloadByKey: map[string]string{
			payloadKey: mustBuildPayloadEnvelopeFromTask(t, &pb.TaskRequest{
				JobId:   jobID,
				TraceId: "trace-c3-lease-lost",
			}),
		},
	}
	db := &mockStreamDB{
		claimAttempt:   11,
		claimOK:        true,
		refreshResults: []bool{false},
		finalizeOK:     true,
		poisonOK:       true,
	}
	runner := &mockStreamRunner{waitForCancel: true}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	consumer := newStreamConsumerForTest(cfg, redisClient, db, runner, logger)

	res := consumer.handleMessage(context.Background(), redis.XMessage{
		ID: "1700000000002-0",
		Values: map[string]interface{}{
			"job_id":      jobID,
			"enqueue_ts":  "1700000002000",
			"payload_ref": payloadKey,
			"priority":    "0",
		},
	})

	if res.reason != streamReasonLeaseLost {
		t.Fatalf("expected reason=%s, got=%s", streamReasonLeaseLost, res.reason)
	}
	if !runner.wasCancelled() {
		t.Fatalf("expected runner to be cancelled on lease_lost")
	}
	if db.finalizeCalls != 0 {
		t.Fatalf("expected no finalize after lease_lost, got %d", db.finalizeCalls)
	}
	if redisClient.ackCalls != 0 {
		t.Fatalf("expected no xack on lease_lost, got %d", redisClient.ackCalls)
	}
}

func TestStreamConsumer_PoisonPayloadMissingMarksFailedThenXAck(t *testing.T) {
	jobID := "job-c3-poison"
	payloadKey := "task:payload:" + jobID

	cfg := &Config{
		JobStreamKey:      "deepoj:jobs",
		JobStreamGroup:    "deepoj:workers",
		JobStreamConsumer: "worker-test-1",
		JobLeaseSec:       60,
		JobHeartbeatSec:   10,
	}
	redisClient := &mockStreamRedis{
		payloadByKey: map[string]string{},
		getErrByKey: map[string]error{
			payloadKey: redis.Nil,
		},
	}
	db := &mockStreamDB{
		poisonOK: true,
	}
	runner := &mockStreamRunner{response: &pb.TaskResponse{Message: "OK"}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	consumer := newStreamConsumerForTest(cfg, redisClient, db, runner, logger)

	res := consumer.handleMessage(context.Background(), redis.XMessage{
		ID: "1700000000003-0",
		Values: map[string]interface{}{
			"job_id":      jobID,
			"enqueue_ts":  "1700000003000",
			"payload_ref": payloadKey,
			"priority":    "0",
		},
	})

	if res.reason != streamReasonPayloadInvalid {
		t.Fatalf("expected reason=%s, got=%s", streamReasonPayloadInvalid, res.reason)
	}
	if db.poisonCalls != 1 {
		t.Fatalf("expected poison DB mark once, got %d", db.poisonCalls)
	}
	if db.poisonJobID != jobID {
		t.Fatalf("expected poison job_id=%s, got %s", jobID, db.poisonJobID)
	}
	if redisClient.ackCalls != 1 {
		t.Fatalf("expected xack once, got %d", redisClient.ackCalls)
	}
	if db.claimCalls != 0 {
		t.Fatalf("expected no claim call on poison payload, got %d", db.claimCalls)
	}
}

func TestDecodeStreamTaskPayload_DefaultsLegacyEnvelopeFields(t *testing.T) {
	taskJSON, err := json.Marshal(&pb.TaskRequest{
		JobId:   "job-legacy",
		TraceId: "trace-legacy",
	})
	if err != nil {
		t.Fatalf("marshal task json: %v", err)
	}
	legacyEnvelope := map[string]any{
		"schema_version": 1,
		"task_data_b64":  base64.StdEncoding.EncodeToString(taskJSON),
	}
	raw, err := json.Marshal(legacyEnvelope)
	if err != nil {
		t.Fatalf("marshal legacy envelope: %v", err)
	}

	taskReq, err := decodeStreamTaskPayload(string(raw))
	if err != nil {
		t.Fatalf("decodeStreamTaskPayload failed: %v", err)
	}
	if taskReq.GetJobId() != "job-legacy" {
		t.Fatalf("unexpected job_id: %s", taskReq.GetJobId())
	}
	if taskReq.GetTraceId() != "trace-legacy" {
		t.Fatalf("unexpected trace_id: %s", taskReq.GetTraceId())
	}
}

func TestStreamConsumer_DBErrorNoXAck(t *testing.T) {
	jobID := "job-c3-db-error"
	payloadKey := "task:payload:" + jobID
	cfg := &Config{
		JobStreamKey:      "deepoj:jobs",
		JobStreamGroup:    "deepoj:workers",
		JobStreamConsumer: "worker-test-1",
		JobLeaseSec:       60,
		JobHeartbeatSec:   10,
	}
	redisClient := &mockStreamRedis{
		payloadByKey: map[string]string{
			payloadKey: mustBuildPayloadEnvelopeFromTask(t, &pb.TaskRequest{
				JobId:   jobID,
				TraceId: "trace-c3-db-error",
			}),
		},
	}
	db := &mockStreamDB{
		claimErr: errors.New("db timeout"),
		poisonOK: true,
	}
	runner := &mockStreamRunner{response: &pb.TaskResponse{Message: "OK"}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	consumer := newStreamConsumerForTest(cfg, redisClient, db, runner, logger)

	res := consumer.handleMessage(context.Background(), redis.XMessage{
		ID: "1700000000004-0",
		Values: map[string]interface{}{
			"job_id":      jobID,
			"enqueue_ts":  "1700000004000",
			"payload_ref": payloadKey,
			"priority":    "0",
		},
	})

	if res.reason != streamReasonDBError {
		t.Fatalf("expected reason=%s, got=%s", streamReasonDBError, res.reason)
	}
	if redisClient.ackCalls != 0 {
		t.Fatalf("expected no xack when db claim errors, got %d", redisClient.ackCalls)
	}
}

func mustBuildPayloadEnvelopeFromTask(t *testing.T, task *pb.TaskRequest) string {
	t.Helper()
	taskJSON, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("marshal task: %v", err)
	}
	envelope := map[string]any{
		"schema_version": 1,
		"task_data_b64":  base64.StdEncoding.EncodeToString(taskJSON),
		"encoding":       "base64",
		"content_type":   "application/json",
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return string(raw)
}
