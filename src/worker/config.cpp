#include <sys/types.h>
#include <unistd.h>
#include <iostream>
#include <cstring>
#include <cstdlib>
#include <string>
#include <yaml-cpp/yaml.h>

#include "sandbox_internal.h"

namespace deep_oj {
    // 定义全局变量实例
    GlobalConfig g_runner_config;
}

// 辅助宏：检查节点是否存在，不存在则报错退出
#define CHECK_NODE(node, name) \
    if (!node) { \
        std::cerr << "[配置] 致命错误: 配置文件缺少关键节点 '" << name << "'" << std::endl; \
        exit(EXIT_FAILURE); \
    }

void LoadConfig(const std::string& path) {
    try {
        std::cout << "[配置] 正在读取: " << path << " ..." << std::endl;
        YAML::Node config = YAML::LoadFile(path);

        // --- 0. 初始化清零 ---
        std::memset(&deep_oj::g_runner_config, 0, sizeof(deep_oj::g_runner_config));

        // --- 1. Server 节点 (允许缺失，使用默认值) ---
        if (config["server"]) {
            YAML::Node srv = config["server"];
            deep_oj::g_runner_config.server_port = srv["port"] ? srv["port"].as<int>() : 50051;
            deep_oj::g_runner_config.parallelism = srv["parallelism"] ? srv["parallelism"].as<int>() : 4;
        } else {
            // 默认值
            deep_oj::g_runner_config.server_port = 50051;
            deep_oj::g_runner_config.parallelism = 4;
            std::cout << "[配置] 未找到 server 节点，使用默认值 (Port: 50051, Thread: 4)" << std::endl;
        }

        // --- 2. Path 节点 (必须存在，否则无法运行) ---
        CHECK_NODE(config["path"], "path");
        YAML::Node pathNode = config["path"];

        CHECK_NODE(pathNode["workspace_root"], "path.workspace_root");
        CHECK_NODE(pathNode["compiler_bin"], "path.compiler_bin");
        CHECK_NODE(pathNode["mount_dirs"], "path.mount_dirs");

        // 读取字符串
        std::string workspace = pathNode["workspace_root"].as<std::string>();
        std::string compiler = pathNode["compiler_bin"].as<std::string>();

        // 安全拷贝 (strncpy)
        std::strncpy(deep_oj::g_runner_config.workspace_root, workspace.c_str(), sizeof(deep_oj::g_runner_config.workspace_root) - 1);
        std::strncpy(deep_oj::g_runner_config.compiler_path, compiler.c_str(), sizeof(deep_oj::g_runner_config.compiler_path) - 1);

        // 读取挂载目录 (List)
        YAML::Node mdirs = pathNode["mount_dirs"];
        if (!mdirs.IsSequence()) {
            std::cerr << "[配置] 错误: 'path.mount_dirs' 必须是一个列表 (sequence)" << std::endl;
            exit(EXIT_FAILURE);
        }

        deep_oj::g_runner_config.mount_count = 0;
        for (std::size_t i = 0; i < mdirs.size(); ++i) {
            if (deep_oj::g_runner_config.mount_count >= 16) break; // 防止越界
            std::string d = mdirs[i].as<std::string>();
            std::strncpy(deep_oj::g_runner_config.mount_dirs[deep_oj::g_runner_config.mount_count], 
                         d.c_str(), 
                         sizeof(deep_oj::g_runner_config.mount_dirs[0]) - 1);
            deep_oj::g_runner_config.mount_count++;
        }

        // 读取挂载文件 (可选 List)
        if (pathNode["mount_files"] && pathNode["mount_files"].IsSequence()) {
            YAML::Node mfiles = pathNode["mount_files"];
            deep_oj::g_runner_config.mount_file_count = 0;
            for (std::size_t i = 0; i < mfiles.size(); ++i) {
                if (deep_oj::g_runner_config.mount_file_count >= 16) break;
                std::string f = mfiles[i].as<std::string>();
                std::strncpy(deep_oj::g_runner_config.mount_files[deep_oj::g_runner_config.mount_file_count], 
                             f.c_str(), 
                             sizeof(deep_oj::g_runner_config.mount_files[0]) - 1);
                deep_oj::g_runner_config.mount_file_count++;
            }
        }

        // --- 3. Compile 节点 (必须存在) ---
        CHECK_NODE(config["compile"], "compile");
        YAML::Node compileNode = config["compile"];
        
        CHECK_NODE(compileNode["cpu_limit_s"], "compile.cpu_limit_s");
        CHECK_NODE(compileNode["real_time_limit_s"], "compile.real_time_limit_s");
        CHECK_NODE(compileNode["memory_limit _mb"], "compile.memory_limit_mb");

        deep_oj::g_runner_config.compile_cpu_limit = compileNode["cpu_limit_s"].as<int>();
        deep_oj::g_runner_config.compile_real_limit = compileNode["real_time_limit_s"].as<int>();

        long long mem_mb = compileNode["memory_limit_mb"].as<long long>();
        deep_oj::g_runner_config.compile_mem_limit = mem_mb * 1024LL * 1024LL; // MB -> Bytes

        // [新增] 读取最大输出限制 (默认 10MB)
        if (compileNode["max_output_size_mb"]) {
            long long out_mb = compileNode["max_output_size_mb"].as<long long>();
            deep_oj::g_runner_config.max_output_size = out_mb * 1024LL * 1024LL;
        } else {
            deep_oj::g_runner_config.max_output_size = 10 * 1024 * 1024; // Default 10MB
            std::cout << "[配置] 未配置 max_output_size_mb，使用默认值 10MB" << std::endl;
        }
        
        // --- 4. Security 节点 (必须存在) ---
        CHECK_NODE(config["security"], "security");
        YAML::Node secNode = config["security"];

        CHECK_NODE(secNode["run_as_uid"], "security.run_as_uid");
        CHECK_NODE(secNode["run_as_gid"], "security.run_as_gid");

        deep_oj::g_runner_config.run_uid = static_cast<uid_t>(secNode["run_as_uid"].as<unsigned long>());
        deep_oj::g_runner_config.run_gid = static_cast<gid_t>(secNode["run_as_gid"].as<unsigned long>());

        std::cout << "[配置] 加载完成。" << std::endl;

    } catch (const YAML::Exception& ex) {
        std::cerr << "[配置] YAML 解析失败: " << ex.what() << std::endl;
        exit(EXIT_FAILURE);
    } catch (const std::exception& ex) {
        std::cerr << "[配置] 加载异常: " << ex.what() << std::endl;
        exit(EXIT_FAILURE);
    } catch (...) {
        std::cerr << "[配置] 发生未知错误" << std::endl;
        exit(EXIT_FAILURE);
    }
}