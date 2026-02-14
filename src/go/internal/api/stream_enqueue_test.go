package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
)

type mockStreamEnqueueClient struct {
	setCalls  int
	xaddCalls int
	delCalls  int
	delKeys   []string
	xaddErr   error
	setKey    string
	setValue  string
	setTTL    time.Duration
}

func (m *mockStreamEnqueueClient) Set(ctx context.Context, key, value string, expiration time.Duration) error {
	m.setCalls++
	m.setKey = key
	m.setValue = value
	m.setTTL = expiration
	return nil
}

func (m *mockStreamEnqueueClient) XAdd(ctx context.Context, args *redis.XAddArgs) (string, error) {
	m.xaddCalls++
	if m.xaddErr != nil {
		return "", m.xaddErr
	}
	return "1700000000000-0", nil
}

func (m *mockStreamEnqueueClient) Del(ctx context.Context, keys ...string) error {
	m.delCalls++
	m.delKeys = append(m.delKeys, keys...)
	return nil
}

func TestEnqueueJobToStream_XAddFailureDeletesPayload(t *testing.T) {
	client := &mockStreamEnqueueClient{xaddErr: errors.New("xadd failed")}
	logger := slog.Default()

	_, err := enqueueJobToStream(context.Background(), client, logger, "job-1", "trace-1", []byte("task-bytes"))
	if err == nil {
		t.Fatalf("expected enqueue error when xadd fails")
	}
	if client.setCalls != 1 {
		t.Fatalf("expected Set called once, got %d", client.setCalls)
	}
	if client.xaddCalls != 1 {
		t.Fatalf("expected XAdd called once, got %d", client.xaddCalls)
	}
	if client.delCalls != 1 {
		t.Fatalf("expected Del called once on payload rollback, got %d", client.delCalls)
	}
	if len(client.delKeys) != 1 {
		t.Fatalf("expected one rollback key, got %d", len(client.delKeys))
	}
	if client.delKeys[0] != "task:payload:job-1" {
		t.Fatalf("unexpected rollback key: %s", client.delKeys[0])
	}
}

func TestEnqueueJobToStreamOrReply5xx_XAddFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/submit", nil)

	client := &mockStreamEnqueueClient{xaddErr: errors.New("xadd failed")}
	logger := slog.Default()

	_, ok := enqueueJobToStreamOrReply5xx(c, client, logger, "job-2", "trace-2", "inflight:key", []byte("task-bytes"))
	if ok {
		t.Fatalf("expected enqueue to fail")
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	if client.delCalls < 2 {
		t.Fatalf("expected payload rollback + inflight cleanup del calls, got %d", client.delCalls)
	}
	seenPayloadRollback := false
	seenInflightCleanup := false
	for _, k := range client.delKeys {
		if k == "task:payload:job-2" {
			seenPayloadRollback = true
		}
		if k == "inflight:key" {
			seenInflightCleanup = true
		}
	}
	if !seenPayloadRollback {
		t.Fatalf("expected payload rollback key in del calls")
	}
	if !seenInflightCleanup {
		t.Fatalf("expected inflight key cleanup in del calls")
	}
}

func TestEnqueueJobToStream_PayloadEnvelopeV1Fields(t *testing.T) {
	client := &mockStreamEnqueueClient{}
	logger := slog.Default()

	_, err := enqueueJobToStream(context.Background(), client, logger, "job-3", "trace-3", []byte(`{"job_id":"job-3"}`))
	if err != nil {
		t.Fatalf("enqueueJobToStream failed: %v", err)
	}
	if client.setCalls != 1 {
		t.Fatalf("expected Set called once, got %d", client.setCalls)
	}
	if client.xaddCalls != 1 {
		t.Fatalf("expected XAdd called once, got %d", client.xaddCalls)
	}
	if client.setKey != "task:payload:job-3" {
		t.Fatalf("unexpected payload key: %s", client.setKey)
	}
	if client.setTTL <= 0 {
		t.Fatalf("expected positive payload TTL, got %s", client.setTTL)
	}

	var envelope map[string]any
	if err := json.Unmarshal([]byte(client.setValue), &envelope); err != nil {
		t.Fatalf("payload envelope is not json: %v", err)
	}
	if envelope["schema_version"] != float64(1) {
		t.Fatalf("unexpected schema_version: %v", envelope["schema_version"])
	}
	if _, ok := envelope["task_data_b64"]; !ok {
		t.Fatalf("missing task_data_b64")
	}
	if envelope["encoding"] != "base64" {
		t.Fatalf("unexpected encoding: %v", envelope["encoding"])
	}
	if envelope["content_type"] != "application/json" {
		t.Fatalf("unexpected content_type: %v", envelope["content_type"])
	}
}
