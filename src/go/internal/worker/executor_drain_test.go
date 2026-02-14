package worker

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDrainLargeStderrNoDeadlock(t *testing.T) {
	const limit = int64(64 * 1024)
	cmd := helperCommand(t, "stderr", limit*4)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stdoutRes, stderrRes, waitErr, startErr := runWithDrain(ctx, cmd, limit, limit)
	if startErr != nil {
		t.Fatalf("start failed: %v", startErr)
	}
	if waitErr != nil {
		t.Fatalf("wait failed: %v", waitErr)
	}
	if !stderrRes.truncated {
		t.Fatalf("expected stderr truncated")
	}
	if int64(len(stderrRes.data)) > limit {
		t.Fatalf("stderr captured exceeds limit: %d", len(stderrRes.data))
	}
	if stderrRes.n <= limit {
		t.Fatalf("expected stderr bytes > limit, got %d", stderrRes.n)
	}
	if stdoutRes.n != 0 {
		t.Fatalf("expected no stdout, got %d bytes", stdoutRes.n)
	}
}

func TestDrainLargeStdoutNoDeadlock(t *testing.T) {
	const limit = int64(64 * 1024)
	cmd := helperCommand(t, "stdout", limit*4)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stdoutRes, stderrRes, waitErr, startErr := runWithDrain(ctx, cmd, limit, limit)
	if startErr != nil {
		t.Fatalf("start failed: %v", startErr)
	}
	if waitErr != nil {
		t.Fatalf("wait failed: %v", waitErr)
	}
	if !stdoutRes.truncated {
		t.Fatalf("expected stdout truncated")
	}
	if int64(len(stdoutRes.data)) > limit {
		t.Fatalf("stdout captured exceeds limit: %d", len(stdoutRes.data))
	}
	if stdoutRes.n <= limit {
		t.Fatalf("expected stdout bytes > limit, got %d", stdoutRes.n)
	}
	if stderrRes.n != 0 {
		t.Fatalf("expected no stderr, got %d bytes", stderrRes.n)
	}
}

func helperCommand(t *testing.T, mode string, size int64) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperProcessDrain")
	cmd.Env = append(os.Environ(),
		"GO_WANT_HELPER_PROCESS=1",
		"HELPER_MODE="+mode,
		"HELPER_SIZE="+strconv.FormatInt(size, 10),
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd
}

func TestHelperProcessDrain(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("HELPER_MODE")))
	if mode == "" {
		mode = "stdout"
	}
	sizeStr := strings.TrimSpace(os.Getenv("HELPER_SIZE"))
	size, _ := strconv.ParseInt(sizeStr, 10, 64)
	if size <= 0 {
		size = 1024
	}

	var out *os.File
	switch mode {
	case "stderr":
		out = os.Stderr
	default:
		out = os.Stdout
	}

	chunk := make([]byte, 32*1024)
	for i := range chunk {
		chunk[i] = 'x'
	}

	written := int64(0)
	for written < size {
		remaining := size - written
		n := int64(len(chunk))
		if remaining < n {
			n = remaining
		}
		_, _ = out.Write(chunk[:n])
		written += n
	}
	os.Exit(0)
}
