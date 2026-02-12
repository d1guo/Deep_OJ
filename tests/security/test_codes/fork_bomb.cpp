/**
 * @file fork_bomb.cpp
 * @brief Fork 炸弹测试
 * 
 * 预期行为:
 * - 如果 Cgroups v2 pids.max 正确配置：进程创建失败 (fork 返回 -1)
 * - 如果只有 setrlimit(RLIMIT_NPROC)：可能绕过限制
 * 
 * 预期结果: RE (Runtime Error) 或 TLE (Time Limit Exceeded)
 * 绝对不能: 系统崩溃或无响应
 */
#include <unistd.h>
int main() {
    while (fork() >= 0);
    return 0;
}
