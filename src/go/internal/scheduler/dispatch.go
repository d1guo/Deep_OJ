package scheduler

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/d1guo/deep_oj/internal/repository"
	"github.com/d1guo/deep_oj/pkg/common"
	pb "github.com/d1guo/deep_oj/pkg/proto" // Keep pkg/proto for gRPC request construction
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
)

// DispatchTask 将任务分发给 Worker (支持重试)
func DispatchTask(ctx context.Context, workerAddr string, taskData []byte, redis *repository.RedisClient) error {
	// 1. 解析任务 (Protobuf)
	task := &pb.TaskRequest{}
	if err := proto.Unmarshal(taskData, task); err != nil {
		// 无法解析的数据，永远无法成功 -> NonRetryable
		return fmt.Errorf("unmarshal task: %w (%w)", err, common.ErrNonRetryable)
	}

	logger := slog.With("job_id", task.JobId, "worker", workerAddr, "trace_id", task.TraceId)
	logger.InfoContext(ctx, "Dispatching task")

	// 2. 建立 gRPC 连接 (带超时)
	connTimeoutMs := getEnvInt("DISPATCH_CONN_TIMEOUT_MS", 3000)
	connCtx, cancelConn := context.WithTimeout(ctx, time.Duration(connTimeoutMs)*time.Millisecond)
	defer cancelConn()

	creds, err := loadClientCreds()
	if err != nil {
		return fmt.Errorf("load tls creds: %w (%w)", err, common.ErrRetryable)
	}
	if creds == nil {
		creds = insecure.NewCredentials()
	}
	conn, err := grpc.DialContext(connCtx, workerAddr,
		grpc.WithTransportCredentials(creds),
		grpc.WithBlock(),
	)
	if err != nil {
		// 连接失败 -> Retryable
		return fmt.Errorf("grpc dial %s: %w (%w)", workerAddr, err, common.ErrRetryable)
	}
	defer conn.Close()

	client := pb.NewJudgeServiceClient(conn)

	// 3. 构造请求 (直接复用 task)
	req := task

	// 4. 重试策略 (指数退避)
	maxRetries := getEnvInt("DISPATCH_MAX_RETRIES", 3)
	backoffBaseMs := getEnvInt("DISPATCH_BACKOFF_BASE_MS", 100)
	var lastErr error

	for i := 0; i < maxRetries; i++ {
		// RPC 超时
		rpcTimeoutMs := getEnvInt("DISPATCH_RPC_TIMEOUT_MS", 5000)
		rpcCtx, cancelRPC := context.WithTimeout(ctx, time.Duration(rpcTimeoutMs)*time.Millisecond) // 发送请求不需要很久，执行是异步的

		_, err := client.ExecuteTask(rpcCtx, req)
		cancelRPC()

		if err == nil {
			logger.InfoContext(ctx, "Task accepted by worker")
			// 成功分发，不需要在这里等待结果，由 ACK Listener 处理
			return nil
		}

		lastErr = err
		logger.WarnContext(ctx, "Dispatch failed", "attempt", i+1, "max_retries", maxRetries, "error", err)

		// 指数退避: base, base*2, base*4...
		backoff := time.Duration(backoffBaseMs*(1<<i)) * time.Millisecond
		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled during retry: %w", ctx.Err())
		case <-time.After(backoff):
			// continue
		}
	}

	return fmt.Errorf("dispatch failed after %d retries: %w (%w)", maxRetries, lastErr, common.ErrRetryable)
}

func loadClientCreds() (credentials.TransportCredentials, error) {
	certFile := os.Getenv("GRPC_TLS_CERT")
	keyFile := os.Getenv("GRPC_TLS_KEY")
	caFile := os.Getenv("GRPC_TLS_CA")
	if certFile == "" || keyFile == "" || caFile == "" {
		return nil, nil
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	caData, err := os.ReadFile(caFile)
	if err != nil {
		return nil, err
	}
	certPool := x509.NewCertPool()
	certPool.AppendCertsFromPEM(caData)

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      certPool,
		MinVersion:   tls.VersionTLS12,
	}
	return credentials.NewTLS(tlsConfig), nil
}
