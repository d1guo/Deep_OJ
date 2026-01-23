#include "sandbox.h"
#include "sandbox_internal.h"
#include "sandbox_isolation.h"

#include <filesystem>
#include <fstream>
#include <iostream>
#include <system_error>
#include <thread>
#include <chrono>
#include <cstring>
#include <cmath>
#include <memory>
#include <vector>
#include <cctype>

// 父进程使用的系统调用
#include <unistd.h>
#include <sys/wait.h>
#include <sys/resource.h>
#include <sched.h> 
#include <signal.h>

namespace deep_oj
{

    namespace fs = std::filesystem;

    namespace {

        bool IsValidRequestId(const std::string& id)
        {
            if (id.empty() || id.length() > 64) return false;
            for (char c : id) {
                if (!std::isalnum(c) && c != '_' && c != '-') return false;
            }
            return true;
        }

        std::string GetExitCodeDescription(int code)
        {
            switch (code)
            {
                // Stage 1
                case ERR_OPEN_OUTPUT:   return "无法打开输出文件 (IO Redirect)";
                case ERR_DUP2:          return "dup2 调用失败 (IO Redirect)";
                case ERR_EXEC_FAILED:   return "execle 调用失败 (程序启动失败)";
                case ERR_CHDIR_FAILED:  return "chdir 切换工作目录失败";
                case ERR_SETGID_FAILED: return "setgid 失败 (降权失败)";
                case ERR_SETUID_FAILED: return "setuid 失败 (降权失败)";

                // Stage 2
                case ERR_RLIMIT_CPU:    return "setrlimit(CPU) 失败";
                case ERR_RLIMIT_MEMORY: return "setrlimit(AS) 失败";
                case ERR_RLIMIT_STACK:  return "setrlimit(STACK) 失败";
                case ERR_RLIMIT_NPROC:  return "setrlimit(NPROC) 失败";
                case ERR_RLIMIT_FSIZE:  return "setrlimit(FSIZE) 失败";

                // Stage 3
                case ERR_MOUNT_PRIVATE:   return "mount --make-rprivate 失败";
                case ERR_MOUNT_BIND_SELF: return "bind mount 工作目录失败";
                case ERR_MOUNT_BIND_LIB:  return "bind mount 系统库失败";
                case ERR_REMOUNT_RO:      return "remount 系统库 (RO) 失败";
                case ERR_PIVOT_ROOT:      return "pivot_root 系统调用失败";
                case ERR_CHDIR_NEW_ROOT:  return "切换到新根目录失败";
                case ERR_UMOUNT_OLD:      return "umount /old_root 失败";
                case ERR_MOUNT_PROC:      return "mount /proc 失败";
                case ERR_MOUNT_TMP:       return "mount /tmp (tmpfs) 失败";
                case ERR_MKDIR_FAILED:    return "mkdir (构建 Rootfs) 失败";
                case ERR_SANDBOX_EXCEPTION: return "沙箱内部异常 (C++ Throw)";

                default: return "与系统相关的未知错误";
            }
        }

        std::string FormatSystemError(const std::string& prefix)
        {
            return prefix + ": " + std::strerror(errno);
        }

        // RAII helpers for parent process management (只在父进程使用，允许 C++ 特性)
        class ProcessGuard
        {
        public:
            explicit ProcessGuard(pid_t pid) : pid_(pid), released_(false) {}

            // 禁止拷贝
            ProcessGuard(const ProcessGuard&) = delete;
            ProcessGuard& operator=(const ProcessGuard&) = delete;

            // 移动构造：窃取资源并将 other 置为安全状态
            ProcessGuard(ProcessGuard&& other) noexcept
                : pid_(other.pid_), released_(other.released_)
            {
                other.pid_ = -1;
                other.released_ = true;
            }

            // 移动赋值：先清理自身资源（如果有），然后窃取 other 并置为安全状态
            ProcessGuard& operator=(ProcessGuard&& other) noexcept
            {
                if (this != &other) {
                    if (!released_ && pid_ > 0) {
                        ::kill(pid_, SIGKILL);
                        int status;
                        for (;;) {
                            pid_t r = waitpid(pid_, &status, 0);
                            if (r == -1) {
                                if (errno == EINTR) continue;
                                break;
                            }
                            break;
                        }
                    }

                    pid_ = other.pid_;
                    released_ = other.released_;

                    other.pid_ = -1;
                    other.released_ = true;
                }
                return *this;
            }

