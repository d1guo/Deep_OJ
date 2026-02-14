/**
 * @file main.cpp (judge_engine)
 * @brief C++ 判题核心引擎 (CLI)
 *
 * 约束:
 * 1. stdout 只输出单行 JSON (末尾 \n)
 * 2. debug/log 仅输出到 stderr
 */
#include <cstdlib>
#include <filesystem>
#include <fstream>
#include <getopt.h>
#include <iostream>
#include <memory>
#include <nlohmann/json.hpp>
#include <sstream>
#include <string>
#include <unistd.h>

#include "sandbox.h"
#include "sandbox_internal.h"

namespace deep_oj {
void LoadConfig(const std::string& path);
}

using json = nlohmann::json;
namespace fs = std::filesystem;

namespace {

struct ExecutionConfig {
    std::string source_path;  // compile mode source path
    std::string code_path;
    std::string input_path;
    std::string output_path;
    std::string error_path;
    int time_limit = 1000;       // ms
    int memory_limit = 131072;   // KB
    std::string work_dir;
    std::string request_id;      // compile/cleanup id
    std::string job_id;
    long long attempt_id = -1;
    bool compile_only = false;
    bool cleanup_only = false;
    bool self_test = false;
    bool attempt_explicit = false;
};

struct VerdictAndStatus {
    std::string verdict;
    std::string status;
};

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

bool ParseAttemptID(const std::string& raw, long long& attempt_id) {
    try {
        attempt_id = std::stoll(raw);
    } catch (...) {
        return false;
    }
    return true;
}

void FillRunMetaFromEnv(ExecutionConfig& config) {
    if (config.job_id.empty()) {
        const char* env_job = std::getenv("JUDGE_JOB_ID");
        if (env_job != nullptr) {
            config.job_id = env_job;
        }
    }
    if (!config.attempt_explicit) {
        const char* env_attempt = std::getenv("JUDGE_ATTEMPT_ID");
        if (env_attempt != nullptr) {
            long long parsed = -1;
            if (ParseAttemptID(env_attempt, parsed)) {
                config.attempt_id = parsed;
                config.attempt_explicit = true;
            }
        }
    }
}

VerdictAndStatus ToVerdictAndStatus(deep_oj::SandboxStatus status) {
    switch (status) {
        case deep_oj::SandboxStatus::OK:
            return {"OK", "Finished"};
        case deep_oj::SandboxStatus::TIME_LIMIT_EXCEEDED:
            return {"TLE", "Time Limit Exceeded"};
        case deep_oj::SandboxStatus::MEMORY_LIMIT_EXCEEDED:
            return {"MLE", "Memory Limit Exceeded"};
        case deep_oj::SandboxStatus::OUTPUT_LIMIT_EXCEEDED:
            return {"OLE", "Output Limit Exceeded"};
        case deep_oj::SandboxStatus::SYSTEM_ERROR:
            return {"SE", "System Error"};
        default:
            return {"RE", "Runtime Error"};
    }
}

json BuildRunJSON(
    const std::string& job_id,
    long long attempt_id,
    const std::string& verdict,
    int time_ms,
    long mem_kb,
    int exit_signal,
    const std::string& sandbox_error,
    int exit_code,
    const std::string& status,
    const std::string& error_message
) {
    json out;
    out["schema_version"] = 1;
    out["job_id"] = job_id;
    out["attempt_id"] = attempt_id;
    out["verdict"] = verdict;
    out["time_ms"] = time_ms;
    out["mem_kb"] = mem_kb;
    out["exit_signal"] = exit_signal;
    out["sandbox_error"] = sandbox_error;

    // Backward-compatible fields for existing Go worker parser.
    out["status"] = status;
    out["time_used"] = time_ms;
    out["memory_used"] = mem_kb;
    out["exit_code"] = exit_code;
    if (!error_message.empty()) {
        out["error"] = error_message;
    }
    return out;
}

void EmitJSONLine(const json& out) {
    std::cout << out.dump() << '\n';
}

void EmitRunSystemError(
    const ExecutionConfig& config,
    const std::string& sandbox_error,
    const std::string& error_message
) {
    EmitJSONLine(BuildRunJSON(
        config.job_id,
        config.attempt_id,
        "SE",
        0,
        0,
        0,
        sandbox_error,
        0,
        "System Error",
        error_message
    ));
}

void PrintUsage(const char* prog) {
    std::cerr << "Usage: " << prog << " [options]\n"
              << "Options:\n"
              << "  -s <path>         Path to source file (compile mode)\n"
              << "  -r <id>           Request ID (compile/cleanup)\n"
              << "  -c <path>         Path to compiled executable (run mode)\n"
              << "  -t <ms>           Time limit (default: 1000)\n"
              << "  -m <kb>           Memory limit (default: 131072)\n"
              << "  -i <path>         Input file path (stdin)\n"
              << "  -o <path>         Output file path (stdout)\n"
              << "  -e <path>         Error file path (stderr)\n"
              << "  -w <path>         Working directory (optional)\n"
              << "  -C <path>         Config file path (required except --self_test)\n"
              << "  --job_id <id>     Job ID passed from worker\n"
              << "  --attempt_id <n>  Attempt ID passed from worker\n"
              << "  --compile         Compile only (requires -s and -r)\n"
              << "  --cleanup         Cleanup temp dir (requires -r)\n"
              << "  --self_test       Print one-line protocol JSON and exit\n";
}

}  // namespace

