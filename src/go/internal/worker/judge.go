package worker

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/d1guo/deep_oj/pkg/common"
	pb "github.com/d1guo/deep_oj/pkg/proto"
	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
)

type JudgeService struct {
	pb.UnimplementedJudgeServiceServer
	config   *Config
	executor *Executor
	tcMgr    *TestCaseManager
	redis    *redis.Client // Added Redis client
	sem      chan struct{}
}

var safeJobIDRegex = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func NewJudgeService(cfg *Config, exec *Executor, tcMgr *TestCaseManager, rdb *redis.Client) *JudgeService {
	return &JudgeService{
		config:   cfg,
		executor: exec,
		tcMgr:    tcMgr,
		redis:    rdb,
		sem:      make(chan struct{}, cfg.PoolSize),
	}
}

func (s *JudgeService) ExecuteTask(ctx context.Context, req *pb.TaskRequest) (*pb.TaskResponse, error) {
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return &pb.TaskResponse{Message: "Busy"}, ctx.Err()
	}

	jobID := req.JobId
	attemptID := attemptIDFromContext(ctx)
	traceID := strings.TrimSpace(req.TraceId)
	if traceID == "" {
		traceID = uuid.NewString()
	}
	logger := newJobLogger(
		jobID,
		attemptID,
		traceID,
		"problem_id", req.ProblemId,
		"lang", req.Language,
	)
	logJobStart(logger)
	taskStart := time.Now()
	taskStatus := "system_error"
	defer func() {
		workerTaskTotal.WithLabelValues(taskStatus).Inc()
		workerTaskDuration.Observe(time.Since(taskStart).Seconds())
	}()
	if !isSafeJobID(req.JobId) {
		taskStatus = "invalid_request"
		logger.Warn("Reject unsafe job_id")
		return &pb.TaskResponse{Message: "Invalid job_id"}, nil
	}

	// 1. Prepare Workspace
	workDir, err := s.tcMgr.Prepare(ctx, fmt.Sprint(req.ProblemId))
	if err != nil {
		if rerr := s.reportError(ctx, jobID, attemptID, traceID, "System Error", fmt.Sprintf("Prepare testcases failed: %v", err)); rerr != nil {
			return &pb.TaskResponse{Message: "Report failed"}, rerr
		}
		return &pb.TaskResponse{Message: "Failed"}, nil
	}
	logger.Info("Testcases ready", "work_dir", workDir)

	// 2. Compile
	jobDir := filepath.Join(s.config.Workspace, "code", jobID)
	if !s.config.KeepWorkdir {
		defer func() {
			if err := os.RemoveAll(jobDir); err != nil {
				logger.Warn("Cleanup failed", "error", err)
			}
		}()
	}

	codePath := filepath.Join(jobDir, "main.cpp")
	if err := SaveFile(codePath, req.Code); err != nil {
		if rerr := s.reportError(ctx, jobID, attemptID, traceID, "System Error", fmt.Sprintf("Save code failed: %v", err)); rerr != nil {
			return &pb.TaskResponse{Message: "Report failed"}, rerr
		}
		return &pb.TaskResponse{Message: "Failed"}, nil
	}

	exePath := filepath.Join(jobDir, "main")
	outDir := jobDir

	compileCtx, cancelCompile := context.WithTimeout(ctx, time.Duration(s.config.CompileTimeoutMs)*time.Millisecond)
	defer cancelCompile()

	compileStart := time.Now()
	cres, err := s.executor.Compile(compileCtx, s.config, jobID, codePath)
	workerCompileDuration.Observe(time.Since(compileStart).Seconds())
	if err != nil {
		taskStatus = "compile_error"
		logger.Warn("Compile failed", "error", err)
		if rerr := s.reportError(ctx, jobID, attemptID, traceID, "Compile Error", err.Error()); rerr != nil {
			return &pb.TaskResponse{Message: "Report failed"}, rerr
		}
		return &pb.TaskResponse{Message: "Compile Error"}, nil
	}
	if cres.Status != "Compiled" || cres.ExePath == "" {
		taskStatus = "compile_error"
		msg := cres.Error
		if msg == "" {
			msg = "compile failed"
		}
		if rerr := s.reportError(ctx, jobID, attemptID, traceID, "Compile Error", msg); rerr != nil {
			return &pb.TaskResponse{Message: "Report failed"}, rerr
		}
		return &pb.TaskResponse{Message: "Compile Error"}, nil
	}
	exePath = cres.ExePath

	// 3. Execute Loop
	inFiles, _ := filepath.Glob(filepath.Join(workDir, "*.in"))
	sort.Strings(inFiles)
	var finalStatus = "Accepted"
	var maxTime = 0
	var maxMem = 0
	checkerPath := filepath.Join(workDir, "checker")
	hasChecker := false
	if isExecutable(checkerPath) {
		logger.Warn("Custom checker detected but host execution is disabled; fallback to diff")
	}

	for _, inPath := range inFiles {
		baseName := strings.TrimSuffix(filepath.Base(inPath), ".in")
		outPath := filepath.Join(workDir, baseName+".out")
		if _, err := os.Stat(outPath); os.IsNotExist(err) {
			outPath = filepath.Join(workDir, baseName+".ans")
		}

		userOut := filepath.Join(outDir, baseName+".user.out")

		caseTimeout := time.Duration(int(req.TimeLimit)+s.config.ExecTimeoutBufferMs) * time.Millisecond
		caseCtx, cancelCase := context.WithTimeout(ctx, caseTimeout)
		res, err := s.executor.Execute(caseCtx, s.config, jobID, attemptID, traceID, exePath, inPath, userOut, int(req.TimeLimit), int(req.MemoryLimit))
		cancelCase()
		if err != nil {
			taskStatus = "system_error"
			var perr *protocolError
			if errors.As(err, &perr) {
				logger.Error(
					"Judge protocol reject",
					"reason", perr.reason,
					"expected_job_id", perr.expectedJobID,
					"expected_attempt_id", perr.expectedAttemptID,
					"actual_job_id", perr.actualJobID,
					"actual_attempt_id", perr.actualAttemptID,
				)
			}
			if rerr := s.reportError(ctx, jobID, attemptID, traceID, "System Error", err.Error()); rerr != nil {
				return &pb.TaskResponse{Message: "Report failed"}, rerr
			}
			return &pb.TaskResponse{Message: "System Error"}, nil
		}

		if res.Status == "Finished" {
			ok := false
			if hasChecker {
				ok = RunChecker(checkerPath, inPath, userOut, outPath)
			} else {
				ok = CompareFiles(userOut, outPath)
			}
			if !ok {
				res.Status = "Wrong Answer"
			} else {
				res.Status = "Accepted"
			}
		}

		if res.TimeUsed > maxTime {
			maxTime = res.TimeUsed
		}
		if res.MemoryUsed > maxMem {
			maxMem = res.MemoryUsed
		}

		if res.Status != "Accepted" {
			finalStatus = res.Status
			break
		}
	}

	// 4. Report Success
	result := map[string]interface{}{
		"job_id":      jobID,
		"attempt_id":  attemptID,
		"status":      finalStatus,
		"time_used":   maxTime,
		"memory_used": maxMem,
		"language":    req.Language.String(),
		"trace_id":    traceID,
		"cache_key":   req.CacheKey,
	}

	logJobSuccess(logger, finalStatus, maxTime, maxMem)
	if err := s.reportResult(ctx, jobID, result); err != nil {
		return &pb.TaskResponse{Message: "Report failed"}, err
	}
	taskStatus = normalizeStatus(finalStatus)

	// Cleanup sandbox workspace in C++ core
	cleanupSec := getEnvInt("CLEANUP_TIMEOUT_SEC", 5)
	if cleanupSec <= 0 {
		cleanupSec = 5
	}
	cleanupTimeout := time.Duration(cleanupSec) * time.Second
	cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancelCleanup()
	if err := s.executor.Cleanup(cleanupCtx, s.config, jobID); err != nil {
		logger.Warn("Cleanup failed", "error", err)
	}

	return &pb.TaskResponse{Message: "OK"}, nil
}

