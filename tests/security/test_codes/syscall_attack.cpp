/**
 * 系统调用攻击测试: 尝试执行危险系统调用
 * 
 * 攻击向量:
 * 1. execve - 执行其他程序
 * 2. system - shell 命令
 * 3. popen - 管道命令
 * 
 * 预期结果: 被 Seccomp 拦截
 */
#include <iostream>
#include <cstdlib>
#include <cstdio>
#include <unistd.h>

int main() {
    // 尝试 1: system()
    std::cout << "Trying system()..." << std::endl;
    int ret = system("cat /etc/passwd");
    std::cout << "system() returned: " << ret << std::endl;
    
    // 尝试 2: popen()
    std::cout << "Trying popen()..." << std::endl;
    FILE* fp = popen("whoami", "r");
    if (fp) {
        char buf[256];
        if (fgets(buf, sizeof(buf), fp)) {
            std::cout << "popen() output: " << buf << std::endl;
        }
        pclose(fp);
    } else {
        std::cout << "popen() failed" << std::endl;
    }
    
    // 尝试 3: execve()
    std::cout << "Trying execve()..." << std::endl;
    char* args[] = {(char*)"/bin/sh", (char*)"-c", (char*)"id", nullptr};
    execve("/bin/sh", args, nullptr);
    std::cout << "execve() failed (expected)" << std::endl;
    
    return 0;
}
