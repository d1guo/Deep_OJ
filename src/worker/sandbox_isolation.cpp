#include "sandbox_isolation.h"
#include "sandbox_internal.h"

#include <unistd.h>
#include <fcntl.h>
#include <sys/stat.h>
#include <sys/mount.h>
#include <sys/syscall.h>
#include <sys/resource.h>
#include <cstring>
#include <cstdio>
#include <cerrno>

// 严重警告: 此文件必须严格遵守 Async-Signal-Safe C 风格。
// 禁止使用: malloc, new, exceptions, std::string, std::vector, iostream。
// 只能使用: glibc 系统调用, stack memory, snprintf 等。

namespace deep_oj {

    namespace {
        
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
            if (mount(work_dir, work_dir, "bind", MS_BIND | MS_REC, nullptr) == -1)
            {
                _exit(ERR_MOUNT_BIND_SELF);
            }

            // 3. 镜像挂载系统库 (/lib, /usr, /bin)
            const char* dirs[] = {"/lib", "/lib64", "/usr", "/bin"};
            char target[512];
            
            for (const char* dir : dirs)
            {
                if (access(dir, F_OK) != 0) continue;

                int n = snprintf(target, sizeof(target), "%s%s", work_dir, dir);
                if (n >= (int)sizeof(target)) _exit(ERR_MKDIR_FAILED);

                // 创建目录 (类似 mkdir -p，假设父目录 work_dir 已存在)
                if (mkdir(target, 0755) == -1 && errno != EEXIST) {
                    _exit(ERR_MKDIR_FAILED);
                }

                // 步骤 A: Bind Mount
                if (mount(dir, target, nullptr, MS_BIND | MS_REC, nullptr) == -1)
                {
                    _exit(ERR_MOUNT_BIND_LIB);
                }

                // 步骤 B: 重新挂载为只读 (Read-Only)
                if (mount(dir, target, nullptr, MS_BIND | MS_REC | MS_RDONLY | MS_REMOUNT, nullptr) == -1)
                {
                    _exit(ERR_REMOUNT_RO);
                }
            }

            // 4. Pivot Root (切换根目录)
            char old_root[512];
            snprintf(old_root, sizeof(old_root), "%s/old_root", work_dir);
            if (mkdir(old_root, 0755) == -1 && errno != EEXIST) _exit(ERR_MKDIR_FAILED);

            // 执行系统调用 syscall(SYS_pivot_root, new_root, put_old)
            if (syscall(SYS_pivot_root, work_dir, old_root) == -1)
            {
                _exit(ERR_PIVOT_ROOT);
            }

            // 5. 切换当前工作目录到新的根 "/"
            if (chdir("/") == -1)
            {
                 _exit(ERR_CHDIR_NEW_ROOT);
            }

