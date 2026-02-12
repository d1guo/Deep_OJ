/**
 * 多线程 Fork 炸弹
 * 
 * 攻击原理:
 * 1. 使用 std::async 创建大量并发任务
 * 2. 每个任务尝试创建更多任务
 * 3. 如果 Cgroups pids.max 正确配置，应该被拦截
 * 
 * 预期结果: 被 Cgroups pids.max 限制
 */
#include <iostream>
#include <future>
#include <vector>

int spawn(int depth) {
    if (depth > 5) return 0;
    
    std::vector<std::future<int>> futures;
    
    for (int i = 0; i < 10; i++) {
        try {
            futures.push_back(std::async(std::launch::async, spawn, depth + 1));
        } catch (...) {
            std::cout << "Failed at depth " << depth << std::endl;
            return depth;
        }
    }
    
    int max_depth = depth;
    for (auto& f : futures) {
        try {
            max_depth = std::max(max_depth, f.get());
        } catch (...) {}
    }
    return max_depth;
}

int main() {
    std::cout << "Starting async fork bomb..." << std::endl;
    int result = spawn(0);
    std::cout << "Reached depth: " << result << std::endl;
    return 0;
}
