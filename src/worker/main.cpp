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

// 返回 true 表示 AC，false 表示 WA
bool check_output(const std::string& user_out_path, const std::string& std_ans_path) {
    if (!fs::exists(std_ans_path)) {
        std::cerr << "[Diff] Standard answer missing: " << std_ans_path << std::endl;
        return false;
    }
    if (!fs::exists(user_out_path)) {
        std::cerr << "[Diff] User output missing: " << user_out_path << std::endl;
        return false;
    }

    std::ifstream f_user(user_out_path);
    std::ifstream f_std(std_ans_path);

    // 自动跳过 whitespace 进行 token-based 比较
    std::string s_u, s_s;
    while (true) {
        bool b_u = (bool)(f_user >> s_u);
        bool b_s = (bool)(f_std >> s_s);

        // 如果一个读完了，另一个没读完 -> WA
        if (b_u != b_s) {
            std::cout << "[Diff] Length Mismatch! (One stream ended early)" << std::endl;
            return false;
        }

        // 如果都读完了 -> AC
        if (!b_u) return true;

        // 如果内容不一样 -> WA
        if (s_u != s_s) {
            std::cout << "[Diff] Mismatch! User: [" << s_u << "] vs Std: [" << s_s << "]" << std::endl;
            return false;
        }
    }
}

// ---------------------------------------------------------
// 🧵 后台工作线程
// ---------------------------------------------------------
void process_task(deep_oj::TaskRequest task) {
    std::string job_id = task.job_id();
    std::cout << "[Worker] 🔥 开始后台处理: " << job_id << std::endl;

    // 1. 初始化沙箱 (使用配置中的工作目录)
    deep_oj::Sandbox sandbox(deep_oj::g_runner_config.workspace_root);
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

    if (!cres.success) {
        result_json["status"] = "Compile Error";
        result_json["error"] = cres.error_message;
        goto REPORT;
    }

    // 3. 运行 & 判题
    {
        // 假设题目 ID 对应的测试数据目录 (生产环境应从配置或 task 读取)
        // 这里为了演示，假设数据就在当前目录的 data/ 下
        // 比如: data/1001/1.in, data/1001/1.out
        
        // [FIXME] 实际生产中这里需要循环测试所有 test case
        std::string std_in = "data/1.in";   // 你的测试输入
        std::string std_out = "data/1.out"; // 你的标准答案
        
        // 获取工作目录并构建绝对路径
        fs::path work_dir = fs::path(cres.exe_path).parent_path();
        fs::path output_path = work_dir / "output.txt";
        fs::path error_path = work_dir / "error.txt";
        
        deep_oj::RunResult rres = sandbox.Run(
            cres.exe_path, 
            std_in,                 // stdin 
            output_path.string(),   // stdout (绝对路径)
            error_path.string(),    // stderr (绝对路径)
            task.time_limit(), 
            task.memory_limit()
        );

        result_json["time_used"] = rres.time_used;
        result_json["memory_used"] = rres.memory_used;

        if (rres.status == deep_oj::SandboxStatus::OK) {
            // 沙箱运行成功，现在进行答案比对
            // [Diff Debug]
            bool is_ac = check_output(output_path.string(), std_out);
            
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
            if (!task.cache_key().empty() && result_json["status"] == "Accepted") {
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
    // 1. 加载配置 (必须在所有逻辑之前)
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