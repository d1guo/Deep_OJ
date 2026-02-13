package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// Result from C++ core (stdout)
type JudgeResult struct {
	SchemaVersion int    `json:"schema_version,omitempty"`
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

func NewExecutor(binPath string) *Executor {
	return &Executor{binPath: binPath}
}

func (e *Executor) Execute(ctx context.Context, cfg *Config, codePath, inputPath, outputPath string, timeLimit, memLimit int) (*JudgeResult, error) {
	// Command: judge_engine -c <code> -i <input> -o <output> -t <time> -m <mem> -C <config>

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
	}

	cmd := exec.Command(e.binPath, args...)
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Capture stdout for JSON result
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := runWithContext(ctx, cmd); err != nil {
		out := stdout.Bytes()
		if len(out) == 0 {
			return nil, fmt.Errorf("execution failed: %w", err)
		}
	}

	var res JudgeResult
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		return nil, fmt.Errorf("failed to parse result json: %w | output: %s", err, stdout.String())
	}

	return &res, nil
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
