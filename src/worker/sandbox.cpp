#include "sandbox.h"

#include <filesystem>
#include <fstream>
#include <format>
#include <iostream>
#include <system_error>
#include <thread>
#include <chrono>

// 系统调用头文件
#include <unistd.h>
#include <fcntl.h>
#include <sys/wait.h>
#include <sys/resource.h>
#include <sys/time.h>
#include <cstring>
#include <cmath>

namespace deep_oj {

    namespace fs = std::filesystem;


    namespace
    {
        /**
         * @brief 辅助函数：将 errno 转换为易读的字符串，并拼接前缀
         * 使用 C++20 std::format 进行格式化
         */
        std::string FormatSystemError(const std::string& prefix)
        {
            return std::format("{}: {}", prefix, std::strerror(errno));
        }
    }

    Sandbox::Sandbox(const std::string& temp_dir) : temp_dir_(temp_dir)
    {
        std::error_code ec;
        fs::path root_path(temp_dir_);

        // 尝试创建根目录
        // create_directories 若目录已存在也会返回 true (但在 ec 模式下主要看 ec 是否为 0)
        fs::create_directories(root_path, ec);

        if (ec)
        {
            // 健壮性检查：如果创建失败，必须抛出异常，不能让损坏的 Sandbox 对象被创建
            throw std::runtime_error(std::format(
                "Sandbox 初始化失败: 无法创建临时目录 '{}'. 原因: {}", 
                temp_dir_, ec.message()
            ));
        }
    }

