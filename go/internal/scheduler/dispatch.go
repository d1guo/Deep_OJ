/**
 * @file dispatch.go
 * @brief 任务分发逻辑
 * 
 * ===========================================================================
 * gRPC 客户端知识点 (面试必备)
 * ===========================================================================
 * 
 * 1. 连接管理:
 *    - grpc.Dial: 创建连接 (默认是非阻塞的)
 *    - WithInsecure: 跳过 TLS (仅开发环境)
 *    - WithBlock: 阻塞直到连接建立
 * 
 * 2. 超时控制:
 *    - context.WithTimeout: 设置 RPC 超时
 *    - 超时后 Client 会收到 DeadlineExceeded
 *    - Server 端也会收到取消信号
 * 
 * 3. 重试策略:
 *    - 指数退避 (Exponential Backoff)
 *    - 初始等待 100ms，每次翻倍
 *    - 最大等待 10s
 *    - 加入抖动 (Jitter) 防止惊群效应
 * 
 * 4. 连接池:
 *    - gRPC 内部使用 HTTP/2 多路复用
 *    - 一个连接可以承载多个并发请求
 *    - 通常不需要手动管理连接池
 */
package scheduler

import (
	"context"
	"log"
	"time"

	"github.com/d1guo/deep_oj/internal/repository"
	pb "github.com/d1guo/deep_oj/pkg/proto" // Keep pkg/proto for gRPC request construction
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
)

// DispatchTask 将任务分发给 Worker (支持重试)
func DispatchTask(ctx context.Context, workerAddr string, taskData []byte, redis *repository.RedisClient) error {
	// 1. 解析任务 (Protobuf)
	task := &pb.TaskRequest{}
	if err := proto.Unmarshal(taskData, task); err != nil {
		return err
	}

	log.Printf("正在派发任务 %s 至 %s", task.JobId, workerAddr)

	// 2. 建立 gRPC 连接 (带超时)
	connCtx, cancelConn := context.WithTimeout(ctx, 3*time.Second)
	defer cancelConn()
	
	conn, err := grpc.DialContext(connCtx, workerAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return err
	}
	defer conn.Close()

	client := pb.NewJudgeServiceClient(conn)

	// 3. 构造请求 (直接复用 task)
	req := task

	// 4. 重试策略 (指数退避)
	// 面试知识点: 为什么需要重试? 如何防止重试风暴?
	maxRetries := 3
	var lastErr error

	for i := 0; i < maxRetries; i++ {
		// RPC 超时
		rpcCtx, cancelRPC := context.WithTimeout(ctx, 5*time.Second) // 发送请求不需要很久，执行是异步的
		
		_, err := client.ExecuteTask(rpcCtx, req)
		cancelRPC()

		if err == nil {
			log.Printf("任务 %s 已被工作节点接收", task.JobId)
			// 成功分发，不需要在这里等待结果，由 ACK Listener 处理
			return nil
		}
		
		lastErr = err
		log.Printf("派发失败 (尝试 %d/%d): %v", i+1, maxRetries, err)

		// 指数退避: 100ms, 200ms, 400ms...
		backoff := time.Duration(100 * (1 << i)) * time.Millisecond
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
			// continue
		}
	}

	return lastErr
}
