/**
 * WA 测试: 错误答案
 * 预期输入: 1 2
 * 预期输出: 3
 * 实际输出: 4 (故意错误)
 */
#include <iostream>
int main() {
    int a, b;
    std::cin >> a >> b;
    std::cout << a + b + 1 << std::endl;  // 故意 +1 导致错误
    return 0;
}
