package worker

import (
	"strconv"
	"testing"
)

func TestJudgeProtocolValidateOK(t *testing.T) {
	jsonLine := validJudgeJSON("job_ok", 0)
	res, perr := parseAndValidateJudgeOutput(jsonLine+"\n", "job_ok", 0)
	if perr != nil {
		t.Fatalf("unexpected protocol error: %v", perr)
	}
	if res == nil {
		t.Fatalf("expected result, got nil")
	}
}

func TestJudgeProtocolJobIDMismatch(t *testing.T) {
	jsonLine := validJudgeJSON("job_actual", 0)
	_, perr := parseAndValidateJudgeOutput(jsonLine, "job_expected", 0)
	if perr == nil {
		t.Fatalf("expected protocol error")
	}
	if perr.reason != reasonJobIDMismatch {
		t.Fatalf("expected reason=%s, got=%s", reasonJobIDMismatch, perr.reason)
	}
}

func TestJudgeProtocolAttemptIDMismatch(t *testing.T) {
	jsonLine := validJudgeJSON("job_ok", 2)
	_, perr := parseAndValidateJudgeOutput(jsonLine, "job_ok", 0)
	if perr == nil {
		t.Fatalf("expected protocol error")
	}
	if perr.reason != reasonAttemptIDMismatch {
		t.Fatalf("expected reason=%s, got=%s", reasonAttemptIDMismatch, perr.reason)
	}
}

func validJudgeJSON(jobID string, attemptID int64) string {
	return "{" +
		"\"job_id\":\"" + jobID + "\"," +
		"\"attempt_id\":" + strconv.FormatInt(attemptID, 10) + "," +
		"\"verdict\":\"OK\"," +
		"\"time_ms\":1," +
		"\"mem_kb\":2," +
		"\"exit_signal\":0," +
		"\"sandbox_error\":\"\"," +
		"\"status\":\"Finished\"," +
		"\"time_used\":1," +
		"\"memory_used\":2," +
		"\"exit_code\":0" +
		"}"
}
