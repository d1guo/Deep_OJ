package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

const (
	// WorkerPrefix Etcd 中 Worker 注册的前缀
	WorkerPrefix = "/deep-oj/workers/"
)

// EtcdDiscovery Etcd 服务发现
type EtcdDiscovery struct {
	client  *clientv3.Client
	workers sync.Map // map[workerID]workerAddress
	rrIndex uint64   // Round-Robin 计数器
}

// NewEtcdDiscovery 创建 Etcd 服务发现实例
func NewEtcdDiscovery(endpoints []string) (*EtcdDiscovery, error) {
	dialMs := getEnvInt("ETCD_DIAL_TIMEOUT_MS", 5000)
	client, err := clientv3.New(clientv3.Config{
		Endpoints:   endpoints,
		DialTimeout: time.Duration(dialMs) * time.Millisecond,
	})
	if err != nil {
		return nil, err
	}

	return &EtcdDiscovery{
		client: client,
	}, nil
}

// Close 关闭 Etcd 连接
func (d *EtcdDiscovery) Close() error {
	return d.client.Close()
}

// WatchWorkers 监听 Worker 注册/注销事件
func (d *EtcdDiscovery) WatchWorkers(ctx context.Context) {
	slog.Info("正在监听工作节点", "prefix", WorkerPrefix)

	// 1. 首先获取现有的 Workers
	resp, err := d.client.Get(ctx, WorkerPrefix, clientv3.WithPrefix())
	if err != nil {
		slog.Error("获取初始工作节点失败", "error", err)
	} else {
		for _, kv := range resp.Kvs {
			workerID := string(kv.Key)[len(WorkerPrefix):]
			workerAddr := string(kv.Value)
			d.workers.Store(workerID, workerAddr)
			slog.Info("发现工作节点", "worker_id", workerID, "addr", workerAddr)
		}
	}

	// 2. 开始监听变化
	watchChan := d.client.Watch(ctx, WorkerPrefix, clientv3.WithPrefix())

	for resp := range watchChan {
		for _, ev := range resp.Events {
			workerID := string(ev.Kv.Key)[len(WorkerPrefix):]

			switch ev.Type {
			case clientv3.EventTypePut:
				// Worker 注册或更新
				workerAddr := string(ev.Kv.Value)
				d.workers.Store(workerID, workerAddr)
				slog.Info("工作节点已注册", "worker_id", workerID, "addr", workerAddr)

			case clientv3.EventTypeDelete:
				// Worker 注销 (Lease 过期或主动注销)
				d.workers.Delete(workerID)
				slog.Info("工作节点已注销", "worker_id", workerID)
			}
		}
	}
}

// GetNextWorker 使用 Round-Robin 获取下一个 Worker
func (d *EtcdDiscovery) GetNextWorker() (string, string, bool) {
	// 收集所有 Worker 信息
	type workerInfo struct {
		id   string
		addr string
	}
	var workers []workerInfo
	d.workers.Range(func(key, value interface{}) bool {
		workers = append(workers, workerInfo{
			id:   key.(string),
			addr: value.(string),
		})
		return true
	})

	if len(workers) == 0 {
		return "", "", false
	}

	// 原子递增计数器，取模得到索引
	idx := atomic.AddUint64(&d.rrIndex, 1)
	w := workers[idx%uint64(len(workers))]
	return w.id, w.addr, true
}

// GetWorkerCount 获取当前 Worker 数量
func (d *EtcdDiscovery) GetWorkerCount() int {
	count := 0
	d.workers.Range(func(key, value interface{}) bool {
		count++
		return true
	})
	return count
}

// IsWorkerActive 检查 Worker 是否在线
func (d *EtcdDiscovery) IsWorkerActive(workerID string) bool {
	_, ok := d.workers.Load(workerID)
	return ok
}
