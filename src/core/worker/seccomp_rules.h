#ifndef DEEP_OJ_SECCOMP_RULES_H
#define DEEP_OJ_SECCOMP_RULES_H

namespace deep_oj {

    /**
     * @brief 加载 Seccomp 白名单规则
     * 仅允许用户程序运行所需的最小系统调用集。
     * 调用失败会直接终止进程。
     */
    void LoadSeccompRules(const char* exe_path);

} // namespace deep_oj

#endif // DEEP_OJ_SECCOMP_RULES_H
