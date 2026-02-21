package scheduler

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/d1guo/deep_oj/internal/repository"
	"github.com/d1guo/deep_oj/pkg/common"
	"github.com/prometheus/client_golang/prometheus"
)

func metricLabels() prometheus.Labels {
	service := os.Getenv("SERVICE_NAME")
	if service == "" {
		service = "deep-oj-scheduler"
	}
	instance := os.Getenv("INSTANCE_ID")
	if instance == "" {
		instance, _ = os.Hostname()
	}
	return prometheus.Labels{"service": service, "instance": instance}
}

var reg = prometheus.WrapRegistererWith(metricLabels(), prometheus.DefaultRegisterer)

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
			Help: "当前发现到的活跃工作节点数",
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

	schedulerJobLatency = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "job_latency_seconds",
			Help:    "任务端到端处理耗时 (seconds)",
			Buckets: prometheus.DefBuckets, // default buckets are fine for now
		},
	)

	controlPlaneOnlyGauge = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "control_plane_only",
			Help: "Whether scheduler runs in control-plane-only mode (1=true, 0=false)",
		},
	)

	legacyLoopsStartedGauge = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "legacy_loops_started",
			Help: "Number of legacy scheduler data-plane loops started",
		},
	)
)

func init() {
	reg.MustRegister(schedulerQueueDepth)
	reg.MustRegister(schedulerActiveWorkers)
	reg.MustRegister(submissionResultTotal)
	reg.MustRegister(schedulerJobLatency)
	reg.MustRegister(controlPlaneOnlyGauge)
	reg.MustRegister(legacyLoopsStartedGauge)
}

func SetControlPlaneOnly(enabled bool) {
	if enabled {
		controlPlaneOnlyGauge.Set(1)
		return
	}
	controlPlaneOnlyGauge.Set(0)
}

func SetLegacyLoopsStarted(count int) {
	if count < 0 {
		count = 0
	}
	legacyLoopsStartedGauge.Set(float64(count))
}

// StartMetricsPoller starts a background loop to update Gauge metrics
func StartMetricsPoller(ctx context.Context, redis *repository.RedisClient, discovery *WorkerDiscovery) {
	pollMs := getEnvInt("SCHEDULER_METRICS_POLL_INTERVAL_MS", 1000)
	ticker := time.NewTicker(time.Duration(pollMs) * time.Millisecond)
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
	pendingCount, err := redis.LLen(ctx, common.QueuePending)
	if err != nil {
		slog.Error("获取待处理队列长度失败", "error", err)
	} else {
		schedulerQueueDepth.WithLabelValues("pending").Set(float64(pendingCount))
	}

	// 2. Processing Queue
	processingCount, err := redis.LLen(ctx, common.QueueProcessing)
	if err != nil {
		slog.Error("获取处理中队列长度失败", "error", err)
	} else {
		schedulerQueueDepth.WithLabelValues("processing").Set(float64(processingCount))
	}
}

func updateWorkerMetrics(ctx context.Context, discovery *WorkerDiscovery) {
	count := discovery.GetWorkerCount()
	schedulerActiveWorkers.Set(float64(count))
}
