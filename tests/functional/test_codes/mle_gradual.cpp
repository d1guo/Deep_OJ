// MLE 测试: 内存超限
// 预期结果: Memory Limit Exceeded
// 注意: 与 memory_bomb 不同，这是逐步分配而非爆炸式
#include <iostream>
#include <vector>
int main() {
    std::vector<int> v;
    // 每次分配 1MB，持续分配直到超限
    while (true) {
        v.resize(v.size() + 1024 * 1024 / sizeof(int));
        // 确保内存被使用 (防止编译器优化)
        v.back() = 1;
    }
    return 0;
}
