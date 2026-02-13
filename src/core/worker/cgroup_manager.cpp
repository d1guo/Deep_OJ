/**
 * @file cgroup_manager.cpp
 * @brief Cgroups v2 资源限制管理器实现
 * 
 * 参考实现:
 * - DOMjudge runguard.cc (ICPC World Finals 使用)
 *   https://github.com/DOMjudge/domjudge/blob/main/judge/runguard.cc
 * 
 * 核心知识点 (面试必备):
 * 
 * 1. Cgroups v2 vs v1 的区别:
 *    - v1: 多层次结构，每个控制器有独立的层次
 *    - v2: 统一层次结构，所有控制器在同一棵树上
 *    - v2 更简洁，避免了资源在不同层次间的冲突
 * 
 * 2. "No Internal Process Constraint" 规则:
 *    - 进程只能存在于叶子节点 (没有子 cgroup 的目录)
 *    - 这确保了资源分配的明确性
 * 
 * 3. 控制器启用流程:
 *    - 在父目录的 cgroup.subtree_control 写入 +controller_name
 *    - 子目录才能使用该控制器
 * 
 * 4. 重要文件:
 *    - cgroup.procs: 存放属于此 cgroup 的进程 PID
 *    - cgroup.subtree_control: 控制子目录可用的控制器
 *    - memory.max: 内存硬限制
 *    - memory.swap.max: Swap 限制
 *    - memory.current: 当前内存使用量
 *    - memory.peak: 峰值内存使用量
 *    - pids.max: 进程数限制
 */

#include "cgroup_manager.h"

#include <fstream>
#include <sstream>
#include <filesystem>
#include <csignal>
#include <unistd.h>
#include <sys/stat.h>
#include <sys/vfs.h>
#include <cstring>
#include <iostream>

// Cgroups v2 文件系统魔数
#ifndef CGROUP2_SUPER_MAGIC
#define CGROUP2_SUPER_MAGIC 0x63677270
#endif

namespace fs = std::filesystem;

