/**
 * @file memory_bomb.cpp
 * @brief 内存炸弹测试
 * 
 * 预期行为:
 * - Cgroups v2 memory.max 触发 OOM Killer
 * - 进程收到 SIGKILL
 * 
 * 预期结果: MLE (Memory Limit Exceeded) 或 RE
 */
#include <vector>
#include <cstdlib>

int main() {
    std::vector<char*> blocks;
    while (true) {
        // 每次分配 100MB
        char* block = (char*)malloc(100 * 1024 * 1024);
        if (block) {
            // 必须实际使用内存，否则 Linux 会延迟分配
            for (int i = 0; i < 100 * 1024 * 1024; i += 4096) {
                block[i] = 1;
            }
            blocks.push_back(block);
        }
    }
    return 0;
}
