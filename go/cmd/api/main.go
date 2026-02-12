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
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/d1guo/deep_oj/internal/api"
	"github.com/d1guo/deep_oj/internal/repository"
)

func main() {
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
		log.Fatalf("❌ Failed to connect to PostgreSQL: %v", err)
	}
	defer db.Close()
	log.Println("✅ Connected to PostgreSQL")

	// =========================================================================
	// 3. 初始化 Redis 连接
	// =========================================================================
	redisClient := repository.NewRedisClient(redisURL)
	if err := redisClient.Ping(ctx); err != nil {
		log.Fatalf("❌ Failed to connect to Redis: %v", err)
	}
	log.Println("✅ Connected to Redis")

	// =========================================================================
	// 4. 创建 Handler 和路由
	// =========================================================================
	handler := api.NewHandler(db, redisClient)

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

		// 提交代码 (需认证)
		v1.POST("/submit", api.AuthMiddleware(), handler.HandleSubmit)
		
		// 查询状态 (公开)
		v1.GET("/status/:job_id", handler.HandleStatus)
		
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
		log.Printf("🚀 API Server starting on :%s", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	// 等待中断信号
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("🛑 Shutting down server...")

	// 给请求 5 秒时间完成
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatal("Server forced to shutdown:", err)
	}

	log.Println("👋 Server exited")
}
