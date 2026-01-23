#include <iostream>
#include <memory>
#include <string>
#include <vector>
#include <thread>
#include <chrono>
#include <mutex>
#include <algorithm>
#include <random>
#include <atomic>
#include <sstream> // 引入流用于处理字符串

#include <grpcpp/grpcpp.h>
#include "judge.grpc.pb.h"

using grpc::Channel;
using grpc::ClientContext;
using grpc::Status;
using namespace deep_oj;

// ANSI 颜色代码
const std::string GREEN = "\033[32m";
const std::string RED = "\033[31m";
const std::string YELLOW = "\033[33m";
const std::string CYAN = "\033[36m";
const std::string RESET = "\033[0m";

static std::atomic<int> g_req_counter{0};

struct TestCase {
    std::string name;
    std::string code;
    int time_limit;
    int mem_limit;
    JudgeStatus expected_status;
};

std::mutex g_io_mutex;

// [改写 1] 使用流输出替代 std::format，保留颜色逻辑
void LogResult(const std::string& name, const std::string& result, bool pass, long time, long mem) {
    std::lock_guard<std::mutex> lock(g_io_mutex);
    std::cout << (pass ? GREEN : RED) << "[" << (pass ? "PASS" : "FAIL") << "] " << RESET
              << result << " | Time: " << time << "ms | Mem: " << mem << "KB | Case: " << name 
              << std::endl;
}

class Scheduler {
public:
    Scheduler(std::shared_ptr<Channel> channel)
        : stub_(JudgeService::NewStub(channel)) {}

    void SubmitTask(const TestCase& test) {
        ClientContext context;
        SubmitRequest request;
        
        auto now = std::chrono::system_clock::now().time_since_epoch().count();
        int unique_id = g_req_counter.fetch_add(1);

        // [改写 2] 使用 std::to_string 拼接唯一的请求 ID
        std::string req_id = "req_" + test.name + "_" + std::to_string(now) + "_" + std::to_string(unique_id);
        
        request.set_request_id(req_id);
        request.set_code(test.code);
        request.set_language(Language::CPP);
        request.set_time_limit(test.time_limit);
        request.set_memory_limit(test.mem_limit);

        SubmitResponse response;
        Status status = stub_->Submit(&context, request, &response);

        if (status.ok()) {
            bool pass = (response.status() == test.expected_status);
            if (test.expected_status == JudgeStatus::RUNTIME_ERROR && response.status() == JudgeStatus::ACCEPTED) {
                pass = false; 
            }

            std::string status_str = JudgeStatus_Name(response.status());
            if (!pass) {
                status_str += " (Exp: " + JudgeStatus_Name(test.expected_status) + ")";
            }
            
            LogResult(test.name, status_str, pass, response.actual_time(), response.actual_memory());
        } else {
            std::lock_guard<std::mutex> lock(g_io_mutex);
            // [改写 3] 直接流输出 RPC 错误信息
            std::cout << RED << "[RPC Error] " << status.error_message() 
                      << " (" << static_cast<int>(status.error_code()) << ")"
                      << RESET << std::endl;
        }
    }

private:
    std::unique_ptr<JudgeService::Stub> stub_;
};

int main() {
    // 确保你的 Worker 也在监听这个 unix socket
    std::string target_str = "unix:///tmp/deep_oj_worker.sock";
    auto channel = grpc::CreateChannel(target_str, grpc::InsecureChannelCredentials());
    Scheduler scheduler(channel);

    std::cout << CYAN << "=== Deep-OJ 调度器启动 ===" << RESET << std::endl;
    std::cout << "连接目标: " << target_str << std::endl;

    std::vector<TestCase> test_suite = {
        { "Hello World", R"(#include <iostream>
int main() { std::cout << "Hello Level 3!" << std::endl; return 0; })", 1000, 32768, JudgeStatus::ACCEPTED },
        { "Infinite Loop", R"(int main() { while(1); return 0; })", 1000, 32768, JudgeStatus::TIME_LIMIT_EXCEEDED },
        { "Memory Bomb", R"(#include <vector>
#include <cstring>
int main() { std::vector<char> v(50*1024*1024); std::memset(v.data(), 1, v.size()); return 0; })", 2000, 16384, JudgeStatus::MEMORY_LIMIT_EXCEEDED },
        { "Security: Read /etc/passwd", R"(#include <fstream>
#include <iostream>
int main() { std::ifstream f("/etc/passwd"); if(f.good()) return 0; return 1; })", 1000, 32768, JudgeStatus::RUNTIME_ERROR },
        { "Security: Network", R"(#include <sys/socket.h>
int main() { return socket(AF_INET, SOCK_STREAM, 0) > 0 ? 0 : 1; })", 1000, 32768, JudgeStatus::RUNTIME_ERROR }
    };

    std::cout << CYAN << "\n>>> 启动并发压力测试 (12 个请求) <<<" << RESET << std::endl;
    
    std::vector<std::thread> threads;
    for (int i = 0; i < 12; ++i) {
        const auto& test_case = test_suite[i % test_suite.size()];
        threads.emplace_back([&scheduler, test_case, i]() {
            std::this_thread::sleep_for(std::chrono::milliseconds(i * 50));
            scheduler.SubmitTask(test_case);
        });
    }

    for (auto& t : threads) {
        if (t.joinable()) t.join();
    }

    std::cout << CYAN << "\n>>> 所有任务已完成 <<<" << RESET << std::endl;
    return 0;
}