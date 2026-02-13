#include "sandbox_isolation.h"
#include "sandbox_internal.h"
#include "seccomp_rules.h"

#include <unistd.h>
#include <fcntl.h>
#include <grp.h>
#include <sys/stat.h>
#include <sys/mount.h>
#include <sys/syscall.h>
#include <sys/resource.h>
#include <sys/prctl.h>
#include <cstring>
#include <cstdio>
#include <cerrno>

// 严重警告: 必须严格遵守 Async-Signal-Safe C 风格
// 禁止使用: malloc/new, exceptions, STL, iostream
// 只能使用: glibc 系统调用, stack memory, snprintf 等。

namespace deep_oj {

    namespace {
        
        // 确保父目录存在
        static void EnsureParentDir(const char* path, const char* base)
        {
            char tmp[512];
            strncpy(tmp, path, sizeof(tmp) - 1);
            tmp[sizeof(tmp)-1] = '\0';
            char* p = strrchr(tmp, '/');
            if (!p) return;
            *p = '\0'; // tmp is parent dir now

            size_t base_len = strlen(base);
            if (base_len == 0) return;
            if (strncmp(tmp, base, base_len) != 0) return;

            size_t len = strlen(tmp);
            for (size_t i = base_len + 1; i <= len; ++i) {
                if (tmp[i] == '\0' || tmp[i] == '/') {
                    char dirbuf[512];
                    memcpy(dirbuf, tmp, i);
                    dirbuf[i] = '\0';
                    if (mkdir(dirbuf, 0755) == -1 && errno != EEXIST) _exit(ERR_MKDIR_FAILED);
                }
            }
        }

        // 辅助函数：安全地设置 Rootfs (Pivot Root)
        // 必须 static，限制在当前编译单元内
        void SetupRootfs(const char* work_dir)
        {
            // 1. 设置挂载传播为 Private (防止污染宿主机)
            if (mount(nullptr, "/", nullptr, MS_PRIVATE | MS_REC, nullptr) == -1)
            {
                _exit(ERR_MOUNT_PRIVATE);
            }

            // 2. 将工作目录 Bind Mount 到自身 (pivot_root 的要求: 不能是 rootfs)
            if (mount(work_dir, work_dir, nullptr, MS_BIND | MS_REC, nullptr) == -1)
            {
                _exit(ERR_MOUNT_BIND_SELF);
            }

            // 3. 挂载目录配置 (mount_dirs)
            char target[512];
            for (int i = 0; i < g_runner_config.mount_count; ++i)
            {
                const char* src = g_runner_config.mount_dirs[i];
                if (access(src, F_OK) != 0) {
                    fprintf(stderr, "[Sandbox] Config Error: Mount dir not found: %s\n", src);
                    _exit(ERR_MOUNT_BIND_LIB);
                }

                int n = snprintf(target, sizeof(target), "%s%s", work_dir, src);
                if (n >= (int)sizeof(target)) _exit(ERR_MKDIR_FAILED);

                // 传入 work_dir 作为基准
                EnsureParentDir(target, work_dir); 

                // A. 创建占位目录
                if (mkdir(target, 0755) == -1 && errno != EEXIST) _exit(ERR_MKDIR_FAILED);

                // B. 绑定挂载
                if (mount(src, target, nullptr, MS_BIND | MS_REC, nullptr) == -1) _exit(ERR_MOUNT_BIND_LIB);
                // C. 重新挂载为只读
                if (mount(src, target, nullptr, MS_BIND | MS_REC | MS_RDONLY | MS_REMOUNT, nullptr) == -1) _exit(ERR_REMOUNT_RO);
            }

            // 4. 挂载文件配置 (mount_files)
            for (int i = 0; i < g_runner_config.mount_file_count; ++i)
            {
                const char* src = g_runner_config.mount_files[i];
                if (access(src, F_OK) != 0) {
                     fprintf(stderr, "[Sandbox] Config Error: Mount file not found: %s\n", src);
                     _exit(ERR_MOUNT_BIND_LIB);
                }

                snprintf(target, sizeof(target), "%s%s", work_dir, src);

                // [核心修复]: 这里也要传入 work_dir！
                EnsureParentDir(target, work_dir);

                // B. 文件占位: open(O_CREAT) -> touch
                int fd = open(target, O_CREAT | O_RDWR, 0666);
                if (fd != -1) close(fd);
                else if (errno != EEXIST) _exit(ERR_MKDIR_FAILED);

                // C. Bind Mount (文件通常不需要 MS_REC)
                if (mount(src, target, nullptr, MS_BIND, nullptr) == -1) _exit(ERR_MOUNT_BIND_LIB);
                // D. Remount RO
                if (mount(src, target, nullptr, MS_BIND | MS_REMOUNT | MS_RDONLY, nullptr) == -1) _exit(ERR_REMOUNT_RO);
            }

            // 5. Pivot Root
            char old_root[512];
            snprintf(old_root, sizeof(old_root), "%s/old_root", work_dir);
            if (mkdir(old_root, 0755) == -1 && errno != EEXIST) _exit(ERR_MKDIR_FAILED);

            if (syscall(SYS_pivot_root, work_dir, old_root) == -1) _exit(ERR_PIVOT_ROOT);
            if (chdir("/") == -1) _exit(ERR_CHDIR_NEW_ROOT);
            if (umount2("/old_root", MNT_DETACH) == -1) _exit(ERR_UMOUNT_OLD);
            if (rmdir("old_root") == -1 && errno != EEXIST && errno != EBUSY) {}
        }

    } // anonymous namespace

