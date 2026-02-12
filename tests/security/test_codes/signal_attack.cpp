/**
 * 信号处理器攻击测试
 * 
 * 攻击原理:
 * 1. 注册自定义信号处理器
 * 2. 尝试忽略 SIGKILL/SIGSTOP (不可能)
 * 3. 尝试通过信号延长执行时间
 * 
 * 预期结果: SIGKILL 无法被捕获
 */
#include <iostream>
#include <csignal>
#include <unistd.h>

volatile sig_atomic_t caught = 0;

void handler(int sig) {
    caught++;
    std::cout << "Caught signal " << sig << " (" << caught << " times)" << std::endl;
    // 尝试忽略信号继续执行
}

int main() {
    // 尝试捕获所有信号
    for (int i = 1; i < 32; i++) {
        if (signal(i, handler) == SIG_ERR) {
            std::cout << "Cannot catch signal " << i << std::endl;
        } else {
            std::cout << "Registered handler for signal " << i << std::endl;
        }
    }
    
    // 无限循环，等待被终止
    std::cout << "Entering infinite loop..." << std::endl;
    while (true) {
        sleep(1);
        std::cout << "Still alive... (caught " << caught << " signals)" << std::endl;
    }
    
    return 0;
}
