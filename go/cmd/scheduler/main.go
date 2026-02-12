/**
 * @file main.go
 * @brief Go Scheduler 入口
 * 
 * 架构定位: 任务调度层
 * 技术选型: Etcd (服务发现) + gRPC (Worker 通信) + Redis (任务队列)
 * 
 * ===========================================================================
 * 面试八股知识点
 * ===========================================================================
 * 
 * 1. Etcd 服务发现 vs 传统配置:
 *    - 传统: 硬编码 Worker 地址，重启才能更新
 *    - Etcd: Worker 动态注册，实时感知变化
 *    - Lease 机制: Worker 定期续约，超时自动注销
 * 
 * 2. 负载均衡策略:
 *    - Round-Robin: 简单轮询，适合同构服务
 *    - Weighted: 加权轮询，根据 Worker 能力分配
 *    - Least-Connections: 最少连接优先
 *    - Consistent-Hashing: 一致性哈希，适合缓存场景
 * 
 * 3. gRPC 优势:
 *    - HTTP/2: 多路复用，头部压缩
 *    - Protobuf: 紧凑的二进制序列化
 *    - 流式传输: 双向流支持
 *    - 代码生成: 强类型接口
 * 
 * 4. 可靠性设计:
 *    - ACK 机制: 任务确认后才从队列移除
 *    - 超时检测: 处理中任务超时后重新入队
 *    - 重试策略: 指数退避 (Exponential Backoff)
 */
package main

import (

	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/d1guo/deep_oj/internal/repository"
	"github.com/d1guo/deep_oj/internal/scheduler"
	pb "github.com/d1guo/deep_oj/pkg/proto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/protobuf/proto"
)

func main() {
	// =========================================================================
	// 1. 读取配置
	// =========================================================================
	etcdEndpoints := os.Getenv("ETCD_ENDPOINTS")
	if etcdEndpoints == "" {
		etcdEndpoints = "localhost:2379"
	}

	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		redisURL = "localhost:6379"
	}

	// =========================================================================
	// 2. 初始化 Context (支持优雅关闭)
	// =========================================================================
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 监听中断信号
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("🛑 Received shutdown signal...")
		cancel()
	}()

	// =========================================================================
	// 3. 初始化 Etcd 服务发现
	// =========================================================================
	endpoints := strings.Split(etcdEndpoints, ",")
	discovery, err := scheduler.NewEtcdDiscovery(endpoints)
	if err != nil {
		log.Fatalf("❌ Failed to connect to Etcd: %v", err)
	}
	defer discovery.Close()
	log.Println("✅ Connected to Etcd")

	// 启动 Worker 监听
	go discovery.WatchWorkers(ctx)

	// =========================================================================
	// 4. 初始化 Redis 客户端
	// =========================================================================
	redisClient := repository.NewRedisClient(redisURL)
	if err := redisClient.Ping(ctx); err != nil {
		log.Fatalf("❌ Failed to connect to Redis: %v", err)
	}
	log.Println("✅ Connected to Redis")

	// =========================================================================
	// 4.5 初始化 PostgreSQL (用于 ACK 回调更新状态)
	// =========================================================================
	postgresURL := os.Getenv("DATABASE_URL")
	if postgresURL == "" {
		postgresURL = "postgres://deep_oj:secret@localhost:5432/deep_oj?sslmode=disable"
	}
	db, err := repository.NewPostgresDB(ctx, postgresURL)
	if err != nil {
		log.Fatalf("❌ Failed to connect to PostgreSQL: %v", err)
	}
	defer db.Close()
	log.Println("✅ Connected to PostgreSQL")

	// 启动 ACK 监听器
	go scheduler.StartAckListener(ctx, redisClient, db)

	// =========================================================================
	// 6. [Task 3.3] 启动监控 (Probes & Metrics)
	// =========================================================================
	
	// 6.1 启动 Metrics Poller (Redis/Etcd 状态)
	go scheduler.StartMetricsPoller(ctx, redisClient, discovery)

	// 6.2 暴露 Prometheus Metrics endpoint
	http.Handle("/metrics", promhttp.Handler())
	go func() {
		metricsPort := ":9091"
		log.Printf("📊 Metrics server starting on %s", metricsPort)
		if err := http.ListenAndServe(metricsPort, nil); err != nil {
			log.Printf("⚠️ Metrics server error: %v", err)
		}
	}()

	// 启动慢路径兜底 (Slow Path)
	go scheduler.StartSlowPath(ctx, redisClient)

	// =========================================================================
	// 5. 启动任务分发循环
	// =========================================================================
	log.Println("🚀 Scheduler started, waiting for tasks...")

	for {
		select {
		case <-ctx.Done():
			log.Println("👋 Scheduler exited")
			return
		default:
		}

		// 阻塞等待任务 (5 秒超时)
		result, err := redisClient.BRPopLPush(ctx, "queue:pending", "queue:processing", 5*time.Second)
		if err != nil || result == "" {
			continue
		}

		// 解析任务 (Protobuf)
		task := &pb.TaskRequest{}
		if err := proto.Unmarshal([]byte(result), task); err != nil {
			log.Printf("⚠️ Failed to parse task: %v", err)
			continue
		}

		jobID := task.JobId
		log.Printf("📦 Received task: %s", jobID)

		// 获取可用 Worker
		workerAddr, ok := discovery.GetNextWorker()
		if !ok {
			log.Printf("⚠️ No workers available, task %s will retry later", jobID)
			// 任务留在 processing 队列，等待重试
			continue
		}

		// 异步分发任务
		go func(addr string, taskData []byte) {
			if err := scheduler.DispatchTask(ctx, addr, taskData, redisClient); err != nil {
				log.Printf("❌ Failed to dispatch task %s: %v", jobID, err)
				// 失败时移回 pending 队列
				redisClient.LPush(ctx, "queue:pending", string(taskData))
			} else {
				// 成功时不移除！等待 ACK Listener 移除
				// redisClient.LRem(ctx, "queue:processing", 1, string(taskData))
				log.Printf("dispatch success")
			}
		}(workerAddr, []byte(result))
	}
}