    int RunChildFn(void* arg)
    {
        auto* args = (RunChildArgs*)(arg);

        // 🔥 [DEBUG] 子进程一上来就打印到屏幕 (fd 1 和 2 此时还没被重定向)
        // 必须使用 write 系统调用，不能用 cout
        // char buf[128];
        // int len = snprintf(buf, sizeof(buf), 
        //     "\n>>> DEBUG_CHILD: Received limit: %lu <<<\n", 
        //     (unsigned long)args->output_limit_bytes);
        // if (len > 0) write(2, buf, len); // 写到 stderr (屏幕)
        // -----------------------------------------------------
        // 1. IO 重定向 (最先执行)
        // -----------------------------------------------------
        // 此时我们还在父进程的文件系统命名空间中，父进程传来的 FD 是有效的。
        // 父进程已经处理好所有 FD（包括 fallback 到 /dev/null），这里禁止 open。
        
        // (A) 设置 Stdin
        if (dup2(args->input_fd, STDIN_FILENO) == -1) _exit(101);

        // (B) 设置 Stdout
        if (dup2(args->output_fd, STDOUT_FILENO) == -1) _exit(102);

        // (C) 设置 Stderr
        if (dup2(args->error_fd, STDERR_FILENO) == -1) _exit(103);

        
        // [安全]: 关闭除 0,1,2 以外的所有文件描述符
        bool close_range_success = false;
        
        #ifdef __NR_close_range
            // 尝试使用高效的 close_range
            // 如果返回 -1 且 errno == ENOSYS (内核不支持)，则回退到循环关闭
            if (syscall(__NR_close_range, 3, ~0U, 0) == 0) {
                close_range_success = true;
            }
        #endif

        if (!close_range_success) {
            // Fallback: 传统循环关闭
            // 获取最大 FD 限制，防止循环过大
            int max_fd = (int)sysconf(_SC_OPEN_MAX);
            if (max_fd < 0) max_fd = 4096;
            if (max_fd > 65536) max_fd = 65536; // 限制上限，防止耗时过久

            for (int fd = 3; fd < max_fd; ++fd) {
                close(fd); 
            }
        }

        
        // -----------------------------------------------------
        // 3. 准备工作区路径
        // -----------------------------------------------------
        char work_dir[256];
        strncpy(work_dir, args->exe_path, sizeof(work_dir) - 1);
        work_dir[sizeof(work_dir)-1] = '\0';
        
        char* last_slash = strrchr(work_dir, '/');
        if (last_slash) *last_slash = '\0'; 
        else _exit(ERR_CHDIR_FAILED);

        // -----------------------------------------------------
        // 4. 构建隔离环境 (Rootfs)
        // -----------------------------------------------------
        SetupRootfs(work_dir);

        // -----------------------------------------------------
        // 4.1 Prepare Mount Points & Permissions
        // Important: Perform mkdir and chmod BEFORE remounting root as Read-Only
        // -----------------------------------------------------
        // 4.1 Prepare Mount Points & Permissions
        // Important: Perform mkdir and chmod BEFORE remounting root as Read-Only
        // -----------------------------------------------------
        // Provide mount points for /tmp and /proc
        if (mkdir("/tmp", 01777) == -1 && errno != EEXIST) _exit(ERR_MOUNT_TMP);
        if (mkdir("/proc", 0755) == -1 && errno != EEXIST) _exit(ERR_MOUNT_PROC);
        
        // [Security Fix] Lock down the root directory permissions.
        // Change mode to 0555 (r-xr-xr-x) so nobody cannot write to /
        if (chmod("/", 0555) == -1) {
            _exit(ERR_CHDIR_FAILED); // Fail safe
        }

        // ----------------------------------------------------------------------------------
        // [Critical Security] Make the root filesystem strictly Read-Only NOW.
        // This is done ASAP to minimize the RW window.
        // ----------------------------------------------------------------------------------
        if (mount(nullptr, "/", nullptr, MS_REMOUNT | MS_BIND | MS_RDONLY, nullptr) == -1) {
            _exit(ERR_REMOUNT_RO);
        }

        // -----------------------------------------------------
        // 4.2 Mount Runtime filesystems (On top of RO Root)
        // -----------------------------------------------------
        // 1. /tmp (RW tmpfs) - configurable size
        char tmpfs_opts[64];
        long long run_tmpfs_mb = g_runner_config.run_tmpfs_size_mb > 0 ? g_runner_config.run_tmpfs_size_mb : 64;
        snprintf(tmpfs_opts, sizeof(tmpfs_opts), "size=%lldm,mode=1777", run_tmpfs_mb);
        if (mount("tmpfs", "/tmp", "tmpfs", 0, tmpfs_opts) == -1) _exit(ERR_MOUNT_TMP);

        // 2. /proc (RO)
        if (mount("proc", "/proc", "proc", 0, nullptr) == -1) _exit(ERR_MOUNT_PROC);
        if (mount("proc", "/proc", "proc", MS_REMOUNT | MS_RDONLY | MS_NOSUID | MS_NOEXEC | MS_NODEV, nullptr) == -1) 
            _exit(ERR_MOUNT_PROC);

        // 4. 资源限制 (setrlimit) - 使用任务参数
        rlimit cpu_limit;
        rlim_t cpu_soft = (args->time_limit_ms + 999) / 1000;
        if (cpu_soft < 1) cpu_soft = 1;
        rlim_t cpu_hard = cpu_soft + 1;
        cpu_limit.rlim_cur = cpu_soft;
        cpu_limit.rlim_max = cpu_hard;
        if (setrlimit(RLIMIT_CPU, &cpu_limit) == -1) _exit(ERR_RLIMIT_CPU);

        // [软硬限优化]: OS 层放宽内存限制，增加缓冲，防止 C++ 在堆分配瞬间抛出 bad_alloc
        rlimit mem_limit;
        rlim_t mem_bytes = (args->memory_limit_kb > 0)
            ? (rlim_t)args->memory_limit_kb * 1024
            : (rlim_t)g_runner_config.compile_mem_limit;
        mem_limit.rlim_cur = mem_bytes;
        mem_limit.rlim_max = mem_bytes;
        if (setrlimit(RLIMIT_AS, &mem_limit) == -1) _exit(ERR_RLIMIT_MEMORY);

        rlimit stack_limit;
        stack_limit.rlim_cur = RLIM_INFINITY; // 栈大小不做限制(也就是跟随系统)或设置大一些
        stack_limit.rlim_max = RLIM_INFINITY;
        if (setrlimit(RLIMIT_STACK, &stack_limit) == -1) _exit(ERR_RLIMIT_STACK);

        rlimit nproc_limit;
        nproc_limit.rlim_cur = 5;
        nproc_limit.rlim_max = 5;
        if (setrlimit(RLIMIT_NPROC, &nproc_limit) == -1) _exit(ERR_RLIMIT_NPROC);

        rlimit fsize_limit;
        // 使用父进程传递的 output_limit_bytes
        fsize_limit.rlim_cur = args->output_limit_bytes;
        fsize_limit.rlim_max = args->output_limit_bytes;
        // char debug_msg[128];
        // int debug_len = snprintf(debug_msg, sizeof(debug_msg), 
        //     "[DEBUG] Child received limit: %lu bytes\n", 
        //     (unsigned long)args->output_limit_bytes);
        // if (debug_len > 0) {
        //     write(STDERR_FILENO, debug_msg, debug_len);
        // }
        if (setrlimit(RLIMIT_FSIZE, &fsize_limit) == -1) _exit(ERR_RLIMIT_FSIZE);
        
        // 5. 清理附加组 + 降权 (nobody)
        if (setgroups(0, nullptr) != 0) _exit(ERR_SETGID_FAILED);
        if (chdir("/") != 0) _exit(ERR_CHDIR_FAILED);
        if (setgid(g_runner_config.run_gid) != 0) _exit(ERR_SETGID_FAILED);
        if (setuid(g_runner_config.run_uid) != 0) _exit(ERR_SETUID_FAILED);

        // -----------------------------------------------------
        // 6. 准备执行路径 (Early Calculation for Seccomp)
        // -----------------------------------------------------
        char new_exe[512];
        const char* filename = strrchr(args->exe_path, '/');
        if (filename) filename++; else filename = args->exe_path;
        // 在新 root 下，路径是 /filename
        snprintf(new_exe, sizeof(new_exe), "/%s", filename);

        // -----------------------------------------------------
        // 7. 安全增强：禁止提升特权 + 加载 Seccomp 白名单 (exec 前最后一步)
        // -----------------------------------------------------
        if (prctl(PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0) != 0) _exit(ERR_SANDBOX_EXCEPTION);
        LoadSeccompRules(new_exe);
        
        // 8. 执行用户程序
        char* const envp[] = { (char*)"PATH=/bin:/usr/bin", nullptr };
        
        execle(new_exe, new_exe, nullptr, envp);

        _exit(ERR_EXEC_FAILED); // 如果 exec 失败
        return 0; 
    }

