// RE 测试: 除零错误
// 预期结果: Runtime Error
#include <iostream>
int main() {
    int a = 1;
    int b = 0;
    std::cout << a / b << std::endl;  // 整数除零，触发 SIGFPE
    return 0;
}
