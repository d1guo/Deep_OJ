/**
 * @file ptrace_test.cpp
 * @brief ptrace 调试测试
 * 
 * 预期行为:
 * - Seccomp 白名单不包含 ptrace 系统调用
 * - 调用 ptrace() 时触发 SIGSYS (Signal 31)
 * 
 * 预期结果: RE (Runtime Error)
 * 
 * 安全考虑:
 * - ptrace 可以用于调试其他进程
 * - 攻击者可能利用它来绕过检测或作弊
 */
#include <sys/ptrace.h>
#include <sys/types.h>
#include <unistd.h>
#include <stdio.h>

int main() {
    pid_t parent = getppid();
    
    // 尝试 attach 到父进程
    long ret = ptrace(PTRACE_ATTACH, parent, NULL, NULL);
    if (ret < 0) {
        printf("ptrace blocked (expected)\n");
        return 0;
    }
    
    printf("DANGER: ptrace succeeded!\n");
    ptrace(PTRACE_DETACH, parent, NULL, NULL);
    return 42;  // 特殊退出码表示安全漏洞
}
