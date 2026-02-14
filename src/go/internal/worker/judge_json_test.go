package worker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestJudgeSelfTestJSONSingleLine(t *testing.T) {
	bin := findJudgeBin(t)
	if bin == "" {
		return
	}

	stdout, stderr, err := runJudge(bin, "--self_test", "--job_id", "demo_job", "--attempt_id", "0")
	if err != nil {
		t.Fatalf("judge self_test failed: %v (stderr=%s)", err, strings.TrimSpace(stderr))
	}
	line := mustSingleLineJSON(t, stdout)
	payload := mustParseJSON(t, line)
	requireFields(t, payload, requiredFields())
}

func TestJudgeSelfTestMissingFieldsStillJSON(t *testing.T) {
	bin := findJudgeBin(t)
	if bin == "" {
		return
	}

	stdout, _, _ := runJudge(bin, "--self_test")
	line := mustSingleLineJSON(t, stdout)
	payload := mustParseJSON(t, line)
	requireFields(t, payload, requiredFields())

	var res struct {
		Verdict      string `json:"verdict"`
		SandboxError string `json:"sandbox_error"`
	}
	if err := json.Unmarshal([]byte(line), &res); err != nil {
		t.Fatalf("invalid JSON: %v | output=%s", err, line)
	}
	if res.Verdict != "SE" {
		t.Fatalf("expected verdict=SE when missing fields, got %q", res.Verdict)
	}
	if res.SandboxError != "missing_job_id" && res.SandboxError != "missing_attempt_id" {
		t.Fatalf("unexpected sandbox_error: %q", res.SandboxError)
	}
}

func findJudgeBin(t *testing.T) string {
	if path := strings.TrimSpace(os.Getenv("JUDGE_BIN")); path != "" {
		if isExecutable(path) {
			return path
		}
		t.Fatalf("JUDGE_BIN set but not executable: %s", path)
	}

	root, ok := findRepoRoot()
	if ok {
		candidates := []string{
			filepath.Join(root, "build", "judge_engine"),
			filepath.Join(root, "bin", "judge_engine"),
		}
		for _, c := range candidates {
			if isExecutable(c) {
				return c
			}
		}
	}

	t.Skip(fmt.Sprintf("judge_engine not found; set JUDGE_BIN or build ./build/judge_engine (cwd=%s)", mustGetwd()))
	return ""
}

func findRepoRoot() (string, bool) {
	dir := mustGetwd()
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "CMakeLists.txt")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", false
}

func mustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}

func runJudge(bin string, args ...string) (string, string, error) {
	cmd := exec.Command(bin, args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func mustSingleLineJSON(t *testing.T, stdout string) string {
	t.Helper()
	trimmed := strings.TrimRight(stdout, "\n")
	if strings.TrimSpace(trimmed) == "" {
		t.Fatalf("stdout empty or only newlines")
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) != 1 {
		t.Fatalf("expected single-line stdout, got %d lines: %q", len(lines), trimmed)
	}
	line := strings.TrimSpace(lines[0])
	if line == "" {
		t.Fatalf("stdout line empty after trim")
	}
	return line
}

func mustParseJSON(t *testing.T, line string) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(line), &payload); err != nil {
		t.Fatalf("invalid JSON: %v | output=%s", err, line)
	}
	return payload
}

func requiredFields() []string {
	return []string{"job_id", "attempt_id", "verdict", "time_ms", "mem_kb", "exit_signal", "sandbox_error"}
}

func requireFields(t *testing.T, payload map[string]any, required []string) {
	t.Helper()
	for _, key := range required {
		if _, ok := payload[key]; !ok {
			t.Fatalf("missing field %q in JSON: %v", key, payload)
		}
	}
}
