#include "sandbox.h"
#include "sandbox_internal.h"
#include "sandbox_isolation.h"

#include <filesystem>
#include <fstream>
#include <format>
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
            return std::format("{}: {}", prefix, std::strerror(errno));
        }

    } // anonymous namespace

    Sandbox::Sandbox(const std::string& temp_dir) : temp_dir_(temp_dir)
    {
        std::error_code ec;
        fs::path root_path(temp_dir_);

        fs::create_directories(root_path, ec);
        if (ec)
        {
            throw std::runtime_error(std::format(
                "Sandbox 初始化失败: 无法创建临时目录 '{}'. 原因: {}", 
                temp_dir_, ec.message()
            ));
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
            result.error_message = std::format("无法创建请求目录 '{}': {}", request_dir.string(), ec.message());
            return result;
        }

        // 2. 写入源代码文件: main.cpp
        fs::path source_file = request_dir / "main.cpp";
        {
            std::ofstream ofs(source_file);
            if (!ofs)
            {
                result.error_message = std::format("无法打开文件进行写入: {}", source_file.string());
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
             result.error_message = std::format("无法设置安全权限: {}", ec.message());
             return result;
        }

        // 3. 准备结果路径
        fs::path exe_file = request_dir / "main";
        fs::path log_file = request_dir / "compile_error.log";

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
        int status;
        const int COMPILE_TIME_LIMIT_MS = 15000; 
        auto start_time = std::chrono::steady_clock::now();
        
        while (true)
        {
            int w = waitpid(pid, &status, WNOHANG);
                
            if (w == -1)
            {
                if (errno == EINTR) continue; 
                result.error_message = FormatSystemError("系统调用 waitpid 失败");
                return result;
            }
            
            if (w != 0)
            {
                break;
            }
            
            auto now = std::chrono::steady_clock::now();
            auto elapsed_ms = std::chrono::duration_cast<std::chrono::milliseconds>(now - start_time).count();
            
            if (elapsed_ms > COMPILE_TIME_LIMIT_MS)
            {
                kill(pid, SIGKILL);
                waitpid(pid, &status, 0);
                
                result.error_message = std::format("编译超时 (> {}ms). 可能存在极度复杂的模板展开。", COMPILE_TIME_LIMIT_MS);
                fs::remove(log_file, ec);
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
                        result.error_message = std::format("g++ 异常退出，状态码: {}", exit_code);
                    }
                }
                else
                {
                    result.error_message = std::format("编译失败 (g++ 退出码: {})，且无法读取错误日志", exit_code);
                }
            }
        }
        else if (WIFSIGNALED(status))
        {
            result.error_message = std::format("编译器异常终止 (Signal: {})", WTERMSIG(status));
        }
        else
        {
            result.error_message = "编译器因未知原因结束";
        }

        fs::remove(log_file, ec); 

        return result;
    }

    RunResult Sandbox::Run(const std::string& exe_path, int time_limit_ms, int memory_limit_kb)
    {
        RunResult result;
        
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
        int status;
        struct rusage usage;

        auto start_time = std::chrono::steady_clock::now();
            
        int real_time_limit_ms = time_limit_ms + 1000;

        while (true)
        {
            pid_t w = wait4(pid, &status, WNOHANG, &usage);

            if (w == -1) 
            {
                if (errno == EINTR) continue; 
                result.status = SandboxStatus::SYSTEM_ERROR;
                return result;
            }

            if (w != 0) 
            {
                break;
            }

            auto now = std::chrono::steady_clock::now();
            int elapsed_ms = std::chrono::duration_cast<std::chrono::milliseconds>(now - start_time).count();

            if (elapsed_ms > real_time_limit_ms)
            {
                kill(pid, SIGKILL);
                wait4(pid, &status, 0, &usage);

                result.status = SandboxStatus::TIME_LIMIT_EXCEEDED;
                result.time_used = elapsed_ms; 
                result.memory_used = usage.ru_maxrss; 
                return result;
            }

            std::this_thread::sleep_for(std::chrono::milliseconds(50));
        }

        double cpu_time_ms = (usage.ru_utime.tv_sec * 1000 + usage.ru_utime.tv_usec / 1000.0) +
                                (usage.ru_stime.tv_sec * 1000 + usage.ru_stime.tv_usec / 1000.0);
        
        result.time_used = static_cast<int>(std::ceil(cpu_time_ms));
        
        result.memory_used = usage.ru_maxrss;

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
                result.error_message = std::format("沙箱恐慌 (退出码 {}): {}", 
                    result.exit_code, GetExitCodeDescription(result.exit_code));
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
            else
            {
                // 其他信号 (SIGSEGV, SIGFPE, SIGABRT, SIGBUS...) 统一归类为 Runtime Error
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
