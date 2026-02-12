/**
 * @file handler.go
 * @brief API 请求处理器
 */
package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog" // [New] Structured Logging
	"net/http"
	"time"

	"github.com/d1guo/deep_oj/internal/model"
	"github.com/d1guo/deep_oj/internal/repository"
	pb "github.com/d1guo/deep_oj/pkg/proto"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

// Handler 处理 API 请求
type Handler struct {
	db    *repository.PostgresDB
	redis *repository.RedisClient
}

// NewHandler 创建新的 Handler
func NewHandler(db *repository.PostgresDB, redis *repository.RedisClient) *Handler {
	return &Handler{db: db, redis: redis}
}

// SubmitRequest 提交请求结构
type SubmitRequest struct {
	Code        string `json:"code" binding:"required"`
	Language    int    `json:"language" binding:"required"` // 1=C++, 2=C, 3=Python...
	TimeLimit   int    `json:"time_limit"`                  // ms
	MemoryLimit int    `json:"memory_limit"`                // KB
	ProblemID   int    `json:"problem_id"`                  // 题目 ID (可选)
}

// SubmitResponse 提交响应结构
type SubmitResponse struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
	Cached bool   `json:"cached,omitempty"`
}

// RateLimit 限流 (简单固定窗口)
// 面试知识点: 常见的限流算法有哪些?
// 1. 固定窗口 (Fixed Window) - 实现简单，边界有突发流量
// 2. 滑动窗口 (Sliding Window) - 平滑，精度高
// 3. 漏桶 (Leaky Bucket) - 恒定流出速率 (平滑突发流量)
// 4. 令牌桶 (Token Bucket) - 允许突发流量 (Guava RateLimiter)
func (h *Handler) checkRateLimit(ctx *gin.Context, ip string) bool {
	key := "rate_limit:" + ip
	limit := 60 // 每分钟 60 次
	
	// 使用 Redis INCR 和 EXPIRE 原子操作
	count, err := h.redis.Incr(ctx, key)
	if err != nil {
		slog.Error("RateLimit Redis Error", "error", err)
		return true // 降级: 放行
	}
	
	if count == 1 {
		h.redis.Expire(ctx, key, 60*time.Second)
	}
	
	if count > int64(limit) {
		return false
	}
	return true
}