            // 6. 卸载旧的根目录 (old_root)
            if (umount2("/old_root", MNT_DETACH) == -1)
            {
                _exit(ERR_UMOUNT_OLD);
            }
        }

    } // anonymous namespace

    int RunChildFn(void* arg)
    {
        auto* args = (RunChildArgs*)(arg);

        // [安全]: 关闭除 0,1,2 以外的所有文件描述符
        #ifdef __NR_close_range
            syscall(__NR_close_range, 3, ~0U, 0);
        #else
            for (int fd = 3; fd < 256; ++fd) close(fd); 
        #endif

        // 打开 /dev/null 备用
        int null_fd = open("/dev/null", O_RDWR);
        if (null_fd == -1) _exit(ERR_OPEN_OUTPUT);

        // 准备 work_dir (字符串操作)
        char work_dir[256];
        strncpy(work_dir, args->exe_path, sizeof(work_dir) - 1);
        work_dir[sizeof(work_dir)-1] = '\0';
        
        // 截断路径以获取目录名 (例如 /tmp/xxx/main -> /tmp/xxx)
        char* last_slash = strrchr(work_dir, '/');
        if (last_slash) *last_slash = '\0'; 
        else _exit(ERR_CHDIR_FAILED); // 必须是绝对路径

        // 1. 设置 Rootfs / Pivot Root
        SetupRootfs(work_dir);

        // 2. 挂载 /proc (ps 等命令需要)
        if (mkdir("/proc", 0755) == -1 && errno != EEXIST) _exit(ERR_MOUNT_PROC);
        if (mount("proc", "/proc", "proc", 0, nullptr) == -1) _exit(ERR_MOUNT_PROC);
        
        // [Security] Remount /proc as Read-Only
        if (mount("proc", "/proc", "proc", MS_REMOUNT | MS_RDONLY | MS_NOSUID | MS_NOEXEC | MS_NODEV, nullptr) == -1) 
            _exit(ERR_MOUNT_PROC);


        // 3. IO 重定向
        int out_fd = open("/output.txt", O_WRONLY | O_CREAT | O_TRUNC, 0644);
        if (out_fd == -1) _exit(ERR_OPEN_OUTPUT);
        
        if (dup2(out_fd, STDOUT_FILENO) == -1) _exit(ERR_DUP2);
        close(out_fd);
        
        if (dup2(null_fd, STDIN_FILENO) == -1) _exit(ERR_DUP2);
        if (dup2(null_fd, STDERR_FILENO) == -1) _exit(ERR_DUP2);
        close(null_fd);

        // 4. 资源限制 (setrlimit)
        rlimit cpu_limit;
        cpu_limit.rlim_cur = (args->time_limit_ms + 999) / 1000; // 向上取整秒
        cpu_limit.rlim_max = cpu_limit.rlim_cur + 1;             // 硬限制多给1秒
        if (setrlimit(RLIMIT_CPU, &cpu_limit) == -1) _exit(ERR_RLIMIT_CPU);

        rlimit mem_limit;
        long long hard_mem_limit = (args->memory_limit_kb * 2 + 1024 * 128) * 1024L; // 放宽硬限制防止OOM Killer误杀
        mem_limit.rlim_cur = hard_mem_limit;
        mem_limit.rlim_max = hard_mem_limit;
        if (setrlimit(RLIMIT_AS, &mem_limit) == -1) _exit(ERR_RLIMIT_MEMORY);

        rlimit stack_limit;
        stack_limit.rlim_cur = RLIM_INFINITY; // 栈大小不做限制(也就是跟随系统)或设置大一些
        stack_limit.rlim_max = RLIM_INFINITY;
        if (setrlimit(RLIMIT_STACK, &stack_limit) == -1) _exit(ERR_RLIMIT_STACK);

        rlimit nproc_limit;
        nproc_limit.rlim_cur = 5; // 只能有一个进程
        nproc_limit.rlim_max = 5;
        if (setrlimit(RLIMIT_NPROC, &nproc_limit) == -1) _exit(ERR_RLIMIT_NPROC);

        rlimit fsize_limit;
        fsize_limit.rlim_cur = 10 * 1024 * 1024; // 输出文件最大 10MB
        fsize_limit.rlim_max = 10 * 1024 * 1024;
        if (setrlimit(RLIMIT_FSIZE, &fsize_limit) == -1) _exit(ERR_RLIMIT_FSIZE);
        
        // 5. 降权 (nobody)
        if (chdir("/") != 0) _exit(ERR_CHDIR_FAILED);
        if (setgid(65534) != 0) _exit(ERR_SETGID_FAILED);
        if (setuid(65534) != 0) _exit(ERR_SETUID_FAILED);
        
        // 6. 执行用户程序 (execle)
        char new_exe[512];
        const char* filename = strrchr(args->exe_path, '/');
        if (filename) filename++; else filename = args->exe_path;

        // 在新 root 下，路径是 /filename
        snprintf(new_exe, sizeof(new_exe), "/%s", filename);

        char* const envp[] = { (char*)"PATH=/bin:/usr/bin", nullptr };
        
        execle(new_exe, new_exe, nullptr, envp);

        _exit(ERR_EXEC_FAILED); // 如果 exec 失败
        return 0; 
    }

    int CompileChildFn(void* arg) {
        auto* args = (CompileArgs*)(arg);

        // 1. [安全] 关闭文件描述符
        #ifdef __NR_close_range
            syscall(__NR_close_range, 3, ~0U, 0);
        #else
            for (int fd = 3; fd < 256; ++fd) close(fd); 
        #endif

        // 2. 准备 work_dir
        char work_dir[256];
        strncpy(work_dir, args->source_path, sizeof(work_dir)-1);
        work_dir[sizeof(work_dir)-1] = '\0';
        char* p = strrchr(work_dir, '/');
        if (p) *p = '\0';

        // 3. [隔离] Setup Rootfs
        SetupRootfs(work_dir);

        // 4. [编译器需求] 挂载 /tmp (tmpfs) 供 g++ 生成临时文件
        if (mkdir("/tmp", 0777) == -1 && errno != EEXIST) _exit(ERR_MKDIR_FAILED);
        // 挂载 tmpfs, 限制 128MB
        if (mount("tmpfs", "/tmp", "tmpfs", 0, "size=128m") == -1) {
             _exit(ERR_MOUNT_PROC);
        }

        // 5. 重定向 stderr 到日志文件
        // 注意: Pivot Root 后路径变了
        const char* log_filename = strrchr(args->log_path, '/');
        log_filename = log_filename ? log_filename + 1 : args->log_path;

        char new_log_path[512];
        snprintf(new_log_path, sizeof(new_log_path), "/%s", log_filename);

        int log_fd = open(new_log_path, O_WRONLY | O_CREAT | O_TRUNC, 0666);
        if (log_fd != -1) {
            dup2(log_fd, STDERR_FILENO); // 编译错误输出到 stderr
            close(log_fd);
        } else {
             _exit(ERR_OPEN_OUTPUT);
        }

        // 6. [资源限制] 给编译器较宽松的限制
        rlimit cpu_lim = { 10, 15 }; // 10s 软限制
        setrlimit(RLIMIT_CPU, &cpu_lim);

        rlimit mem_lim = { 1024L * 1024 * 1024, 1024L * 1024 * 1024 }; // 1GB 内存
        setrlimit(RLIMIT_AS, &mem_lim);

        rlimit fsize_lim = { 64 * 1024 * 1024, 64 * 1024 * 1024 }; // 64MB 输出限制
        setrlimit(RLIMIT_FSIZE, &fsize_lim);

        // 7. [安全] 降权
        if (setgid(65534) != 0) _exit(ERR_SETGID_FAILED);
        if (setuid(65534) != 0) _exit(ERR_SETUID_FAILED);

        // 8. 执行 g++
        const char* src_filename = strrchr(args->source_path, '/');
        src_filename = src_filename ? src_filename + 1 : args->source_path;
        char new_src_path[512];
        snprintf(new_src_path, sizeof(new_src_path), "/%s", src_filename);

        const char* exe_filename = strrchr(args->exe_path, '/');
        exe_filename = exe_filename ? exe_filename + 1 : args->exe_path;
        char new_exe_path[512];
        snprintf(new_exe_path, sizeof(new_exe_path), "/%s", exe_filename);

        char* const envp[] = { (char*)"PATH=/bin:/usr/bin", nullptr };
        
        execlp("g++", "g++", 
               "-std=c++20", "-O2", 
               new_src_path, "-o", new_exe_path, 
               nullptr, envp);

        // 错误处理
        const char* msg = "Failed to launch g++: ";
        write(STDERR_FILENO, msg, strlen(msg));
        write(STDERR_FILENO, strerror(errno), strlen(strerror(errno)));
        _exit(ERR_EXEC_FAILED);
        return 0;
    }

} // namespace deep_oj
