#ifndef DEEP_OJ_CGROUP_MANAGER_H
#define DEEP_OJ_CGROUP_MANAGER_H

#include <string>
#include <cstdint>
#include <sys/types.h>

namespace deep_oj {

/**
 * @brief Cgroups v2 资源限制管理器
 * 
 * 参考实现:
 * - DOMjudge runguard.cc: memory.max, memory.swap.max, cpuset
 * - Linux Kernel Documentation: cgroups-v2.rst
 * 
 * 核心原理:
 * 1. Cgroups v2 使用统一层次结构 (Unified Hierarchy)
 * 2. 遵循 "No Internal Process Constraint" 规则:
 *    - 只能在叶子节点 (没有子 cgroup 的目录) 添加进程
 *    - 父目录启用控制器后，子目录才能使用这些控制器
 * 
 * 安全防御:
 * - pids.max: 限制进程数，防止 Fork 炸弹
 * - memory.max: 限制内存使用量，防止内存耗尽
 * - memory.swap.max = 0: 禁用 Swap，防止绕过内存限制
 */
class CgroupManager {
public:
    /**
     * @brief 构造函数
     * @param cgroup_root Cgroup 根目录，默认 /sys/fs/cgroup/deep_oj
     * @param job_id 任务 ID，用于创建唯一的子 cgroup
     */
    CgroupManager(const std::string& cgroup_root, const std::string& job_id);
    
    /**
     * @brief 析构函数 - 自动清理资源
     * 
     * 资源在析构函数中自动释放，确保异常场景下也能正确清理 cgroup。
     */
    ~CgroupManager();
    
    // 禁止拷贝
    // 因为我们管理着文件系统资源，拷贝会导致重复释放
    CgroupManager(const CgroupManager&) = delete;
    CgroupManager& operator=(const CgroupManager&) = delete;
    
    // 允许移动
    CgroupManager(CgroupManager&& other) noexcept;
    CgroupManager& operator=(CgroupManager&& other) noexcept;
    
    /**
     * @brief 创建 cgroup 目录并启用控制器
     * 
     * 实现步骤:
     * 1. 确保父目录存在
     * 2. 在父目录的 cgroup.subtree_control 启用 +memory +pids
     * 3. 创建子目录 /sys/fs/cgroup/deep_oj/{job_id}
     * 
     * @return true 如果创建成功
     */
    bool Create();
    
    /**
     * @brief 设置内存限制
     * 
     * 参考 DOMjudge runguard.cc:
     * - memory.max: 内存硬限制 (字节)
     * - memory.swap.max: Swap 限制，设为 0 禁用 Swap
     * 
     * @param bytes 内存限制（字节）
     * @param disable_swap 是否禁用 Swap (强烈建议 true)
     * @return true 如果设置成功
     */
    bool SetMemoryLimit(uint64_t bytes, bool disable_swap = true);
    
    /**
     * @brief 设置进程数限制 (防 Fork 炸弹)
     * 
     * Fork 炸弹示例: while(fork()>=0);
     * 如果不限制进程数，恶意代码可以在毫秒内创建数千个进程，
     * 耗尽系统 PID 空间，导致整个系统无法创建新进程。
     * 
     * @param max_pids 最大进程数（推荐值: 20）
     * @return true 如果设置成功
     */
    bool SetPidsLimit(int max_pids);

    /**
     * @brief 设置 CPU 配额
     * @param max_cores 可用核数上限 (例如 1.0 表示最多 1 核)
     */
    bool SetCPULimit(double max_cores);

    /**
     * @brief 设置 IO 吞吐限制 (io.max)
     * @param read_bps 读限速 (bytes/s), <=0 表示不限制
     * @param write_bps 写限速 (bytes/s), <=0 表示不限制
     * @param target_path 用于定位块设备主次设备号的路径
     */
    bool SetIOLimit(long long read_bps, long long write_bps, const std::string& target_path);
    
    /**
     * @brief 将进程添加到 cgroup
     * 
     * 原理: 向 cgroup.procs 文件写入 PID
     * 注意: 只能向叶子节点添加进程
     * 
     * @param pid 要添加的进程 ID
     * @return true 如果添加成功
     */
    bool AddProcess(pid_t pid);
    
    /**
     * @brief 获取峰值内存使用量
     * 
     * 参考 DOMjudge: 读取 memory.peak (Cgroups v2)
     * 如果不存在，回退到 memory.current
     * 
     * @return 峰值内存使用量（字节）
     */
    uint64_t GetMemoryPeak() const;
    
    /**
     * @brief 获取当前内存使用量
     * @return 当前内存使用量（字节）
     */
    uint64_t GetMemoryCurrent() const;
    
    /**
     * @brief 销毁 cgroup
     * 
     * 参考 DOMjudge cgroup_kill():
     * 1. 读取 cgroup.procs 获取所有进程
     * 2. 向每个进程发送 SIGKILL
     * 3. 等待进程退出
     * 4. 删除 cgroup 目录
     * 
     * 注意: rmdir 只对空的 cgroup 有效，必须先杀死所有进程
     */
    void Destroy();
    
    /**
     * @brief 获取 cgroup 路径
     * @return 完整的 cgroup 路径
     */
    const std::string& GetPath() const { return cgroup_path_; }
    
    /**
     * @brief 检查 cgroup 是否已创建
     * @return true 如果已创建
     */
    bool IsCreated() const { return created_; }
    
    /**
     * @brief 检查系统是否支持 Cgroups v2
     * 
     * 检测方法: 检查 /sys/fs/cgroup 是否为 cgroup2 类型
     * 
     * @return true 如果支持 Cgroups v2
     */
    static bool IsSupported();

private:
    /**
     * @brief 向文件写入值
     * 
     * 用于写入 cgroup 控制文件，如 memory.max, pids.max 等
     * 
     * @param filename 文件名
     * @param value 要写入的值
     * @return true 如果写入成功
     */
    bool WriteToFile(const std::string& filename, const std::string& value);
    
    /**
     * @brief 从文件读取值
     * 
     * 用于读取 cgroup 统计信息，如 memory.current 等
     * 
     * @param filename 文件名
     * @return 文件内容（第一行）
     */
    std::string ReadFromFile(const std::string& filename) const;
    
    /**
     * @brief 杀死 cgroup 内所有进程
     * 
     * 读取 cgroup.procs 并向每个 PID 发送 SIGKILL
     */
    void KillAllProcesses();
    
    /**
     * @brief 确保父目录存在并启用控制器
     * @return true 如果成功
     */
    bool EnsureParentReady();

private:
    std::string cgroup_root_;   ///< cgroup 根目录 (如 /sys/fs/cgroup/deep_oj)
    std::string job_id_;        ///< 任务 ID
    std::string cgroup_path_;   ///< 完整路径 = cgroup_root_ + "/" + job_id_
    bool created_ = false;      ///< 是否已创建
};

} // namespace deep_oj

#endif // DEEP_OJ_CGROUP_MANAGER_H
