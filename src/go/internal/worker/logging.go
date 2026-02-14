package worker

import "log/slog"

func newJobLogger(jobID string, attemptID int64, traceID string, extra ...any) *slog.Logger {
	attrs := []any{
		"job_id", jobID,
		"attempt_id", attemptID,
		"trace_id", traceID,
	}
	attrs = append(attrs, extra...)
	return slog.With(attrs...)
}

func logJobStart(logger *slog.Logger) {
	logger.Info("Job start")
}

func logJudgeExecEnd(logger *slog.Logger, res *JudgeResult, truncatedStdout, truncatedStderr bool) {
	logger.Info(
		"Judge exec end",
		"exit_code", res.ExitCode,
		"exit_signal", res.ExitSignal,
		"time_ms", res.TimeMs,
		"mem_kb", res.MemKb,
		"truncated_stdout", truncatedStdout,
		"truncated_stderr", truncatedStderr,
	)
}

func logJobSuccess(logger *slog.Logger, verdict string, timeMs int, memKb int) {
	logger.Info(
		"Job success",
		"verdict", verdict,
		"time_ms", timeMs,
		"mem_kb", memKb,
	)
}
