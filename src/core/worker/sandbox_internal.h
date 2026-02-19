#ifndef DEEP_OJ_SANDBOX_INTERNAL_H
#define DEEP_OJ_SANDBOX_INTERNAL_H

#include <sys/types.h>
#include <sys/resource.h>
#include <unistd.h>

namespace deep_oj {

    // 定义子进程栈大小: 8MB (防止 glibc/STL 爆栈)
    const int STACK_SIZE = 8 * 1024 * 1024;

    // 退出状态码定义
    enum SandboxExitCode {
        EXIT_OK = 0, // 正常退出

        // 第一阶段: 基础设置与执行 (120-139)
        ERR_OPEN_OUTPUT      = 120, // 打开输出文件失败
        ERR_DUP2             = 121, // 重定向标准输出/输入失败
        ERR_EXEC_FAILED      = 127, // execle 执行失败
        ERR_CHDIR_FAILED     = 128, // 切换工作目录失败
        ERR_SETGID_FAILED    = 129, // 设置组 ID 失败
        ERR_SETUID_FAILED    = 130, // 设置用户 ID 失败

        // 第二阶段: 资源限制 (140-159)
        ERR_RLIMIT_CPU       = 141, // 设置 CPU 时间限制失败
        ERR_RLIMIT_MEMORY    = 142, // 设置内存限制失败
        ERR_RLIMIT_STACK     = 143, // 设置栈限制失败
        ERR_RLIMIT_NPROC     = 144, // 设置进程数限制失败
        ERR_RLIMIT_FSIZE     = 145, // 设置文件大小限制失败

        // 第三阶段: 隔离与文件系统 (190-200)
        ERR_MOUNT_PRIVATE    = 190, // mount --make-private 失败
        ERR_MOUNT_BIND_SELF  = 191, // bind mount 工作目录失败
        ERR_MOUNT_BIND_LIB   = 192, // 挂载系统库 (/lib, /usr...) 失败
        ERR_REMOUNT_RO       = 193, // 重新挂载为只读失败
        ERR_PIVOT_ROOT       = 194, // pivot_root 系统调用失败
        ERR_CHDIR_NEW_ROOT   = 195, // 切换到新根目录失败
        ERR_UMOUNT_OLD       = 196, // 卸载旧根目录 (/old_root) 失败
        ERR_MOUNT_PROC       = 197, // 挂载 /proc 失败
        ERR_MKDIR_FAILED     = 198, // 创建目录失败
        ERR_SANDBOX_EXCEPTION = 199, // 沙箱内部异常
        ERR_MOUNT_TMP        = 200  // 挂载 /tmp 失败
    };

    // ... namespace deep_oj ...

    struct GlobalConfig {
        // 工作区与编译器配置
        char workspace_root[256];
        char compiler_path[256];
        
        // 挂载目录
        char mount_dirs[16][256];
        int mount_count;
        
        // 挂载文件
        char mount_files[16][256];
        int mount_file_count;

        // 编译限制
        int compile_cpu_limit;
        int compile_real_limit;
        long long compile_mem_limit;
        
        // 输出文件限制（字节）
        long long max_output_size;
        long long output_buffer_size; // 超限缓冲大小 (Soft->Hard)

        // 运行时 /tmp tmpfs 大小 (MB)
        long long run_tmpfs_size_mb;

        // 编译相关限制
        long long compile_tmpfs_size_mb;
        long long compile_log_max_bytes;
        int compile_max_errors;

        // 安全限制
        uid_t run_uid;
        gid_t run_gid;

        int pool_size; // 核心判题并发数

        // cgroup 限制
        int cgroup_pids_limit;
        double cgroup_cpu_max_cores;
        long long cgroup_io_read_bps;
        long long cgroup_io_write_bps;
    };
    extern GlobalConfig g_runner_config;

    /**
     * @brief 运行子进程所需的参数 (C 风格结构体)
     */
    struct RunChildArgs {
        char exe_path[256];     // 可执行文件路径
        char input_file[256];  
        char output_file[256];
        char error_file[256];

        int time_limit_ms;      // 时间限制 (毫秒)
        int memory_limit_kb;    // 内存限制 (KB)
        rlim_t output_limit_bytes; // 输出文件大小限制
        
        int input_fd;  // 对应 stdin
        int output_fd; // 对应 stdout
        int error_fd;  // 对应 stderr
    };

    /**
     * @brief 编译子进程所需的参数 (C 风格结构体)
     */
    struct CompileArgs {
        char source_path[256];  // 源代码路径
        char exe_path[256];     // 目标可执行文件路径
        char log_path[256];     // 编译日志路径 (用于 stderr)
    };

} // namespace deep_oj

#endif // DEEP_OJ_SANDBOX_INTERNAL_H