    CompileResult Sandbox::Compile(const std::string& request_id, const std::string& source_code)
    {
        CompileResult result;
        std::error_code ec;

        // 1. 准备请求专属的工作区: /tmp/deep_oj/{request_id}/
        fs::path request_dir = fs::path(temp_dir_) / request_id;
        
        fs::create_directories(request_dir, ec);
        if (ec)
        {
            result.error_message = std::format("无法创建请求目录 '{}': {}", request_dir.string(), ec.message());
            return result;
        }

        // 2. 写入源代码文件: /tmp/deep_oj/{request_id}/main.cpp
        fs::path source_file = request_dir / "main.cpp";
        {
            std::ofstream ofs(source_file);
            if (!ofs)
            {
                result.error_message = std::format("无法打开文件进行写入: {}", source_file.string());
                return result;
            }
            ofs << source_code;
            // ofstream 会在析构时自动关闭，但手动检查写入状态更稳妥
            if (!ofs.good())
            {
                result.error_message = "写入源代码时发生 I/O 错误";
                return result;
            }
        }

        // 3. 准备编译产物路径和日志路径
        fs::path exe_file = request_dir / "main";
        fs::path log_file = request_dir / "compile_error.log";

        // 4. Fork 进程进行编译
        pid_t pid = fork();

        if (pid == -1)
        {
            // Fork 失败（极少见，可能是资源耗尽）
            result.error_message = FormatSystemError("系统调用 fork 失败");
            return result;
        }
        else if (pid == 0)
        {
            // ================= 子进程 (Child Process) =================
            // 在这里，我们变成了 g++ 的化身

            // A. 重定向 stderr 到日志文件
            // 打开（或创建）日志文件，只写、截断、权限 644
            int log_fd = open(log_file.c_str(), O_WRONLY | O_CREAT | O_TRUNC, 0644);
            if (log_fd == -1)
            {
                // 如果连日志文件都打不开，直接退出，父进程会收到异常退出码
                _exit(111);
            }
 
            // 将标准错误 (STDERR_FILENO, 2) 指向 log_fd
            if (dup2(log_fd, STDERR_FILENO) == -1)
            {
                _exit(112);
            }
            
            // 此时 log_fd 已经不需要了（stderr 已经持有该文件的引用）
            close(log_fd);

            // B. 执行 g++ 编译命令
            // 相当于命令行: g++ -std=c++20 /path/to/source -o /path/to/exe
            execlp(
                "g++", "g++", 
                "-std=c++20",                           // 强制 C++20
                "-O2",                                  // 开启优化
                source_file.c_str(), 
                "-o", exe_file.c_str(), 
                nullptr                                 // 参数列表必须以此结尾
            );

            // C. 如果 execlp 返回了，说明执行失败（例如没找到 g++）
            // 注意：这里必须使用 _exit 而不是 exit，避免刷新父进程的缓冲区
            std::cerr << "g++ 启动失败: " << std::strerror(errno) << std::endl; 
            _exit(errno); 
        }
        else
        {
            // ================= 父进程 (Parent Process) =================
            // 等待子进程结束，加入 Watchdog 防止编译超时（如模板元编程死循环）
            int status;
            
            // 编译限时：10秒
            const int COMPILE_TIME_LIMIT_MS = 10000;
            auto start_time = std::chrono::steady_clock::now();
            
            while (true)
            {
                // WNOHANG: 非阻塞等待
                int w = waitpid(pid, &status, WNOHANG);
                
                if (w == -1)
                {
                    result.error_message = FormatSystemError("系统调用 waitpid 失败");
                    return result;
                }
                
                if (w != 0)
                {
                    // 编译器进程已退出
                    break;
                }
                
                // 检查是否超时
                auto now = std::chrono::steady_clock::now();
                auto elapsed_ms = std::chrono::duration_cast<std::chrono::milliseconds>(now - start_time).count();
                
                if (elapsed_ms > COMPILE_TIME_LIMIT_MS)
                {
                    // 编译超时，很可能是遭到了模板攻击
                    kill(pid, SIGKILL);
                    
                    // 收尸，防止僵尸进程
                    waitpid(pid, &status, 0);
                    
                    result.error_message = std::format("编译超时 (> {}ms). 可能存在极度复杂的模板展开。", COMPILE_TIME_LIMIT_MS);
                    // 清理残留文件
                    fs::remove(log_file, ec);
                    return result;
                }
                
                std::this_thread::sleep_for(std::chrono::milliseconds(50));
            }

            // 检查子进程退出状态
            if (WIFEXITED(status))
            {
                int exit_code = WEXITSTATUS(status);
                if (exit_code == 0)
                {
                    // 编译成功
                    result.success = true;
                    result.exe_path = exe_file.string();
                }
                else
                {
                    // 编译失败：g++ 返回了非 0 状态码
                    // 从日志文件中读取错误信息
                    std::ifstream ifs(log_file);
                    if (ifs)
                    {
                        // 一次性读取整个文件内容
                        std::stringstream buffer;
                        buffer << ifs.rdbuf();
                        result.error_message = buffer.str();
                        
                         // 如果日志为空但退出了，手动补一条
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
                // g++ 被信号杀死了（也就是 crash 了）
                result.error_message = std::format("编译器异常终止 (Signal: {})", WTERMSIG(status));
            }
            else
            {
                result.error_message = "编译器因未知原因结束";
            }

            // 5. 清理临时日志文件
            // 无论编译成功与否，错误日志都已经读取（或不需要了），删除之以保持清洁
            // 使用 remove 且不抛异常的版本
            fs::remove(log_file, ec); 
        }

        return result;
    }

    RunResult Sandbox::Run(const std::string& exe_path, int time_limit_ms, int memory_limit_kb)
    {
        RunResult result;
        
        // 1. 准备重定向输出文件
        // 假设输出文件位于可执行文件的同一目录下，名为 "output.txt"
        fs::path exe_dir = fs::path(exe_path).parent_path();
        fs::path output_file = exe_dir / "output.txt";

        // 2. Fork 子进程进行运行
        pid_t pid = fork();

        if (pid == -1)
        {
            result.status = SandboxStatus::SYSTEM_ERROR; // System Error
            return result;
        }
        else if (pid == 0)
        {
            // ================= 子进程 =================

            // A. 重定向标准输出到文件
            int out_fd = open(output_file.c_str(), O_WRONLY | O_CREAT | O_TRUNC, 0644);
            if (out_fd == -1)
            {
                // 如果连输出文件都打不开（如磁盘满），直接视为系统错误退出
                // 使用错误码 120 表示 I/O 准备失败
                _exit(120);
            }

            if (dup2(out_fd, STDOUT_FILENO) == -1)
            {
                 _exit(121); // 重定向失败
            }
            close(out_fd);

            // B. 设置资源限制 (setrlimit)
            
            // B1. CPU 时间限制 (RLIMIT_CPU)
            // setrlimit 的单位是秒，所以需要将毫秒转换为秒并向上取整
            // 如果程序运行超过这个硬限制，内核会发送 SIGXCPU 秒杀它
            rlimit cpu_limit;
            cpu_limit.rlim_cur = (time_limit_ms + 999) / 1000; 
            cpu_limit.rlim_max = cpu_limit.rlim_cur + 1; // 给一秒缓冲，虽然通常直接用硬限制也可以
            setrlimit(RLIMIT_CPU, &cpu_limit);

            // B2. 虚拟内存限制 (RLIMIT_AS)
            // 策略优化 (Level 1.7): "宽进严出"
            // 我们设置一个较宽松的硬限制 (比如 用户限制的 2倍 或 加上 128MB)，
            // 这样程序超一点点内存时不会直接 crash (RE)，而是能继续跑，
            // 最后我们在父进程里检查实际用量来判定 MLE。
            // 这样可以彻底区分 "内存分配失败导致的崩溃" 和 "逻辑错误导致的崩溃"。
            rlimit mem_limit;
            long long hard_mem_limit = (memory_limit_kb * 2 + 1024 * 128) * 1024L; 
            mem_limit.rlim_cur = hard_mem_limit;
            mem_limit.rlim_max = hard_mem_limit;
            setrlimit(RLIMIT_AS, &mem_limit);

            // B3. 输出文件大小限制 (RLIMIT_FSIZE)
            // 防止恶意程序通过无限打印撑爆磁盘
            // 限制为 10MB (10 * 1024 * 1024)
            rlimit fsize_limit;
            fsize_limit.rlim_cur = 10 * 1024 * 1024; 
            fsize_limit.rlim_max = 10 * 1024 * 1024;
            setrlimit(RLIMIT_FSIZE, &fsize_limit);

            // C. 执行用户程序
            execl(exe_path.c_str(), exe_path.c_str(), nullptr);

            // 如果 execl 返回，说明执行失败
            _exit(127);
        }
        else
        {
            // ================= 父进程 =================
            int status;
            struct rusage usage;

            // Watchdog (看门狗) 机制
            // 使用稳态时钟记录开始时间
            auto start_time = std::chrono::steady_clock::now();
            
            // 真实时间限制建议：给 CPU 限制时间多加 1秒 的宽限，或者是 2倍，防止正常程序因调度被误杀
            // 这里我们采用 "limit + 1000ms" 的策略
            int real_time_limit_ms = time_limit_ms + 1000;

            while (true)
            {
                // 1. 非阻塞检查子进程状态 (WNOHANG)
                // 如果子进程状态未改变，wait4 返回 0
                pid_t w = wait4(pid, &status, WNOHANG, &usage);

                if (w == -1) 
                {
                   result.status = SandboxStatus::SYSTEM_ERROR;
                   return result;
                }

                if (w != 0) 
                {
                    // 子进程已结束 (可能是正常退出，也可能是被 setrlimit 杀掉)
                    break;
                }

                // 2. 子进程还在运行，检查是否超时 (Real Time Check)
                auto now = std::chrono::steady_clock::now();
                int elapsed_ms = std::chrono::duration_cast<std::chrono::milliseconds>(now - start_time).count();

                if (elapsed_ms > real_time_limit_ms)
                {
                    // 严重超时 (可能是 sleep 或者是死循环绕过了 CPU 统计)
                    // 物理超度
                    kill(pid, SIGKILL);
                    
                    // 必须收尸！否则变僵尸进程
                    // 这里使用阻塞wait，因为我们已经发了KILL信号，子进程马上就会结束
                    wait4(pid, &status, 0, &usage);

                    result.status = SandboxStatus::TIME_LIMIT_EXCEEDED;
                    result.time_used = elapsed_ms; // 这种情况下记录的是墙上时间作为参考
                    result.memory_used = usage.ru_maxrss; // 仍然尝试获取一下内存
                    return result;
                }

                // 3. 避免空转烧 CPU
                std::this_thread::sleep_for(std::chrono::milliseconds(50));
            }

            // 计算运行时间 (用户态 + 内核态)
            // usage.ru_utime 和 usage.ru_stime 都是 timeval 结构 (sec, usec)
            double cpu_time_ms = (usage.ru_utime.tv_sec * 1000 + usage.ru_utime.tv_usec / 1000.0) +
                                 (usage.ru_stime.tv_sec * 1000 + usage.ru_stime.tv_usec / 1000.0);
            
            result.time_used = static_cast<int>(std::ceil(cpu_time_ms));
            
            // 获取内存使用峰值
            // ru_maxrss 单位：在 Linux 上是 KB
            result.memory_used = usage.ru_maxrss;

            // 优先判定资源超限 (MLE)
            // 只要实际物理内存使用超过了用户设定的限制，无论程序最后是死是活，都判 MLE。
            // 这种逻辑非常公正：你用了那么多资源，这就是犯规。
            if (result.memory_used > memory_limit_kb)
            {
                result.status = SandboxStatus::MEMORY_LIMIT_EXCEEDED;
                return result;
            }

            // 根据退出状态判断结果
            if (WIFEXITED(status))
            {
                // 正常退出
                result.exit_code = WEXITSTATUS(status);
                if (result.exit_code == 0)
                {
                    result.status = SandboxStatus::OK; // OK
                }
                else if (result.exit_code == 120 || result.exit_code == 121 || result.exit_code == 127)
                {
                    // 120/121: I/O 重定向失败
                    // 127: execl 执行用户程序失败（无论是因为文件不存在还是依赖缺失）
                    result.status = SandboxStatus::SYSTEM_ERROR; // System Error
                }
                else
                {
                    result.status = SandboxStatus::RUNTIME_ERROR; // Runtime Error (Non-zero exit code)
                }
            }
            else if (WIFSIGNALED(status))
            {
                // 被信号杀死
                int signal = WTERMSIG(status);
                
                if (signal == SIGXCPU)
                {
                    result.status = SandboxStatus::TIME_LIMIT_EXCEEDED; // Time Limit Exceeded
                }
                else if (signal == SIGSEGV)
                {
                    result.status = SandboxStatus::RUNTIME_ERROR; // Runtime Error (Segfault)
                }
                else if (signal == SIGFPE)
                {
                     result.status = SandboxStatus::RUNTIME_ERROR; // Runtime Error (Float Exception / Div by Zero)
                }
                else if (signal == SIGABRT)
                {
                    // 既然我们已经放宽了内存硬限制并优先检查了 MLE，
                    // 这里的 SIGABRT 就几乎肯定是用户的逻辑问题 (assert失败, throw exception等)，而非 OOM。
                    result.status = SandboxStatus::RUNTIME_ERROR;
                }
                else if (signal == SIGKILL)
                {
                    // 可能是超过时间被系统强杀，也可能是超过内存被 OOM Killer 杀掉
                    // 在 setrlimit 场景下，CPU 超限通常是 SIGXCPU
                    // 内存超限通常会导致 malloc 失败或者被 OOM（如果用了 Cgroup），但单纯 RLIMIT_AS 会导致申请内存返回 null
                    // 这里我们为了保险，如果时间已经超过了限制，优先判 TLE
                    if (result.time_used > time_limit_ms)
                    {
                         result.status = SandboxStatus::TIME_LIMIT_EXCEEDED; // TLE
                    }
                    else if (result.memory_used > memory_limit_kb)
                    {
                         result.status = SandboxStatus::MEMORY_LIMIT_EXCEEDED; // Memory Limit Exceeded
                    }
                    else
                    {
                        result.status = SandboxStatus::RUNTIME_ERROR; // Runtime Error (Signal Killed but unknown reason)
                    }
                }
                else if (signal == SIGXFSZ) {
                     result.status = SandboxStatus::RUNTIME_ERROR; // OLE 被视为运行时错误的一种，或者单独定义状态
                }
                else
                {
                    result.status = SandboxStatus::RUNTIME_ERROR; // Other Runtime Error
                }
            }
            else
            {
                result.status = SandboxStatus::SYSTEM_ERROR; // Unknown System Error
            }
        }

        return result;
    }
}
