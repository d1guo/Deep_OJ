// TLE 测试: 死循环
// 预期结果: Time Limit Exceeded
#include <iostream>
int main() {
    while (true) {
        // 无限循环，触发 TLE
    }
    return 0;
}
