// d1guo/deep_oj/.../src/worker/config.cpp

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

    // 辅助宏：检查节点是否存在，不存在则报错退出
    #define CHECK_NODE(node, name) \
        if (!node) { \
            std::cerr << "[配置] 致命错误: 配置文件缺少关键节点 '" << name << "'" << std::endl; \
            exit(EXIT_FAILURE); \
        }

    void LoadConfig(const std::string& path) {
    try {
        std::cerr << "[配置] 正在读取: " << path << " ..." << std::endl;
        YAML::Node config = YAML::LoadFile(path);

        // 初始化配置
        std::memset(&deep_oj::g_runner_config, 0, sizeof(deep_oj::g_runner_config));

        // Core 线程池配置默认值
        deep_oj::g_runner_config.pool_size = 4;
        deep_oj::g_runner_config.run_tmpfs_size_mb = 64;
        deep_oj::g_runner_config.compile_tmpfs_size_mb = 128;
        deep_oj::g_runner_config.compile_log_max_bytes = 16 * 1024 * 1024;
        deep_oj::g_runner_config.compile_max_errors = 10;
        deep_oj::g_runner_config.cgroup_pids_limit = 20;
        deep_oj::g_runner_config.cgroup_cpu_max_cores = 1.0;
        deep_oj::g_runner_config.cgroup_io_read_bps = 100LL * 1024LL * 1024LL;  // 100MB/s
        deep_oj::g_runner_config.cgroup_io_write_bps = 100LL * 1024LL * 1024LL; // 100MB/s

        // Runner 配置 (核心判题线程池)
        if (config["runner"]) {
            YAML::Node runner = config["runner"];
            if (runner["pool_size"]) {
                deep_oj::g_runner_config.pool_size = runner["pool_size"].as<int>();
            } 
            // 读取 max_output_size (默认 10MB)
            deep_oj::g_runner_config.max_output_size = runner["max_output_size"] ? runner["max_output_size"].as<long long>() : 10 * 1024 * 1024;
            // 读取 output_buffer_size (默认 2MB)
            deep_oj::g_runner_config.output_buffer_size = runner["output_buffer_size"] ? runner["output_buffer_size"].as<long long>() : 2 * 1024 * 1024;
            if (runner["tmpfs_size_mb"]) {
                deep_oj::g_runner_config.run_tmpfs_size_mb = runner["tmpfs_size_mb"].as<long long>();
            }
            if (runner["cgroup_pids_limit"]) {
                deep_oj::g_runner_config.cgroup_pids_limit = runner["cgroup_pids_limit"].as<int>();
            }
            if (runner["cgroup_cpu_max_cores"]) {
                deep_oj::g_runner_config.cgroup_cpu_max_cores = runner["cgroup_cpu_max_cores"].as<double>();
            }
            if (runner["cgroup_io_read_bps"]) {
                deep_oj::g_runner_config.cgroup_io_read_bps = runner["cgroup_io_read_bps"].as<long long>();
            }
            if (runner["cgroup_io_write_bps"]) {
                deep_oj::g_runner_config.cgroup_io_write_bps = runner["cgroup_io_write_bps"].as<long long>();
            }

        } else {
             // 默认值
             deep_oj::g_runner_config.max_output_size = 10 * 1024 * 1024;
             deep_oj::g_runner_config.output_buffer_size = 2 * 1024 * 1024;
        }

        // Path 配置 (必须存在)
        CHECK_NODE(config["path"], "path");
        YAML::Node pathNode = config["path"];

        CHECK_NODE(pathNode["workspace_root"], "path.workspace_root");
        CHECK_NODE(pathNode["compiler_bin"], "path.compiler_bin");
        CHECK_NODE(pathNode["mount_dirs"], "path.mount_dirs");

        std::string workspace = pathNode["workspace_root"].as<std::string>();
        std::string compiler = pathNode["compiler_bin"].as<std::string>();

        std::strncpy(deep_oj::g_runner_config.workspace_root, workspace.c_str(), sizeof(deep_oj::g_runner_config.workspace_root) - 1);
        std::strncpy(deep_oj::g_runner_config.compiler_path, compiler.c_str(), sizeof(deep_oj::g_runner_config.compiler_path) - 1);

        YAML::Node mdirs = pathNode["mount_dirs"];
        if (!mdirs.IsSequence()) {
            std::cerr << "[配置] 错误: 'path.mount_dirs' 必须是一个列表" << std::endl;
            exit(EXIT_FAILURE);
        }

        deep_oj::g_runner_config.mount_count = 0;
        for (std::size_t i = 0; i < mdirs.size(); ++i) {
            if (deep_oj::g_runner_config.mount_count >= 16) break;
            std::string d = mdirs[i].as<std::string>();
            std::strncpy(deep_oj::g_runner_config.mount_dirs[deep_oj::g_runner_config.mount_count], 
                         d.c_str(), 
                         sizeof(deep_oj::g_runner_config.mount_dirs[0]) - 1);
            deep_oj::g_runner_config.mount_count++;
        }

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

        // Compile 配置
        CHECK_NODE(config["compile"], "compile");
        YAML::Node compileNode = config["compile"];
        
        CHECK_NODE(compileNode["cpu_limit_s"], "compile.cpu_limit_s");
        CHECK_NODE(compileNode["real_time_limit_s"], "compile.real_time_limit_s");
        CHECK_NODE(compileNode["memory_limit_mb"], "compile.memory_limit_mb");

        deep_oj::g_runner_config.compile_cpu_limit = compileNode["cpu_limit_s"].as<int>();
        deep_oj::g_runner_config.compile_real_limit = compileNode["real_time_limit_s"].as<int>();

        long long mem_mb = compileNode["memory_limit_mb"].as<long long>();
        deep_oj::g_runner_config.compile_mem_limit = mem_mb * 1024LL * 1024LL; 

        // 最大输出限制
        if (compileNode["max_output_size_mb"]) {
            long long out_mb = compileNode["max_output_size_mb"].as<long long>();
            deep_oj::g_runner_config.max_output_size = out_mb * 1024LL * 1024LL;
        } else {
            deep_oj::g_runner_config.max_output_size = 10 * 1024 * 1024; // 10MB
        }
        if (compileNode["tmpfs_size_mb"]) {
            deep_oj::g_runner_config.compile_tmpfs_size_mb = compileNode["tmpfs_size_mb"].as<long long>();
        }
        if (compileNode["log_max_size_mb"]) {
            long long log_mb = compileNode["log_max_size_mb"].as<long long>();
            deep_oj::g_runner_config.compile_log_max_bytes = log_mb * 1024LL * 1024LL;
        }
        if (compileNode["max_errors"]) {
            deep_oj::g_runner_config.compile_max_errors = compileNode["max_errors"].as<int>();
        }
        if (deep_oj::g_runner_config.compile_max_errors <= 0) {
            deep_oj::g_runner_config.compile_max_errors = 10;
        }
        
        // Security 配置
        CHECK_NODE(config["security"], "security");
        YAML::Node secNode = config["security"];

        CHECK_NODE(secNode["run_as_uid"], "security.run_as_uid");
        CHECK_NODE(secNode["run_as_gid"], "security.run_as_gid");

        deep_oj::g_runner_config.run_uid = static_cast<uid_t>(secNode["run_as_uid"].as<unsigned long>());
        deep_oj::g_runner_config.run_gid = static_cast<gid_t>(secNode["run_as_gid"].as<unsigned long>());

        std::cerr << "[配置] 加载完成。WorkRoot: " << deep_oj::g_runner_config.workspace_root 
                  << ", PoolSize: " << deep_oj::g_runner_config.pool_size << std::endl;

    } catch (const YAML::Exception& ex) {
        std::cerr << "[配置] YAML 解析失败: " << ex.what() << std::endl;
        exit(EXIT_FAILURE);
    } catch (const std::exception& ex) {
        std::cerr << "[配置] 加载异常: " << ex.what() << std::endl;
        exit(EXIT_FAILURE);
    }
} // End LoadConfig
} // namespace deep_oj