func (s *JudgeService) reportError(ctx context.Context, jobID string, attemptID int64, traceID, status, msg string) error {
	result := map[string]interface{}{
		"job_id":        jobID,
		"attempt_id":    attemptID,
		"status":        status,
		"error_message": msg,
		"trace_id":      traceID,
	}
	return s.reportResult(ctx, jobID, result)
}

func getResultTTL() time.Duration {
	ttlSec := getEnvInt("RESULT_TTL_SEC", 600)
	if ttlSec <= 0 {
		ttlSec = 600
	}
	return time.Duration(ttlSec) * time.Second
}

func (s *JudgeService) reportResult(ctx context.Context, jobID string, result map[string]interface{}) error {
	jsonBytes, _ := json.Marshal(result)

	resultKey := common.ResultKeyPrefix + jobID
	setOK := false
	if ok, err := s.redis.SetNX(ctx, resultKey, string(jsonBytes), getResultTTL()).Result(); err != nil {
		slog.Error("Redis setnx failed", "job_id", jobID, "error", err)
		return err
	} else {
		setOK = ok
		if !setOK {
			slog.Warn("Duplicate result detected, still enqueueing stream", "job_id", jobID)
		}
	}

	maxRetries := getEnvInt("RESULT_STREAM_MAX_RETRIES", 3)
	if maxRetries <= 0 {
		maxRetries = 3
	}
	backoffBaseMs := getEnvInt("RESULT_STREAM_BACKOFF_MS", 100)
	if backoffBaseMs <= 0 {
		backoffBaseMs = 100
	}
	var lastErr error
	for i := 0; i < maxRetries; i++ {
		if err := s.redis.XAdd(ctx, &redis.XAddArgs{
			Stream: common.ResultStream,
			Values: map[string]interface{}{
				"job_id": jobID,
				"result": string(jsonBytes),
			},
		}).Err(); err != nil {
			lastErr = err
			time.Sleep(time.Duration(backoffBaseMs*(1<<i)) * time.Millisecond)
			continue
		}
		lastErr = nil
		break
	}
	if lastErr != nil {
		slog.Error("Redis stream add failed", "job_id", jobID, "error", lastErr)
		if setOK {
			_ = s.redis.Del(ctx, resultKey).Err()
		}
		return lastErr
	}

	// Optional: keep PubSub for debugging/legacy
	if err := s.redis.Publish(ctx, "job_done", jobID).Err(); err != nil {
		slog.Error("Redis publish failed", "job_id", jobID, "error", err)
	}

	traceID, _ := result["trace_id"].(string)
	slog.Info("Reported result", "job_id", jobID, "trace_id", traceID, "app_status", result["status"])
	return nil
}

