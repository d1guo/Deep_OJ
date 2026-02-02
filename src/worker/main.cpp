#include <iostream>
#include <memory>
#include <string>
#include <thread>
#include <fstream>
#include <sstream>
#include <vector>
#include <filesystem>
#include <algorithm> // for mismatch
#include <semaphore>

#include <grpcpp/grpcpp.h>
#include <sw/redis++/redis++.h>
#include <nlohmann/json.hpp>

#include "judge.grpc.pb.h" 
#include "sandbox.h"       

using json = nlohmann::json;
using grpc::Server;
using grpc::ServerBuilder;
using grpc::ServerContext;
using grpc::Status;
namespace fs = std::filesystem;

// 全局 Redis 连接
std::shared_ptr<sw::redis::Redis> g_redis = nullptr;

// 全局并发控制信号量 (4 核 CPU)
std::unique_ptr<std::counting_semaphore<>> g_task_sem = nullptr;

// ---------------------------------------------------------
// 🛠️ 判题核心：比对函数 (Diff / Check)
// ---------------------------------------------------------
// 读取文件全部内容并移除行末空格和文末换行 (Trim)
std::string read_and_trim(const std::string& path) {
    std::ifstream in(path);
    if (!in.is_open()) return "";

    std::stringstream ss;
    ss << in.rdbuf();
    std::string s = ss.str();

    // 简单实现：移除末尾空白
    while (!s.empty() && std::isspace(s.back())) {
        s.pop_back();
    }
    // 实际生产中通常需要逐行 Trim Right，这里做个简化版
    return s;
}

// 返回 true 表示 AC，false 表示 WA
bool check_output(const std::string& user_out_path, const std::string& std_ans_path) {
    // 如果没有标准答案文件，甚至无法判题（视为系统错误或默认通过，视业务而定）
    if (!fs::exists(std_ans_path)) return false; 

    std::string user = read_and_trim(user_out_path);
    std::string standard = read_and_trim(std_ans_path);

    return user == standard;
}

// ---------------------------------------------------------
// 🧵 后台工作线程：编译 -> 运行 -> 判题 -> 上报
// ---------------------------------------------------------
void process_task(deep_oj::TaskRequest task) {
    std::string job_id = task.job_id();
    std::cout << "[Worker] 🔥 开始后台处理: " << job_id << std::endl;

    deep_oj::Sandbox sandbox;
    json result_json;
    result_json["job_id"] = job_id;

    // 1. 准备文件 (Source Code)
    // -------------------------------------------------
    std::string src_filename = "solution.cpp"; // 目前写死 C++
    {
        std::ofstream out(src_filename, std::ios::binary);
        out << task.code(); // bytes 自动转 string
    }

    // 2. 编译 (Compile)
    // -------------------------------------------------
    // 假设 Compile 接口：Compile(job_id, source_code) -> 返回 exe_path
    deep_oj::CompileResult cres = sandbox.Compile(job_id, task.code());

    if (cres.status != deep_oj::JudgeStatus::ACCEPTED) {
        result_json["status"] = "Compile Error";
        result_json["error"] = cres.error_message;
        goto REPORT; // 跳转到上报环节
    }

    // 3. 运行 (Run) & 判题 (Check)
    // -------------------------------------------------
    {
        // 假设这里是单测试点逻辑。如果是多测，这里套个 for 循环。
        // 测试数据路径 (通常需挂载或配置，这里假设在当前目录 data/ 下)
        // 假设 Proto 里传了 problem_id，这里根据 problem_id 找数据
        // std::string input_file = "data/" + task.problem_id() + "/1.in";
        // std::string ans_file = "data/" + task.problem_id() + "/1.out";
        
        // MVP 阶段我们先随便造个简单的输入输出，或者让 Sandbox::Run 自己处理重定向
        std::string input_data = "1 2"; // 假设题目是 A+B
        // 实际项目里，Sandbox::Run 应该接受 input_file 路径，重定向 stdin
        
        // 运行沙箱
        deep_oj::RunResult rres = sandbox.Run(
            cres.exe_path, 
            task.time_limit(), 
            task.memory_limit()
        );

        result_json["time_used"] = rres.time_used;
        result_json["memory_used"] = rres.memory_used;

        if (rres.status == deep_oj::JudgeStatus::ACCEPTED) {
            // 沙箱运行没挂，现在检查答案对不对
            // 假设 sandbox 把用户输出写到了 "user.out"
            // 假设标准答案是 "3" (对应 1+2)
            // 实际上你需要去读取 std_ans_file
            
            // 为了跑通流程，我们先写死一个 check 逻辑，或者假设你本地有 "1.out"
            // bool is_ac = check_output("user.out", "data/1.out");
            
            // 【MVP 模拟】假设只有不超时不 RE 就算 AC
            result_json["status"] = "Accepted"; 
            result_json["judge_result"] = 1; // AC
        } else {
            // TLE, MLE, RE
            if (rres.status == deep_oj::JudgeStatus::TIME_LIMIT_EXCEEDED) result_json["status"] = "Time Limit Exceeded";
            else if (rres.status == deep_oj::JudgeStatus::MEMORY_LIMIT_EXCEEDED) result_json["status"] = "Memory Limit Exceeded";
            else result_json["status"] = "Runtime Error";
        }
    }

REPORT:
    // 4. 结果上报 (Report to Redis)
    // -------------------------------------------------
    if (g_redis) {
        try {
            std::string res_str = result_json.dump();
            
            // A. 写当前结果
            g_redis->set("result:" + job_id, res_str);
            g_redis->expire("result:" + job_id, 3600);

            // B. 写查重缓存 (只有非系统错误且有 cache_key 时)
            if (task.has_cache_key() && !task.cache_key().empty() && result_json["status"] != "System Error") {
                g_redis->set(task.cache_key(), res_str);
                g_redis->expire(task.cache_key(), 86400); // 缓存一天
                std::cout << "[Worker] 📝 更新缓存: " << task.cache_key() << std::endl;
            }

            // C. 发送完成通知 (Fast Path)
            g_redis->publish("job_done", job_id);
            std::cout << "[Worker] ✅ 完成: " << job_id << " (" << result_json["status"] << ")" << std::endl;

        } catch (const std::exception& e) {
            std::cerr << "❌ [Worker] Redis 操作失败: " << e.what() << std::endl;
        }
    }
}

