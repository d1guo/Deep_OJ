/**
 * @file discovery.go
 * @brief Etcd 服务发现
 * 
 * ===========================================================================
 * Etcd 核心知识点 (面试必备)
 * ===========================================================================
 * 
 * 1. Etcd 是什么?
 *    - 分布式 KV 存储
 *    - 基于 Raft 共识算法
 *    - Kubernetes 的核心组件
 * 
 * 2. Lease (租约) 机制:
 *    - 创建 Lease 并指定 TTL
 *    - 将 Key 与 Lease 绑定
 *    - 定期 KeepAlive 续约
 *    - 不续约则 Key 自动删除
 *    - 用于实现心跳检测
 * 
 * 3. Watch 机制:
 *    - 监听 Key 或前缀的变化
 *    - 基于 MVCC (多版本并发控制)
 *    - 支持历史版本回放
 * 
 * 4. Round-Robin 负载均衡:
 *    - 使用 atomic.AddUint64 实现无锁计数
 *    - 取模得到轮询索引
 *    - 简单高效，适合同构服务
 */
package scheduler

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

const (
	// WorkerPrefix Etcd 中 Worker 注册的前缀
	WorkerPrefix = "/workers/"
)

// EtcdDiscovery Etcd 服务发现
type EtcdDiscovery struct {
	client  *clientv3.Client
	workers sync.Map // map[workerID]workerAddress
	rrIndex uint64   // Round-Robin 计数器
}

// NewEtcdDiscovery 创建 Etcd 服务发现实例
func NewEtcdDiscovery(endpoints []string) (*EtcdDiscovery, error) {
	client, err := clientv3.New(clientv3.Config{
		Endpoints:   endpoints,
		DialTimeout: 5 * time.Second,
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
	// 1. 首先获取现有的 Workers
	resp, err := d.client.Get(ctx, WorkerPrefix, clientv3.WithPrefix())
	if err != nil {
		log.Printf("获取初始节点失败: %v", err)
	} else {
		for _, kv := range resp.Kvs {
			workerID := string(kv.Key)[len(WorkerPrefix):]
			workerAddr := string(kv.Value)
			d.workers.Store(workerID, workerAddr)
			log.Printf("发现工作节点: %s -> %s", workerID, workerAddr)
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
				log.Printf("工作节点已注册: %s -> %s", workerID, workerAddr)

			case clientv3.EventTypeDelete:
				// Worker 注销 (Lease 过期或主动注销)
				d.workers.Delete(workerID)
				log.Printf("工作节点已注销: %s", workerID)
			}
		}
	}
}

// GetNextWorker 使用 Round-Robin 获取下一个 Worker
func (d *EtcdDiscovery) GetNextWorker() (string, bool) {
	// 收集所有 Worker 地址
	var workers []string
	d.workers.Range(func(key, value interface{}) bool {
		workers = append(workers, value.(string))
		return true
	})

	if len(workers) == 0 {
		return "", false
	}

	// 原子递增计数器，取模得到索引
	idx := atomic.AddUint64(&d.rrIndex, 1)
	return workers[idx%uint64(len(workers))], true
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
