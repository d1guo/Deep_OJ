#include <iostream>
#include <memory>
#include <string>
#include <vector>
#include <format>
#include <chrono>
#include <thread>
#include <cstring>
#include <algorithm> // for std::all_of
#include <grpcpp/grpcpp.h>
#include "judge.grpc.pb.h"

using grpc::Channel;
using grpc::ClientContext;
using grpc::Status;
using namespace deep_oj;

struct TestCase {
    std::string name;
    std::string code;
    int time_limit_ms;
    int mem_limit_kb;
    JudgeStatus expected_status;
};

class SchedulerClient
{
public:
    SchedulerClient(std::shared_ptr<Channel> channel)
        : stub_(JudgeService::NewStub(channel)) {}

    void RunTests()
    {
        std::vector<TestCase> tests = {
            {
                .name = "1. Normal AC (Hello World)",
                .code = R"(
#include <iostream>
int main() {
    std::cout << "Hello, Namespace!" << std::endl;
    return 0;
}
)",
                .time_limit_ms = 1000,
                .mem_limit_kb = 32768, // 32MB
                .expected_status = JudgeStatus::ACCEPTED
            },
            {
                .name = "2. TLE Test (Infinite Loop)",
                .code = R"(
int main() {
    volatile int i = 0;
    while(true) {
        i++;
    }
    return 0;
}
)",
                .time_limit_ms = 1000, // 1s
                .mem_limit_kb = 32768,
                .expected_status = JudgeStatus::TIME_LIMIT_EXCEEDED
            },
            {
                .name = "3. MLE Test (OOM)",
                .code = R"(
#include <vector>
#include <cstring>
int main() {
    // 尝试分配 50MB (超过限制 16MB)
    // 必须真的写入数据，通过 page fault 触发物理内存分配
    size_t size = 50 * 1024 * 1024;
    std::vector<char> v(size);
    std::memset(v.data(), 1, size);
    return 0;
}
)",
                .time_limit_ms = 2000,
                .mem_limit_kb = 16384, // 16MB
                .expected_status = JudgeStatus::MEMORY_LIMIT_EXCEEDED
            },
             {
                .name = "4. Runtime Error (Div by Zero)",
                .code = R"(
#include <iostream>
int main() {
    volatile int a = 1;
    volatile int b = 0;
    std::cout << (a / b) << std::endl;
    return 0;
}
)",
                .time_limit_ms = 1000,
                .mem_limit_kb = 32768,
                .expected_status = JudgeStatus::RUNTIME_ERROR
            },
            {
                .name = "5. Security Test (Privilege Escalation - Read /etc/shadow)",
                .code = R"(
#include <iostream>
#include <fstream>

int main() {
    // 尝试读取只有 root 才能读的文件 /etc/shadow
    // 注意：因为我们目前只做了 Namespace 隔离，没有做 chroot/pivot_root，
    // 而且是以 root 身份运行 worker，如果读取成功，说明存在严重安全漏洞。
    std::ifstream file("/etc/shadow");
    if (file.good()) {
        std::string line;
        std::getline(file, line);
        // 读取成功！返回 0 (AC)，代表攻击成功（系统不安全）
        std::cout << "HACKED: " << line.substr(0, 10) << "..." << std::endl;
        return 0; 
    }
    // 读取失败（权限拒绝），返回 1 (RE)，代表攻击失败（系统安全）
    return 1;
}
)",
                .time_limit_ms = 1000,
                .mem_limit_kb = 32768,
                // 我们期望它失败 (RE)，如果 AC 了说明可以通过代码读取宿主机敏感文件
                .expected_status = JudgeStatus::RUNTIME_ERROR 
            },
             {
                .name = "5. Security Test (Root & File Write)",
                .code = R"(
#include <iostream>
#include <fstream>
#include <unistd.h>

int main() {
    // 1. Check Root
    if (getuid() == 0) {
        // We are root! Try to leave a mark on the host system.
        std::ofstream ofs("/tmp/hacked_by_worker.txt");
        if (ofs) {
            ofs << "I was here. UID=" << getuid();
            return 1; // Return non-zero to signal 'success' of the hack (which is a Runtime Error for the OJ)
        }
    }
    return 0; 
}
)",
                .time_limit_ms = 1000,
                .mem_limit_kb = 32768,
                .expected_status = JudgeStatus::ACCEPTED // We expect AC: Permission denied (Safe)
            },
            {
                .name = "6. Network Access (Google DNS)",
                .code = R"(
#include <sys/socket.h>
#include <arpa/inet.h>
#include <unistd.h>
#include <cstring>
#include <iostream>

int main() {
    int sock = socket(AF_INET, SOCK_STREAM, 0);
    if (sock == -1) return 0; // Failed to create socket

    struct sockaddr_in server;
    server.sin_addr.s_addr = inet_addr("8.8.8.8");
    server.sin_family = AF_INET;
    server.sin_port = htons(53);

    // Try to connect (Timeout usually)
    struct timeval tv;
    tv.tv_sec = 2;
    tv.tv_usec = 0;
    setsockopt(sock, SOL_SOCKET, SO_RCVTIMEO, (const char*)&tv, sizeof tv);

    if (connect(sock, (struct sockaddr *)&server , sizeof(server)) < 0) {
        return 1; // Connect failed (Good! Secure!) -> Runtime Error
    }
    
    return 0; // Connect success (Bad! Insecure!) -> Accepted
}
)",
                .time_limit_ms = 3000,
                .mem_limit_kb = 32768,
                .expected_status = JudgeStatus::RUNTIME_ERROR // Expect secure failure
            },
            {
                .name = "7. Process Isolation (Count PIDs)",
                .code = R"(
#include <filesystem>
#include <iostream>

namespace fs = std::filesystem;

int main() {
    int count = 0;
    // Count directories in /proc that are numbers
    for (const auto& entry : fs::directory_iterator("/proc")) {
        if (entry.is_directory()) {
            std::string name = entry.path().filename().string();
            if (std::all_of(name.begin(), name.end(), ::isdigit)) {
                count++;
            }
        }
    }
    
    // If we see more than 5 processes, we are likely seeing the host
    if (count > 5) {
        return 1; // Leaked! RE
    }
    return 0; // Isolated! AC
}
)",
                .time_limit_ms = 1000,
                .mem_limit_kb = 32768,
                .expected_status = JudgeStatus::ACCEPTED
            },
            {
                .name = "8. Fork Bomb Attempt",
                .code = R"(
#include <unistd.h>
#include <sys/wait.h>
#include <vector>

int main() {
    // Try to fork 50 times
    std::vector<pid_t> pids;
    for(int i=0; i<50; ++i) {
         pid_t pid = fork();
         if (pid == 0) {
             // Child: sleep and exit
             sleep(1);
             return 0;
         } else if (pid > 0) {
             pids.push_back(pid);
         } else {
             // Fork failed (Good!)
             return 1; 
         }
    }
    // Clean up
    for(auto p : pids) waitpid(p, nullptr, 0);
    return 0; // Fork succeeded seems bad if limit is low
}
)",
                .time_limit_ms = 3000,
                .mem_limit_kb = 32768,
                .expected_status = JudgeStatus::RUNTIME_ERROR // Ideally we want to block forks
            },
            {
                .name = "9. File System Read Injection (Host File)",
                .code = R"(
#include <fstream>
#include <iostream>

int main() {
    // Try to read /etc/os-release (World readable)
    std::ifstream ifs("/etc/os-release");
    if(ifs.good()) {
        return 0; // Success (Bad for strict sandbox) -> AC
    }
    return 1; // Fail (Good) -> RE
}
)",
                .time_limit_ms = 1000,
                .mem_limit_kb = 32768,
                .expected_status = JudgeStatus::RUNTIME_ERROR // We WANT it to fail, but it will pass now
            }
        };

        int passed = 0;
        for (const auto& test : tests)
        {
            std::cout << "\n========================================" << std::endl;
            std::cout << "Running Test: " << test.name << std::endl;
            std::cout << "----------------------------------------" << std::endl;

            SubmitRequest request;
            request.set_request_id("REQ-" + std::to_string(std::chrono::system_clock::now().time_since_epoch().count()));
            request.set_code(test.code);
            request.set_language(Language::CPP);
            request.set_time_limit(test.time_limit_ms);
            request.set_memory_limit(test.mem_limit_kb);

            SubmitResponse response;
            ClientContext context;

            Status status = stub_->Submit(&context, request, &response);

            if (!status.ok())
            {
                std::cout << "[RPC Failed] " << status.error_code() << ": " << status.error_message() << std::endl;
                continue;
            }

            std::cout << "Actual Status: " << JudgeStatus_Name(response.status()) << std::endl;
            std::cout << "Message: " << response.error_message() << std::endl;
            std::cout << "Time: " << response.actual_time() << "ms" << std::endl;
            std::cout << "Memory: " << response.actual_memory() << "KB" << std::endl;

            if (response.status() == test.expected_status)
            {
                std::cout << ">> Result: PASS" << std::endl;
                passed++;
            }
            else
            {
                std::cout << ">> Result: FAIL (Expected " << JudgeStatus_Name(test.expected_status) << ")" << std::endl;
            }
        }

        std::cout << "\n========================================" << std::endl;
        std::cout << "Summary: " << passed << "/" << tests.size() << " tests passed." << std::endl;
    }

private:
    std::unique_ptr<JudgeService::Stub> stub_;
};

int main()
{
    SchedulerClient client(grpc::CreateChannel(
        "unix:///tmp/deep_oj_worker.sock", grpc::InsecureChannelCredentials()));

    client.RunTests();

    return 0;
}
