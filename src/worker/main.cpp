#include <iostream>
#include <memory>
#include <string>
#include <thread>
#include <fstream>
#include <sstream>
#include <vector>
#include <filesystem>
#include <algorithm> 
#include <semaphore>
#include <cstring> // for memcmp

#include <grpcpp/grpcpp.h>
#include <sw/redis++/redis++.h>
#include <nlohmann/json.hpp>

#include "judge.grpc.pb.h" 
#include "sandbox.h"
// [关键修正] 必须引入这个头文件才能读取配置
#include "sandbox_internal.h" 

// [关键修正] 声明 Config 加载函数 (在 config.cpp 定义)
void LoadConfig(const std::string& path);

using json = nlohmann::json;
using grpc::Server;
using grpc::ServerBuilder;
using grpc::ServerContext;
using grpc::Status;
namespace fs = std::filesystem;

// 全局 Redis 连接
std::shared_ptr<sw::redis::Redis> g_redis = nullptr;

// 全局并发控制信号量
std::unique_ptr<std::counting_semaphore<>> g_task_sem = nullptr;

// ---------------------------------------------------------
// 🛠️ 判题核心：流式比对 (Stream Comparator) - OOM Safe
// ---------------------------------------------------------

// Helper: 计算文件的“有效长度” (忽略末尾所有空白字符)
std::streampos get_effective_size(const std::string& path) {
    std::ifstream in(path, std::ios::binary | std::ios::ate);
    if (!in.is_open()) return 0;

    std::streampos len = in.tellg();
    if (len == 0) return 0;

    const size_t BUF_SIZE = 8192;
    char buffer[BUF_SIZE];
    
    std::streampos current_pos = len;
    while (current_pos > 0) {
        // 计算本次要读取的大小
        size_t read_size = (current_pos >= (std::streampos)BUF_SIZE) ? BUF_SIZE : (size_t)current_pos;
        
        // 移动读指针到块的起始位置
        std::streampos start_pos = current_pos - (std::streampos)read_size;
        in.seekg(start_pos);
        in.read(buffer, read_size);
        
        // 倒序检查 buffer
        for (long i = read_size - 1; i >= 0; --i) {
            if (!std::isspace(static_cast<unsigned char>(buffer[i]))) {
                // 找到了最后一个非空字符，有效长度 = start_pos + i + 1
                return start_pos + (std::streampos)(i + 1);
            }
        }
        
        current_pos = start_pos;
    }
    
    // 全是空白
    return 0;
}

// 返回 true 表示 AC，false 表示 WA
bool check_output(const std::string& user_out_path, const std::string& std_ans_path) {
    if (!fs::exists(std_ans_path)) return false; 
    // 用户输出如果不存在，直接 WA (可能是 Crash 或被删)
    if (!fs::exists(user_out_path)) return false; 
    
    // 1. 获取有效长度 (O(1) 只需要扫末尾空字符)
    std::streampos user_len = get_effective_size(user_out_path);
    std::streampos std_len = get_effective_size(std_ans_path);

    if (user_len != std_len) return false;
    if (user_len == 0) return true; // 都是空或者全是空白字符 -> AC

    // 2. 流式逐块比对
    std::ifstream f1(user_out_path, std::ios::binary);
    std::ifstream f2(std_ans_path, std::ios::binary);
    
    const size_t BUF_SIZE = 8192;
    char buf1[BUF_SIZE];
    char buf2[BUF_SIZE];

    std::streampos remaining = user_len;

    while (remaining > 0) {
        size_t to_read = (remaining >= (std::streampos)BUF_SIZE) ? BUF_SIZE : (size_t)remaining;
        
        f1.read(buf1, to_read);
        f2.read(buf2, to_read);

        if (!f1 || !f2) return false; // 读取异常

        if (std::memcmp(buf1, buf2, to_read) != 0) {
            return false; 
        }

        remaining -= to_read;
    }

    // [关键修正] 语法错误修复
    return true;
}

