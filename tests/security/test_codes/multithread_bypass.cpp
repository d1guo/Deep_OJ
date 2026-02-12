/**
 * 多线程安全测试: 尝试绕过 setrlimit
 * 
 * 攻击原理:
 * 1. 主线程设置了资源限制 (setrlimit)
 * 2. 子线程可能在限制生效前创建
 * 3. 如果沙箱实现不当，子线程可能逃脱限制
 * 
 * 预期结果: 被 Cgroups pids.max 或 Seccomp 拦截
 */
#include <iostream>
#include <thread>
#include <vector>
#include <chrono>

void worker(int id) {
    // 尝试在子线程中分配大量内存
    std::vector<int> data;
    try {
        data.resize(1024 * 1024 * 100);  // 400 MB
        data[0] = id;
        std::cout << "Thread " << id << " allocated memory!" << std::endl;
    } catch (...) {
        std::cout << "Thread " << id << " failed to allocate" << std::endl;
    }
}

int main() {
    std::vector<std::thread> threads;
    
    // 尝试创建多个线程绕过限制
    for (int i = 0; i < 10; i++) {
        try {
            threads.emplace_back(worker, i);
        } catch (const std::exception& e) {
            std::cout << "Failed to create thread " << i << ": " << e.what() << std::endl;
            break;
        }
    }
    
    for (auto& t : threads) {
        if (t.joinable()) {
            t.join();
        }
    }
    
    std::cout << "Multi-thread test completed" << std::endl;
    return 0;
}
