package worker

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestTraceIDPropagation(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	orig := slog.Default()
	slog.SetDefault(logger)
	defer slog.SetDefault(orig)

	jobID := "job_trace"
	attemptID := int64(0)
	traceID := "trace_123"

	jobLogger := newJobLogger(jobID, attemptID, traceID)
	logJobStart(jobLogger)
	logJudgeExecEnd(jobLogger, &JudgeResult{ExitCode: 0, ExitSignal: 0, TimeMs: 3, MemKb: 42}, false, false)
	logJobSuccess(jobLogger, "Accepted", 3, 42)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 log lines, got %d", len(lines))
	}
	for _, line := range lines {
		var payload map[string]any
		if err := json.Unmarshal([]byte(line), &payload); err != nil {
			t.Fatalf("invalid log json: %v", err)
		}
		if payload["trace_id"] != traceID {
			t.Fatalf("trace_id mismatch: %v", payload["trace_id"])
		}
		if payload["job_id"] != jobID {
			t.Fatalf("job_id mismatch: %v", payload["job_id"])
		}
		if payload["attempt_id"] != float64(attemptID) {
			t.Fatalf("attempt_id mismatch: %v", payload["attempt_id"])
		}
	}
}
