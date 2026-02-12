/**
 * @file ack_listener.go
 * @brief 任务结果确认监听器
 */
package scheduler

import (
	"context"
	"encoding/json"
	"log"

	"github.com/d1guo/deep_oj/internal/repository"
	pb "github.com/d1guo/deep_oj/pkg/proto"
	"google.golang.org/protobuf/proto"
)

// StartAckListener 启动 ACK 监听
func StartAckListener(ctx context.Context, redis *repository.RedisClient, db *repository.PostgresDB) {
	log.Println("开始监听 ACK 频道 'job_done'...")

	// 1. 订阅频道
	pubsub := redis.Subscribe(ctx, "job_done")
	defer pubsub.Close()

	ch := pubsub.Channel()

	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-ch:
			// 2. 收到消息 (Payload 是 job_id)
			jobID := msg.Payload
			log.Printf("收到任务 ACK: %s", jobID)

			// 3. 处理结果并清理队列
			handleTaskCompletion(ctx, jobID, redis, db)
		}
	}
}

func handleTaskCompletion(ctx context.Context, jobID string, redis *repository.RedisClient, db *repository.PostgresDB) {
	// 1. 从处理中队列找到该任务 (用于获取语言信息和清理)
	items, err := redis.LRange(ctx, "queue:processing", 0, -1)
	if err != nil {
		log.Printf("handleTaskCompletion: 无法读取处理中队列: %v", err)
		return
	}

	var targetTask *pb.TaskRequest
	var rawItem string
	for _, item := range items {
		task := &pb.TaskRequest{}
		if err := proto.Unmarshal([]byte(item), task); err != nil {
			continue
		}
		if task.JobId == jobID {
			targetTask = task
			rawItem = item
			break
		}
	}

	language := "unknown"
	if targetTask != nil {
		// 映射语言枚举到字符串
		switch targetTask.Language {
		case pb.Language_CPP:
			language = "cpp"
		case pb.Language_JAVA:
			language = "java"
		case pb.Language_PYTHON:
			language = "python"
		case pb.Language_GO:
			language = "go"
		default:
			language = "other"
		}
	}

	// 2. 处理结果 (同步到 DB + 更新指标)
	processResult(ctx, jobID, language, redis, db)

	// 3. 从队列移除
	if rawItem != "" {
		err := redis.LRem(ctx, "queue:processing", 1, rawItem)
		if err != nil {
			log.Printf("handleTaskCompletion: 移除任务 %s 失败: %v", jobID, err)
		} else {
			log.Printf("已从处理中队列清理任务 %s", jobID)
		}
	} else {
		log.Printf("handleTaskCompletion: 处理中队列未找到任务 %s", jobID)
	}
}

func processResult(ctx context.Context, jobID string, language string, redis *repository.RedisClient, db *repository.PostgresDB) {
	// 1. 从 Redis 读取结果
	// Worker 写入的 key 是 "result:<job_id>"
	resultKey := "result:" + jobID
	resultJSON, err := redis.Get(ctx, resultKey)
	if err != nil || resultJSON == "" {
		log.Printf("Redis 中未找到任务 %s 的结果: %v", jobID, err)
		return
	}

	// 2. 解析结果
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
		log.Printf("解析结果 JSON 失败: %v", err)
		return
	}

	status, _ := result["status"].(string) // "Accepted", "Wrong Answer", etc.

	// [Task 3.3] 更新业务指标
	// 使用从 TaskRequest 提取的真实语言标识
	submissionResultTotal.WithLabelValues(status, language).Inc()

	// 3. 更新 PostgreSQL
	// 使用 UpdateSubmission 更新状态和结果 JSON
	err = db.UpdateSubmission(ctx, jobID, status, result)
	if err != nil {
		log.Printf("更新任务 %s 的数据库状态失败: %v", jobID, err)
		return
	}

	log.Printf("任务 %s 已同步至数据库 (状态: %s, 语言: %s)", jobID, status, language)
}
