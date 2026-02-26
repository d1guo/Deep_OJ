// src/worker/seccomp_rules.cpp
#include "seccomp_rules.h"
#include "sandbox_internal.h"
#include <seccomp.h>
#include <fcntl.h>
#include <cstdlib>
#include <cstring>

namespace deep_oj {

    bool ShouldEnableRunSeccomp() {
        const char* mode = std::getenv("JUDGE_RUN_SECCOMP_MODE");
        if (mode == nullptr || mode[0] == '\0') {
            return true;
        }
        return std::strcmp(mode, "off") != 0;
    }

    void LoadSeccompRules(const char* exe_path) {
        (void)exe_path;
        scmp_filter_ctx ctx = seccomp_init(SCMP_ACT_KILL);
        if (!ctx) _exit(ERR_SANDBOX_EXCEPTION);

        // 核心防御：open/openat 禁止写权限 (只读)
        if (seccomp_rule_add(ctx, SCMP_ACT_ALLOW, SCMP_SYS(open), 1, 
                SCMP_CMP(1, SCMP_CMP_MASKED_EQ, O_WRONLY | O_RDWR, 0)) != 0) {
             seccomp_release(ctx); _exit(ERR_SANDBOX_EXCEPTION);
        }
        if (seccomp_rule_add(ctx, SCMP_ACT_ALLOW, SCMP_SYS(openat), 1, 
                SCMP_CMP(2, SCMP_CMP_MASKED_EQ, O_WRONLY | O_RDWR, 0)) != 0) {
             seccomp_release(ctx); _exit(ERR_SANDBOX_EXCEPTION);
        }

        // 允许执行程序；不使用指针等值约束，避免对 argv 地址布局过敏导致误杀。
        if (seccomp_rule_add(ctx, SCMP_ACT_ALLOW, SCMP_SYS(execve), 0) != 0) {
            seccomp_release(ctx); _exit(ERR_SANDBOX_EXCEPTION);
        }
        if (seccomp_rule_add(ctx, SCMP_ACT_ALLOW, SCMP_SYS(execveat), 0) != 0) {
            seccomp_release(ctx); _exit(ERR_SANDBOX_EXCEPTION);
        }

        // 允许其他基础系统调用
        #define ALLOW_SYSCALL(name) \
            if (seccomp_rule_add(ctx, SCMP_ACT_ALLOW, SCMP_SYS(name), 0) != 0) { \
                seccomp_release(ctx); _exit(ERR_SANDBOX_EXCEPTION); \
            }
        
        ALLOW_SYSCALL(read);
        ALLOW_SYSCALL(write);
        ALLOW_SYSCALL(close);
        ALLOW_SYSCALL(fstat);
        ALLOW_SYSCALL(lseek);
        ALLOW_SYSCALL(brk);
        ALLOW_SYSCALL(mmap);
        ALLOW_SYSCALL(munmap);
        ALLOW_SYSCALL(mprotect);
        ALLOW_SYSCALL(exit);
        ALLOW_SYSCALL(exit_group);
        ALLOW_SYSCALL(uname);
        ALLOW_SYSCALL(access); // access 其实也可以限制只读，但风险不大
        ALLOW_SYSCALL(readlink);
        ALLOW_SYSCALL(gettimeofday);
        ALLOW_SYSCALL(clock_gettime);
        ALLOW_SYSCALL(rt_sigaction);
        ALLOW_SYSCALL(rt_sigprocmask);
        ALLOW_SYSCALL(rt_sigreturn);
        ALLOW_SYSCALL(sigaltstack);
        ALLOW_SYSCALL(getpid);
        ALLOW_SYSCALL(getppid);
        ALLOW_SYSCALL(gettid);
        ALLOW_SYSCALL(getuid);
        ALLOW_SYSCALL(geteuid);
        ALLOW_SYSCALL(getgid);
        ALLOW_SYSCALL(getegid);
        ALLOW_SYSCALL(fcntl);
        ALLOW_SYSCALL(statx);
        ALLOW_SYSCALL(statfs);
        ALLOW_SYSCALL(fstatfs);
        ALLOW_SYSCALL(ioctl);
        ALLOW_SYSCALL(madvise);
        ALLOW_SYSCALL(sched_getaffinity);
        
        // 运行时加载器与 libc 依赖调用
        ALLOW_SYSCALL(prlimit64);
        ALLOW_SYSCALL(rseq);
        ALLOW_SYSCALL(getrandom);
        ALLOW_SYSCALL(futex);
        ALLOW_SYSCALL(newfstatat);
        ALLOW_SYSCALL(faccessat);
        ALLOW_SYSCALL(pread64);
        ALLOW_SYSCALL(arch_prctl);
        ALLOW_SYSCALL(set_tid_address);
        ALLOW_SYSCALL(set_robust_list);

        if (seccomp_load(ctx) != 0) {
            seccomp_release(ctx);
            _exit(ERR_SANDBOX_EXCEPTION);
        }
        seccomp_release(ctx);
        #undef ALLOW_SYSCALL
    }

    void LoadCompileSeccompRules() {
        scmp_filter_ctx ctx = seccomp_init(SCMP_ACT_ALLOW);
        if (!ctx) _exit(ERR_SANDBOX_EXCEPTION);

        // 编译阶段采用“默认允许 + 高危系统调用拒绝”策略，
        // 兼顾编译工具链可用性和基础安全收敛。
        #define DENY_SYSCALL(name) \
            if (seccomp_rule_add(ctx, SCMP_ACT_KILL, SCMP_SYS(name), 0) != 0) { \
                seccomp_release(ctx); _exit(ERR_SANDBOX_EXCEPTION); \
            }

        DENY_SYSCALL(ptrace);
        DENY_SYSCALL(mount);
        DENY_SYSCALL(umount2);
        DENY_SYSCALL(reboot);
        DENY_SYSCALL(swapon);
        DENY_SYSCALL(swapoff);
        DENY_SYSCALL(kexec_load);
        DENY_SYSCALL(init_module);
        DENY_SYSCALL(finit_module);
        DENY_SYSCALL(delete_module);

        if (seccomp_load(ctx) != 0) {
            seccomp_release(ctx);
            _exit(ERR_SANDBOX_EXCEPTION);
        }
        seccomp_release(ctx);
    }
}
