// TLE 测试: CPU 密集型计算
// 预期结果: Time Limit Exceeded
#include <iostream>
int main() {
    long long sum = 0;
    for (long long i = 0; i < 1e18; i++) {
        sum += i;
    }
    std::cout << sum << std::endl;
    return 0;
}
