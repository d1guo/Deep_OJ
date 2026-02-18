package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Result from C++ core (stdout)
type JudgeResult struct {
	SchemaVersion int    `json:"schema_version,omitempty"`
	JobId         string `json:"job_id,omitempty"`
	AttemptId     int64  `json:"attempt_id,omitempty"`
	Verdict       string `json:"verdict,omitempty"`
	TimeMs        int    `json:"time_ms,omitempty"`
	MemKb         int64  `json:"mem_kb,omitempty"`
	ExitSignal    int    `json:"exit_signal,omitempty"`
	SandboxError  string `json:"sandbox_error,omitempty"`
	TimeUsed      int    `json:"time_used"`
	MemoryUsed    int    `json:"memory_used"`
	ExitCode      int    `json:"exit_code"`
	Status        string `json:"status"`
	Error         string `json:"error,omitempty"`
}

type CompileResult struct {
	SchemaVersion int    `json:"schema_version,omitempty"`
	Status        string `json:"status"`
	ExePath       string `json:"exe_path,omitempty"`
	Error         string `json:"error,omitempty"`
}

type Executor struct {
	binPath string
}

const (
	defaultStdoutLimitBytes int64 = 256 * 1024
	defaultStderrLimitBytes int64 = 1024 * 1024
	envStdoutLimitBytes           = "JUDGE_STDOUT_LIMIT_BYTES"
	envStderrLimitBytes           = "JUDGE_STDERR_LIMIT_BYTES"

	reasonEmptyStdout       = "empty_stdout"
	reasonMultilineStdout   = "multiline_stdout"
	reasonInvalidJSON       = "invalid_json"
	reasonMissingField      = "missing_field"
	reasonJobIDMismatch     = "job_id_mismatch"
	reasonAttemptIDMismatch = "attempt_id_mismatch"
)

const (
	judgeResultOK       = "ok"
	judgeResultReject   = "reject"
	judgeResultError    = "error"
	judgeResultTimeout  = "timeout"
	judgeVerdictUnknown = "UNKNOWN"
)

type protocolError struct {
	reason            string
	expectedJobID     string
	expectedAttemptID int64
	actualJobID       string
	actualAttemptID   int64
	field             string
}

type drainResult struct {
	data      []byte
	truncated bool
	n         int64
	err       error
}

func (e *protocolError) Error() string {
	if e.field != "" {
		return fmt.Sprintf("judge protocol error: %s (field=%s)", e.reason, e.field)
	}
	return fmt.Sprintf("judge protocol error: %s", e.reason)
}

func NewExecutor(binPath string) *Executor {
	return &Executor{binPath: binPath}
}

