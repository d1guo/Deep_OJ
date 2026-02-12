package scheduler

import (
	"context"
	"log"
	"time"

	"github.com/d1guo/deep_oj/internal/repository"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	// System Metrics
	schedulerQueueDepth = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "scheduler_queue_depth",
			Help: "Redis 队列当前任务深度",
		},
		[]string{"queue"},
	)

	schedulerActiveWorkers = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "scheduler_active_workers",
			Help: "Etcd 中活跃的工作节点数",
		},
	)

	// Business Metrics
	submissionResultTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "submission_result_total",
			Help: "按状态和语言统计的已处理提交总数",
		},
		[]string{"status", "language"},
	)
)

func init() {
	prometheus.MustRegister(schedulerQueueDepth)
	prometheus.MustRegister(schedulerActiveWorkers)
	prometheus.MustRegister(submissionResultTotal)
}

// StartMetricsPoller starts a background loop to update Gauge metrics
func StartMetricsPoller(ctx context.Context, redis *repository.RedisClient, discovery *EtcdDiscovery) {
	// 提高指标更新频率到 1s，确保观测实时性
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			updateQueueMetrics(ctx, redis)
			updateWorkerMetrics(ctx, discovery)
		}
	}
}

func updateQueueMetrics(ctx context.Context, redis *repository.RedisClient) {
	// 1. Pending Queue
	pendingCount, err := redis.LLen(ctx, "queue:pending")
	if err != nil {
		log.Printf("获取等待队列长度失败: %v", err)
	} else {
		schedulerQueueDepth.WithLabelValues("pending").Set(float64(pendingCount))
	}

	// 2. Processing Queue
	processingCount, err := redis.LLen(ctx, "queue:processing")
	if err != nil {
		log.Printf("获取处理中队列长度失败: %v", err)
	} else {
		schedulerQueueDepth.WithLabelValues("processing").Set(float64(processingCount))
	}
}

func updateWorkerMetrics(ctx context.Context, discovery *EtcdDiscovery) {
	count := discovery.GetWorkerCount()
	schedulerActiveWorkers.Set(float64(count))
}
