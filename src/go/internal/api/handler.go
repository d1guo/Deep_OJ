/**
 * @file handler.go
 * @brief API 请求处理器
 */
package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog" // [New] Structured Logging
	"net/http"
	"time"

	"github.com/d1guo/deep_oj/internal/model"
	"github.com/d1guo/deep_oj/internal/repository"
	"github.com/d1guo/deep_oj/pkg/common"
	pb "github.com/d1guo/deep_oj/pkg/proto"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Handler 处理 API 请求
type Handler struct {
	db    *repository.PostgresDB
	redis *repository.RedisClient
	minio *repository.MinIOClient // [New] MinIO Client
}

// NewHandler 创建新的 Handler
func NewHandler(db *repository.PostgresDB, redis *repository.RedisClient, minio *repository.MinIOClient) *Handler {
	return &Handler{db: db, redis: redis, minio: minio}
}

type submitPersistenceStore interface {
	CreateSubmission(ctx context.Context, s *model.Submission) error
	CreateSubmissionAndOutbox(ctx context.Context, submission *model.Submission, event *repository.OutboxEvent) error
}

// SubmitRequest 提交请求结构
type SubmitRequest struct {
	Code        string `json:"code" binding:"required"`
	Language    int    `json:"language" binding:"required"` // 当前仅支持 1=C++
	TimeLimit   int    `json:"time_limit"`                  // ms
	MemoryLimit int    `json:"memory_limit"`                // KB
	ProblemID   int    `json:"problem_id"`                  // 题目 ID (可选)
}

const defaultMaxSubmitBodyBytes int64 = 1 << 20 // 1MB

type rateLimitConfig struct {
	ipLimit   int
	userLimit int
	window    time.Duration
}

func getRateLimitConfig() rateLimitConfig {
	windowSec := getEnvInt("RATE_LIMIT_WINDOW_SEC", 60)
	return rateLimitConfig{
		ipLimit:   getEnvInt("RATE_LIMIT_IP_PER_WINDOW", 60),
		userLimit: getEnvInt("RATE_LIMIT_USER_PER_WINDOW", 120),
		window:    time.Duration(windowSec) * time.Second,
	}
}

// SubmitResponse 提交响应结构
type SubmitResponse struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
	Cached bool   `json:"cached,omitempty"`
}

// RateLimit 限流 (简单固定窗口)
func (h *Handler) checkRateLimit(ctx *gin.Context, ip string, userID int) bool {
	// 简化版限流: IP + 用户双维度固定窗口
	cfg := getRateLimitConfig()
	if !h.consumeRateLimit(ctx, "rate_limit:ip:"+ip, cfg.ipLimit, cfg.window) {
		return false
	}
	if userID > 0 {
		if !h.consumeRateLimit(ctx, fmt.Sprintf("rate_limit:user:%d", userID), cfg.userLimit, cfg.window) {
			return false
		}
	}
	return true
}

func (h *Handler) consumeRateLimit(ctx *gin.Context, key string, limit int, window time.Duration) bool {
	count, err := h.redis.Incr(ctx, key)
	if err != nil {
		slog.Error("RateLimit Redis Error", "error", err)
		return true // 降级: 放行
	}
	if count == 1 {
		h.redis.Expire(ctx, key, window)
	}
	return count <= int64(limit)
}

