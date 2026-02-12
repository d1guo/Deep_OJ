/**
 * @file output_bomb.cpp
 * @brief 输出炸弹测试
 * 
 * 预期行为:
 * - setrlimit(RLIMIT_FSIZE) 限制文件大小
 * - 超出限制时收到 SIGXFSZ
 * 
 * 预期结果: OLE (Output Limit Exceeded) 或 RE
 */
#include <iostream>

int main() {
    // 无限输出字符
    while (true) {
        std::cout << "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n";
    }
    return 0;
}
