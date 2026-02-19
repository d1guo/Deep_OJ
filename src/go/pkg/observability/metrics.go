package observability

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"log/slog"
)

// StartMetricsServer 在后台启动 Prometheus 指标 HTTP 服务。
func StartMetricsServer(addr string) {
	go func() {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())

		slog.Info("指标服务启动中", "addr", addr)
		if err := http.ListenAndServe(addr, mux); err != nil {
			slog.Error("指标服务异常", "error", err)
		}
	}()
}
