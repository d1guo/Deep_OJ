package observability

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"log/slog"
)

// StartMetricsServer starts a background HTTP server for Prometheus metrics
func StartMetricsServer(addr string) {
	go func() {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())

		slog.Info("Starting metrics server", "addr", addr)
		if err := http.ListenAndServe(addr, mux); err != nil {
			slog.Error("Metrics server failed", "error", err)
		}
	}()
}