int main(int argc, char** argv) {
    ExecutionConfig config;
    int opt;
    static struct option long_opts[] = {
        {"compile", no_argument, nullptr, 1},
        {"cleanup", no_argument, nullptr, 2},
        {"job_id", required_argument, nullptr, 3},
        {"attempt_id", required_argument, nullptr, 4},
        {"self_test", no_argument, nullptr, 5},
        {"help", no_argument, nullptr, 'h'},
        {nullptr, 0, nullptr, 0}
    };
    while ((opt = getopt_long(argc, argv, "s:r:c:t:m:i:o:e:w:C:h", long_opts, nullptr)) != -1) {
        switch (opt) {
            case 's':
                config.source_path = optarg;
                break;
            case 'r':
                config.request_id = optarg;
                break;
            case 'c':
                config.code_path = optarg;
                break;
            case 't':
                config.time_limit = std::stoi(optarg);
                break;
            case 'm':
                config.memory_limit = std::stoi(optarg);
                break;
            case 'i':
                config.input_path = optarg;
                break;
            case 'o':
                config.output_path = optarg;
                break;
            case 'e':
                config.error_path = optarg;
                break;
            case 'w':
                config.work_dir = optarg;
                break;
            case 'C':
                deep_oj::LoadConfig(optarg);
                break;
            case 1:
                config.compile_only = true;
                break;
            case 2:
                config.cleanup_only = true;
                break;
            case 3:
                config.job_id = optarg;
                break;
            case 4:
                if (!ParseAttemptID(optarg, config.attempt_id)) {
                    config.attempt_id = -1;
                } else {
                    config.attempt_explicit = true;
                }
                break;
            case 5:
                config.self_test = true;
                break;
            case 'h':
                PrintUsage(argv[0]);
                return 0;
            default:
                PrintUsage(argv[0]);
                return 1;
        }
    }

    FillRunMetaFromEnv(config);

    if (config.self_test) {
        const bool missing_job_id = config.job_id.empty();
        const bool missing_attempt_id = !config.attempt_explicit;
        if (missing_job_id || missing_attempt_id) {
            if (missing_attempt_id) {
                config.attempt_id = -1;
            }
            const std::string err_code = missing_job_id ? "missing_job_id" : "missing_attempt_id";
            const std::string err_msg = missing_job_id
                                            ? "missing required --job_id/JUDGE_JOB_ID"
                                            : "missing required --attempt_id/JUDGE_ATTEMPT_ID";
            EmitJSONLine(BuildRunJSON(
                config.job_id,
                config.attempt_id,
                "SE",
                0,
                0,
                0,
                err_code,
                0,
                "System Error",
                err_msg
            ));
            return 0;
        }
        EmitJSONLine(BuildRunJSON(
            config.job_id,
            config.attempt_id,
            "OK",
            1,
            64,
            0,
            "",
            0,
            "Finished",
            ""
        ));
        return 0;
    }

    std::string cfg_err;
    if (!ValidateConfig(cfg_err)) {
        if (!config.code_path.empty() || !config.input_path.empty() || !config.output_path.empty()) {
            EmitRunSystemError(config, "config_invalid", cfg_err);
        } else {
            json result_json;
            result_json["schema_version"] = 1;
            result_json["status"] = "System Error";
            result_json["error"] = cfg_err;
            EmitJSONLine(result_json);
        }
        return 1;
    }

    deep_oj::Sandbox sandbox(deep_oj::g_runner_config.workspace_root);

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
        EmitJSONLine(result_json);
        return 0;
    }

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
        EmitJSONLine(result_json);
        return cres.success ? 0 : 2;
    }

    if (config.code_path.empty() || config.input_path.empty() || config.output_path.empty()) {
        EmitRunSystemError(config, "missing_run_args", "run mode requires -c, -i and -o");
        return 1;
    }

    if (config.job_id.empty()) {
        EmitRunSystemError(config, "missing_job_id", "missing required --job_id/JUDGE_JOB_ID");
        return 1;
    }

    if (!config.attempt_explicit) {
        EmitRunSystemError(config, "missing_attempt_id", "missing required --attempt_id/JUDGE_ATTEMPT_ID");
        return 1;
    }

    if (config.work_dir.empty()) {
        config.work_dir = fs::path(config.code_path).parent_path().string();
    }
    if (config.error_path.empty()) {
        config.error_path = (fs::path(config.work_dir) / "stderr.txt").string();
    }

    deep_oj::RunResult rres = sandbox.Run(
        config.code_path,
        config.input_path,
        config.output_path,
        config.error_path,
        config.time_limit,
        config.memory_limit
    );

    VerdictAndStatus mapped = ToVerdictAndStatus(rres.status);
    std::string sandbox_error = rres.sandbox_error;
    if (mapped.verdict == "SE" && sandbox_error.empty()) {
        sandbox_error = "system_error";
    }

    EmitJSONLine(BuildRunJSON(
        config.job_id,
        config.attempt_id,
        mapped.verdict,
        rres.time_used,
        rres.memory_used,
        rres.exit_signal,
        sandbox_error,
        rres.exit_code,
        mapped.status,
        rres.error_message
    ));
    return 0;
}
