#ifndef DEEP_OJ_ETCD_REGISTRY_H
#define DEEP_OJ_ETCD_REGISTRY_H

#include <string>
#include <thread>
#include <atomic>

namespace deep_oj {

/**
 * @brief Etcd 服务注册器
 * 
 * ===========================================================================
 * 核心知识点 (面试必备)
 * ===========================================================================
 * 
 * 1. 服务注册流程:
 *    a) Worker 启动时向 Etcd 注册自己的地址
 *    b) 创建 Lease (租约) 并设置 TTL
 *    c) 将 KV 与 Lease 绑定
 *    d) 后台线程定期 KeepAlive 续约
 * 
 * 2. 健康检查机制:
 *    - 如果 Worker 崩溃，无法续约
 *    - Lease 过期后，Etcd 自动删除 Key
 *    - Scheduler 通过 Watch 感知 Worker 下线
 * 
 * 3. 为什么不用配置文件?
 *    - 静态配置需要重启才能生效
 *    - Worker 动态扩缩容时需要手动更新
 *    - Etcd 提供实时、自动的服务发现
 * 
 * 使用示例:
 *   EtcdRegistry registry("localhost:2379");
 *   registry.Register("worker-1", "192.168.1.100:50051");
 *   // ... 工作 ...
 *   registry.Deregister();
 */
class EtcdRegistry {
public:
    /**
     * @brief 构造函数
     * @param etcd_endpoints Etcd 地址 (逗号分隔)
     * @param ttl_seconds Lease TTL (秒)
     */
    explicit EtcdRegistry(const std::string& etcd_endpoints, int ttl_seconds = 5);
    
    /**
     * @brief 析构函数 - 自动注销
     */
    ~EtcdRegistry();
    
    // 禁止拷贝
    EtcdRegistry(const EtcdRegistry&) = delete;
    EtcdRegistry& operator=(const EtcdRegistry&) = delete;
    
    /**
     * @brief 向 Etcd 注册 Worker
     * 
     * 注册后会启动后台线程进行心跳续约
     * 
     * @param worker_id Worker 唯一标识
     * @param address Worker gRPC 地址 (host:port)
     * @return true 如果注册成功
     */
    bool Register(const std::string& worker_id, const std::string& address);
    
    /**
     * @brief 主动注销
     * 
     * 会停止心跳线程并删除 Etcd 中的 Key
     */
    void Deregister();
    
    /**
     * @brief 检查是否已注册
     */
    bool IsRegistered() const { return registered_; }

private:
    /**
     * @brief 心跳续约线程
     */
    void KeepAliveLoop();

private:
    std::string etcd_endpoints_;
    int ttl_seconds_;
    std::string key_;
    int64_t lease_id_ = 0;
    
    std::atomic<bool> registered_{false};
    std::atomic<bool> running_{false};
    std::thread keepalive_thread_;
};

} // namespace deep_oj

#endif // DEEP_OJ_ETCD_REGISTRY_H
