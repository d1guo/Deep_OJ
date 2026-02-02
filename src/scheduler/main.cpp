#include <iostream>
#include <memory>
#include <string>
#include <thread>
#include <atomic>
#include <csignal>
#include <chrono>

#include <grpcpp/grpcpp.h>
#include <sw/redis++/redis++.h>

// 引入自动生成的 Proto 头文件
#include "judge.grpc.pb.h"

// ---------------------------------------------------------
// 全局控制变量
// ---------------------------------------------------------
std::atomic<bool> g_running(true);

// 信号处理
void SignalHandler(int signum) {
    std::cout << "\n[Scheduler] 收到信号 " << signum << "，准备停止..." << std::endl;
    g_running = false;
}

// ---------------------------------------------------------
// 线程 1: 任务清理员 (Ack Listener - Fast Path)
// 职责: 订阅 job_done 频道，收到消息后从 queue:processing 中删除任务
// ---------------------------------------------------------
void AckListenerLoop(std::string redis_url, std::shared_ptr<sw::redis::Redis> redis_writer) {
    try {
        // Subscriber 需要一个独立的 Redis 连接
        auto redis_sub = sw::redis::Redis(redis_url);
        auto sub = redis_sub.subscriber();

        // 注册回调：收到消息时触发
        sub.on_message([redis_writer](std::string channel, std::string job_id) {
            // 从 processing 队列中移除 (ACK)
            // ZREM queue:processing <job_id>
            redis_writer->zrem("queue:processing", job_id);
            std::cout << "[Ack] 任务完成，已清理状态: " << job_id << std::endl;
        });

        // 订阅频道
        sub.subscribe("job_done");
        std::cout << "[Ack] 已启动监听 job_done 频道..." << std::endl;

        // 循环等待消息
        while (g_running) {
            try {
                sub.consume(); // 阻塞消费，有消息就触发回调
            } catch (const sw::redis::TimeoutError& e) {
                // 正常的超时，继续循环
                continue;
            } catch (...) {
                std::this_thread::sleep_for(std::chrono::milliseconds(100));
            }
        }
    } catch (const std::exception& e) {
        std::cerr << "❌ [Ack Error] " << e.what() << std::endl;
    }
}

// ---------------------------------------------------------
// 线程 2: 任务调度员 (Dispatcher)
// 职责: 从 Redis 拉取任务 -> 通过 gRPC 发给 Worker
// ---------------------------------------------------------
void DispatchLoop(std::shared_ptr<sw::redis::Redis> redis, std::unique_ptr<deep_oj::JudgeService::Stub> stub) {
    std::cout << "[Dispatcher] 开始监听待办任务队列..." << std::endl;

    while (g_running) {
        try {
            // 1. 【核心】原子拉取 (Reliable Queue Pattern)
            // BRPOPLPUSH: 阻塞式地从 pending 弹出，塞入 processing
            // timeout = 2秒，防止死锁卡住无法响应退出信号
            auto result = redis->brpoplpush("queue:pending", "queue:processing", 2);

            if (!result) {
                // 超时没取到任务，继续循环
                continue;
            }

            std::string task_data = *result;
            
            // 2. 反序列化
            deep_oj::TaskRequest task;
            if (!task.ParseFromString(task_data)) {
                std::cerr << "❌ [Error] 任务解析失败，丢弃任务。" << std::endl;
                // 注意：生产环境可能需要把这个坏数据存到 "dead_letter_queue"
                // 这里简单起见，因为已经在 processing 里了，不处理就是一种暂存
                continue;
            }

            std::cout << "[Dispatcher] ⚡ 获取任务: " << task.job_id() << " -> 派发中..." << std::endl;

            // 3. gRPC 呼叫 Worker
            grpc::ClientContext context;
            context.set_deadline(std::chrono::system_clock::now() + std::chrono::seconds(5)); // 5秒发不出去就算失败
            
            deep_oj::TaskResponse response;
            grpc::Status status = stub->ExecuteTask(&context, task, &response);

            if (!status.ok()) {
                std::cerr << "❌ [RPC Error] Worker 连接失败: " << status.error_message() << std::endl;
                
                // 【重试策略】
                // 既然发给 Worker 失败了，任务还在 processing 队列里。
                // 我们应该把它扔回 pending 队列，让其他 Scheduler 或者稍后重试
                // LPUSH queue:pending <data>
                // ZREM queue:processing <data> (可选，或者等超时兜底)
                redis->lpush("queue:pending", task_data);
                redis->zrem("queue:processing", task_data); 
                
                std::this_thread::sleep_for(std::chrono::seconds(1)); // 歇一会防雪崩
            } else {
                // 发送成功！
                // 注意：这里不删除 processing 里的任务。
                // 只有等 AckListener 收到 "job_done" 或者是 SlowPath 兜底时才删。
                std::cout << "[Dispatcher] ✅ 派发成功 (Worker 已接单)" << std::endl;
            }

        } catch (const std::exception& e) {
            std::cerr << "[Redis Error] " << e.what() << std::endl;
            std::this_thread::sleep_for(std::chrono::seconds(1));
        }
    }
}

int main(int argc, char** argv) {
    // 注册信号
    std::signal(SIGINT, SignalHandler);
    std::signal(SIGTERM, SignalHandler);

    // 1. 读取配置
    const char* env_redis = std::getenv("REDIS_HOST");
    std::string redis_host = env_redis ? env_redis : "127.0.0.1";
    std::string redis_url = "tcp://" + redis_host + ":6379";

    const char* env_worker = std::getenv("WORKER_ADDR");
    std::string worker_addr = env_worker ? env_worker : "127.0.0.1:50051";

    // 2. 初始化 Redis 连接
    // 这是一个主连接，用于 DispatchLoop
    std::shared_ptr<sw::redis::Redis> redis = nullptr;
    try {
        redis = std::make_shared<sw::redis::Redis>(redis_url);
        if (redis->ping() == "PONG") {
            std::cout << "[Scheduler] Redis 连接成功: " << redis_url << std::endl;
        }
    } catch (const std::exception& e) {
        std::cerr << "❌ [Fatal] 无法连接 Redis: " << e.what() << std::endl;
        return 1;
    }

    // 3. 初始化 Worker gRPC Stub
    auto channel = grpc::CreateChannel(worker_addr, grpc::InsecureChannelCredentials());
    auto stub = deep_oj::JudgeService::NewStub(channel);
    std::cout << "[Scheduler] Worker 目标地址: " << worker_addr << std::endl;

    // 4. 启动后台线程 (Ack Listener)
    // 这里传入 redis_url 是因为 Subscriber 需要建立自己的新 TCP 连接
    std::thread ack_thread(AckListenerLoop, redis_url, redis);

    // 5. 主线程执行 Dispatch Loop
    DispatchLoop(redis, std::move(stub));

    // 退出处理
    if (ack_thread.joinable()) {
        ack_thread.join();
    }
    std::cout << "[Scheduler] 再见。" << std::endl;
    return 0;
}