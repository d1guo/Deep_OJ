#include <iostream>
#include <string>
#include <vector>
#include <random>
#include <sstream>
#include <iomanip>
#include <chrono>

// 引入自动生成的 Protobuf 头文件
#include "judge.pb.h" 

// Redis 客户端
#include <sw/redis++/redis++.h>

// Crow HTTP 框架
#include "crow_all.h" 

// JSON 库
#include <nlohmann/json.hpp>

// SHA256 库
#include "picosha2.h" 

using json = nlohmann::json;

// --- 生成随机 JobID (UUID) ---
std::string generate_uuid_v4() {
    static std::random_device rd;
    static std::mt19937_64 gen(rd());
    static std::uniform_int_distribution<uint64_t> dis;
    uint64_t part1 = dis(gen);
    uint64_t part2 = dis(gen);
    part1 = (part1 & 0xFFFFFFFFFFFF0FFFULL) | 0x0000000000004000ULL;
    part2 = (part2 & 0x3FFFFFFFFFFFFFFFULL) | 0x8000000000000000ULL;
    std::stringstream ss;
    ss << std::hex << std::setfill('0') << std::setw(8) << (part1 >> 32) << "-"
       << std::setw(4) << ((part1 >> 16) & 0xFFFF) << "-" << std::setw(4) << (part1 & 0xFFFF) << "-"
       << std::setw(4) << (part2 >> 48) << "-" << std::setw(12) << (part2 & 0xFFFFFFFFFFFFULL);
    return ss.str();
}

// --- 【新增】生成缓存指纹 (Deduplication Key) ---
std::string generate_cache_key(const std::string& code, int lang, int time_limit, int memory_limit) {
    // 拼接关键因子：代码 + 语言 + 时间限制 + 内存限制
    // (注意：题目ID也应该加进去，这里假设 request 里将来会有 problem_id)
    std::stringstream raw_ss;
    raw_ss << code << "|" << lang << "|" << time_limit << "|" << memory_limit;
    
    std::string hex_str;
    picosha2::hash256_hex_string(raw_ss.str(), hex_str);
    return "oj:cache:" + hex_str;
}

int main(int argc, char** argv) {
    // 1. 初始化 Redis 连接
    std::shared_ptr<sw::redis::Redis> redis = nullptr;
    try {
        const char* env_redis = std::getenv("REDIS_HOST");
        std::string redis_host = env_redis ? env_redis : "127.0.0.1";
        std::string redis_url = "tcp://" + redis_host + ":6379";
        std::cout << "[API] 正在连接 Redis: " << redis_url << " ..." << std::endl;
        redis = std::make_shared<sw::redis::Redis>(redis_url);
        if (redis->ping() == "PONG") std::cout << "[API] Redis 连接成功！" << std::endl;
    } catch (const std::exception& e) {
        std::cerr << "❌ [Fatal] Redis 连接失败: " << e.what() << std::endl;
        return 1;
    }

    crow::App app;

    // =========================================================
    // 接口 1: 提交代码 (POST /api/submit)
    // =========================================================
    CROW_ROUTE(app, "/api/submit").methods(crow::HTTPMethod::POST)
    ([redis](const crow::request& req) {
        json req_json;
        try { req_json = json::parse(req.body); } 
        catch (...) { return crow::response(400, "Invalid JSON"); }

        if (!req_json.contains("code") || !req_json.contains("language")) {
            return crow::response(400, "Missing 'code' or 'language'");
        }
        
        // 提取参数
        std::string code = req_json["code"].get<std::string>();
        int lang_val = req_json.value("language", 1);
        int time_limit = req_json.value("time_limit", 1000);
        int memory_limit = req_json.value("memory_limit", 65536);

        // -----------------------------------------------------
        // 🚀 【新增逻辑】缓存拦截 (Result Caching)
        // -----------------------------------------------------
        std::string cache_key = generate_cache_key(code, lang_val, time_limit, memory_limit);
        try {
            auto cached_val = redis->get(cache_key);
            if (cached_val) {
                // 🎯 命中缓存！
                // 只有 AC/WA/CE 这种确定性结果会被 Worker 存入 cache
                // TLE/RE 通常不存，为了防止系统抖动导致误判
                std::cout << "[API] 缓存命中! Key=" << cache_key.substr(0, 15) << "..." << std::endl;
                json resp;
                resp["job_id"] = "cached"; // 或者生成一个新的 ID 指向旧结果
                resp["status"] = "Finished";
                resp["data"] = json::parse(*cached_val); // 直接返回旧的详细结果
                resp["cached"] = true; // 告诉前端这是缓存结果
                return crow::response(200, resp.dump());
            }
        } catch (...) {
            // Redis 出错不应该阻断正常提交，降级处理
        }

        // -----------------------------------------------------
        // 🐢 未命中，正常入队流程
        // -----------------------------------------------------
        std::string job_id = generate_uuid_v4();
        deep_oj::TaskRequest task;
        task.set_job_id(job_id);
        task.set_code(code);
        task.set_language(static_cast<deep_oj::Language>(lang_val));
        task.set_time_limit(time_limit);
        task.set_memory_limit(memory_limit);

        // 【关键】把 cache_key 传给 Worker
        // 这样 Worker 跑完后，知道要更新哪个缓存 Key
        // *需要在 judge.proto 里加一个 string cache_key = 6;*
        task.set_cache_key(cache_key); 
        std::string serialized_data;
        task.SerializeToString(&serialized_data);
        try {
            redis->lpush("queue:pending", serialized_data);
            std::cout << "[API] 任务入队: " << job_id << std::endl;
        } catch (const std::exception& e) {
            return crow::response(500, "Redis Push Failed");
        }
        json resp;
        resp["job_id"] = job_id;
        resp["status"] = "Queuing";
        return crow::response(200, resp.dump());
    });

    // =========================================================
    // 接口 2: 查询状态 (GET /api/status/<job_id>)
    // 逻辑: 查 Redis 缓存 -> 返回结果
    // =========================================================
    CROW_ROUTE(app, "/api/status/<string>")
    ([redis](std::string job_id){
        json resp;
        resp["job_id"] = job_id;
        try {

            // 查 Redis 里的结果缓存 (Worker 跑完会写这里的)
            // 假设 Key 格式为 "result:job_id"
            auto result_str = redis->get("result:" + job_id);
            if (result_str) {

                // 命中缓存，说明跑完了
                // 这里假设 Worker 存的是 JSON 字符串，如果是 Proto 需要反序列化
                // 简单起见，我们让 Worker 往 Redis 存结果时存 JSON 字符串，方便前端查
                // 或者在这里做 Protobuf -> JSON 的转换
                try {
                    resp["data"] = json::parse(*result_str);
                    resp["status"] = "Finished";
                } catch (...) {
                    resp["status"] = "DataError"; // 存的数据坏了
                }
            } else {

                // 没命中，说明还在队列里或者正在跑
                resp["status"] = "Judging"; 
            }
        } catch (const std::exception& e) {
            return crow::response(500, "Redis Error");
        }
        return crow::response(200, resp.dump());
    });

    // 启动服务
    int port = 18080;
    std::cout << "🚀 [Deep-OJ API] 启动成功，端口: " << port << std::endl;
    std::cout << "   - 模式: 异步队列 (Redis)" << std::endl;
    app.port(port).multithreaded().run();
    return 0;
}