    int CompileChildFn(void* arg) {
        // 1. 将参数重命名为 config，避免与后面的 argv 冲突
        auto* config = (CompileArgs*)(arg);

        // 1. [安全] 关闭文件描述符
        bool close_range_success = false;
        
        #ifdef __NR_close_range
            // 尝试使用高效的 close_range
            // 如果返回 -1 且 errno == ENOSYS (内核不支持)，则回退到循环关闭
            if (syscall(__NR_close_range, 3, ~0U, 0) == 0) {
                close_range_success = true;
            }
        #endif

        if (!close_range_success) {
            // Fallback: 传统循环关闭
            // 获取最大 FD 限制，防止循环过大
            int max_fd = (int)sysconf(_SC_OPEN_MAX);
            if (max_fd < 0) max_fd = 4096;
            if (max_fd > 65536) max_fd = 65536; // 限制上限，防止耗时过久

            for (int fd = 3; fd < max_fd; ++fd) {
                close(fd); 
            }
        }

        // 2. 准备 work_dir
        char work_dir[256];
        strncpy(work_dir, config->source_path, sizeof(work_dir) - 1);
        work_dir[sizeof(work_dir) - 1] = '\0';
        char* p = strrchr(work_dir, '/');
        if (p) *p = '\0';

        // 3. [隔离] Setup Rootfs
        SetupRootfs(work_dir);

        // 4. [编译器需求] 挂载 /tmp
        if (mkdir("/tmp", 01777) == -1 && errno != EEXIST) _exit(ERR_MKDIR_FAILED);
        char compile_tmpfs_opts[64];
        long long compile_tmpfs_mb = g_runner_config.compile_tmpfs_size_mb > 0 ? g_runner_config.compile_tmpfs_size_mb : 128;
        snprintf(compile_tmpfs_opts, sizeof(compile_tmpfs_opts), "size=%lldm,mode=1777", compile_tmpfs_mb);
        if (mount("tmpfs", "/tmp", "tmpfs", 0, compile_tmpfs_opts) == -1) {
            _exit(ERR_MOUNT_TMP);
        }

        // 5. 重定向 stderr
        const char* log_filename = strrchr(config->log_path, '/');
        log_filename = log_filename ? log_filename + 1 : config->log_path;

        char new_log_path[512];
        snprintf(new_log_path, sizeof(new_log_path), "/%s", log_filename);

        int log_fd = open(new_log_path, O_WRONLY | O_CREAT | O_TRUNC, 0644);
        if (log_fd != -1) {
            dup2(log_fd, STDERR_FILENO);
            close(log_fd);
        } else {
            _exit(ERR_OPEN_OUTPUT);
        }

        // 6. [资源限制] 使用配置中的值，失败则直接退出
        rlimit cpu_lim;
        cpu_lim.rlim_cur = g_runner_config.compile_cpu_limit;
        cpu_lim.rlim_max = g_runner_config.compile_real_limit;
        if (setrlimit(RLIMIT_CPU, &cpu_lim) == -1) _exit(ERR_RLIMIT_CPU);

        rlimit mem_lim;
        mem_lim.rlim_cur = (rlim_t)g_runner_config.compile_mem_limit;
        mem_lim.rlim_max = (rlim_t)g_runner_config.compile_mem_limit;
        if (setrlimit(RLIMIT_AS, &mem_lim) == -1) _exit(ERR_RLIMIT_MEMORY);

        // [Log Bomb Defense] Limit stderr output file size.
        rlimit fsize_lim;
        long long log_limit = g_runner_config.compile_log_max_bytes > 0 ? g_runner_config.compile_log_max_bytes : (16 * 1024 * 1024);
        fsize_lim.rlim_cur = log_limit;
        fsize_lim.rlim_max = log_limit;
        if (setrlimit(RLIMIT_FSIZE, &fsize_lim) == -1) _exit(ERR_RLIMIT_FSIZE);

        // 7. [安全] 清理附加组并降权，使用配置中的 UID/GID
        if (setgroups(0, nullptr) != 0) _exit(ERR_SETGID_FAILED);
        if (setgid(g_runner_config.run_gid) != 0) _exit(ERR_SETGID_FAILED);
        if (setuid(g_runner_config.run_uid) != 0) _exit(ERR_SETUID_FAILED);
        if (prctl(PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0) != 0) _exit(ERR_SANDBOX_EXCEPTION);

        // 8. 准备 g++ 执行路径及参数，确保使用配置中的 compiler_path
        const char* src_filename = strrchr(config->source_path, '/');
        src_filename = src_filename ? src_filename + 1 : config->source_path;
        char new_src_path[512];
        snprintf(new_src_path, sizeof(new_src_path), "/%s", src_filename);

        const char* exe_filename = strrchr(config->exe_path, '/');
        exe_filename = exe_filename ? exe_filename + 1 : config->exe_path;
        char new_exe_path[512];
        snprintf(new_exe_path, sizeof(new_exe_path), "/%s", exe_filename);

        char max_errors_opt[32];
        int max_errors = g_runner_config.compile_max_errors > 0 ? g_runner_config.compile_max_errors : 10;
        snprintf(max_errors_opt, sizeof(max_errors_opt), "-fmax-errors=%d", max_errors);

        char* const compiler_argv[] =
        {
            (char*)g_runner_config.compiler_path,
            (char*)"-std=c++20",
            (char*)"-O2",
            (char*)"-pipe",
            // [Log Bomb Defense] Limit max error messages to 10
            (char*)max_errors_opt,
            (char*)new_src_path,
            (char*)"-o",
            (char*)new_exe_path,
            nullptr
        };

        // 执行编译器
        execv(compiler_argv[0], compiler_argv);

        // 如果执行到这里，说明失败了
        _exit(ERR_EXEC_FAILED);
        return 0;
    }

} // namespace deep_oj