func (e *Executor) Execute(ctx context.Context, cfg *Config, jobID string, attemptID int64, traceID string, codePath, inputPath, outputPath string, timeLimit, memLimit int) (*JudgeResult, error) {
	// Command: judge_engine -c <code> -i <input> -o <output> -t <time> -m <mem> -C <config> --job_id <id> --attempt_id <n>

	configPath := cfg.ConfigPath
	if configPath == "" {
		return nil, fmt.Errorf("missing config.yaml: set JUDGER_CONFIG or mount config.yaml")
	}
	if _, err := os.Stat(configPath); err != nil {
		return nil, fmt.Errorf("config.yaml not found at %s: %w", configPath, err)
	}

	args := []string{
		"-c", codePath,
		"-i", inputPath,
		"-o", outputPath,
		"-t", fmt.Sprintf("%d", timeLimit),
		"-m", fmt.Sprintf("%d", memLimit),
		"-C", configPath,
		"-w", filepath.Dir(inputPath), // Use case dir as work dir? Or temp?
		// Sandbox handles work dir creation if passed, or defaults.
		"--job_id", jobID,
		"--attempt_id", fmt.Sprintf("%d", attemptID),
	}

	cmd := exec.Command(e.binPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	logger := newJobLogger(jobID, attemptID, traceID)
	stdoutLimit := getDrainLimit(envStdoutLimitBytes, defaultStdoutLimitBytes)
	stderrLimit := getDrainLimit(envStderrLimitBytes, defaultStderrLimitBytes)

	start := time.Now()
	judgeExecInflight.Inc()
	execInflight.Inc()
	resultLabel := judgeResultError
	defer func() {
		judgeExecInflight.Dec()
		execInflight.Dec()
		judgeExecTotal.WithLabelValues(resultLabel).Inc()
		judgeExecDuration.WithLabelValues(resultLabel).Observe(time.Since(start).Seconds())
		execTotal.WithLabelValues(resultLabel).Inc()
		execDurationSeconds.WithLabelValues(resultLabel).Observe(time.Since(start).Seconds())
	}()

	stdoutRes, stderrRes, waitErr, startErr := runWithDrain(ctx, cmd, stdoutLimit, stderrLimit)
	if startErr != nil {
		return nil, fmt.Errorf("execution failed: %w", startErr)
	}

	if stdoutRes.truncated || stderrRes.truncated {
		logger.Warn(
			"Judge output truncated",
			"truncated_stdout", stdoutRes.truncated,
			"truncated_stderr", stderrRes.truncated,
			"stdout_len", stdoutRes.n,
			"stderr_len", stderrRes.n,
		)
		if stdoutRes.truncated {
			judgeOutputTruncatedTotal.WithLabelValues("stdout").Inc()
		}
		if stderrRes.truncated {
			judgeOutputTruncatedTotal.WithLabelValues("stderr").Inc()
		}
	}

	if waitErr != nil && errors.Is(waitErr, context.DeadlineExceeded) {
		resultLabel = judgeResultTimeout
	}

	res, perr := parseAndValidateJudgeOutput(string(stdoutRes.data), jobID, attemptID)
	if perr != nil {
		if resultLabel != judgeResultTimeout {
			resultLabel = judgeResultReject
		}
		judgeProtocolErrorsTotal.WithLabelValues(perr.reason).Inc()
		return nil, perr
	}

	resultLabel = judgeResultOK
	if waitErr != nil {
		// Preserve previous behavior: prefer structured JSON output over process exit code.
		_ = waitErr
	}

	logJudgeExecEnd(logger, res, stdoutRes.truncated, stderrRes.truncated)
	judgeVerdictTotal.WithLabelValues(normalizeVerdict(res.Verdict)).Inc()
	verdictTotal.WithLabelValues(normalizeVerdict(res.Verdict)).Inc()

	return res, nil
}

func (e *Executor) Compile(ctx context.Context, cfg *Config, requestID, sourcePath string) (*CompileResult, error) {
	configPath := cfg.ConfigPath
	if configPath == "" {
		return nil, fmt.Errorf("missing config.yaml: set JUDGER_CONFIG or mount config.yaml")
	}
	if _, err := os.Stat(configPath); err != nil {
		return nil, fmt.Errorf("config.yaml not found at %s: %w", configPath, err)
	}

	args := []string{
		"--compile",
		"-s", sourcePath,
		"-r", requestID,
		"-C", configPath,
	}

	cmd := exec.Command(e.binPath, args...)
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := runWithContext(ctx, cmd); err != nil {
		if stdout.Len() == 0 {
			return nil, fmt.Errorf("compile failed: %w", err)
		}
	}

	var res CompileResult
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		return nil, fmt.Errorf("failed to parse compile json: %w | output: %s", err, stdout.String())
	}
	return &res, nil
}

func (e *Executor) Cleanup(ctx context.Context, cfg *Config, requestID string) error {
	if requestID == "" {
		return nil
	}
	configPath := cfg.ConfigPath
	if configPath == "" {
		return fmt.Errorf("missing config.yaml: set JUDGER_CONFIG or mount config.yaml")
	}
	args := []string{
		"--cleanup",
		"-r", requestID,
		"-C", configPath,
	}
	cmd := exec.Command(e.binPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return runWithContext(ctx, cmd)
}

func runWithContext(ctx context.Context, cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}

	return waitWithContext(ctx, cmd)
}

