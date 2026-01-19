#include <iostream>
#include <thread>
#include <chrono>
#include <memory>
#include <format>
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

// 全局信号量，限制最大并发数为 4
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
        // 尝试获取信号量，如果满载可以阻塞，或者立即返回 ResourceExhausted
        // 这里我们选择阻塞等待，直到有资源可用
        SemaphoreGuard guard(active_tasks_sem);

        std::cout << std::format("[Worker] 收到请求 ID: {}", request->request_id()) << std::endl;

        // 1. 初始化响应 ID
        response->set_request_id(request->request_id());

        // 2. 检查语言支持 (目前仅支持 C++20)
        if (request->language() != Language::CPP)
        {
            std::cout << "[Worker] 错误: 收到不支持的语言类型" << std::endl;
            response->set_status(JudgeStatus::SYSTEM_ERROR);
            response->set_error_message("Worker 目前仅支持 C++ (Language::CPP)");
            return Status::OK;
        }

        try
        {
            // 3. 实例化 Sandbox (可能抛出异常)
            // 使用 /tmp/deep_oj_workspace 作为工作根目录
            Sandbox sandbox("/tmp/deep_oj_workspace");

            // [Security] RAII Guard to ensure cleanup always happens
            struct RequestGuard {
                Sandbox& sb;
                std::string id;
                ~RequestGuard() { 
                    sb.Cleanup(id);
                    std::cout << "[Worker] Cleaned up request " << id << std::endl; 
                }
            } request_guard(sandbox, request->request_id());

            std::cout << "[Worker] Sandbox 初始化完成，开始编译..." << std::endl;

            // 4. 调用 Sandbox 进行编译
            CompileResult compile_res = sandbox.Compile(request->request_id(), request->code());

            if (compile_res.success)
            {
                std::cout << std::format("[Worker] 编译成功: {}", compile_res.exe_path) << std::endl;
                
                // 5. 运行 Sandbox::Run (资源限制 + 监控)
                std::cout << "[Worker] 开始运行 (Run)..." << std::endl;
                RunResult run_res = sandbox.Run(
                    compile_res.exe_path, 
                    request->time_limit(), 
                    request->memory_limit()
                );

                // 填写运行时间和内存
                response->set_actual_time(run_res.time_used);
                response->set_actual_memory(run_res.memory_used);

                // 根据运行状态设置 Proto JudgeStatus
                if (run_res.status == SandboxStatus::OK) // OK -> ACCEPTED
                {
                     response->set_status(JudgeStatus::ACCEPTED);
                     std::cout << "[Worker] 运行结束: ACCEPTED" << std::endl;
                }
                else if (run_res.status == SandboxStatus::TIME_LIMIT_EXCEEDED) // TLE
                {
                    response->set_status(JudgeStatus::TIME_LIMIT_EXCEEDED);
                    std::cout << "[Worker] 运行结束: TLE" << std::endl;
                }
                else if (run_res.status == SandboxStatus::MEMORY_LIMIT_EXCEEDED) // MLE
                {
                    response->set_status(JudgeStatus::MEMORY_LIMIT_EXCEEDED);
                    std::cout << "[Worker] 运行结束: MLE" << std::endl;
                }
                else if (run_res.status == SandboxStatus::RUNTIME_ERROR) // RE
                {
                    response->set_status(JudgeStatus::RUNTIME_ERROR);
                    response->set_error_message(std::format("运行时错误 (退出码: {})", run_res.exit_code));
                    std::cout << "[Worker] 运行结束: RE" << std::endl;
                }
                else // SE
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
                // 将具体的编译器报错回传给 Scheduler
                response->set_error_message(compile_res.error_message);
            }
        }
        catch (const std::exception& e)
        {
            // 捕获 Sandbox 初始化失败或其他运行时异常
            std::cerr << "[Worker] 发生严重异常: " << e.what() << std::endl;
            response->set_status(JudgeStatus::SYSTEM_ERROR);
            response->set_error_message(std::format("Worker 内部异常: {}", e.what()));
        }

        return Status::OK;
    }
};

void RunServer()
{
    std::string server_address("unix:///tmp/deep_oj_worker.sock");
    WorkerImpl service;

    ServerBuilder builder;
    builder.AddListeningPort(server_address, grpc::InsecureServerCredentials());
    builder.RegisterService(&service);
    
    std::unique_ptr<Server> server(builder.BuildAndStart());
    
    // 更改 Socket 文件权限，允许非 root 用户(如 Scheduler)连接
    // chmod 的参数是 八进制，0666 表示所有人可读写 (Socket 不需要执行权限)
    // TODO: [Security] 在生产环境中，应设置为 0770 并使用专门的用户组管理权限
    // [TODO]: 生产环境应改为 0770 并配合用户组 (oj-group)，防止未授权连接。
    // 目前开发环境为了调试方便，暂时全开。
    if (chmod("/tmp/deep_oj_worker.sock", 0666) != 0) {
        perror("chmod failed");
    }

    std::cout << "[Worker] 服务已启动，监听地址: " << server_address << std::endl;
    server->Wait();
}

int main()
{
    RunServer();
    return 0;
}
