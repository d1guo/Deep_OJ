#include <sys/types.h>
#include <unistd.h>
#include "sandbox_internal.h"

#include <yaml-cpp/yaml.h>
#include <iostream>
#include <cstring>
#include <cstdlib>
#include <string>


namespace deep_oj {
    // 定义全局变量
    GlobalConfig g_runner_config;
}

void LoadConfig(const std::string& path) {
    try {
        YAML::Node config = YAML::LoadFile(path);

        // path 节点
        if (!config["path"]) {
            std::cerr << "Missing 'path' node in config" << std::endl;
            exit(EXIT_FAILURE);
        }
        YAML::Node pathNode = config["path"];

        if (!pathNode["workspace_root"]) {
            std::cerr << "Missing 'path.workspace_root' in config" << std::endl;
            exit(EXIT_FAILURE);
        }
        if (!pathNode["compiler_bin"]) {
            std::cerr << "Missing 'path.compiler_bin' in config" << std::endl;
            exit(EXIT_FAILURE);
        }
        // 注意：mount_dirs 和 mount_files 是可选的，或者应该检查
        if (!pathNode["mount_dirs"]) {
            std::cerr << "Missing 'path.mount_dirs' in config" << std::endl;
            exit(EXIT_FAILURE);
        }
        
        // 初始化全局配置为零
        std::memset(&deep_oj::g_runner_config, 0, sizeof(deep_oj::g_runner_config));

        std::string workspace_root = pathNode["workspace_root"].as<std::string>();
        std::string compiler_bin = pathNode["compiler_bin"].as<std::string>();

        // 安全拷贝字符串
        std::strncpy(deep_oj::g_runner_config.workspace_root, workspace_root.c_str(), sizeof(deep_oj::g_runner_config.workspace_root) - 1);
        
        std::strncpy(deep_oj::g_runner_config.compiler_path, compiler_bin.c_str(), sizeof(deep_oj::g_runner_config.compiler_path) - 1);

        // mount_dirs
        YAML::Node mdirs = pathNode["mount_dirs"];
        if (!mdirs.IsSequence()) {
            std::cerr << "'path.mount_dirs' must be a sequence" << std::endl;
            exit(EXIT_FAILURE);
        }
        deep_oj::g_runner_config.mount_count = 0;
        for (std::size_t i = 0; i < mdirs.size(); ++i) {
            if (deep_oj::g_runner_config.mount_count >= 16) break;
            std::string d = mdirs[i].as<std::string>();
            std::strncpy(deep_oj::g_runner_config.mount_dirs[deep_oj::g_runner_config.mount_count], d.c_str(), sizeof(deep_oj::g_runner_config.mount_dirs[0]) - 1);
            deep_oj::g_runner_config.mount_count++;
        }

        // mount_files (检查是否存在，兼容旧配置)
        if (pathNode["mount_files"] && pathNode["mount_files"].IsSequence()) {
            YAML::Node mfiles = pathNode["mount_files"];
            deep_oj::g_runner_config.mount_file_count = 0;
            for (std::size_t i = 0; i < mfiles.size(); ++i) {
                if (deep_oj::g_runner_config.mount_file_count >= 16) break;
                std::string f = mfiles[i].as<std::string>();
                std::strncpy(deep_oj::g_runner_config.mount_files[deep_oj::g_runner_config.mount_file_count], f.c_str(), sizeof(deep_oj::g_runner_config.mount_files[0]) - 1);
                deep_oj::g_runner_config.mount_file_count++;
            }
        }

        // compile 节点
        if (!config["compile"]) {
            std::cerr << "Missing 'compile' node in config" << std::endl;
            exit(EXIT_FAILURE);
        }
        YAML::Node compileNode = config["compile"];
        
        // [修复点]: 键名修正为与 YAML 一致 (cpu_limit_s 等)
        if (!compileNode["cpu_limit_s"]) {
            std::cerr << "Missing 'compile.cpu_limit_s' in config" << std::endl;
            exit(EXIT_FAILURE);
        }
        if (!compileNode["real_time_limit_s"]) {
            std::cerr << "Missing 'compile.real_time_limit_s' in config" << std::endl;
            exit(EXIT_FAILURE);
        }
        if (!compileNode["memory_limit_mb"]) {
            std::cerr << "Missing 'compile.memory_limit_mb' in config" << std::endl;
            exit(EXIT_FAILURE);
        }

        deep_oj::g_runner_config.compile_cpu_limit = compileNode["cpu_limit_s"].as<int>();
        deep_oj::g_runner_config.compile_real_limit = compileNode["real_time_limit_s"].as<int>();
        long long mem_mb = compileNode["memory_limit_mb"].as<long long>();
        deep_oj::g_runner_config.compile_mem_limit = mem_mb * 1024LL * 1024LL; // MB -> bytes

        // security 节点
        if (!config["security"]) {
            std::cerr << "Missing 'security' node in config" << std::endl;
            exit(EXIT_FAILURE);
        }
        YAML::Node secNode = config["security"];
        if (!secNode["run_as_uid"]) {
            std::cerr << "Missing 'security.run_as_uid' in config" << std::endl;
            exit(EXIT_FAILURE);
        }
        if (!secNode["run_as_gid"]) {
            std::cerr << "Missing 'security.run_as_gid' in config" << std::endl;
            exit(EXIT_FAILURE);
        }

        deep_oj::g_runner_config.run_uid = static_cast<uid_t>(secNode["run_as_uid"].as<unsigned long>());
        deep_oj::g_runner_config.run_gid = static_cast<gid_t>(secNode["run_as_gid"].as<unsigned long>());

    } catch (const YAML::Exception& ex) {
        std::cerr << "Failed to load config file '" << path << "': " << ex.what() << std::endl;
        exit(EXIT_FAILURE);
    } catch (const std::exception& ex) {
        std::cerr << "Error while loading config '" << path << "': " << ex.what() << std::endl;
        exit(EXIT_FAILURE);
    } catch (...) {
        std::cerr << "Unknown error while loading config '" << path << "'" << std::endl;
        exit(EXIT_FAILURE);
    }
}