func runWithDrain(ctx context.Context, cmd *exec.Cmd, stdoutLimit, stderrLimit int64) (drainResult, drainResult, error, error) {
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return drainResult{}, drainResult{}, nil, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return drainResult{}, drainResult{}, nil, err
	}

	if err := cmd.Start(); err != nil {
		return drainResult{}, drainResult{}, nil, err
	}

	stdoutCh := make(chan drainResult, 1)
	stderrCh := make(chan drainResult, 1)

	go func() {
		stdoutCh <- DrainWithLimit(stdoutPipe, stdoutLimit)
	}()
	go func() {
		stderrCh <- DrainWithLimit(stderrPipe, stderrLimit)
	}()

	waitErr := waitWithContext(ctx, cmd)
	stdoutRes := <-stdoutCh
	stderrRes := <-stderrCh
	return stdoutRes, stderrRes, waitErr, nil
}

func waitWithContext(ctx context.Context, cmd *exec.Cmd) error {
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		err := <-done
		if err == nil {
			err = ctx.Err()
		}
		return fmt.Errorf("command timed out: %w", err)
	}
}

func DrainWithLimit(r io.Reader, limit int64) drainResult {
	if limit < 0 {
		limit = 0
	}
	var buf bytes.Buffer
	if limit > 0 {
		buf.Grow(int(minInt64(limit, 64*1024)))
	}
	tmp := make([]byte, 32*1024)
	var total int64
	truncated := false
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			total += int64(n)
			if int64(buf.Len()) < limit {
				remain := limit - int64(buf.Len())
				if int64(n) <= remain {
					_, _ = buf.Write(tmp[:n])
				} else {
					_, _ = buf.Write(tmp[:remain])
					truncated = true
				}
			} else {
				truncated = true
			}
		}
		if err != nil {
			if err == io.EOF {
				err = nil
			}
			return drainResult{
				data:      buf.Bytes(),
				truncated: truncated,
				n:         total,
				err:       err,
			}
		}
	}
}