// ---------------------------------------------------------
// gRPC 服务实现 (只负责接单)
// ---------------------------------------------------------
class WorkerServiceImpl final : public deep_oj::JudgeService::Service {
    Status ExecuteTask(ServerContext* context, const deep_oj::TaskRequest* request,
                  deep_oj::TaskResponse* response) override {
        
        // 1. 收到请求，打印日志
        std::cout << "[Worker] 收到调度请求 (ID: " << request->job_id() << ")" << std::endl;

        // 2. 并发控制 (Backpressure)
        if (!g_task_sem->try_acquire()) {
            std::cout << "[Worker] ⚠️ High Load - 拒绝请求: " << request->job_id() << std::endl;
            return Status(grpc::RESOURCE_EXHAUSTED, "Worker is busy");
        }

        // 3. 启动分离线程，立刻干活
        // 使用 Lambda 捕获 request 副本，并在内部管理信号量释放
        std::thread([req_copy = *request]() mutable {
            // RAII 守卫：确保线程退出时释放信号量
            struct Guard {
                ~Guard() { g_task_sem->release(); }
            } guard;

            process_task(std::move(req_copy));
        }).detach();

        // 4. 立即回复 OK
        return Status::OK;
    }
};

int main(int argc, char** argv) {
    // 1. Redis 连接
    try {
        const char* env_redis = std::getenv("REDIS_HOST");
        std::string redis_host = env_redis ? env_redis : "127.0.0.1";
        g_redis = std::make_shared<sw::redis::Redis>("tcp://" + redis_host + ":6379");
        std::cout << "[Worker] Redis 连接成功" << std::endl;
    } catch (const std::exception& e) {
        std::cerr << "❌ [Fatal] Redis 连接失败" << std::endl;
        return 1;
    }

    // 2. 初始化全局信号量
    // 假设 g_runner_config.pool_size 已经在 config.cpp 加载好了
    // 如果没有加载配置的代码，这里先读取一下或给默认值
    int pool_size = deep_oj::g_runner_config.pool_size;
    if (pool_size <= 0) pool_size = 1;
    g_task_sem = std::make_unique<std::counting_semaphore<>>(pool_size);
    std::cout << "[Worker] 🔥 并发模型已初始化: Max Threads = " << pool_size << std::endl;

    // 3. 启动 gRPC Server
    std::string server_address("0.0.0.0:50051");
    WorkerServiceImpl service;

    ServerBuilder builder;
    builder.AddListeningPort(server_address, grpc::InsecureServerCredentials());
    builder.RegisterService(&service);
    
    std::unique_ptr<Server> server(builder.BuildAndStart());
    std::cout << "🚀 [Worker] 启动监听: " << server_address << std::endl;
    server->Wait();

    return 0;
}