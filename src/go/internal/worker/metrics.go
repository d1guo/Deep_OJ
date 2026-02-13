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
)

// InitMetrics registers worker metrics
func InitMetrics() {
	reg.MustRegister(workerTaskTotal)
	reg.MustRegister(workerTaskDuration)
	reg.MustRegister(workerCompileDuration)
	reg.MustRegister(workerDownloadDuration)
	reg.MustRegister(workerUnzipDuration)
}