// HandleSubmit 处理代码提交
func (h *Handler) HandleSubmit(c *gin.Context) {
	start := time.Now()
	path := c.FullPath()
	method := c.Request.Method
	ctx := c.Request.Context()

	// [New] Structured Logging (Start)
	reqID := GetRequestID(c)
	logger := slog.With("request_id", reqID, "trace_id", reqID, "ip", c.ClientIP(), "path", path)

	logger.Info("HandleSubmit 开始处理")

	// 0. Rate Limiting
	userID := 0
	if uid, exists := c.Get("user_id"); exists {
		userID = int(uid.(int64))
	}
	if !h.checkRateLimit(c, c.ClientIP(), userID) {
		logger.Warn("访问频率超限")
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "请求过于频繁", "code": "RATE_LIMITED"})
		RequestTotal.WithLabelValues(method, path, "429").Inc()
		return
	}

	// Limit request body size to prevent JSON DoS
	maxBodyBytes := getEnvInt64("SUBMIT_BODY_MAX_BYTES", defaultMaxSubmitBodyBytes)
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBodyBytes)

	var req SubmitRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "请求体过大", "code": "BODY_TOO_LARGE"})
			RequestTotal.WithLabelValues(method, path, "413").Inc()
			return
		}
		logger.Warn("请求体格式非法", "error", err)
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "请求格式非法",
			"details": err.Error(),
			"code":    "BAD_REQUEST",
		})
		RequestTotal.WithLabelValues(method, path, "400").Inc()
		return
	}

	// 0.5 输入验证 (防御性编程)
	maxCodeBytes := getEnvInt("SUBMIT_CODE_MAX_BYTES", 65536)
	if len(req.Code) > maxCodeBytes {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("代码过长 (最大 %d 字节)", maxCodeBytes), "code": "INVALID_PARAM"})
		return
	}
	if req.Language != int(pb.Language_CPP) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "当前仅支持 C++ (language=1)", "code": "INVALID_PARAM"})
		return
	}

	// 设置默认值 & 限制最大值
	defaultTimeLimit := getEnvInt("SUBMIT_DEFAULT_TIME_LIMIT_MS", 1000)
	maxTimeLimit := getEnvInt("SUBMIT_MAX_TIME_LIMIT_MS", 10000)
	if req.TimeLimit <= 0 {
		req.TimeLimit = defaultTimeLimit
	} else if req.TimeLimit > maxTimeLimit {
		req.TimeLimit = maxTimeLimit
	}

	defaultMemLimit := getEnvInt("SUBMIT_DEFAULT_MEMORY_LIMIT_KB", 65536)
	maxMemLimit := getEnvInt("SUBMIT_MAX_MEMORY_LIMIT_KB", 524288)
	if req.MemoryLimit <= 0 {
		req.MemoryLimit = defaultMemLimit
	} else if req.MemoryLimit > maxMemLimit {
		req.MemoryLimit = maxMemLimit
	}

	// 1. 生成缓存 Key (去重)
	testcaseHash := ""
	if req.ProblemID > 0 {
		problem, err := h.db.GetProblem(ctx, req.ProblemID)
		if err != nil {
			logger.Warn("Problem not found", "problem_id", req.ProblemID, "error", err)
			c.JSON(http.StatusBadRequest, gin.H{"error": "题目不存在", "code": "PROBLEM_NOT_FOUND"})
			return
		}
		testcaseHash = problem.TestcaseHash
	}
	cacheKey := generateCacheKey(req.Code, req.Language, req.TimeLimit, req.MemoryLimit, req.ProblemID, testcaseHash)

	// 2. 检查缓存 (Cache Aside 模式 - 读)
	cached, err := h.redis.Get(ctx, cacheKey)
	if err == nil && cached != "" {
		logger.Info("命中缓存", "cache_key", cacheKey)
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(cached), &data); err != nil {
			logger.Warn("缓存反序列化失败，降级为非缓存流程", "error", err)
			_ = h.redis.Del(ctx, cacheKey)
		} else {
			// Create a submission record for cache hit to keep history consistent
			jobID := uuid.New().String()
			submission := &model.Submission{
				JobID:       jobID,
				Code:        req.Code,
				Language:    req.Language,
				TimeLimit:   req.TimeLimit,
				MemoryLimit: req.MemoryLimit,
				ProblemID:   req.ProblemID,
				Status:      "pending",
				State:       "pending",
			}
			if uid, exists := c.Get("user_id"); exists {
				submission.UserID = uid.(int64)
			}
			if err := h.db.CreateSubmission(ctx, submission); err != nil {
				logger.Error("数据库创建失败(缓存命中)", "error", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "数据库错误", "code": "DB_ERROR"})
				return
			}

			status := "Finished"
			if s, ok := data["status"].(string); ok && s != "" {
				status = s
			}
			_, _, _ = h.db.UpdateSubmissionResultIfNotDone(ctx, jobID, status, data)

			c.JSON(http.StatusOK, gin.H{
				"job_id": jobID,
				"status": "Finished",
				"state":  "done",
				"data":   data,
				"cached": true,
			})

			// Metrics
			RequestTotal.WithLabelValues(method, path, "200").Inc()
			RequestDuration.WithLabelValues(method, path, "200").Observe(time.Since(start).Seconds())
			return
		}

	}

	// 3. 生成 Job ID (UUID v4)
	jobID := uuid.New().String()
	logger = logger.With("job_id", jobID) // 将 job_id 注入日志上下文

	// 去重：相同 cache_key 已在处理中，直接返回已有 job_id
	inflightKey := common.InFlightKeyPrefix + cacheKey
	inflightTTLSec := getEnvInt("SUBMIT_INFLIGHT_TTL_SEC", 600)
	if ok, err := h.redis.SetNX(ctx, inflightKey, jobID, time.Duration(inflightTTLSec)*time.Second); err != nil {
		logger.Warn("Inflight SetNX failed", "error", err)
	} else if !ok {
		if existing, err := h.redis.Get(ctx, inflightKey); err == nil && existing != "" {
			c.JSON(http.StatusOK, SubmitResponse{
				JobID:  existing,
				Status: "Queuing",
			})
			return
		}
	}

	// 4. 构建提交对象与任务载荷
	submission := &model.Submission{
		JobID:       jobID,
		Code:        req.Code,
		Language:    req.Language,
		TimeLimit:   req.TimeLimit,
		MemoryLimit: req.MemoryLimit,
		ProblemID:   req.ProblemID,
		Status:      "pending",
		State:       "pending",
	}

	// [New] 获取当前登录用户 ID
	if uid, exists := c.Get("user_id"); exists {
		submission.UserID = uid.(int64)
		logger = logger.With("user_id", submission.UserID)
	}

	traceID := reqID
	if traceID == "" {
		traceID = jobID
	}
	enqueueTS := time.Now().UnixMilli()
	task := &pb.TaskRequest{
		JobId:       jobID,
		Code:        []byte(req.Code),
		Language:    pb.Language(req.Language),
		TimeLimit:   int32(req.TimeLimit),
		MemoryLimit: int32(req.MemoryLimit),
		CacheKey:    cacheKey,
		ProblemId:   uint32(req.ProblemID),
		SubmitTime:  enqueueTS,
		TraceId:     traceID,
	}

	taskData, err := json.Marshal(task)
	if err != nil {
		logger.Error("任务 JSON 序列化错误", "error", err)
		_ = h.redis.Del(ctx, inflightKey)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "序列化错误", "code": "INTERNAL_ERROR"})
		return
	}

	outboxEnabled := getEnvBool("OUTBOX_ENABLED", true)
	if outboxEnabled {
		streamKey := getEnvString("JOB_STREAM_KEY", defaultJobStreamKey)
		outboxEvent := &repository.OutboxEvent{
			EventType: repository.OutboxEventTypeSubmissionEnqueue,
			JobID:     jobID,
			StreamKey: streamKey,
			EnqueueTS: enqueueTS,
			Priority:  0,
			Payload:   taskData,
			Status:    repository.OutboxStatusPending,
		}
		if err := persistSubmissionWithDeliveryMode(ctx, h.db, true, submission, outboxEvent, nil); err != nil {
			logger.Error("数据库创建失败(outbox)", "error", err)
			_ = h.redis.Del(ctx, inflightKey)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "数据库错误",
				"code":  "DB_ERROR",
			})
			SubmissionTotal.WithLabelValues(fmt.Sprintf("%d", req.Language), "error").Inc()
			return
		}
		logger.Info("任务提交成功(outbox)", "trace_id", traceID, "outbox_id", outboxEvent.ID)
	} else {
		enqueueFn := func() error {
			streamEntryID, ok := enqueueJobToStreamOrReply5xx(c, h.redis, logger, jobID, traceID, inflightKey, taskData)
			if !ok {
				return fmt.Errorf("stream enqueue failed")
			}
			logger.Info("任务提交成功", "stream_entry_id", streamEntryID)
			return nil
		}
		if err := persistSubmissionWithDeliveryMode(ctx, h.db, false, submission, nil, enqueueFn); err != nil {
			logger.Error("提交失败(直投递模式)", "error", err)
			if !c.Writer.Written() {
				_ = h.redis.Del(ctx, inflightKey)
				c.JSON(http.StatusInternalServerError, gin.H{
					"error": "数据库错误",
					"code":  "DB_ERROR",
				})
			}
			SubmissionTotal.WithLabelValues(fmt.Sprintf("%d", req.Language), "error").Inc()
			return
		}
	}

	// metrics
	SubmissionTotal.WithLabelValues(fmt.Sprintf("%d", req.Language), "submitted").Inc()
	RequestTotal.WithLabelValues(method, path, "200").Inc()
	RequestDuration.WithLabelValues(method, path, "200").Observe(time.Since(start).Seconds())

	// 6. 返回 Job ID
	c.JSON(http.StatusOK, SubmitResponse{
		JobID:  jobID,
		Status: "Queuing",
	})
}