            ~ProcessGuard()
            {
                if (released_ || pid_ <= 0) return;

                int status;
                // 检查子进程是否已经退出
                pid_t w = waitpid(pid_, &status, WNOHANG);
                if (w == 0) {
                    // 仍在运行，尝试强杀并回收
                    ::kill(pid_, SIGKILL);
                    // 阻塞等待回收（忽略 EINTR）
                    for (;;) {
                        pid_t r = waitpid(pid_, &status, 0);
                        if (r == -1) {
                            if (errno == EINTR) continue;
                            break;
                        }
                        break;
                    }
                }
                // w > 0 或者 ECHILD 表示已经退出/被回收
            }

            // 非阻塞地检查是否退出，返回 waitpid 的返回值 (pid, 0, -1)
            pid_t wait_nonblock(int &status)
            {
                if (pid_ <= 0) return -1;
                for (;;) {
                    pid_t w = waitpid(pid_, &status, WNOHANG);
                    if (w == -1 && errno == EINTR) continue;
                    return w;
                }
            }

            // 非阻塞获取 rusage
            pid_t wait_nonblock_rusage(int &status, struct rusage *usage)
            {
                if (pid_ <= 0) return -1;
                for (;;) {
                    pid_t w = wait4(pid_, &status, WNOHANG, usage);
                    if (w == -1 && errno == EINTR) continue;
                    return w;
                }
            }

            // 阻塞等待并返回 (用于超时后强制回收)
            pid_t wait(int &status)
            {
                if (pid_ <= 0) return -1;
                for (;;) {
                    pid_t w = waitpid(pid_, &status, 0);
                    if (w == -1 && errno == EINTR) continue;
                    return w;
                }
            }

            // 阻塞等待并收集 rusage
            pid_t wait_rusage(int &status, struct rusage *usage)
            {
                if (pid_ <= 0) return -1;
                for (;;) {
                    pid_t w = wait4(pid_, &status, 0, usage);
                    if (w == -1 && errno == EINTR) continue;
                    return w;
                }
            }

            bool kill()
            {
                if (pid_ <= 0) return false;
                return ::kill(pid_, SIGKILL) == 0;
            }

            void release()
            {
                released_ = true;
                pid_ = -1;
            }

        private:
            pid_t pid_;
            bool released_;
        };

        class DirectoryGuard
        {
        public:
            explicit DirectoryGuard(fs::path p) : path_(std::move(p)), committed_(false) {}

            // 禁止拷贝
            DirectoryGuard(const DirectoryGuard&) = delete;
            DirectoryGuard& operator=(const DirectoryGuard&) = delete;

            // 移动构造：窃取 path 并将 other 标记为已提交以防止析构时删除
            DirectoryGuard(DirectoryGuard&& other) noexcept
                : path_(std::move(other.path_)), committed_(other.committed_)
            {
                other.committed_ = true;
            }

            // 移动赋值：先清理自身未提交的目录，然后窃取 other
            DirectoryGuard& operator=(DirectoryGuard&& other) noexcept
            {
                if (this != &other) {
                    if (!committed_) {
                        std::error_code ec;
                        if (fs::exists(path_, ec)) {
                            fs::remove_all(path_, ec);
                        }
                    }

                    path_ = std::move(other.path_);
                    committed_ = other.committed_;
                    other.committed_ = true;
                }
                return *this;
            }

            ~DirectoryGuard()
            {
                if (committed_) return;
                std::error_code ec;
                if (fs::exists(path_, ec)) {
                    fs::remove_all(path_, ec);
                }
            }

            void commit() { committed_ = true; }
        private:
            fs::path path_;
            bool committed_;
        };

    } // anonymous namespace

    Sandbox::Sandbox(const std::string& temp_dir) : temp_dir_(temp_dir)
    {
        std::error_code ec;
        fs::path root_path(temp_dir_);

        fs::create_directories(root_path, ec);
        if (ec)
        {
            throw std::runtime_error("Sandbox 初始化失败: 无法创建根临时目录 '" + temp_dir_ + "': " + ec.message());
        }
    }

