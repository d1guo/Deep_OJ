package api

import (
	"os"

	"github.com/prometheus/client_golang/prometheus"
)

func metricLabels() prometheus.Labels {
	service := os.Getenv("SERVICE_NAME")
	if service == "" {
		service = "deep-oj-api"
	}
	instance := os.Getenv("INSTANCE_ID")
	if instance == "" {
		instance, _ = os.Hostname()
	}
	return prometheus.Labels{"service": service, "instance": instance}
}

var reg = prometheus.WrapRegistererWith(metricLabels(), prometheus.DefaultRegisterer)

var (
	// RequestDuration 记录请求耗时
	RequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request duration in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "path", "status"},
	)

	// RequestTotal 记录请求总数
	RequestTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total number of HTTP requests",
		},
		[]string{"method", "path", "status"},
	)

	// SubmissionTotal 记录提交总数
	SubmissionTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "submission_total",
			Help: "Total number of code submissions",
		},
		[]string{"language", "status"},
	)

	// APIStreamEnqueueTotal 记录 Streams 入队结果
	apiStreamEnqueueTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "api_stream_enqueue_total",
			Help: "Total number of API stream enqueue attempts",
		},
		[]string{"status"},
	)

	// APIStreamEnqueueLatencyMs 记录 Streams 入队耗时（毫秒）
	apiStreamEnqueueLatencyMs = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "api_stream_enqueue_latency_ms",
			Help:    "Latency of API stream enqueue in milliseconds",
			Buckets: []float64{1, 2, 5, 10, 20, 50, 100, 200, 500, 1000, 2000},
		},
	)

	apiOutboxDispatchTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "api_outbox_dispatch_total",
			Help: "Total number of outbox dispatch attempts",
		},
		[]string{"status", "reason"},
	)

	apiOutboxDispatchLatencySeconds = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "api_outbox_dispatch_latency_seconds",
			Help:    "Latency of one outbox dispatch loop in seconds",
			Buckets: prometheus.DefBuckets,
		},
	)

	apiOutboxPending = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "api_outbox_pending",
			Help: "Current number of pending outbox events",
		},
	)
)

func init() {
	// 注册指标
	// 注意: 在实际生产环境中，建议在 cmd/api/main.go 中统一注册
	// 这里为了演示方便直接 init
	reg.MustRegister(RequestDuration)
	reg.MustRegister(RequestTotal)
	reg.MustRegister(SubmissionTotal)
	reg.MustRegister(apiStreamEnqueueTotal)
	reg.MustRegister(apiStreamEnqueueLatencyMs)
	reg.MustRegister(apiOutboxDispatchTotal)
	reg.MustRegister(apiOutboxDispatchLatencySeconds)
	reg.MustRegister(apiOutboxPending)
}
