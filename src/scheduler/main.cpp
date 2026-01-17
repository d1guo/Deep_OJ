#include <iostream>
#include <memory>
#include <string>
#include <format>
#include <grpcpp/grpcpp.h>
#include "judge.grpc.pb.h"

using grpc::Channel;
using grpc::ClientContext;
using grpc::Status;
using namespace deep_oj;

class SchedulerClient
{
public:
    SchedulerClient(std::shared_ptr<Channel> channel)
        : stub_(JudgeService::NewStub(channel)) {}

    // 发送请求的函数
    void CallWorker()
    {
        // 1. 准备请求数据
        SubmitRequest request;
        request.set_request_id("REQ-TEST-2026");
        
        // 编写一段简单的 C++ 代码用于测试
        // 也可以故意写错来测试 COMPILE_ERROR
        std::string code = R"(
    #include <iostream>
    #include <thread>
    #include <chrono>
    int main() {
        // 睡 5 秒，超过默认的时间限制
        std::this_thread::sleep_for(std::chrono::seconds(5));
        return 0;
    }
)";
        request.set_code(code);
        
        // 关键变更：使用 Proto 枚举设置语言
        request.set_language(Language::CPP);
        
        request.set_time_limit(1000);  // 1000ms
        request.set_memory_limit(65536); // 64MB

        std::cout << "[Scheduler] 正在呼叫 Worker..." << std::endl;

        // 2. 准备接收响应的容器
        SubmitResponse response;
        ClientContext context;

        // 3. 真正的 RPC 调用
        Status status = stub_->Submit(&context, request, &response);

        // 4. 检查结果
        if (status.ok())
        {
            std::cout << "[Scheduler] 调用成功！" << std::endl;
            
            // 使用 Protobuf 生成的辅助函数将 Enum 转为字符串名称，方便调试
            // 例如: JudgeStatus_Name(1) -> "ACCEPTED"
            std::cout << "   -> 结果状态: " << JudgeStatus_Name(response.status()) << std::endl;
            
            if (response.status() == JudgeStatus::ACCEPTED)
            {
                std::cout << "   -> 判题通过 (AC)!" << std::endl;
                std::cout << "   -> 实际耗时: " << response.actual_time() << "ms" << std::endl;
            }
            else if (response.status() == JudgeStatus::COMPILE_ERROR)
            {
                std::cout << "   -> 编译错误 (CE):" << std::endl;
                std::cout << response.error_message() << std::endl;
            }
            else
            {
                std::cout << "   -> 其他错误: " << response.error_message() << std::endl;
            }
        }
        else
        {
            std::cout << "[Scheduler] 调用失败: " << status.error_code() 
                      << ": " << status.error_message() << std::endl;
        }
    }

private:
    std::unique_ptr<JudgeService::Stub> stub_;
};

int main(int argc, char** argv)
{
    // 建立连接：连接到本机的 unix socket
    SchedulerClient client(grpc::CreateChannel(
        "unix:///tmp/deep_oj_worker.sock", grpc::InsecureChannelCredentials()));

    client.CallWorker();

    return 0;
}