    CompileResult Sandbox::Compile(const std::string& request_id, const std::string& source_code)
    {
        CompileResult result;

        if (!IsValidRequestId(request_id))
        {
             std::cerr << "[Security] 严重警告: 检测到非法 request_id '" << request_id << "'。请求已拒绝。\n";
             result.error_message = "安全违规: 请求 ID 包含非法字符 (潜在路径遍历攻击)";
             result.success = false;
             return result;
        }

        std::error_code ec;

        // 1. 准备工作区: /tmp/deep_oj/{request_id}/
        fs::path request_dir = fs::path(temp_dir_) / request_id;
        
        fs::create_directories(request_dir, ec);
        if (ec)
        {
            result.error_message = "无法创建请求目录 '" + request_dir.string() + "': " + ec.message();
        }

        // 2. 写入源代码文件: main.cpp
        fs::path source_file = request_dir / "main.cpp";
        {
            std::ofstream ofs(source_file);
            if (!ofs)
            {
                result.error_message = "无法打开文件进行写入: " + source_file.string();
                return result;
            }
            ofs << source_code;
            if (!ofs.good())
            {
                result.error_message = "写入源代码时发生 I/O 错误";
                return result;
            }
        }

        // [安全修复]: 更改目录所有者为 nobody (UID 65534)，并限制权限为 700
        // 700 : 所有者（Owner）拥有完全控制权。同组/系统其他用户没有任何权限。
        // 这样只有 nobody 用户可以访问该目录，防止 Time-of-Check to Time-of-Use 攻击
        if (chown(request_dir.c_str(), 65534, 65534) != 0)
        {
             result.error_message = FormatSystemError("无法修改目录所有者为 nobody");
             return result;
        }
        
        // 仅允许所有者 (nobody) 读写执行
        fs::permissions(request_dir, fs::perms::owner_all, ec);
        if (ec)
        {
            result.error_message = "无法设置目录权限: " + ec.message();
            return result;
        }

        // 3. 准备结果路径
        fs::path exe_file = request_dir / "main";
        fs::path log_file = request_dir / "compile_error.log";

        // RAII guard: 保证异常或早退时临时目录被清理
        DirectoryGuard dir_guard(request_dir);

        // 4. 调用 Clone (隔离层)
        CompileArgs args;
        strncpy(args.source_path, source_file.c_str(), 255); args.source_path[255] = 0;
        strncpy(args.exe_path, exe_file.c_str(), 255); args.exe_path[255] = 0;
        strncpy(args.log_path, log_file.c_str(), 255); args.log_path[255] = 0;

        auto stack_mem = std::make_unique<char[]>(STACK_SIZE);
        char* stack_top = stack_mem.get() + STACK_SIZE;

        pid_t pid = clone(CompileChildFn, stack_top, 
                        CLONE_NEWPID | CLONE_NEWNS | CLONE_NEWIPC | CLONE_NEWUTS | SIGCHLD | CLONE_NEWNET,
                        &args);
        
        if (pid == -1)
        {
            result.error_message = FormatSystemError("系统调用 clone (compile) 失败");
            return result;
        }

        // ================= 父进程 =================
        // RAII guard for child process lifecycle
        ProcessGuard proc(pid);

        int status;
        const int COMPILE_TIME_LIMIT_MS = g_runner_config.compile_real_limit * 1000; // milliseconds
        auto start_time = std::chrono::steady_clock::now();
        
        while (true)
        {
            int w = proc.wait_nonblock(status);
                
            if (w == -1)
            {
                if (errno == EINTR) continue; 
                result.error_message = FormatSystemError("系统调用 waitpid 失败");
                return result;
            }
            
            if (w != 0)
            {
                proc.release();
                break;
            }
            
            auto now = std::chrono::steady_clock::now();
            auto elapsed_ms = std::chrono::duration_cast<std::chrono::milliseconds>(now - start_time).count();
            
            if (elapsed_ms > COMPILE_TIME_LIMIT_MS)
            {
                proc.kill();
                proc.wait(status);
                result.error_message = "编译超时 (> " + std::to_string(COMPILE_TIME_LIMIT_MS) + "ms). 可能存在极度复杂的模板展开。";
                return result;
            }
            
            std::this_thread::sleep_for(std::chrono::milliseconds(50));
        }

        // 检查退出状态
        if (WIFEXITED(status))
        {
            int exit_code = WEXITSTATUS(status);
            if (exit_code == 0)
            {
                // 编译成功：保留 request_dir (可供后续执行使用)
                dir_guard.commit();
                result.success = true;
                result.exe_path = exe_file.string();
            }
            else
            {
                std::ifstream ifs(log_file);
                if (ifs)
                {
                    std::stringstream buffer;
                    buffer << ifs.rdbuf();
                    result.error_message = buffer.str();
                    
                    if (result.error_message.empty()) {
                        result.error_message = "g++ 异常退出，状态码: " + std::to_string(exit_code);
                    }
                }
                else
                {
                    result.error_message = "编译失败 (g++ 退出码: " + std::to_string(exit_code) + ")，且无法读取错误日志";
                }
            }
        }
        else if (WIFSIGNALED(status))
        {
            result.error_message = "编译器异常终止 (Signal: " + std::to_string(WTERMSIG(status)) + ")";
        }
        else
        {
            result.error_message = "编译器因未知原因结束";
        }

        return result;
    }

