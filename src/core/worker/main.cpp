/**
 * @file main.cpp (judge_engine)
 * @brief C++ 判题核心引擎 (CLI)
 * 
 * 职责:
 * 1. 接收命令行参数 (代码路径, 时间限制, 内存限制)
 * 2. 启动沙箱运行代码
 * 3. 结果以 JSON 格式输出到 stdout
 * 
 * 只有最纯粹的计算逻辑，不包含网络通信。
 */
#include <iostream>
#include <string>
#include <vector>
#include <memory>
#include <filesystem>
#include <fstream>
#include <sstream>
#include <nlohmann/json.hpp>
#include <getopt.h>
#include <unistd.h>

#include "sandbox.h"
#include "sandbox_internal.h"

namespace deep_oj {
    void LoadConfig(const std::string& path);
}

using json = nlohmann::json;
namespace fs = std::filesystem;

bool ValidateConfig(std::string& err) {
    if (deep_oj::g_runner_config.workspace_root[0] == '\0') {
        err = "workspace_root not set";
        return false;
    }
    if (deep_oj::g_runner_config.compiler_path[0] == '\0') {
        err = "compiler_path not set";
        return false;
    }
    if (!fs::exists(deep_oj::g_runner_config.workspace_root)) {
        err = std::string("workspace_root not found: ") + deep_oj::g_runner_config.workspace_root;
        return false;
    }
    if (access(deep_oj::g_runner_config.compiler_path, X_OK) != 0) {
        err = std::string("compiler not executable: ") + deep_oj::g_runner_config.compiler_path;
        return false;
    }
    return true;
}

struct ExecutionConfig {
    std::string source_path;  // compile mode source path
    std::string code_path;
    std::string input_path;   // 可选：如果不传，则由 caller 负责重定向 stdin? 
                              // 还是由 Sandbox 负责？Current Sandbox Run takes stdin_path.
    std::string output_path;  // stdout 写入位置
    std::string error_path;   // stderr 写入位置
    int time_limit = 1000;    // ms
    int memory_limit = 131072;   // KB (默认 128MB)
    std::string work_dir;
    std::string request_id;   // compile/cleanup id
    bool compile_only = false;
    bool cleanup_only = false;
};

void print_usage(const char* prog) {
    std::cerr << "Usage: " << prog << " [options]\n"
              << "Options:\n"
              << "  -s <path>    Path to source file (compile mode)\n"
              << "  -r <id>      Request ID (compile/cleanup)\n"
              << "  -c <path>    Path to compiled executable (user code)\n"
              << "  -t <ms>      Time limit (default: 1000)\n"
              << "  -m <kb>      Memory limit (default: 131072)\n"
              << "  -i <path>    Input file path (stdin)\n"
              << "  -o <path>    Output file path (stdout)\n"
              << "  -e <path>    Error file path (stderr)\n"
              << "  -w <path>    Working directory (optional)\n"
              << "  -C <path>    Config file path (required for sandbox settings)\n"
              << "  --compile    Compile only (requires -s and -r)\n"
              << "  --cleanup    Cleanup temp dir (requires -r)\n";
}

