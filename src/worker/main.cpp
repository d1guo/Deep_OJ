#include <iostream>
#include <thread>
#include <chrono>
#include <memory>
#include <string> // 显式引入 string
#include <semaphore> 
#include <sys/stat.h>
#include <unistd.h>
#include <grpcpp/grpcpp.h>
#include "judge.grpc.pb.h"
#include "sandbox.h"

using grpc::Server;
using grpc::ServerBuilder;
using grpc::ServerContext;
using grpc::Status;
using namespace deep_oj;

// 声明 LoadConfig（在 config.cpp 中定义）
void LoadConfig(const std::string& path);

// 全局信号量，限制最大并发数为 4 (C++20 特性，g++ 12 支持)
static std::counting_semaphore<4> active_tasks_sem(4);

// RAII 风格的信号量守卫
struct SemaphoreGuard {
    std::counting_semaphore<4>& sem;
    SemaphoreGuard(std::counting_semaphore<4>& s) : sem(s) { 
        sem.acquire(); 
    }
    ~SemaphoreGuard() { 
        sem.release(); 
    }
};

class WorkerImpl final : public JudgeService::Service
{
    Status Submit(ServerContext* context, const SubmitRequest* request, SubmitResponse* response) override
    {
        // 阻塞等待资源可用
        SemaphoreGuard guard(active_tasks_sem);

        // [改写 1] 使用流式输出替代 format
        std::cout << "[Worker] 收到请求 ID: " << request->request_id() << std::endl;

        response->set_request_id(request->request_id());

        if (request->language() != Language::CPP)
        {
            std::cout << "[Worker] 错误: 收到不支持的语言类型" << std::endl;
            response->set_status(JudgeStatus::SYSTEM_ERROR);
            response->set_error_message("Worker 目前仅支持 C++ (Language::CPP)");
            return Status::OK;
        }

        try
        {
            Sandbox sandbox("/tmp/deep_oj_workspace");

            struct RequestGuard {
                Sandbox& sb;
                std::string id;
                ~RequestGuard() { 
                    sb.Cleanup(id);
                    std::cout << "[Worker] Cleaned up request " << id << std::endl; 
                }
            } request_guard(sandbox, request->request_id());

            std::cout << "[Worker] Sandbox 初始化完成，开始编译..." << std::endl;

            CompileResult compile_res = sandbox.Compile(request->request_id(), request->code());

            if (compile_res.success)
            {
                // [改写 2] 编译成功的日志
                std::cout << "[Worker] 编译成功: " << compile_res.exe_path << std::endl;
                
                std::cout << "[Worker] 开始运行 (Run)..." << std::endl;
                RunResult run_res = sandbox.Run(
                    compile_res.exe_path, 
                    request->time_limit(), 
                    request->memory_limit()
                );

                response->set_actual_time(run_res.time_used);
                response->set_actual_memory(run_res.memory_used);

                if (run_res.status == SandboxStatus::OK)
                {
                     response->set_status(JudgeStatus::ACCEPTED);
                     std::cout << "[Worker] 运行结束: ACCEPTED" << std::endl;
                }
                else if (run_res.status == SandboxStatus::TIME_LIMIT_EXCEEDED)
                {
                    response->set_status(JudgeStatus::TIME_LIMIT_EXCEEDED);
                    std::cout << "[Worker] 运行结束: TLE" << std::endl;
                }
                else if (run_res.status == SandboxStatus::MEMORY_LIMIT_EXCEEDED)
                {
                    response->set_status(JudgeStatus::MEMORY_LIMIT_EXCEEDED);
                    std::cout << "[Worker] 运行结束: MLE" << std::endl;
                }
                else if (run_res.status == SandboxStatus::RUNTIME_ERROR)
                {
                    response->set_status(JudgeStatus::RUNTIME_ERROR);
                    // [改写 3] 错误消息拼接
                    response->set_error_message("运行时错误 (退出码: " + std::to_string(run_res.exit_code) + ")");
                    std::cout << "[Worker] 运行结束: RE" << std::endl;
                }
                else
                {
                    response->set_status(JudgeStatus::SYSTEM_ERROR);
                    if (!run_res.error_message.empty()) {
                        response->set_error_message(run_res.error_message);
                    } else {
                        response->set_error_message("执行过程中发生沙箱系统错误");
                    }
                     std::cout << "[Worker] 运行结束: SE (" << run_res.error_message << ")" << std::endl;
                }
            }
            else
            {
                std::cout << "[Worker] 编译失败!" << std::endl;
                response->set_status(JudgeStatus::COMPILE_ERROR);
                response->set_error_message(compile_res.error_message);
            }
        }
        catch (const std::exception& e)
        {
            std::cerr << "[Worker] 发生严重异常: " << e.what() << std::endl;
            response->set_status(JudgeStatus::SYSTEM_ERROR);
            // [改写 4] 内部异常消息拼接
            response->set_error_message("Worker 内部异常: " + std::string(e.what()));
        }

        return Status::OK;
    }
};

void RunServer()
{
    std::string server_address("0.0.0.0:50051");
    WorkerImpl service;

    ServerBuilder builder;
    builder.AddListeningPort(server_address, grpc::InsecureServerCredentials());
    builder.RegisterService(&service);
    
    std::unique_ptr<Server> server(builder.BuildAndStart());
    
    if (chmod("/tmp/deep_oj_worker.sock", 0666) != 0) {
        perror("chmod failed");
    }

    std::cout << "[Worker] 服务已启动，监听地址: " << server_address << std::endl;
    server->Wait();
}

int main()
{
    LoadConfig("config.yaml");
    RunServer();
    return 0;
}