// HandleSubmit 处理代码提交
func (h *Handler) HandleSubmit(c *gin.Context) {
	start := time.Now()
	path := c.FullPath()
	method := c.Request.Method
		
	// [New] Structured Logging (Start)
	reqID := c.GetHeader("X-Request-ID")
	logger := slog.With("request_id", reqID, "ip", c.ClientIP(), "path", path)
	
	logger.Info("HandleSubmit 开始处理")

	// =========================================================================
	// 0. Rate Limiting
	// =========================================================================
	if !h.checkRateLimit(c, c.ClientIP()) {
		logger.Warn("访问频率超限")
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "请求过于频繁"})
		RequestTotal.WithLabelValues(method, path, "429").Inc()
		return
	}

	var req SubmitRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		logger.Warn("请求体格式非法", "error", err)
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "请求格式非法",
			"details": err.Error(),
		})
		RequestTotal.WithLabelValues(method, path, "400").Inc()
		return
	}

	// 0.5 输入验证 (防御性编程)
	if len(req.Code) > 65536 { // 64KB 限制
		c.JSON(http.StatusBadRequest, gin.H{"error": "代码过长 (最大 64KB)"})
		return
	}
	if req.Language < 1 || req.Language > 4 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "不支持的语言"})
		return
	}
	
	// 设置默认值 & 限制最大值
	if req.TimeLimit <= 0 {
		req.TimeLimit = 1000 // 1 秒
	} else if req.TimeLimit > 10000 {
		req.TimeLimit = 10000 // 最大 10s
	}
	
	if req.MemoryLimit <= 0 {
		req.MemoryLimit = 65536 // 64 MB
	} else if req.MemoryLimit > 524288 { // 最大 512MB
		req.MemoryLimit = 524288
	}

	// =========================================================================
	// 1. 生成缓存 Key (去重)
	// =========================================================================
	cacheKey := generateCacheKey(req.Code, req.Language, req.TimeLimit, req.MemoryLimit)

	// =========================================================================
	// 2. 检查缓存 (Cache Aside 模式 - 读)
	// =========================================================================
	ctx := c.Request.Context()
	cached, err := h.redis.Get(ctx, cacheKey)
	if err == nil && cached != "" {
		logger.Info("命中缓存", "cache_key", cacheKey)
		c.JSON(http.StatusOK, gin.H{
			"job_id": "cached",
			"status": "Finished",
			"data":   cached,
			"cached": true,
		})
		
		// Metrics
		RequestTotal.WithLabelValues(method, path, "200").Inc()
		RequestDuration.WithLabelValues(method, path, "200").Observe(time.Since(start).Seconds())
		return
	}

	// =========================================================================
	// 3. 生成 Job ID (UUID v4)
	// =========================================================================
	jobID := uuid.New().String()
	logger = logger.With("job_id", jobID) // 将 job_id 注入日志上下文

	// =========================================================================
	// 4. 写入 PostgreSQL (状态: Pending)
	// =========================================================================
	submission := &model.Submission{
		JobID:       jobID,
		Code:        req.Code,
		Language:    req.Language,
		TimeLimit:   req.TimeLimit,
		MemoryLimit: req.MemoryLimit,
		ProblemID:   req.ProblemID,
		Status:      "pending",
	}

	// [New] 获取当前登录用户 ID
	if uid, exists := c.Get("user_id"); exists {
		submission.UserID = int(uid.(int64))
		logger = logger.With("user_id", submission.UserID)
	}

	if err := h.db.CreateSubmission(ctx, submission); err != nil {
		logger.Error("数据库创建失败", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "数据库错误",
		})
		
		SubmissionTotal.WithLabelValues(fmt.Sprintf("%d", req.Language), "error").Inc()
		return
	}

	// =========================================================================
	// 5. 序列化并推送到 Redis 队列
	// =========================================================================
	task := &pb.TaskRequest{
		JobId:       jobID,
		Code:        []byte(req.Code),
		Language:    pb.Language(req.Language),
		TimeLimit:   int32(req.TimeLimit),
		MemoryLimit: int32(req.MemoryLimit),
		CacheKey:    cacheKey,
		ProblemId:   uint32(req.ProblemID),
		SubmitTime:  time.Now().UnixMilli(),
	}

	taskData, err := proto.Marshal(task)
	if err != nil {
		logger.Error("Proto 序列化错误", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "序列化错误"})
		return
	}
	if err := h.redis.LPush(ctx, "queue:pending", string(taskData)); err != nil {
		logger.Error("Redis 推送错误", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "队列错误",
		})
		return
	}

	logger.Info("任务提交成功")

	// metrics
	SubmissionTotal.WithLabelValues(fmt.Sprintf("%d", req.Language), "submitted").Inc()
	RequestTotal.WithLabelValues(method, path, "200").Inc()
	RequestDuration.WithLabelValues(method, path, "200").Observe(time.Since(start).Seconds())

	// =========================================================================
	// 6. 返回 Job ID
	// =========================================================================
	c.JSON(http.StatusOK, SubmitResponse{
		JobID:  jobID,
		Status: "Queuing",
	})
}

// HandleStatus 查询判题状态
func (h *Handler) HandleStatus(c *gin.Context) {
	jobID := c.Param("job_id")

	if jobID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少 job_id"})
		return
	}

	ctx := c.Request.Context()

	// =========================================================================
	// 1. 先查 Redis 缓存 (热点数据)
	// =========================================================================
	resultKey := "result:" + jobID
	result, err := h.redis.Get(ctx, resultKey)
	if err == nil && result != "" {
		var data map[string]interface{}
		json.Unmarshal([]byte(result), &data)
		c.JSON(http.StatusOK, gin.H{
			"job_id": jobID,
			"status": "Finished",
			"data":   data,
		})
		return
	}

	// =========================================================================
	// 2. 查 PostgreSQL (持久化数据)
	// =========================================================================
	submission, err := h.db.GetSubmission(ctx, jobID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "找不到提交记录"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"job_id": jobID,
		"status": submission.Status,
		"data":   submission.Result,
	})
}

// generateCacheKey 生成缓存指纹
// 相同代码 + 相同限制 = 相同结果
func generateCacheKey(code string, lang, timeLimit, memLimit int) string {
	data := fmt.Sprintf("%s|%d|%d|%d", code, lang, timeLimit, memLimit)
	hash := sha256.Sum256([]byte(data))
	return "oj:cache:" + hex.EncodeToString(hash[:])
}
