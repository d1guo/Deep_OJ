/**
 * @file main.go
 * @brief Go API Server 入口
 *
 * 架构定位: I/O 密集层
 * 技术选型: Gin Framework + PostgreSQL + Redis
 *
 * ===========================================================================
 * 面试八股知识点
 * ===========================================================================
 *
 * 1. 为什么选择 Gin?
 *    - 高性能: 基于 httprouter，性能是 net/http 的 40 倍
 *    - 中间件: 支持链式中间件 (Logger, Recovery, CORS, Auth)
 *    - 开发效率: JSON 绑定、验证、渲染开箱即用
 *
 * 2. Goroutine 的优势 (相比 C++ 线程):
 *    - 轻量: 2KB 栈空间 vs 1MB+ 线程栈
 *    - 调度: M:N 调度 (GMP 模型)，用户态切换成本低
 *    - 并发: 可以轻松创建数十万 Goroutine
 *
 * 3. Context 的作用:
 *    - 超时控制: WithTimeout, WithDeadline
 *    - 取消传播: 父 Context 取消，所有子 Context 都会收到通知
 *    - 值传递: 请求范围的数据 (RequestID, UserID)
 *
 * 4. GMP 调度模型:
 *    - G (Goroutine): 用户级协程
 *    - M (Machine): 操作系统线程
 *    - P (Processor): 调度上下文，持有本地运行队列
 *    - M:N 关系: M 个 G 运行在 N 个 OS 线程上
 */
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/d1guo/deep_oj/internal/appconfig"
	"github.com/d1guo/deep_oj/internal/api"
	"github.com/d1guo/deep_oj/internal/repository"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func getEnvInt(key string, fallback int) int {
	if value, ok := os.LookupEnv(key); ok {
		if i, err := strconv.Atoi(value); err == nil && i > 0 {
			return i
		}
	}
	return fallback
}