namespace deep_oj {

// ============================================================================
// 构造函数与析构函数
// ============================================================================

CgroupManager::CgroupManager(const std::string& root, const std::string& job_id)
    : cgroup_root_(root)
    , job_id_(job_id)
    , created_(false)
{
    // 构建完整路径
    // 示例: /sys/fs/cgroup/deep_oj/job_12345
    cgroup_path_ = cgroup_root_ + "/" + job_id_;
}

CgroupManager::~CgroupManager() {
    // RAII: 析构时自动清理资源
    // 即使程序异常退出，也会调用析构函数 (stack unwinding)
    if (created_) {
        Destroy();
    }
}

// 移动构造函数
CgroupManager::CgroupManager(CgroupManager&& other) noexcept
    : cgroup_root_(std::move(other.cgroup_root_))
    , job_id_(std::move(other.job_id_))
    , cgroup_path_(std::move(other.cgroup_path_))
    , created_(other.created_)
{
    // 标记原对象为"已转移"，防止其析构时再次 Destroy
    other.created_ = false;
}

// 移动赋值运算符
CgroupManager& CgroupManager::operator=(CgroupManager&& other) noexcept {
    if (this != &other) {
        // 先清理自己的资源
        if (created_) {
            Destroy();
        }
        
        cgroup_root_ = std::move(other.cgroup_root_);
        job_id_ = std::move(other.job_id_);
        cgroup_path_ = std::move(other.cgroup_path_);
        created_ = other.created_;
        
        other.created_ = false;
    }
    return *this;
}

// ============================================================================
// 静态方法
// ============================================================================

bool CgroupManager::IsSupported() {
    // 检查 /sys/fs/cgroup 是否为 cgroup2 文件系统
    struct statfs buf;
    if (statfs("/sys/fs/cgroup", &buf) != 0) {
        return false;
    }
    return buf.f_type == CGROUP2_SUPER_MAGIC;
}

// ============================================================================
// 核心功能实现
// ============================================================================

bool CgroupManager::EnsureParentReady() {
    std::error_code ec;
    
    // 1. 确保 cgroup 根目录存在
    if (!fs::exists(cgroup_root_, ec)) {
        // 创建根目录 (例如 /sys/fs/cgroup/deep_oj)
        if (!fs::create_directories(cgroup_root_, ec)) {
            std::cerr << "[CgroupManager] Failed to create root dir: " 
                      << cgroup_root_ << std::endl;
            return false;
        }
    }
    
    // 2. 获取根目录的父目录 (通常是 /sys/fs/cgroup)
    fs::path parent_path = fs::path(cgroup_root_).parent_path();
    std::string subtree_control = parent_path.string() + "/cgroup.subtree_control";
    
    // 3. 在父目录启用控制器
    // 这使得我们的子目录可以使用 memory 和 pids 控制器
    // [Rootless Mode Fix]: 如果我们是普通用户 (通过 setup_cgroups.sh 授权)，
    // 我们可能没有权限写入 /sys/fs/cgroup/cgroup.subtree_control。
    // 但只要运维脚本已经开启了它们，这里失败是可以接受的。
    if (!WriteToFile(subtree_control, "+memory +pids")) {
         std::cerr << "[CgroupManager] Warning: Failed to enable controllers in " << subtree_control 
                   << " (Assuming admin already enabled them)" << std::endl;
    }
    
    // 4. 在我们的根目录也启用控制器 (为了让 job 子目录能用)
    // 这是必须成功的，因为 setup_cgroups.sh 应该把 deep_oj 目录的所有权给了我们
    std::string our_subtree_control = cgroup_root_ + "/cgroup.subtree_control";
    if (!WriteToFile(our_subtree_control, "+memory +pids")) {
        std::cerr << "[CgroupManager] Error: Failed to enable controllers in " << our_subtree_control 
                  << ". Please check ownership or delegation." << std::endl;
        return false;
    }
    
    return true;
}

bool CgroupManager::Create() {
    if (created_) {
        return true;  // 已经创建过了
    }
    
    // 1. 确保父目录准备好
    if (!EnsureParentReady()) {
        std::cerr << "[CgroupManager] Failed to prepare parent directories" << std::endl;
        return false;
    }
    
    // 2. 创建子 cgroup 目录
    std::error_code ec;
    if (!fs::create_directories(cgroup_path_, ec)) {
        if (ec) {
            std::cerr << "[CgroupManager] Failed to create cgroup: " 
                      << cgroup_path_ << " - " << ec.message() << std::endl;
            return false;
        }
    }
    
    created_ = true;
    std::cerr << "[CgroupManager] Created cgroup: " << cgroup_path_ << std::endl;
    return true;
}

bool CgroupManager::SetMemoryLimit(uint64_t bytes, bool disable_swap) {
    if (!created_) {
        std::cerr << "[CgroupManager] Cgroup not created" << std::endl;
        return false;
    }
    
    // 1. 设置 memory.max (硬限制)
    // 超过此限制时，进程会被 OOM killer 终止
    if (!WriteToFile(cgroup_path_ + "/memory.max", std::to_string(bytes))) {
        std::cerr << "[CgroupManager] Failed to set memory.max" << std::endl;
        return false;
    }
    
    // 2. 设置 memory.swap.max (Swap 限制)
    // 设为 0 完全禁用 Swap，防止程序通过 Swap 绕过内存限制
    if (disable_swap) {
        // 注意: 某些系统可能不支持 memory.swap.max，忽略错误
        WriteToFile(cgroup_path_ + "/memory.swap.max", "0");
    }
    
    std::cerr << "[CgroupManager] Set memory limit: " << bytes << " bytes" 
              << (disable_swap ? ", swap disabled" : "") << std::endl;
    return true;
}

bool CgroupManager::SetPidsLimit(int max_pids) {
    if (!created_) {
        std::cerr << "[CgroupManager] Cgroup not created" << std::endl;
        return false;
    }
    
    // 设置 pids.max (进程数限制)
    // 这是防御 Fork 炸弹的关键
    // 推荐值: 20 (足够正常程序使用，但限制了 Fork 炸弹)
    if (!WriteToFile(cgroup_path_ + "/pids.max", std::to_string(max_pids))) {
        std::cerr << "[CgroupManager] Failed to set pids.max" << std::endl;
        return false;
    }
    
    std::cerr << "[CgroupManager] Set pids limit: " << max_pids << std::endl;
    return true;
}

bool CgroupManager::AddProcess(pid_t pid) {
    if (!created_) {
        std::cerr << "[CgroupManager] Cgroup not created" << std::endl;
        return false;
    }
    
    // 向 cgroup.procs 写入 PID
    // 这会将进程移动到此 cgroup，并应用所有资源限制
    if (!WriteToFile(cgroup_path_ + "/cgroup.procs", std::to_string(pid))) {
        std::cerr << "[CgroupManager] Failed to add process " << pid << std::endl;
        return false;
    }
    
    std::cerr << "[CgroupManager] Added process " << pid << " to cgroup" << std::endl;
    return true;
}

uint64_t CgroupManager::GetMemoryPeak() const {
    if (!created_) return 0;
    
    // 尝试读取 memory.peak (Cgroups v2)
    std::string val = ReadFromFile(cgroup_path_ + "/memory.peak");
    if (!val.empty()) {
        try {
            return std::stoull(val);
        } catch (...) {}
    }
    
    // Fallback: 使用 memory.current
    return GetMemoryCurrent();
}

uint64_t CgroupManager::GetMemoryCurrent() const {
    if (!created_) return 0;
    
    std::string val = ReadFromFile(cgroup_path_ + "/memory.current");
    if (!val.empty()) {
        try {
            return std::stoull(val);
        } catch (...) {}
    }
    return 0;
}

void CgroupManager::KillAllProcesses() {
    // 读取 cgroup.procs 获取所有进程 PID
    std::string procs = ReadFromFile(cgroup_path_ + "/cgroup.procs");
    if (procs.empty()) return;
    
    std::istringstream iss(procs);
    pid_t pid;
    int killed = 0;
    
    while (iss >> pid) {
        if (pid > 0) {
            // 发送 SIGKILL (无法被捕获或忽略)
            kill(pid, SIGKILL);
            killed++;
        }
    }
    
    if (killed > 0) {
        std::cerr << "[CgroupManager] Killed " << killed << " processes" << std::endl;
    }
}

void CgroupManager::Destroy() {
    if (!created_) return;
    
    std::cerr << "[CgroupManager] Destroying cgroup: " << cgroup_path_ << std::endl;
    
    // 1. 第一次杀死所有进程
    KillAllProcesses();
    
    // 2. 等待进程退出 (50ms)
    // 进程被 SIGKILL 后需要一点时间来清理
    usleep(50000);
    
    // 3. 再次尝试杀死残留进程
    KillAllProcesses();
    usleep(10000);
    
    // 4. 尝试删除 cgroup 目录
    // rmdir 只对空的 cgroup 有效
    std::error_code ec;
    if (fs::remove(cgroup_path_, ec)) {
        std::cerr << "[CgroupManager] Removed cgroup directory" << std::endl;
    } else {
        // 可能还有子进程没有完全退出，再等一会
        usleep(100000);  // 100ms
        KillAllProcesses();
        usleep(50000);
        fs::remove(cgroup_path_, ec);
    }
    
    created_ = false;
}

// ============================================================================
// 辅助方法
// ============================================================================

bool CgroupManager::WriteToFile(const std::string& path, const std::string& value) {
    std::ofstream ofs(path);
    if (!ofs) {
        std::cerr << "[CgroupManager] Failed to open " << path << " for writing" << std::endl;
        return false;
    }
    ofs << value;
    return ofs.good();
}

std::string CgroupManager::ReadFromFile(const std::string& path) const {
    std::ifstream ifs(path);
    if (!ifs) {
        return "";
    }
    std::string content;
    std::getline(ifs, content);
    return content;
}

} // namespace deep_oj