// ---------------------------------------------------------
// 🧵 后台工作线程
// ---------------------------------------------------------
void process_task(deep_oj::TaskRequest task) {
    std::string job_id = task.job_id();
    std::cout << "[Worker] 🔥 开始后台处理: " << job_id << std::endl;

    deep_oj::Sandbox sandbox;
    json result_json;
    result_json["job_id"] = job_id;

    // 1. 准备源码
    std::string src_filename = "solution.cpp"; 
    {
        std::ofstream out(src_filename, std::ios::binary);
        out << task.code(); 
    }

    // 2. 编译
    deep_oj::CompileResult cres = sandbox.Compile(job_id, task.code());

    if (cres.status != deep_oj::JudgeStatus::ACCEPTED) { // 注意：这里CompileResult结构体其实没有status字段，只有success
        // 修正：适配 Sandbox::Compile 接口定义
        if (!cres.success) {
            result_json["status"] = "Compile Error";
            result_json["error"] = cres.error_message;
            goto REPORT;
        }
    }

    // 3. 运行 & 判题
    {
        // 假设题目 ID 对应的测试数据目录 (生产环境应从配置或 task 读取)
        // 这里为了演示，假设数据就在当前目录的 data/ 下
        // 比如: data/1001/1.in, data/1001/1.out
        // string problem_id = task.problem_id(); ...
        
        // 临时构造输入输出文件名
        // 在 Sandbox::Run 内部，用户输出通常被重定向到 Sandbox 目录下的 "output.txt"
        // 假设我们只测一组数据：
        
        // [FIXME] 实际生产中这里需要循环测试所有 test case
        std::string std_in = "data/1.in";   // 你的测试输入
        std::string std_out = "data/1.out"; // 你的标准答案
        
        // 运行沙箱
        // 注意：Run 接口参数需要传 stdin/stdout/stderr 的路径
        // 我们利用 Sandbox 自动处理路径
        std::string user_out_filename = "output.txt";
        
        deep_oj::RunResult rres = sandbox.Run(
            cres.exe_path, 
            std_in,             // stdin (挂载或拷贝)
            user_out_filename,  // stdout (写到哪里)
            "error.txt",        // stderr
            task.time_limit(), 
            task.memory_limit()
        );

        result_json["time_used"] = rres.time_used;
        result_json["memory_used"] = rres.memory_used;

        if (rres.status == deep_oj::SandboxStatus::OK) {
            // 沙箱运行成功，现在进行答案比对
            // 用户的输出文件完整路径：exe_path 的父目录 + output.txt
            fs::path user_out_path = fs::path(cres.exe_path).parent_path() / user_out_filename;
            
            // [关键修正] 调用新的判题函数
            bool is_ac = check_output(user_out_path.string(), std_out);
            
            if (is_ac) {
                result_json["status"] = "Accepted";
                result_json["judge_result"] = 1; 
            } else {
                result_json["status"] = "Wrong Answer";
                result_json["judge_result"] = 2; 
            }
        } else {
            // 异常状态映射
            if (rres.status == deep_oj::SandboxStatus::TIME_LIMIT_EXCEEDED) result_json["status"] = "Time Limit Exceeded";
            else if (rres.status == deep_oj::SandboxStatus::MEMORY_LIMIT_EXCEEDED) result_json["status"] = "Memory Limit Exceeded";
            else if (rres.status == deep_oj::SandboxStatus::OUTPUT_LIMIT_EXCEEDED) result_json["status"] = "Output Limit Exceeded";
            else result_json["status"] = "Runtime Error";
        }
    }

REPORT:
    // 4. 结果上报
    if (g_redis) {
        try {
            std::string res_str = result_json.dump();
            g_redis->set("result:" + job_id, res_str);
            g_redis->expire("result:" + job_id, 3600);
            
            // 只有 AC 才写 Cache
            if (task.has_cache_key() && !task.cache_key().empty() && result_json["status"] == "Accepted") {
                g_redis->set(task.cache_key(), res_str);
                g_redis->expire(task.cache_key(), 86400); 
            }
            g_redis->publish("job_done", job_id);
            std::cout << "[Worker] ✅ 完成: " << job_id << " (" << result_json["status"] << ")" << std::endl;
        } catch (...) {}
    }
    
    // 清理临时文件
    sandbox.Cleanup(job_id);
}

// ... WorkerServiceImpl 保持不变 ...
// ... 为了节省篇幅省略，请保留原来的 Service 代码 ...
class WorkerServiceImpl final : public deep_oj::JudgeService::Service {
    Status ExecuteTask(ServerContext* context, const deep_oj::TaskRequest* request,
                  deep_oj::TaskResponse* response) override {
        
        std::cout << "[Worker] 收到调度请求 (ID: " << request->job_id() << ")" << std::endl;

        if (!g_task_sem->try_acquire()) {
            std::cout << "[Worker] ⚠️ High Load - 拒绝请求: " << request->job_id() << std::endl;
            return Status(grpc::RESOURCE_EXHAUSTED, "Worker is busy");
        }

        std::thread([req_copy = *request]() mutable {
            struct Guard {
                ~Guard() { g_task_sem->release(); }
            } guard;
            process_task(std::move(req_copy));
        }).detach();

        return Status::OK;
    }
};

int main(int argc, char** argv) {
    // [关键修正] 1. 加载配置 (必须在所有逻辑之前)
    LoadConfig("config.yaml");

    // 2. Redis 连接
    try {
        const char* env_redis = std::getenv("REDIS_HOST");
        std::string redis_host = env_redis ? env_redis : "127.0.0.1";
        g_redis = std::make_shared<sw::redis::Redis>("tcp://" + redis_host + ":6379");
        std::cout << "[Worker] Redis 连接成功" << std::endl;
    } catch (const std::exception& e) {
        std::cerr << "❌ [Fatal] Redis 连接失败" << std::endl;
        return 1;
    }

    // 3. 初始化全局信号量
    int pool_size = deep_oj::g_runner_config.pool_size;
    if (pool_size <= 0) pool_size = 4; // 兜底
    g_task_sem = std::make_unique<std::counting_semaphore<>>(pool_size);
    std::cout << "[Worker] 🔥 并发模型已初始化: Max Threads = " << pool_size << std::endl;

    // 4. 启动 gRPC Server
    // 建议从配置读取端口
    int port = deep_oj::g_runner_config.server_port;
    if (port <= 0) port = 50051;
    std::string server_address("0.0.0.0:" + std::to_string(port));
    
    WorkerServiceImpl service;

    ServerBuilder builder;
    builder.AddListeningPort(server_address, grpc::InsecureServerCredentials());
    builder.RegisterService(&service);
    
    std::unique_ptr<Server> server(builder.BuildAndStart());
    std::cout << "🚀 [Worker] 启动监听: " << server_address << std::endl;
    server->Wait();

    return 0;
}