func main() {
	cfg, cfgPath, err := appconfig.Load()
	if err != nil {
		slog.Error("Failed to load config", "path", cfgPath, "error", err)
		os.Exit(1)
	}
	if cfgPath != "" {
		slog.Info("Loaded config", "path", cfgPath)
	}
	if cfg != nil {
		appconfig.SetEnvIfEmptyInt("REDIS_POOL_SIZE", cfg.Redis.PoolSize)
		appconfig.SetEnvIfEmptyInt("REDIS_MIN_IDLE_CONNS", cfg.Redis.MinIdleConns)
		appconfig.SetEnvIfEmptyInt("REDIS_DIAL_TIMEOUT_MS", cfg.Redis.DialTimeoutMs)
		appconfig.SetEnvIfEmptyInt("REDIS_READ_TIMEOUT_MS", cfg.Redis.ReadTimeoutMs)
		appconfig.SetEnvIfEmptyInt("REDIS_WRITE_TIMEOUT_MS", cfg.Redis.WriteTimeoutMs)
		appconfig.SetEnvIfEmptyInt("PG_MAX_CONNS", cfg.Postgres.MaxConns)
		appconfig.SetEnvIfEmptyInt("PG_MIN_CONNS", cfg.Postgres.MinConns)
		appconfig.SetEnvIfEmptyInt("PG_MAX_CONN_LIFETIME_MIN", cfg.Postgres.MaxConnLifetimeMin)
		appconfig.SetEnvIfEmptyInt("PG_MAX_CONN_IDLE_MIN", cfg.Postgres.MaxConnIdleMin)

		appconfig.SetEnvIfEmptyInt("PORT", cfg.API.Port)
		appconfig.SetEnvIfEmpty("GIN_MODE", cfg.API.GinMode)
		appconfig.SetEnvIfEmpty("DATABASE_URL", cfg.API.DatabaseURL)
		appconfig.SetEnvIfEmpty("REDIS_URL", cfg.API.RedisURL)
		appconfig.SetEnvIfEmpty("MINIO_ENDPOINT", cfg.API.MinIO.Endpoint)
		appconfig.SetEnvIfEmpty("MINIO_ACCESS_KEY", cfg.API.MinIO.AccessKey)
		appconfig.SetEnvIfEmpty("MINIO_SECRET_KEY", cfg.API.MinIO.SecretKey)
		appconfig.SetEnvIfEmpty("MINIO_BUCKET", cfg.API.MinIO.Bucket)

		if len(cfg.API.Auth.AdminUsers) > 0 {
			appconfig.SetEnvIfEmpty("ADMIN_USERS", strings.Join(cfg.API.Auth.AdminUsers, ","))
		}
		appconfig.SetEnvIfEmpty("JWT_SECRET", cfg.API.Auth.JWTSecret)
		appconfig.SetEnvIfEmptyInt("JWT_EXPIRE_HOURS", cfg.API.Auth.JWTExpireHours)
		appconfig.SetEnvIfEmptyInt("OAUTH_STATE_TTL_SEC", cfg.API.Auth.OAuthStateTTLSeconds)
		appconfig.SetEnvIfEmpty("GITHUB_CLIENT_ID", cfg.API.Auth.OAuth.GitHub.ClientID)
		appconfig.SetEnvIfEmpty("GITHUB_CLIENT_SECRET", cfg.API.Auth.OAuth.GitHub.ClientSecret)
		appconfig.SetEnvIfEmpty("GITHUB_REDIRECT_URL", cfg.API.Auth.OAuth.GitHub.RedirectURL)
		if cfg.API.Limits.SubmitBodyMaxBytes > 0 {
			appconfig.SetEnvIfEmpty("SUBMIT_BODY_MAX_BYTES", fmt.Sprintf("%d", cfg.API.Limits.SubmitBodyMaxBytes))
		}
		if cfg.API.Limits.ProblemZipMaxBytes > 0 {
			appconfig.SetEnvIfEmpty("PROBLEM_ZIP_MAX_BYTES", fmt.Sprintf("%d", cfg.API.Limits.ProblemZipMaxBytes))
		}
		if cfg.API.Limits.SubmitCodeMaxBytes > 0 {
			appconfig.SetEnvIfEmpty("SUBMIT_CODE_MAX_BYTES", fmt.Sprintf("%d", cfg.API.Limits.SubmitCodeMaxBytes))
		}
		appconfig.SetEnvIfEmptyInt("SUBMIT_DEFAULT_TIME_LIMIT_MS", cfg.API.Limits.DefaultTimeLimitMs)
		appconfig.SetEnvIfEmptyInt("SUBMIT_MAX_TIME_LIMIT_MS", cfg.API.Limits.MaxTimeLimitMs)
		appconfig.SetEnvIfEmptyInt("SUBMIT_DEFAULT_MEMORY_LIMIT_KB", cfg.API.Limits.DefaultMemoryLimitKb)
		appconfig.SetEnvIfEmptyInt("SUBMIT_MAX_MEMORY_LIMIT_KB", cfg.API.Limits.MaxMemoryLimitKb)
		appconfig.SetEnvIfEmptyInt("SUBMIT_INFLIGHT_TTL_SEC", cfg.API.Limits.InflightTTLSec)
		appconfig.SetEnvIfEmptyInt("RATE_LIMIT_IP_PER_WINDOW", cfg.API.Limits.RateLimit.IPLimit)
		appconfig.SetEnvIfEmptyInt("RATE_LIMIT_USER_PER_WINDOW", cfg.API.Limits.RateLimit.UserLimit)
		appconfig.SetEnvIfEmptyInt("RATE_LIMIT_WINDOW_SEC", cfg.API.Limits.RateLimit.WindowSec)
		appconfig.SetEnvIfEmptyInt("PROBLEM_DEFAULT_TIME_LIMIT_MS", cfg.API.Limits.ProblemDefaults.TimeLimitMs)
		appconfig.SetEnvIfEmptyInt("PROBLEM_DEFAULT_MEMORY_LIMIT_MB", cfg.API.Limits.ProblemDefaults.MemoryLimitMB)
		appconfig.SetEnvIfEmptyInt("API_SHUTDOWN_TIMEOUT_SEC", cfg.API.ShutdownTimeoutSec)

		appconfig.SetEnvIfEmpty("SERVICE_NAME", cfg.API.Metrics.ServiceName)
		appconfig.SetEnvIfEmpty("INSTANCE_ID", cfg.API.Metrics.InstanceID)
	}

	// =========================================================================
	// 1. 初始化配置 (从环境变量读取)
	// =========================================================================
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://deep_oj:secret@localhost:5432/deep_oj?sslmode=disable"
	}

	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		redisURL = "localhost:6379"
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// =========================================================================
	// 2. 初始化数据库连接
	// =========================================================================
	ctx := context.Background()

	db, err := repository.NewPostgresDB(ctx, dbURL)
	if err != nil {
		slog.Error("Failed to connect to PostgreSQL", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	slog.Info("Connected to PostgreSQL")

	// =========================================================================
	// 3. 初始化 Redis 连接
	// =========================================================================
	redisClient := repository.NewRedisClient(redisURL)
	if err := redisClient.Ping(ctx); err != nil {
		slog.Error("Failed to connect to Redis", "error", err)
		os.Exit(1)
	}
	slog.Info("Connected to Redis")

	// =========================================================================
	// 4. 创建 Handler 和路由
	// =========================================================================

	// =========================================================================
	// 3.5 初始化 MinIO 连接
	// =========================================================================
	minioEndpoint := os.Getenv("MINIO_ENDPOINT")
	if minioEndpoint == "" {
		minioEndpoint = "localhost:9000"
	}
	minioAccessKey := os.Getenv("MINIO_ACCESS_KEY")
	if minioAccessKey == "" {
		minioAccessKey = "minioadmin"
	}
	minioSecretKey := os.Getenv("MINIO_SECRET_KEY")
	if minioSecretKey == "" {
		minioSecretKey = "minioadmin"
	}
	minioBucket := os.Getenv("MINIO_BUCKET")
	if minioBucket == "" {
		minioBucket = "deep-oj-problems"
	}

	minioClient, err := repository.NewMinIOClient(minioEndpoint, minioAccessKey, minioSecretKey, minioBucket)
	if err != nil {
		slog.Error("Failed to connect to MinIO", "error", err)
		os.Exit(1)
	}
	slog.Info("Connected to MinIO")

	// =========================================================================
	// 4. 创建 Handler 和路由
	// =========================================================================
	handler := api.NewHandler(db, redisClient, minioClient)

	// Gin 模式设置
	if os.Getenv("GIN_MODE") == "release" {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.Default()

	// 中间件
	r.Use(api.CORSMiddleware())
	r.Use(api.RequestIDMiddleware())
	r.Use(api.MetricsMiddleware()) // [Task 3.3] Metrics

	// Metrics endpoint
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))

	// API 路由
	v1 := r.Group("/api/v1")
	{
		// 认证接口
		auth := v1.Group("/auth")
		{
			auth.POST("/register", handler.Register)
			auth.POST("/login", handler.Login)

			// OAuth 2.0 (GitHub)
			auth.GET("/github/login", handler.HandleGitHubLogin)
			auth.GET("/github/callback", handler.HandleGitHubCallback)
		}

		// 题目管理 (管理员) -> 暂时不加鉴权，或者复用 AuthMiddleware
		// 实际上应该有 AdminMiddleware，这里暂用 AuthMiddleware
		problems := v1.Group("/problems")
		problems.Use(api.AuthMiddleware(), api.AdminMiddleware())
		{
			problems.POST("", handler.HandleCreateProblem)
			problems.DELETE("/:id", handler.HandleDeleteProblem)
		}

		// 提交代码 (需认证)
		v1.POST("/submit", api.AuthMiddleware(), handler.HandleSubmit)

		// 查询状态 (仅本人/管理员)
		v1.GET("/status/:job_id", api.AuthMiddleware(), handler.HandleStatus)

		// 健康检查
		v1.GET("/health", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"status": "ok"})
		})
	}

	// =========================================================================
	// 5. 优雅关闭 (Graceful Shutdown)
	// =========================================================================
	srv := &http.Server{
		Addr:    ":" + port,
		Handler: r,
	}

	// 启动服务器 (在 Goroutine 中)
	go func() {
		slog.Info("API Server starting", "port", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("Server error", "error", err)
			os.Exit(1)
		}
	}()

	// 等待中断信号
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("Shutting down server...")

	// 给请求 5 秒时间完成
	shutdownSec := getEnvInt("API_SHUTDOWN_TIMEOUT_SEC", 5)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(shutdownSec)*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("Server forced to shutdown", "error", err)
		os.Exit(1)
	}

	slog.Info("Server exited")
}
