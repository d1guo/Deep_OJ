// RE 测试: 段错误 (空指针解引用)
// 预期结果: Runtime Error
#include <iostream>
int main() {
    int* ptr = nullptr;
    *ptr = 42;  // 空指针解引用，触发 SIGSEGV
    return 0;
}