int main(int argc, char** argv) {
    ExecutionConfig config;
    int opt;
    static struct option long_opts[] = {
        {"compile", no_argument, nullptr, 1},
        {"cleanup", no_argument, nullptr, 2},
        {"help",    no_argument, nullptr, 'h'},
        {nullptr, 0, nullptr, 0}
    };
    while ((opt = getopt_long(argc, argv, "s:r:c:t:m:i:o:e:w:C:h", long_opts, nullptr)) != -1) {
        switch (opt) {
            case 's': config.source_path = optarg; break;
            case 'r': config.request_id = optarg; break;
            case 'c': config.code_path = optarg; break;
            case 't': config.time_limit = std::stoi(optarg); break;
            case 'm': config.memory_limit = std::stoi(optarg); break;
            case 'i': config.input_path = optarg; break;
            case 'o': config.output_path = optarg; break;
            case 'e': config.error_path = optarg; break;
            case 'w': config.work_dir = optarg; break;
            case 'C': deep_oj::LoadConfig(optarg); break;
            case 1: config.compile_only = true; break;
            case 2: config.cleanup_only = true; break;
            case 'h': print_usage(argv[0]); return 0;
            default: print_usage(argv[0]); return 1;
        }
    }

    std::string cfg_err;
    if (!ValidateConfig(cfg_err)) {
        json result_json;
        result_json["schema_version"] = 1;
        result_json["status"] = "System Error";
        result_json["error"] = cfg_err;
        std::cout << result_json.dump() << std::endl;
        return 1;
    }

    // 1. 初始化沙箱
    deep_oj::Sandbox sandbox(deep_oj::g_runner_config.workspace_root);

    // Cleanup mode
    if (config.cleanup_only) {
        if (config.request_id.empty()) {
            std::cerr << "Error: cleanup requires -r <request_id>\n";
            return 1;
        }
        sandbox.Cleanup(config.request_id);
        json result_json;
        result_json["schema_version"] = 1;
        result_json["status"] = "Cleaned";
        result_json["request_id"] = config.request_id;
        std::cout << result_json.dump() << std::endl;
        return 0;
    }

    // Compile mode
    if (config.compile_only || !config.source_path.empty()) {
        if (config.source_path.empty() || config.request_id.empty()) {
            std::cerr << "Error: compile requires -s <source_path> and -r <request_id>\n";
            return 1;
        }
        std::ifstream ifs(config.source_path);
        if (!ifs) {
            std::cerr << "Error: failed to open source file: " << config.source_path << "\n";
            return 1;
        }
        std::stringstream buffer;
        buffer << ifs.rdbuf();
        deep_oj::CompileResult cres = sandbox.Compile(config.request_id, buffer.str());
        json result_json;
        result_json["schema_version"] = 1;
        if (cres.success) {
            result_json["status"] = "Compiled";
            result_json["exe_path"] = cres.exe_path;
        } else {
            result_json["status"] = "Compile Error";
            result_json["error"] = cres.error_message;
        }
        std::cout << result_json.dump() << std::endl;
        return cres.success ? 0 : 2;
    }

    // Run mode
    if (config.code_path.empty() || config.input_path.empty() || config.output_path.empty()) {
        std::cerr << "Error: Missing required arguments.\n";
        print_usage(argv[0]);
        return 1;
    }

    if (config.work_dir.empty()) {
        config.work_dir = fs::path(config.code_path).parent_path().string();
    }

    if (config.error_path.empty()) {
        config.error_path = (fs::path(config.work_dir) / "stderr.txt").string();
    }
    
    // 2. 运行
    deep_oj::RunResult rres = sandbox.Run(
        config.code_path, 
        config.input_path, 
        config.output_path, 
        config.error_path, 
        config.time_limit, 
        config.memory_limit
    );

    // 3. 构建结果 JSON
    json result_json;
    result_json["schema_version"] = 1;
    result_json["time_used"] = rres.time_used;
    result_json["memory_used"] = rres.memory_used;
    result_json["exit_code"] = rres.exit_code;
    
    // 状态映射
    std::string status_str;
    switch (rres.status) {
        case deep_oj::SandboxStatus::OK: status_str = "Finished"; break; // 注意：Go Wrapper 负责判断 WA/AC
        case deep_oj::SandboxStatus::TIME_LIMIT_EXCEEDED: status_str = "Time Limit Exceeded"; break;
        case deep_oj::SandboxStatus::MEMORY_LIMIT_EXCEEDED: status_str = "Memory Limit Exceeded"; break;
        case deep_oj::SandboxStatus::OUTPUT_LIMIT_EXCEEDED: status_str = "Output Limit Exceeded"; break;
        default: status_str = "Runtime Error"; break;
    }
    result_json["status"] = status_str;

    if (!rres.error_message.empty()) {
        result_json["error"] = rres.error_message;
    }

    // 4. 输出到 stdout
    std::cout << result_json.dump() << std::endl;

    return 0;
}
