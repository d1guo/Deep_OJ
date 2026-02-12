// RE 测试: 数组越界
// 预期结果: Runtime Error
#include <iostream>
int main() {
    int arr[10];
    // 访问远超数组边界的内存
    std::cout << arr[1000000] << std::endl;
    return 0;
}
