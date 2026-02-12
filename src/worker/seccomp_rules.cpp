// src/worker/seccomp_rules.cpp
#include "seccomp_rules.h"
#include "sandbox_internal.h"
#include <seccomp.h>
#include <fcntl.h>

namespace deep_oj {

    void LoadSeccompRules(const char* exe_path) {
        scmp_filter_ctx ctx = seccomp_init(SCMP_ACT_KILL);
        if (!ctx) _exit(ERR_SANDBOX_EXCEPTION);

        // 辅助宏：允许指定系统调用（带参数检查）
        // 允许系统调用，且参数 0 (a0) 必须等于指定值
        #define ALLOW_SYS_ARG0_EQ(syscall_name, specific_val) \
            if (seccomp_rule_add(ctx, SCMP_ACT_ALLOW, SCMP_SYS(syscall_name), 1, \
                SCMP_A0(SCMP_CMP_EQ, (scmp_datum_t)(specific_val))) != 0) { \
                seccomp_release(ctx); _exit(ERR_SANDBOX_EXCEPTION); \
            }

        // 核心防御：execve 限制
        // 允许执行当前程序
        ALLOW_SYS_ARG0_EQ(execve, exe_path);

        // 核心防御：open/openat 禁止写权限 (只读)
        if (seccomp_rule_add(ctx, SCMP_ACT_ALLOW, SCMP_SYS(open), 1, 
                SCMP_CMP(1, SCMP_CMP_MASKED_EQ, O_WRONLY | O_RDWR, 0)) != 0) {
             seccomp_release(ctx); _exit(ERR_SANDBOX_EXCEPTION);
        }
        if (seccomp_rule_add(ctx, SCMP_ACT_ALLOW, SCMP_SYS(openat), 1, 
                SCMP_CMP(2, SCMP_CMP_MASKED_EQ, O_WRONLY | O_RDWR, 0)) != 0) {
             seccomp_release(ctx); _exit(ERR_SANDBOX_EXCEPTION);
        }
        
        // 允许其他基础系统调用
        #define ALLOW_SYSCALL(name) seccomp_rule_add(ctx, SCMP_ACT_ALLOW, SCMP_SYS(name), 0)
        
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
        
        // 加上刚才发现的“必杀名单”
        ALLOW_SYSCALL(prlimit64);
        ALLOW_SYSCALL(rseq);
        ALLOW_SYSCALL(getrandom);
        ALLOW_SYSCALL(futex);
        ALLOW_SYSCALL(newfstatat);
        ALLOW_SYSCALL(faccessat); // [Fix] Required by some runtimes
        ALLOW_SYSCALL(pread64);   // [Fix] Required by glibc/loader
        ALLOW_SYSCALL(arch_prctl); // [Fix] Required by TLS (glibc)
        ALLOW_SYSCALL(set_tid_address); // [Fix] Required by glibc
        ALLOW_SYSCALL(set_robust_list); // [Fix] Required by glibc

        if (seccomp_load(ctx) != 0) {
            seccomp_release(ctx);
            _exit(ERR_SANDBOX_EXCEPTION);
        }
        seccomp_release(ctx);
    }
}