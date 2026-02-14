package worker

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestMetricsNoJobIDLabel(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("unable to locate test file path")
	}
	metricsPath := filepath.Join(filepath.Dir(file), "metrics.go")
	data, err := os.ReadFile(metricsPath)
	if err != nil {
		t.Fatalf("read metrics.go failed: %v", err)
	}
	lower := strings.ToLower(string(data))
	if strings.Contains(lower, "job_id") || strings.Contains(lower, "jobid") {
		t.Fatalf("metrics.go contains forbidden job_id label")
	}
}

func TestMetricsEmitBasic(t *testing.T) {
	reg := prometheus.NewRegistry()
	collectors := []prometheus.Collector{
		judgeExecDuration,
		judgeExecInflight,
		judgeExecTotal,
		judgeVerdictTotal,
		judgeProtocolErrorsTotal,
		judgeOutputTruncatedTotal,
	}
	for _, c := range collectors {
		if err := reg.Register(c); err != nil {
			t.Fatalf("register metric failed: %v", err)
		}
	}

	judgeExecInflight.Inc()
	judgeExecTotal.WithLabelValues(judgeResultOK).Inc()
	judgeExecDuration.WithLabelValues(judgeResultOK).Observe(0.01)
	judgeVerdictTotal.WithLabelValues("OK").Inc()
	judgeProtocolErrorsTotal.WithLabelValues(reasonInvalidJSON).Inc()
	judgeOutputTruncatedTotal.WithLabelValues("stdout").Inc()
	judgeExecInflight.Dec()

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather failed: %v", err)
	}
	found := map[string]bool{}
	for _, mf := range mfs {
		found[mf.GetName()] = true
	}
	required := []string{
		"judge_exec_duration_seconds",
		"judge_exec_inflight",
		"judge_exec_total",
		"judge_verdict_total",
		"judge_protocol_errors_total",
		"judge_output_truncated_total",
	}
	for _, name := range required {
		if !found[name] {
			t.Fatalf("missing metric: %s", name)
		}
	}
}
