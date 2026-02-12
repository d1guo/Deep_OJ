#ifndef DEEP_OJ_SANDBOX_ISOLATION_H
#define DEEP_OJ_SANDBOX_ISOLATION_H

namespace deep_oj {

    /**
     * @brief 运行子进程的入口点 (隔离层)
     * 兼容 clone() 函数签名。
     * @param arg 指向 RunChildArgs 结构体的指针
     * @return int 退出码
     */
    int RunChildFn(void* arg);

    /**
     * @brief 编译子进程的入口点 (隔离层)
     * 兼容 clone() 函数签名。
     * @param arg 指向 CompileArgs 结构体的指针
     * @return int 退出码
     */
    int CompileChildFn(void* arg);

} // namespace deep_oj

#endif // DEEP_OJ_SANDBOX_ISOLATION_H
