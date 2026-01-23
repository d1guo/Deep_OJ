#include "sandbox.h"
#include <iostream>
#include <vector>
#include <fstream>
#include <chrono>
#include <thread>
#include <cstring>
#include <cassert>
#include <sys/wait.h>
#include <iomanip>

using namespace deep_oj;

// ================== 测试工具函数 ==================
const std::string RESET = "\033[0m";
const std::string RED = "\033[31m";
const std::string GREEN = "\033[32m";
const std::string YELLOW = "\033[33m";
const std::string BLUE = "\033[34m";

void PrintResult(const std::string& name, bool pass, const std::string& msg = "") {
    std::cout << "[" << (pass ? GREEN + "PASS" : RED + "FAIL") << RESET << "] " 
              << std::left << std::setw(25) << name 
              << (msg.empty() ? "" : " | " + msg) << std::endl;
}
void LoadConfig(const std::string& path);
std::string StatusToString(SandboxStatus s)
{
    switch(s) {
        case SandboxStatus::OK: return "OK";
        case SandboxStatus::TIME_LIMIT_EXCEEDED: return "TLE";
        case SandboxStatus::MEMORY_LIMIT_EXCEEDED: return "MLE";
        case SandboxStatus::OUTPUT_LIMIT_EXCEEDED: return "OLE";
        case SandboxStatus::RUNTIME_ERROR: return "RE";
        case SandboxStatus::SYSTEM_ERROR: return "SE";
        default: return "UNKNOWN";
    }
}

void RunTest(Sandbox& sandbox, 
             std::string name, 
             std::string code, 
             SandboxStatus expected_status, 
             int time_limit = 2000, 
             int mem_limit = 128 * 1024) 
{
    std::string req_id = "test_" + name;
    
    // 1. Compile
    auto compile_res = sandbox.Compile(req_id, code);
    if (!compile_res.success) {
        PrintResult(name, false, "Compile Failed: " + compile_res.error_message);
        return;
    }

    // 2. Run
    auto run_res = sandbox.Run(compile_res.exe_path, time_limit, mem_limit);

    // 3. Verify
    bool pass = (run_res.status == expected_status);
    
    // 特殊情况：如果是 Hack 类测试，只要不是 OK 就算通过（通常是 RE 或 SE）
    // 但为了严谨，我们通常期望 Hack 行为触发 RE (Permission Denied)
    
    std::string msg = "Exp: " + StatusToString(expected_status) + 
                      ", Got: " + StatusToString(run_res.status);
    
    if (!pass && run_res.status == SandboxStatus::RUNTIME_ERROR) {
        msg += " (" + std::to_string(run_res.exit_code) + ")";
    }

    PrintResult(name, pass, msg);
}

