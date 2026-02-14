package worker

import (
	"os"

	"github.com/prometheus/client_golang/prometheus"
)

func metricLabels() prometheus.Labels {
	service := os.Getenv("SERVICE_NAME")
	if service == "" {
		service = "deep-oj-worker"
	}
	instance := os.Getenv("INSTANCE_ID")
	if instance == "" {
		instance, _ = os.Hostname()
	}
	return prometheus.Labels{"service": service, "instance": instance}
}

var reg = prometheus.WrapRegistererWith(metricLabels(), prometheus.DefaultRegisterer)

var (
	workerTaskTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "worker_task_total",
			Help: "Total number of tasks processed by worker",
		},
		[]string{"status"},
	)

	workerTaskDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "worker_task_duration_seconds",
			Help:    "End-to-end task duration in seconds",
			Buckets: prometheus.DefBuckets,
		},
	)

	workerCompileDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "worker_compile_duration_seconds",
			Help:    "Compile duration in seconds",
			Buckets: prometheus.DefBuckets,
		},
	)

	workerDownloadDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "worker_download_duration_seconds",
			Help:    "Testcase download duration in seconds",
			Buckets: prometheus.DefBuckets,
		},
	)

	workerUnzipDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "worker_unzip_duration_seconds",
			Help:    "Testcase unzip duration in seconds",
			Buckets: prometheus.DefBuckets,
		},
	)

	judgeExecDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "judge_exec_duration_seconds",
			Help:    "Judge execution duration in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"result"},
	)

	judgeExecInflight = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "judge_exec_inflight",
			Help: "In-flight judge executions",
		},
	)

	judgeExecTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "judge_exec_total",
			Help: "Total number of judge executions",
		},
		[]string{"result"},
	)

	judgeVerdictTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "judge_verdict_total",
			Help: "Total number of judge verdicts",
		},
		[]string{"verdict"},
	)

	judgeProtocolErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "judge_protocol_errors_total",
			Help: "Total number of judge protocol validation errors",
		},
		[]string{"reason"},
	)

	judgeOutputTruncatedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "judge_output_truncated_total",
			Help: "Total number of judge output truncations",
		},
		[]string{"stream"},
	)

	workerStreamConsumeTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "worker_stream_consume_total",
			Help: "Total number of worker stream consume attempts",
		},
		[]string{"status", "reason"},
	)

	workerStreamConsumeLatencyMs = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "worker_stream_consume_latency_ms",
			Help:    "Latency of processing one stream message in milliseconds",
			Buckets: []float64{1, 2, 5, 10, 20, 50, 100, 200, 500, 1000, 2000},
		},
	)

	workerStreamInflight = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "worker_stream_inflight",
			Help: "Current number of in-flight stream messages processed by worker",
		},
	)

	workerClaimTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "worker_claim_total",
			Help: "Total number of DB claim attempts for stream messages",
		},
		[]string{"status", "reason"},
	)

	workerLeaseHeartbeatTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "worker_lease_heartbeat_total",
			Help: "Total number of lease heartbeat outcomes",
		},
		[]string{"status", "reason"},
	)

	workerStaleAttemptTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "worker_stale_attempt_total",
			Help: "Total number of stale attempt fenced write-back rejections",
		},
	)
)

// InitMetrics registers worker metrics
func InitMetrics() {
	reg.MustRegister(workerTaskTotal)
	reg.MustRegister(workerTaskDuration)
	reg.MustRegister(workerCompileDuration)
	reg.MustRegister(workerDownloadDuration)
	reg.MustRegister(workerUnzipDuration)
	reg.MustRegister(judgeExecDuration)
	reg.MustRegister(judgeExecInflight)
	reg.MustRegister(judgeExecTotal)
	reg.MustRegister(judgeVerdictTotal)
	reg.MustRegister(judgeProtocolErrorsTotal)
	reg.MustRegister(judgeOutputTruncatedTotal)
	reg.MustRegister(workerStreamConsumeTotal)
	reg.MustRegister(workerStreamConsumeLatencyMs)
	reg.MustRegister(workerStreamInflight)
	reg.MustRegister(workerClaimTotal)
	reg.MustRegister(workerLeaseHeartbeatTotal)
	reg.MustRegister(workerStaleAttemptTotal)
}
