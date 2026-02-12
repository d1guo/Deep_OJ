/**
 * @file etcd_registry.cpp
 * @brief Etcd 服务注册实现
 * 
 * 注意: 本实现使用 HTTP API 与 Etcd 通信，无需额外依赖
 * 生产环境建议使用 etcd-cpp-api 或 gRPC API
 */

#include "etcd_registry.h"

#include <iostream>
#include <chrono>
#include <cstring>
#include <curl/curl.h>
#include <nlohmann/json.hpp>

using json = nlohmann::json;

namespace deep_oj {

// ============================================================================
// CURL 回调函数
// ============================================================================
static size_t WriteCallback(void* contents, size_t size, size_t nmemb, std::string* userp) {
    userp->append((char*)contents, size * nmemb);
    return size * nmemb;
}

// ============================================================================
// 构造函数与析构函数
// ============================================================================

EtcdRegistry::EtcdRegistry(const std::string& etcd_endpoints, int ttl_seconds)
    : etcd_endpoints_(etcd_endpoints)
    , ttl_seconds_(ttl_seconds)
{
    curl_global_init(CURL_GLOBAL_DEFAULT);
}

EtcdRegistry::~EtcdRegistry() {
    Deregister();
    curl_global_cleanup();
}

// ============================================================================
// 注册与注销
// ============================================================================

bool EtcdRegistry::Register(const std::string& worker_id, const std::string& address) {
    if (registered_) {
        std::cerr << "[EtcdRegistry] Already registered" << std::endl;
        return false;
    }
    
    key_ = "/workers/" + worker_id;
    
    CURL* curl = curl_easy_init();
    if (!curl) {
        std::cerr << "[EtcdRegistry] Failed to init CURL" << std::endl;
        return false;
    }
    
    std::string response;
    
    // 1. 创建 Lease
    std::string lease_url = "http://" + etcd_endpoints_ + "/v3/lease/grant";
    json lease_req;
    lease_req["TTL"] = ttl_seconds_;
    std::string lease_body = lease_req.dump();
    
    curl_easy_setopt(curl, CURLOPT_URL, lease_url.c_str());
    curl_easy_setopt(curl, CURLOPT_POSTFIELDS, lease_body.c_str());
    curl_easy_setopt(curl, CURLOPT_WRITEFUNCTION, WriteCallback);
    curl_easy_setopt(curl, CURLOPT_WRITEDATA, &response);
    
    struct curl_slist* headers = nullptr;
    headers = curl_slist_append(headers, "Content-Type: application/json");
    curl_easy_setopt(curl, CURLOPT_HTTPHEADER, headers);
    
    CURLcode res = curl_easy_perform(curl);
    if (res != CURLE_OK) {
        std::cerr << "[EtcdRegistry] Lease grant failed: " << curl_easy_strerror(res) << std::endl;
        curl_slist_free_all(headers);
        curl_easy_cleanup(curl);
        return false;
    }
    
    try {
        auto lease_resp = json::parse(response);
        lease_id_ = std::stoll(lease_resp["ID"].get<std::string>());
    } catch (const std::exception& e) {
        std::cerr << "[EtcdRegistry] Failed to parse lease response: " << e.what() << std::endl;
        curl_slist_free_all(headers);
        curl_easy_cleanup(curl);
        return false;
    }
    
    // 2. 注册 KV (绑定 Lease)
    // Etcd v3 API 使用 base64 编码
    response.clear();
    
    std::string put_url = "http://" + etcd_endpoints_ + "/v3/kv/put";
    json put_req;
    
    // base64 编码 key 和 value
    auto base64_encode = [](const std::string& input) {
        static const char* table = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
        std::string output;
        int val = 0, valb = -6;
        for (unsigned char c : input) {
            val = (val << 8) + c;
            valb += 8;
            while (valb >= 0) {
                output.push_back(table[(val >> valb) & 0x3F]);
                valb -= 6;
            }
        }
        if (valb > -6) output.push_back(table[((val << 8) >> (valb + 8)) & 0x3F]);
        while (output.size() % 4) output.push_back('=');
        return output;
    };
    
    put_req["key"] = base64_encode(key_);
    put_req["value"] = base64_encode(address);
    put_req["lease"] = std::to_string(lease_id_);
    std::string put_body = put_req.dump();
    
    curl_easy_setopt(curl, CURLOPT_URL, put_url.c_str());
    curl_easy_setopt(curl, CURLOPT_POSTFIELDS, put_body.c_str());
    curl_easy_setopt(curl, CURLOPT_WRITEDATA, &response);
    
    res = curl_easy_perform(curl);
    curl_slist_free_all(headers);
    curl_easy_cleanup(curl);
    
    if (res != CURLE_OK) {
        std::cerr << "[EtcdRegistry] KV put failed: " << curl_easy_strerror(res) << std::endl;
        return false;
    }
    
    // 3. 启动心跳线程
    registered_ = true;
    running_ = true;
    keepalive_thread_ = std::thread(&EtcdRegistry::KeepAliveLoop, this);
    
    std::cerr << "[EtcdRegistry] Registered: " << key_ << " -> " << address 
              << " (LeaseID: " << lease_id_ << ", TTL: " << ttl_seconds_ << "s)" << std::endl;
    
    return true;
}

void EtcdRegistry::Deregister() {
    if (!registered_) return;
    
    running_ = false;
    if (keepalive_thread_.joinable()) {
        keepalive_thread_.join();
    }
    
    // 撤销 Lease (Key 会自动删除)
    CURL* curl = curl_easy_init();
    if (curl) {
        std::string revoke_url = "http://" + etcd_endpoints_ + "/v3/lease/revoke";
        json revoke_req;
        revoke_req["ID"] = std::to_string(lease_id_);
        std::string revoke_body = revoke_req.dump();
        
        struct curl_slist* headers = nullptr;
        headers = curl_slist_append(headers, "Content-Type: application/json");
        
        curl_easy_setopt(curl, CURLOPT_URL, revoke_url.c_str());
        curl_easy_setopt(curl, CURLOPT_POSTFIELDS, revoke_body.c_str());
        curl_easy_setopt(curl, CURLOPT_HTTPHEADER, headers);
        
        curl_easy_perform(curl);
        curl_slist_free_all(headers);
        curl_easy_cleanup(curl);
    }
    
    registered_ = false;
    std::cerr << "[EtcdRegistry] Deregistered: " << key_ << std::endl;
}

// ============================================================================
// 心跳续约
// ============================================================================

void EtcdRegistry::KeepAliveLoop() {
    // 每 TTL/3 续约一次
    int interval = std::max(1, ttl_seconds_ / 3);
    
    while (running_) {
        std::this_thread::sleep_for(std::chrono::seconds(interval));
        
        if (!running_) break;
        
        CURL* curl = curl_easy_init();
        if (!curl) continue;
        
        std::string keepalive_url = "http://" + etcd_endpoints_ + "/v3/lease/keepalive";
        json keepalive_req;
        keepalive_req["ID"] = std::to_string(lease_id_);
        std::string keepalive_body = keepalive_req.dump();
        
        struct curl_slist* headers = nullptr;
        headers = curl_slist_append(headers, "Content-Type: application/json");
        
        std::string response;
        curl_easy_setopt(curl, CURLOPT_URL, keepalive_url.c_str());
        curl_easy_setopt(curl, CURLOPT_POSTFIELDS, keepalive_body.c_str());
        curl_easy_setopt(curl, CURLOPT_HTTPHEADER, headers);
        curl_easy_setopt(curl, CURLOPT_WRITEFUNCTION, WriteCallback);
        curl_easy_setopt(curl, CURLOPT_WRITEDATA, &response);
        
        CURLcode res = curl_easy_perform(curl);
        curl_slist_free_all(headers);
        curl_easy_cleanup(curl);
        
        if (res != CURLE_OK) {
            std::cerr << "[EtcdRegistry] KeepAlive failed: " << curl_easy_strerror(res) << std::endl;
        }
    }
}

} // namespace deep_oj
