package scheduler

import (
	"context"
	"log"
	"time"

	"github.com/d1guo/deep_oj/internal/repository"
	pb "github.com/d1guo/deep_oj/pkg/proto"
	"google.golang.org/protobuf/proto"
)

// StartSlowPath 启动慢路径兜底 (处理超时任务)
func StartSlowPath(ctx context.Context, redis *repository.RedisClient) {
	log.Println("启动 Slow Path 监控 (间隔=5s, 超时=30s)...")
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			checkTimeoutTasks(ctx, redis)
		}
	}
}

func checkTimeoutTasks(ctx context.Context, redis *repository.RedisClient) {
	// 1. 获取所有正在处理的任务
	// Note: For production use with very large queues, consider pagination (SCAN/LRANGE with offset).
	tasks, err := redis.LRange(ctx, "queue:processing", 0, -1)
	if err != nil {
		log.Printf("SlowPath: 获取处理中队列失败: %v", err)
		return
	}

	now := time.Now().UnixMilli()
	recoveredCount := 0

	for _, taskData := range tasks {
		task := &pb.TaskRequest{}
		// 尝试解析 Protobuf
		if err := proto.Unmarshal([]byte(taskData), task); err != nil {
			log.Printf("SlowPath: 任务解析失败: %v", err)
			continue
		}

		// 2. 检查超时 (> 30s)
		if task.SubmitTime > 0 && (now-task.SubmitTime > 30000) {
			log.Printf("SlowPath: 任务 %s 超时 (耗时 %ds) -> 正在恢复...", 
				task.JobId, (now-task.SubmitTime)/1000)

			// 3. 原子恢复 (LREM + LPUSH)
			// 使用 Watch 或者 Pipeline? Pipeline 只能保证批量执行，不能保证原子性(除非用 MULTI)
			// RedisClient 需要暴露 TxPipeline
			
			// 这里我们使用 Lua 脚本或者简单的 Pipeline (Redis 单线程特性，LREM+LPUSH 几乎是立即发生的，但不是原子的)
			// 为了严谨，应该用 TxPipeline (MULTI/EXEC)
			
			// 调用 redis.TxPipeline
			// 但 repository.RedisClient 没有暴露 TxPipeline。
			// 我们先在 redis.go 里加一个 RestoreTask 方法比较好?
			// 为了简单，我们暂时用裸调，或者给 RedisClient 加个 RequeueTask 方法?
			// 
			// Let's assume RedisClient needs expansion or we use raw client if exposed? 
			// RedisClient struct has `client *redis.Client` strictly private.
			
			// Option: Add RequeueTask to repository/redis.go
			err := redis.RequeueTask(ctx, "queue:processing", "queue:pending", taskData)
			if err != nil {
				log.Printf("SlowPath: 重新入队任务 %s 失败: %v", task.JobId, err)
			} else {
				recoveredCount++
			}
		}
	}

	if recoveredCount > 0 {
		log.Printf("SlowPath: 已恢复 %d 个任务", recoveredCount)
	}
}
