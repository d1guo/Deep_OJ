/**
 * 文件系统攻击测试: 尝试读取敏感文件
 * 
 * 攻击目标:
 * 1. /etc/passwd - 系统用户
 * 2. /etc/shadow - 密码哈希 (需要 root)
 * 3. /proc/self/maps - 内存布局
 * 4. 其他评测机敏感文件
 * 
 * 预期结果: 被 chroot 或权限限制拦截
 */
#include <iostream>
#include <fstream>
#include <string>

int main() {
    const char* targets[] = {
        "/etc/passwd",
        "/etc/shadow",
        "/proc/self/maps",
        "/proc/self/environ",
        "/flag",
        "/home/judge/config.yaml",
        "../../../etc/passwd",  // 路径穿越
        "../../../../flag"
    };
    
    for (const char* target : targets) {
        std::ifstream file(target);
        if (file.is_open()) {
            std::string line;
            std::getline(file, line);
            std::cout << "[LEAK] " << target << ": " << line << std::endl;
        } else {
            std::cout << "[BLOCKED] " << target << std::endl;
        }
    }
    
    return 0;
}
