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
#include "etcd_registry.h"
// 引入 Internal 头文件读取配置
#include "sandbox_internal.h" 

// 声明 Config 加载函数
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
// 判题核心：流式比对 (Stream Comparator)
// ---------------------------------------------------------

// 返回 true 表示 AC，false 表示 WA
bool check_output(const std::string& user_out_path, const std::string& std_ans_path) {
    if (!fs::exists(std_ans_path)) {
        std::cerr << "[比对] 标准答案缺失: " << std_ans_path << std::endl;
        return false;
    }
    if (!fs::exists(user_out_path)) {
        std::cerr << "[比对] 用户输出缺失: " << user_out_path << std::endl;
        return false;
    }

    std::ifstream f_user(user_out_path);
    std::ifstream f_std(std_ans_path);

    // 自动跳过 whitespace 进行 token-based 比较
    std::string s_u, s_s;
    std::cout << "[比对] 正在比较 用户输出: " << user_out_path << " vs 标准答案: " << std_ans_path << std::endl;
    while (true) {
        bool b_u = (bool)(f_user >> s_u);
        bool b_s = (bool)(f_std >> s_s);

        // 如果一个读完了，另一个没读完 -> WA
        if (b_u != b_s) {
            std::cout << "[比对] 长度不一致! 用户输出结束=" << !b_u << ", 标准答案结束=" << !b_s << std::endl;
            // Debug: print contents
            std::ifstream f_dbg(user_out_path);
            std::stringstream buffer;
            buffer << f_dbg.rdbuf();
            std::cout << "[比对] 用户内容 DUMP: [" << buffer.str() << "]" << std::endl;
            return false;
        }

        // 如果都读完了 -> AC
        if (!b_u) return true;

        // 如果内容不一样 -> WA
        if (s_u != s_s) {
            std::cout << "[比对] 内容不匹配! 用户: [" << s_u << "] vs 标准答案: [" << s_s << "]" << std::endl;
            return false;
        }
    }
}

// ---------------------------------------------------------
// 后台工作线程
// ---------------------------------------------------------
void process_task(deep_oj::TaskRequest task) {
    std::string job_id = task.job_id();
    std::cout << "[Worker] 开始后台处理: " << job_id << std::endl;

    // 1. 初始化沙箱
    deep_oj::Sandbox sandbox(deep_oj::g_runner_config.workspace_root);
    json result_json;
    result_json["job_id"] = job_id;

    // 注：源码写入由 sandbox.Compile() 内部完成，无需在此重复写入

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
        // [TODO] 生产环境需循环测试所有用例
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
            if (rres.status == deep_oj::SandboxStatus::TIME_LIMIT_EXCEEDED) {
                result_json["status"] = "Time Limit Exceeded";
            }
            else if (rres.status == deep_oj::SandboxStatus::MEMORY_LIMIT_EXCEEDED) {
                result_json["status"] = "Memory Limit Exceeded";
            }
            else if (rres.status == deep_oj::SandboxStatus::OUTPUT_LIMIT_EXCEEDED) {
                result_json["status"] = "Output Limit Exceeded";
            }
            else {
                // Runtime Error 处理逻辑
                result_json["status"] = "Runtime Error";
                result_json["exit_code"] = rres.exit_code; 
                
                // 1. 读取沙箱内的 stderr
                std::ifstream err_fs(error_path);
                if (err_fs.good()) {
                    std::stringstream buffer;
                    buffer << err_fs.rdbuf();
                    std::string err_msg = buffer.str();
                    
                    if (!err_msg.empty()) {
                        // 将 stderr 内容回填到 JSON，前端可以弹窗显示
                        result_json["error_message"] = "[Sandbox Stderr]: " + err_msg;
                        std::cout << "[Worker] Stderr: " << err_msg << std::endl;
                    } else {
                        result_json["error_message"] = "Process exited abnormally with no output.";
                    }
                }
                
                // 2. 如果是 Signal 导致的 (如 SegFault)，Sandbox.cpp 已经填了 error_message
                if (!rres.error_message.empty()) {
                     // 优先展示 Sandbox 捕获的信号描述
                     result_json["error_message"] = rres.error_message;
                }
            }
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
            
            // 配合 Scheduler 的 LREM，必须发送完整的任务数据
            // 之前只发 job_id 会导致 Scheduler 无法在 Listener 中匹配到队列里的元素
            g_redis->publish("job_done", job_id);
            
            std::cout << "[Worker] ✅ 完成: " << job_id << " (" << result_json["status"] << ")" << std::endl;
        } catch (const std::exception& e) {
            std::cerr << "❌ [Worker] 结果上报失败 (" << job_id << "): " << e.what() << std::endl;
        }
    }
    
    // 清理临时文件
    sandbox.Cleanup(job_id);
}

