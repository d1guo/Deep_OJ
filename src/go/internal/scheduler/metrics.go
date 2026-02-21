package scheduler

import (
	"context"
	"os"
	"time"

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
	schedulerActiveWorkers = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "scheduler_active_workers",
			Help: "当前发现到的活跃工作节点数",
		},
	)

	controlPlaneOnlyGauge = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "control_plane_only",
			Help: "调度器是否处于仅控制面模式（1=是，0=否）",
		},
	)

	legacyLoopsStartedGauge = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "legacy_loops_started",
			Help: "legacy 数据面循环启动次数",
		},
	)
)

func init() {
	reg.MustRegister(schedulerActiveWorkers)
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

// StartMetricsPoller 周期刷新调度器指标。
func StartMetricsPoller(ctx context.Context, discovery *WorkerDiscovery) {
	pollMs := getEnvInt("SCHEDULER_METRICS_POLL_INTERVAL_MS", 1000)
	ticker := time.NewTicker(time.Duration(pollMs) * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			updateWorkerMetrics(discovery)
		}
	}
}

func updateWorkerMetrics(discovery *WorkerDiscovery) {
	count := discovery.GetWorkerCount()
	schedulerActiveWorkers.Set(float64(count))
}