    RunResult Sandbox::Run(const std::string& exe_path, int time_limit_ms, int memory_limit_kb)
    {
        RunResult result;
        
        // Prepare output path: child writes to /output.txt relative to sandbox root
        fs::path output_path = fs::path(exe_path).parent_path() / "output.txt";
        const uint64_t OUTPUT_LIMIT_BYTES = 10ULL * 1024ULL * 1024ULL; // 10MB logical limit

        // 1. 准备参数与栈
        RunChildArgs args;
        std::strncpy(args.exe_path, exe_path.c_str(), sizeof(args.exe_path) - 1);
        args.exe_path[sizeof(args.exe_path) - 1] = '\0';
        
        args.time_limit_ms = time_limit_ms;
        args.memory_limit_kb = memory_limit_kb;
        
        auto stack_mem = std::make_unique<char[]>(STACK_SIZE);
        char* stack_top = stack_mem.get() + STACK_SIZE; 

        // 2. Clone 子进程 (隔离层)
        pid_t pid = clone(RunChildFn, stack_top, CLONE_NEWPID | CLONE_NEWNS | CLONE_NEWNET | CLONE_NEWIPC | CLONE_NEWUTS | SIGCHLD, &args);

        if (pid == -1)
        {
            result.status = SandboxStatus::SYSTEM_ERROR; 
            std::cerr << "[Sandbox] Clone failed: " << std::strerror(errno) << std::endl;
            return result;
        }
        
        // ================= 父进程 =================
        // RAII guard for child
        ProcessGuard proc(pid);

        int status;
        struct rusage usage{};

        auto start_time = std::chrono::steady_clock::now();
            
        int real_time_limit_ms = time_limit_ms + 1000;

        // Flag to mark that parent killed process for MLE (avoid ambiguous interpretation later)
        bool parent_killed_for_mle = false;

        while (true)
        {
            pid_t w = proc.wait_nonblock_rusage(status, &usage);

            if (w == -1)
            {
                if (errno == EINTR) continue; 
                result.status = SandboxStatus::SYSTEM_ERROR;
                return result;
            }

            if (w != 0) 
            {
                proc.release();
                break;
            }

            // Real-time MLE detection: if ru_maxrss exceeds logical limit, kill and mark MLE
            if (usage.ru_maxrss > memory_limit_kb)
            {
                proc.kill();
                proc.wait_rusage(status, &usage);
                proc.release();

                result.status = SandboxStatus::MEMORY_LIMIT_EXCEEDED;
                result.time_used = static_cast<int>(std::chrono::duration_cast<std::chrono::milliseconds>(
                    std::chrono::steady_clock::now() - start_time).count());
                result.memory_used = usage.ru_maxrss;
                parent_killed_for_mle = true;
                return result;
            }

            auto now = std::chrono::steady_clock::now();
            int elapsed_ms = std::chrono::duration_cast<std::chrono::milliseconds>(now - start_time).count();

            if (elapsed_ms > real_time_limit_ms)
            {
                proc.kill();
                proc.wait_rusage(status, &usage);
                proc.release();

                result.status = SandboxStatus::TIME_LIMIT_EXCEEDED;
                result.time_used = elapsed_ms; 
                result.memory_used = usage.ru_maxrss; 
                return result;
            }

            // Adaptive polling: if process is close to memory limit, poll faster to reduce risk window
            double mem_ratio = 0.0;
            if (memory_limit_kb > 0) mem_ratio = usage.ru_maxrss / (double)memory_limit_kb;
            if (mem_ratio > 0.8) {
                std::this_thread::sleep_for(std::chrono::milliseconds(10)); // High alert mode
            } else {
                std::this_thread::sleep_for(std::chrono::milliseconds(50)); // Normal mode
            }
        }

        double cpu_time_ms = (usage.ru_utime.tv_sec * 1000 + usage.ru_utime.tv_usec / 1000.0) +
                                (usage.ru_stime.tv_sec * 1000 + usage.ru_stime.tv_usec / 1000.0);
        
        result.time_used = static_cast<int>(std::ceil(cpu_time_ms));
        
        result.memory_used = usage.ru_maxrss;

        // Post-exit Output Limit check (robust OLE detection)
        std::error_code ec;
        if (fs::exists(output_path, ec)) {
            uint64_t out_size = 0;
            out_size = fs::file_size(output_path, ec);
            if (!ec && out_size >= OUTPUT_LIMIT_BYTES) {
                result.status = SandboxStatus::OUTPUT_LIMIT_EXCEEDED;
                result.memory_used = usage.ru_maxrss;
                return result;
            }
        }

        // If parent already killed for MLE we returned earlier; otherwise continue
        if (result.memory_used > memory_limit_kb)
        {
            result.status = SandboxStatus::MEMORY_LIMIT_EXCEEDED;
            return result;
        }

        if (WIFEXITED(status))
        {
            result.exit_code = WEXITSTATUS(status);
            if (result.exit_code == 0)
            {
                result.status = SandboxStatus::OK; 
            }
            else if (result.exit_code >= 120) 
            {
                result.status = SandboxStatus::SYSTEM_ERROR; 
                result.error_message = "沙箱恐慌 (退出码 " + std::to_string(result.exit_code) + "): " + GetExitCodeDescription(result.exit_code);
            }
            else
            {
                result.status = SandboxStatus::RUNTIME_ERROR; 
            }
        }
        else if (WIFSIGNALED(status))
        {
            int signal = WTERMSIG(status);
            
            if (signal == SIGXCPU)
            {
                result.status = SandboxStatus::TIME_LIMIT_EXCEEDED; 
            }
            else if (signal == SIGKILL)
            {
                // SIGKILL 可能是因为 MLE 导致的 OOM，也可能是超时被我们手动杀掉
                if (result.time_used > time_limit_ms)
                {
                    result.status = SandboxStatus::TIME_LIMIT_EXCEEDED; 
                }
                else if (result.memory_used > memory_limit_kb)
                {
                    result.status = SandboxStatus::MEMORY_LIMIT_EXCEEDED; 
                }
                else
                {
                    result.status = SandboxStatus::RUNTIME_ERROR; 
                }
            }
            else if (signal == SIGXFSZ) {
                 // 文件大小超限 (Output Limit Exceeded)
                 result.status = SandboxStatus::OUTPUT_LIMIT_EXCEEDED; 
            }
            else if (signal == SIGABRT || signal == SIGSEGV) {
                // Heuristic: if process aborted or segfaulted while memory usage close to limit, treat as MLE
                if (result.memory_used >= static_cast<int>(std::ceil(memory_limit_kb * 0.95))) {
                    result.status = SandboxStatus::MEMORY_LIMIT_EXCEEDED;
                } else {
                    result.status = SandboxStatus::RUNTIME_ERROR;
                }
            }
            else
            {
                // 其他信号 (SIGFPE, SIGBUS...) 统一归类为 Runtime Error
                result.status = SandboxStatus::RUNTIME_ERROR;
            }
        }
        else
        {
            result.status = SandboxStatus::SYSTEM_ERROR; 
        }
    
        return result;
    }

    void Sandbox::Cleanup(const std::string& request_id)
    {
        if (request_id.empty()) return;
        
        // 双重校验，尽管 Compile 里校验过，这里再校验一次防止意外调用
        if (!IsValidRequestId(request_id)) return;

        std::error_code ec;
        fs::path request_dir = fs::path(temp_dir_) / request_id;

        if (fs::exists(request_dir, ec))
        {
            fs::remove_all(request_dir, ec);
            if (ec)
            {
                std::cerr << "[Sandbox] Cleanup Warning: Failed to remove " << request_dir << ": " << ec.message() << std::endl;
            }
        }
    }

} // namespace deep_oj