func getDrainLimit(env string, def int64) int64 {
	if value := strings.TrimSpace(os.Getenv(env)); value != "" {
		if parsed, err := strconv.ParseInt(value, 10, 64); err == nil && parsed > 0 {
			return parsed
		}
	}
	return def
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func normalizeVerdict(verdict string) string {
	switch strings.ToUpper(strings.TrimSpace(verdict)) {
	case "OK":
		return "OK"
	case "TLE":
		return "TLE"
	case "MLE":
		return "MLE"
	case "OLE":
		return "OLE"
	case "RE":
		return "RE"
	case "SE":
		return "SE"
	default:
		return judgeVerdictUnknown
	}
}

func parseAndValidateJudgeOutput(raw string, expectedJobID string, expectedAttemptID int64) (*JudgeResult, *protocolError) {
	trimmed := strings.TrimRight(raw, "\n")
	if strings.TrimSpace(trimmed) == "" {
		return nil, &protocolError{
			reason:            reasonEmptyStdout,
			expectedJobID:     expectedJobID,
			expectedAttemptID: expectedAttemptID,
		}
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) != 1 {
		return nil, &protocolError{
			reason:            reasonMultilineStdout,
			expectedJobID:     expectedJobID,
			expectedAttemptID: expectedAttemptID,
		}
	}
	line := strings.TrimSpace(lines[0])
	if line == "" {
		return nil, &protocolError{
			reason:            reasonEmptyStdout,
			expectedJobID:     expectedJobID,
			expectedAttemptID: expectedAttemptID,
		}
	}

	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &rawMap); err != nil {
		return nil, &protocolError{
			reason:            reasonInvalidJSON,
			expectedJobID:     expectedJobID,
			expectedAttemptID: expectedAttemptID,
		}
	}

	required := []string{"job_id", "attempt_id", "verdict", "time_ms", "mem_kb", "exit_signal", "sandbox_error"}
	for _, key := range required {
		rawVal, ok := rawMap[key]
		if !ok || len(rawVal) == 0 || string(rawVal) == "null" {
			return nil, &protocolError{
				reason:            reasonMissingField,
				expectedJobID:     expectedJobID,
				expectedAttemptID: expectedAttemptID,
				field:             key,
			}
		}
	}

	var actualJobID string
	if err := json.Unmarshal(rawMap["job_id"], &actualJobID); err != nil {
		return nil, &protocolError{
			reason:            reasonInvalidJSON,
			expectedJobID:     expectedJobID,
			expectedAttemptID: expectedAttemptID,
			field:             "job_id",
		}
	}
	var actualAttemptID int64
	if err := json.Unmarshal(rawMap["attempt_id"], &actualAttemptID); err != nil {
		return nil, &protocolError{
			reason:            reasonInvalidJSON,
			expectedJobID:     expectedJobID,
			expectedAttemptID: expectedAttemptID,
			field:             "attempt_id",
		}
	}
	var verdict string
	if err := json.Unmarshal(rawMap["verdict"], &verdict); err != nil {
		return nil, &protocolError{
			reason:            reasonInvalidJSON,
			expectedJobID:     expectedJobID,
			expectedAttemptID: expectedAttemptID,
			field:             "verdict",
		}
	}
	var timeMs int
	if err := json.Unmarshal(rawMap["time_ms"], &timeMs); err != nil {
		return nil, &protocolError{
			reason:            reasonInvalidJSON,
			expectedJobID:     expectedJobID,
			expectedAttemptID: expectedAttemptID,
			field:             "time_ms",
		}
	}
	var memKb int64
	if err := json.Unmarshal(rawMap["mem_kb"], &memKb); err != nil {
		return nil, &protocolError{
			reason:            reasonInvalidJSON,
			expectedJobID:     expectedJobID,
			expectedAttemptID: expectedAttemptID,
			field:             "mem_kb",
		}
	}
	var exitSignal int
	if err := json.Unmarshal(rawMap["exit_signal"], &exitSignal); err != nil {
		return nil, &protocolError{
			reason:            reasonInvalidJSON,
			expectedJobID:     expectedJobID,
			expectedAttemptID: expectedAttemptID,
			field:             "exit_signal",
		}
	}
	var sandboxError string
	if err := json.Unmarshal(rawMap["sandbox_error"], &sandboxError); err != nil {
		return nil, &protocolError{
			reason:            reasonInvalidJSON,
			expectedJobID:     expectedJobID,
			expectedAttemptID: expectedAttemptID,
			field:             "sandbox_error",
		}
	}

	if actualJobID != expectedJobID {
		return nil, &protocolError{
			reason:            reasonJobIDMismatch,
			expectedJobID:     expectedJobID,
			expectedAttemptID: expectedAttemptID,
			actualJobID:       actualJobID,
			actualAttemptID:   actualAttemptID,
		}
	}
	if actualAttemptID != expectedAttemptID {
		return nil, &protocolError{
			reason:            reasonAttemptIDMismatch,
			expectedJobID:     expectedJobID,
			expectedAttemptID: expectedAttemptID,
			actualJobID:       actualJobID,
			actualAttemptID:   actualAttemptID,
		}
	}

	var res JudgeResult
	if err := json.Unmarshal([]byte(line), &res); err != nil {
		return nil, &protocolError{
			reason:            reasonInvalidJSON,
			expectedJobID:     expectedJobID,
			expectedAttemptID: expectedAttemptID,
		}
	}
	return &res, nil
}
