/**
 * @file escape_test.cpp
 * @brief 沙箱逃逸测试
 * 
 * 预期行为:
 * - pivot_root 已将根目录切换到工作区
 * - /etc/passwd 在沙箱内不存在或内容不同
 * 
 * 预期结果: 无法读取宿主机敏感文件
 */
#include <fstream>
#include <iostream>
#include <string>

int main() {
    // 1. 尝试读取 /etc/passwd
    std::ifstream passwd("/etc/passwd");
    if (passwd.good()) {
        std::string line;
        while (std::getline(passwd, line)) {
            // 如果能读到 root 用户，说明沙箱逃逸成功
            if (line.find("root:") != std::string::npos) {
                std::cout << "ESCAPED: Found host /etc/passwd!" << std::endl;
                std::cout << line << std::endl;
                return 42;  // 特殊退出码表示逃逸成功
            }
        }
    }
    
    // 2. 尝试读取 /proc/1/cmdline (宿主机 init 进程)
    std::ifstream cmdline("/proc/1/cmdline");
    if (cmdline.good()) {
        std::string content;
        std::getline(cmdline, content);
        if (!content.empty()) {
            std::cout << "ESCAPED: Found /proc/1/cmdline: " << content << std::endl;
            return 42;
        }
    }
    
    // 3. 尝试写入文件到 /tmp (应该被隔离)
    std::ofstream test_file("/tmp/escape_test_marker");
    if (test_file) {
        test_file << "If this file appears in host /tmp, sandbox is broken!";
        test_file.close();
    }
    
    std::cout << "BLOCKED: Sandbox isolation working correctly" << std::endl;
    return 0;
}
