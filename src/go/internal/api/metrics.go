package api

import (
	"os"
	"strconv"

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
	// SubmitTotal 记录 submit 请求总量（按结果分类）
	submitTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "submit_total",
			Help: "Total number of submit requests",
		},
		[]string{"result"},
	)

	// Submit429Total 记录 submit 429 总量（按原因分类）
	submit429Total = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "submit_429_total",
			Help: "Total number of submit requests rejected with 429",
		},
		[]string{"reason"},
	)

	// RequestDurationSeconds 记录请求耗时（按 route 与状态码粗粒度）
	requestDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "request_duration_seconds",
			Help:    "HTTP request duration in seconds grouped by route and status class",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"route", "status_class"},
	)

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

	apiBackpressureRejectTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "api_backpressure_reject_total",
			Help: "Total number of submit requests rejected by backpressure",
		},
		[]string{"reason"},
	)

	apiBackpressureCheckTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "api_backpressure_check_total",
			Help: "Total number of backpressure checks by status/source",
		},
		[]string{"status", "source"},
	)

	apiPressureSnapshot = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "api_pressure_snapshot",
			Help: "Latest pressure snapshot values from stream poller",
		},
		[]string{"field"},
	)

	streamLagGauge = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "stream_lag",
			Help: "Current stream lag from XINFO GROUPS",
		},
	)

	streamBacklogGauge = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "stream_backlog",
			Help: "Current stream backlog (xlen - entries_read)",
		},
	)

	streamOldestAgeSecondsGauge = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "oldest_age_seconds",
			Help: "Age in seconds of the oldest queued/pending stream message",
		},
	)
)

func init() {
	// 注册指标
	// 注意: 在实际生产环境中，建议在 cmd/api/main.go 中统一注册
	// 这里为了演示方便直接 init
	reg.MustRegister(submitTotal)
	reg.MustRegister(submit429Total)
	reg.MustRegister(requestDurationSeconds)
	reg.MustRegister(RequestDuration)
	reg.MustRegister(RequestTotal)
	reg.MustRegister(SubmissionTotal)
	reg.MustRegister(apiStreamEnqueueTotal)
	reg.MustRegister(apiStreamEnqueueLatencyMs)
	reg.MustRegister(apiOutboxDispatchTotal)
	reg.MustRegister(apiOutboxDispatchLatencySeconds)
	reg.MustRegister(apiOutboxPending)
	reg.MustRegister(apiBackpressureRejectTotal)
	reg.MustRegister(apiBackpressureCheckTotal)
	reg.MustRegister(apiPressureSnapshot)
	reg.MustRegister(streamLagGauge)
	reg.MustRegister(streamBacklogGauge)
	reg.MustRegister(streamOldestAgeSecondsGauge)

	// Pre-initialize low-cardinality label sets so key metrics are always visible on /metrics.
	submitTotal.WithLabelValues("all")
	submitTotal.WithLabelValues("success")
	submitTotal.WithLabelValues("error")
	submit429Total.WithLabelValues("rate_limit")
	submit429Total.WithLabelValues("backpressure")
	requestDurationSeconds.WithLabelValues("unknown", "2xx")
}

func statusClassFromCode(statusCode int) string {
	if statusCode <= 0 {
		return "unknown"
	}
	class := statusCode / 100
	if class < 1 || class > 5 {
		return "other"
	}
	return strconv.Itoa(class) + "xx"
}
