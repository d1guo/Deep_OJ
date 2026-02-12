package api

import (
	"github.com/prometheus/client_golang/prometheus"
)

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
)

func init() {
	// 注册指标
	// 注意: 在实际生产环境中，建议在 cmd/api/main.go 中统一注册
	// 这里为了演示方便直接 init
	prometheus.MustRegister(RequestDuration)
	prometheus.MustRegister(RequestTotal)
	prometheus.MustRegister(SubmissionTotal)
}