func persistSubmissionWithDeliveryMode(
	ctx context.Context,
	store submitPersistenceStore,
	outboxEnabled bool,
	submission *model.Submission,
	event *repository.OutboxEvent,
	directEnqueue func() error,
) error {
	if outboxEnabled {
		return store.CreateSubmissionAndOutbox(ctx, submission, event)
	}
	if err := store.CreateSubmission(ctx, submission); err != nil {
		return err
	}
	if directEnqueue != nil {
		return directEnqueue()
	}
	return nil
}

// HandleStatus 查询判题状态
func (h *Handler) HandleStatus(c *gin.Context) {
	jobID := c.Param("job_id")

	if jobID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少 job_id", "code": "BAD_REQUEST"})
		return
	}

	ctx := c.Request.Context()
	userIDAny, userExists := c.Get("user_id")
	usernameAny, _ := c.Get("username")

	// 1. 先查 PostgreSQL (权限校验)
	submission, err := h.db.GetSubmission(ctx, jobID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "找不到提交记录", "code": "NOT_FOUND"})
		return
	}

	// 权限校验: 仅本人或管理员可查
	if !userExists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未登录", "code": "UNAUTHORIZED"})
		return
	}
	userID := userIDAny.(int64)
	username, _ := usernameAny.(string)
	if !IsAdminUsername(username) && submission.UserID != userID {
		c.JSON(http.StatusForbidden, gin.H{"error": "无权限", "code": "FORBIDDEN"})
		return
	}

	// 2. 再查 Redis 缓存 (热点数据)
	resultKey := common.ResultKeyPrefix + jobID
	result, err := h.redis.Get(ctx, resultKey)
	if err == nil && result != "" {
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(result), &data); err != nil {
			slog.Warn("缓存反序列化失败，回退到 DB", "job_id", jobID, "error", err)
			_ = h.redis.Del(ctx, resultKey)
		} else {
			c.JSON(http.StatusOK, gin.H{
				"job_id": jobID,
				"status": "Finished",
				"state":  "done",
				"data":   data,
			})
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"job_id": jobID,
		"status": submission.Status,
		"state":  submission.State,
		"data":   submission.Result,
	})
}

// generateCacheKey 生成缓存指纹
// 相同代码 + 相同限制 = 相同结果
func generateCacheKey(code string, lang, timeLimit, memLimit, problemID int, testcaseHash string) string {
	data := fmt.Sprintf("%s|%d|%d|%d|%d|%s", code, lang, timeLimit, memLimit, problemID, testcaseHash)
	hash := sha256.Sum256([]byte(data))
	return common.CacheKeyPrefix + hex.EncodeToString(hash[:])
}