// 全局任务去重 (防止 Double Dispatch 导致的竞态)
std::mutex g_job_mutex;
std::set<std::string> g_running_jobs;

class WorkerServiceImpl final : public deep_oj::JudgeService::Service {
    Status ExecuteTask(ServerContext* context, const deep_oj::TaskRequest* request,
                  deep_oj::TaskResponse* response) override {
        
        std::string job_id = request->job_id();
        std::cout << "[Worker] 收到调度请求 (ID: " << job_id << ")" << std::endl;

        // 1. 幂等性检查
        {
            std::lock_guard<std::mutex> lock(g_job_mutex);
            if (g_running_jobs.count(job_id)) {
                std::cout << "[Worker] ⚠️ Ignored duplicate request: " << job_id << std::endl;
                return Status::OK; // 视为成功，不重复执行
            }
            g_running_jobs.insert(job_id);
        }

        if (!g_task_sem->try_acquire()) {
            std::cout << "[Worker] ⚠️ High Load - 拒绝请求: " << job_id << std::endl;
            // 释放 Job ID
            {
                std::lock_guard<std::mutex> lock(g_job_mutex);
                g_running_jobs.erase(job_id);
            }
            return Status(grpc::RESOURCE_EXHAUSTED, "Worker is busy");
        }

        std::thread([req_copy = *request]() mutable {
            std::string jid = req_copy.job_id();
            
            // RAII Cleanup Guard
            struct ResourceGuard {
                std::string id;
                ResourceGuard(std::string i) : id(i) {}
                ~ResourceGuard() { 
                    g_task_sem->release();
                    std::lock_guard<std::mutex> lock(g_job_mutex);
                    g_running_jobs.erase(id);
                }
            } guard(jid);
            
            process_task(std::move(req_copy));
        }).detach();

        return Status::OK;
    }
};

int main(int argc, char** argv) {
    // 1. 加载配置 (必须在所有逻辑之前)
    // 1. 加载配置 (优先从 config/worker.yaml 加载)
    if (std::filesystem::exists("config/worker.yaml")) {
        LoadConfig("config/worker.yaml");
    } else {
        LoadConfig("config.yaml");
    }

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
    int port = deep_oj::g_runner_config.server_port;
    if (port <= 0) port = 50051;
    std::string server_address("0.0.0.0:" + std::to_string(port));
    
    WorkerServiceImpl service;

    ServerBuilder builder;
    builder.AddListeningPort(server_address, grpc::InsecureServerCredentials());
    builder.RegisterService(&service);
    
    std::unique_ptr<Server> server(builder.BuildAndStart());
    std::cout << "🚀 [Worker] 启动监听: " << server_address << std::endl;

    // 5. [可选] Etcd 服务发现注册
    // 设置 ETCD_ENDPOINTS 环境变量以启用 (例如: "127.0.0.1:2379")
    // 不设置则跳过，保持向后兼容
    std::unique_ptr<deep_oj::EtcdRegistry> etcd;
    const char* etcd_endpoints = std::getenv("ETCD_ENDPOINTS");
    if (etcd_endpoints && strlen(etcd_endpoints) > 0) {
        try {
            const char* env_worker_id = std::getenv("WORKER_ID");
            std::string worker_id = env_worker_id ? env_worker_id : ("worker_" + std::to_string(port));
            
            // 对外地址: 优先使用 WORKER_ADDR，否则用 server_address
            const char* env_addr = std::getenv("WORKER_ADDR");
            std::string advertise_addr = env_addr ? env_addr : server_address;

            etcd = std::make_unique<deep_oj::EtcdRegistry>(etcd_endpoints, 10);
            if (etcd->Register(worker_id, advertise_addr)) {
                std::cout << "[Worker] ✅ Etcd 注册成功: " << worker_id << " -> " << advertise_addr << std::endl;
            } else {
                std::cerr << "[Worker] ⚠️ Etcd 注册失败，继续运行 (无服务发现)" << std::endl;
                etcd.reset();
            }
        } catch (const std::exception& e) {
            std::cerr << "[Worker] ⚠️ Etcd 初始化异常: " << e.what() << "，继续运行" << std::endl;
            etcd.reset();
        }
    }

    server->Wait();

    // 6. 优雅关闭: EtcdRegistry 析构函数自动 Deregister
    return 0;
}