int main() {
    std::cout << "[Init] Loading Config for Security Test..." << std::endl;
    try {
        // 假设你在 build 目录运行，config.yaml 就在当前目录
        LoadConfig("config.yaml"); 
    } catch (const std::exception& e) {
        std::cerr << "[Fatal] Config load failed: " << e.what() << std::endl;
        return 1;
    }
    // ---------------------------------------------------------

    std::cout << "=== DeepOJ Sandbox: Ultimate Security & Stress Test ===" << std::endl;
    std::cout << BLUE << "=== DeepOJ Sandbox: Ultimate Security & Stress Test ===" << RESET << std::endl;

    try {
        Sandbox sandbox("/tmp/deep_oj_tests");

        // ================= 1. 基础功能测试 =================
        std::cout << YELLOW << "\n[Group 1: Basic Logic]" << RESET << std::endl;
        
        RunTest(sandbox, "Normal_AC", 
            R"(int main() { return 0; })", 
            SandboxStatus::OK);

        RunTest(sandbox, "Normal_RE", 
            R"(int main() { return 1; })", 
            SandboxStatus::RUNTIME_ERROR); // Exit code 1

        RunTest(sandbox, "Normal_SegFault", 
            R"(int main() { int* p=0; *p=1; return 0; })", 
            SandboxStatus::RUNTIME_ERROR); // Signal 11 (SIGSEGV)

        // ================= 2. 资源限制测试 =================
        std::cout << YELLOW << "\n[Group 2: Resource Limits]" << RESET << std::endl;

        // TLE: 死循环
        RunTest(sandbox, "Limit_Time_Loop", 
            R"(int main() { while(true); return 0; })", 
            SandboxStatus::TIME_LIMIT_EXCEEDED);

        // MLE: 疯狂分配内存 (256MB > 128MB Limit)
        // MLE: 逐步分配内存，触发父进程的监控 (Soft Limit)
        // 不要一次性申请 256MB，因为那会直接撞上 OS 的硬限制 (RLIMIT_AS) 导致 RE
        RunTest(sandbox, "Limit_Memory", 
            R"(
                #include <vector>
                #include <cstring>
                #include <thread>
                #include <chrono>
                
                int main() {
                    std::vector<char*> v;
                    // 目标：申请 200MB (限制是 128MB)
                    // 每次申请 10MB，分 20 次
                    for(int i=0; i<20; i++) {
                        char* p = new char[10 * 1024 * 1024]; 
                        // 关键：必须 memset 写入，才会真正占用物理内存(RSS)
                        std::memset(p, 0, 10 * 1024 * 1024);
                        v.push_back(p);
                        
                        // 给父进程一点反应时间 (10ms)
                        // 因为父进程是轮询的，不是中断触发的
                        std::this_thread::sleep_for(std::chrono::milliseconds(10));
                    }
                    return 0;
                }
            )", 
            SandboxStatus::MEMORY_LIMIT_EXCEEDED);

        // OLE: 输出超限 (10MB Limit)
        // [优化]: 加大输出量到 20MB，确保冲破缓冲区强制触发 SIGXFSZ
        RunTest(sandbox, "Limit_Output", 
            R"(
                #include <cstdio>
                int main() {
                    // 20MB > 10MB Limit
                    for(int i=0; i<20*1024*1024; i++) putchar('A'); 
                    return 0;
                }
            )", 
            SandboxStatus::OUTPUT_LIMIT_EXCEEDED);

        // Fork Bomb: 尝试耗尽进程数
        // 期望：因为 setrlimit NPROC=5，Fork 失败返回 -1，最终死循环被 TLE 杀掉
        // 或者因为无法 fork 导致逻辑错误退出 RE
        // 只要不是 OK 就行
        RunTest(sandbox, "Limit_Fork_Bomb", 
            R"(
                #include <unistd.h>
                int main() {
                    while(1) { fork(); }
                    return 0;
                }
            )", 
            SandboxStatus::TIME_LIMIT_EXCEEDED); // 或者是 RUNTIME_ERROR

        // ================= 3. 文件系统隔离测试 =================
        std::cout << YELLOW << "\n[Group 3: Filesystem Isolation]" << RESET << std::endl;

        // 尝试读取宿主机敏感文件
        // 期望：RE (文件不存在)
        RunTest(sandbox, "FS_Read_Host_Etc", 
            R"(
                #include <fstream>
                int main() {
                    std::ifstream f("/etc/passwd");
                    return f.good() ? 0 : 1;
                }
            )", 
            SandboxStatus::RUNTIME_ERROR);

        // 尝试写入根目录 (这就是你刚才挂的那个)
        // 期望：RE (Permission Denied -> exit 1)
        RunTest(sandbox, "FS_Write_Root", 
            R"(
                #include <fstream>
                int main() {
                    std::ofstream f("/hacked.txt");
                    return f.good() ? 0 : 1; 
                }
            )", 
            SandboxStatus::RUNTIME_ERROR);

        // 尝试覆盖系统命令 (/bin/ls)
        RunTest(sandbox, "FS_Overwrite_Bin", 
            R"(
                #include <cstdio>
                int main() {
                    FILE* f = fopen("/bin/ls", "w");
                    return f ? 0 : 1;
                }
            )", 
            SandboxStatus::RUNTIME_ERROR);

        // ================= 4. 网络隔离测试 =================
        std::cout << YELLOW << "\n[Group 4: Network Isolation]" << RESET << std::endl;

        // 尝试创建一个 TCP Socket 连接公网 (8.8.8.8)
        // 因为用了 CLONE_NEWNET，只有 lo 设备，且没配置路由，connect 应该失败
        RunTest(sandbox, "Net_Connect_Internet", 
            R"(
                #include <sys/socket.h>
                #include <arpa/inet.h>
                #include <unistd.h>
                int main() {
                    int sock = socket(AF_INET, SOCK_STREAM, 0);
                    if (sock < 0) return 1; // socket creation failed (good)
                    
                    struct sockaddr_in serv_addr;
                    serv_addr.sin_family = AF_INET;
                    serv_addr.sin_port = htons(80);
                    inet_pton(AF_INET, "8.8.8.8", &serv_addr.sin_addr);
                    
                    if (connect(sock, (struct sockaddr*)&serv_addr, sizeof(serv_addr)) == 0) {
                        return 0; // Connected! (BAD)
                    }
                    return 1; // Failed (Good)
                }
            )", 
            SandboxStatus::RUNTIME_ERROR);

        // 尝试绑定端口 (Server)
        RunTest(sandbox, "Net_Bind_Port", 
            R"(
                #include <sys/socket.h>
                #include <netinet/in.h>
                int main() {
                    int sock = socket(AF_INET, SOCK_STREAM, 0);
                    struct sockaddr_in addr;
                    addr.sin_family = AF_INET;
                    addr.sin_addr.s_addr = INADDR_ANY;
                    addr.sin_port = htons(8080);
                    // 在 NEWNET 里，bind 应该成功(因为是独立的 localhost)，但外面访问不到
                    // 这里主要是测试不会 Crash
                    if (bind(sock, (struct sockaddr*)&addr, sizeof(addr)) == 0) return 0;
                    return 1;
                }
            )", 
            SandboxStatus::OK); // 内部 Bind 允许成功，只要出不去就行

        // ================= 5. 系统攻击测试 =================
        std::cout << YELLOW << "\n[Group 5: System Attacks]" << RESET << std::endl;

        // 尝试杀掉其他进程 (PID -1 = kill all possible)
        RunTest(sandbox, "Sys_Kill_All", 
            R"(
                #include <signal.h>
                int main() {
                    // Try to kill everything I can see
                    if (kill(-1, SIGKILL) == 0) return 0;
                    return 1;
                }
            )", 
            SandboxStatus::RUNTIME_ERROR); // 只能杀自己，或者 kill 返回失败

        // 尝试调用 reboot (需要 root 权限，且受 Capability 限制)
        RunTest(sandbox, "Sys_Reboot", 
            R"(
                #include <sys/reboot.h>
                #include <unistd.h>
                int main() {
                    syscall(169, 0xFEE1DEAD, 672274793, 0x1234567); // sys_reboot
                    return 1; // Should fail and reach here
                }
            )", 
            SandboxStatus::RUNTIME_ERROR);

    } catch (const std::exception& e) {
        std::cerr << RED << "Test Suite Fatal Error: " << e.what() << RESET << std::endl;
        return 1;
    }

    std::cout << BLUE << "\n=== All Tests Completed ===" << RESET << std::endl;
    return 0;
}