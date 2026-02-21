package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

type workerEndpoint struct {
	id   string
	addr string
}

// WorkerDiscovery 提供无 etcd 的工作节点发现。
// 支持两种环境变量：
// - WORKER_ADDR=host:port
// - WORKER_ADDRS=id1=host1:port1,id2=host2:port2 或 host1:port1,host2:port2
type WorkerDiscovery struct {
	workers      []workerEndpoint
	rrIndex      uint64
	probeTimeout time.Duration
}

func NewWorkerDiscovery() (*WorkerDiscovery, error) {
	rawWorkers := strings.TrimSpace(os.Getenv("WORKER_ADDRS"))
	if rawWorkers == "" {
		rawWorkers = strings.TrimSpace(os.Getenv("WORKER_ADDR"))
	}
	workers := parseWorkerEndpoints(rawWorkers)
	if len(workers) == 0 {
		slog.Warn("未配置可用工作节点，调度器将等待 worker 上线", "env", "WORKER_ADDR/WORKER_ADDRS")
	}
	probeMs := getEnvInt("WORKER_PROBE_TIMEOUT_MS", 1000)
	if probeMs <= 0 {
		probeMs = 1000
	}
	return &WorkerDiscovery{
		workers:      workers,
		probeTimeout: time.Duration(probeMs) * time.Millisecond,
	}, nil
}

func parseWorkerEndpoints(raw string) []workerEndpoint {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	workers := make([]workerEndpoint, 0, len(parts))
	seenIDs := make(map[string]struct{}, len(parts))
	for idx, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id := ""
		addr := part
		if strings.Contains(part, "=") {
			pair := strings.SplitN(part, "=", 2)
			id = strings.TrimSpace(pair[0])
			addr = strings.TrimSpace(pair[1])
		}
		if addr == "" {
			continue
		}
		if id == "" {
			id = fmt.Sprintf("worker-%d", idx+1)
		}
		if _, exists := seenIDs[id]; exists {
			id = fmt.Sprintf("%s-%d", id, idx+1)
		}
		seenIDs[id] = struct{}{}
		workers = append(workers, workerEndpoint{
			id:   id,
			addr: addr,
		})
	}
	return workers
}

// Close 为接口兼容保留，当前无外部连接需要关闭。
func (d *WorkerDiscovery) Close() error {
	return nil
}

// WatchWorkers 在无 etcd 模式下仅阻塞等待退出信号，避免调用方改动。
func (d *WorkerDiscovery) WatchWorkers(ctx context.Context) {
	<-ctx.Done()
}

// GetNextWorker 使用 Round-Robin 获取下一个 worker。
func (d *WorkerDiscovery) GetNextWorker() (string, string, bool) {
	if len(d.workers) == 0 {
		return "", "", false
	}
	idx := atomic.AddUint64(&d.rrIndex, 1)
	w := d.workers[(idx-1)%uint64(len(d.workers))]
	return w.id, w.addr, true
}

func (d *WorkerDiscovery) GetWorkerCount() int {
	return len(d.workers)
}

// IsWorkerActive 通过 TCP 探活判断 worker 是否在线。
func (d *WorkerDiscovery) IsWorkerActive(workerID string) bool {
	for _, w := range d.workers {
		if w.id == workerID {
			return probeTCPAddress(w.addr, d.probeTimeout)
		}
	}
	return false
}

func probeTCPAddress(rawAddr string, timeout time.Duration) bool {
	addr := normalizeDialAddress(rawAddr)
	if addr == "" {
		return false
	}
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return false
	}
	if err := conn.Close(); err != nil {
		slog.Debug("关闭探活连接失败", "addr", addr, "error", err)
	}
	return true
}

func normalizeDialAddress(raw string) string {
	addr := strings.TrimSpace(raw)
	if addr == "" {
		return ""
	}
	if strings.Contains(addr, "://") {
		u, err := url.Parse(addr)
		if err != nil {
			return ""
		}
		if u.Host != "" {
			return strings.TrimSpace(u.Host)
		}
		return ""
	}
	return addr
}
