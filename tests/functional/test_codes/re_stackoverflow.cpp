// RE 测试: 栈溢出 (无限递归)
// 预期结果: Runtime Error
#include <iostream>
void infinite_recursion(int depth) {
    char buffer[65536];  // 64KB 每次递归消耗栈空间 (快速触发 SIGSEGV)
    buffer[0] = depth;
    infinite_recursion(depth + 1);
}
int main() {
    infinite_recursion(0);
    return 0;
}