func (s *JudgeService) UpdateStatus(ctx context.Context, req *pb.TaskResult) (*pb.Ack, error) {
	return &pb.Ack{}, nil
}

// Helper functions
// Helper functions

func UnzipWithLimits(src, dest string, maxTotalBytes, maxFileBytes int64, maxFiles int) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()

	if err := os.MkdirAll(dest, 0755); err != nil {
		return err
	}

	var totalWritten int64
	fileCount := 0

	for _, f := range r.File {
		fpath := filepath.Join(dest, f.Name)
		if !strings.HasPrefix(fpath, filepath.Clean(dest)+string(os.PathSeparator)) {
			return fmt.Errorf("illegal file path: %s", fpath)
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(fpath, 0755); err != nil {
				return err
			}
			continue
		}

		fileCount++
		if maxFiles > 0 && fileCount > maxFiles {
			return fmt.Errorf("zip contains too many files: %d", fileCount)
		}
		if maxFileBytes > 0 && int64(f.UncompressedSize64) > maxFileBytes {
			return fmt.Errorf("zip entry too large: %s", f.Name)
		}

		if err := os.MkdirAll(filepath.Dir(fpath), 0755); err != nil {
			return err
		}

		outFile, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			return err
		}

		rc, err := f.Open()
		if err != nil {
			outFile.Close()
			return err
		}

		var reader io.Reader = rc
		if maxFileBytes > 0 {
			reader = io.LimitReader(rc, maxFileBytes+1)
		}
		written, err := io.Copy(outFile, reader)
		outFile.Close()
		rc.Close()

		if err != nil {
			return err
		}
		if maxFileBytes > 0 && written > maxFileBytes {
			return fmt.Errorf("zip entry exceeds max size: %s", f.Name)
		}

		totalWritten += written
		if maxTotalBytes > 0 && totalWritten > maxTotalBytes {
			return fmt.Errorf("zip exceeds max total size")
		}
	}
	return nil
}

// runCombinedWithContext removed: compilation is handled by C++ core sandbox

func SaveFile(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, content, 0644)
}

func CompareFiles(file1, file2 string) bool {
	f1, err := os.ReadFile(file1)
	if err != nil {
		return false
	}
	f2, err := os.ReadFile(file2)
	if err != nil {
		return false
	}

	s1 := trim(string(f1))
	s2 := trim(string(f2))

	return s1 == s2
}

func RunChecker(checkerPath, inputPath, userOut, answerPath string) bool {
	checkerTimeoutMs := getEnvInt("CHECKER_TIMEOUT_MS", 2000)
	if checkerTimeoutMs <= 0 {
		checkerTimeoutMs = 2000
	}
	checkerTimeout := time.Duration(checkerTimeoutMs) * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), checkerTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, checkerPath, inputPath, userOut, answerPath)
	if err := cmd.Run(); err != nil {
		return false
	}
	return true
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Mode()&0111 != 0
}

func trim(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
}

func normalizeStatus(status string) string {
	switch strings.ToLower(status) {
	case "accepted":
		return "accepted"
	case "wrong answer":
		return "wrong_answer"
	case "compile error":
		return "compile_error"
	case "time limit exceeded":
		return "time_limit_exceeded"
	case "memory limit exceeded":
		return "memory_limit_exceeded"
	case "output limit exceeded":
		return "output_limit_exceeded"
	case "runtime error":
		return "runtime_error"
	default:
		return "system_error"
	}
}

func isSafeJobID(jobID string) bool {
	return safeJobIDRegex.MatchString(jobID)
}
