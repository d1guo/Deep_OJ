#include <iostream>
#include <vector>
#include <string>
#include <fstream>
#include <cstring>
#include <filesystem>
#include "../src/worker/sandbox.h"
#include "../src/worker/sandbox_internal.h" // For GlobalConfig definition

using namespace deep_oj;
namespace fs = std::filesystem;

// ANSI Colors
#define GREEN "\033[0;32m"
#define RED "\033[0;31m"
#define RESET "\033[0m"

void RunTest(Sandbox& sandbox, const std::string& name, const std::string& source_file, 
             SandboxStatus expected_status, const std::string& expected_output_substr = "") {
    std::cout << "Testing " << name << "..." << std::flush;

    // 1. Read Source
    std::ifstream t(source_file);
    if (!t.is_open()) {
        std::cerr << RED << " [FAIL] Cannot open source: " << source_file << RESET << std::endl;
        return;
    }
    std::stringstream buffer;
    buffer << t.rdbuf();
    std::string source_code = buffer.str();

    // 2. Compile
    CompileResult c_res = sandbox.Compile("test_" + name, source_code);
    if (!c_res.success) {
        std::cerr << RED << " [FAIL] Compilation failed: " << c_res.error_message << RESET << std::endl;
        return;
    }

    // 3. Run
    // Defaults: 1000ms, 128MB
    // Create dummy input
    std::string input_file = "/tmp/test.in";
    std::ofstream(input_file).put(' '); 

    RunResult r_res = sandbox.Run(c_res.exe_path, input_file, "/tmp/test.out", "/tmp/test.err", 1000, 128 * 1024);

    // 4. Verify Status
    bool status_match = (r_res.status == expected_status);
    
    // Special handling for Runtime Error which might be what we want for Seccomp violations
    if (expected_status == SandboxStatus::RUNTIME_ERROR && r_res.exit_code != 0 && r_res.status == SandboxStatus::OK) {
        // Sometimes non-zero exit is treated as OK by Sandbox if not specifically SEGV/etc? 
        // Let's check sandbox implementation logic. Usually non-zero exit is RE.
        // Actually Sandbox::Run returns RUNTIME_ERROR for non-zero exit code.
    }
    
    // For Fork Bomb: We expect TLE or RE (due to pids limit)
    // For Syscall: We expect RE (SIGSYS)
    // For File Escape: We expect OK but output should NOT contain content
    
    bool passed = false;
    std::string failure_reason;

    if (name.find("fork_bomb") != std::string::npos) {
        // Fork bomb should trigger RE (cannot fork) or TLE (stuck)
        // If pids.max works, fork fails with -1, program might exit with error or finish quickly.
        // The test code: `while(fork()>=0);` -> if fork returns -1, it exits loop and returns 0 (OK).
        // So actually, if PIDS limit works, status should be OK! And time used should be small.
        if (r_res.status == SandboxStatus::OK) {
             passed = true;
        } else if (r_res.status == SandboxStatus::RUNTIME_ERROR) {
             passed = true; // Also fine if it crashes
        } else {
             failure_reason = "Status: " + std::to_string((int)r_res.status);
        }
    } 
    else if (name.find("syscall") != std::string::npos) {
        // Syscall attack -> Seccomp kills it -> RE (SIGSYS usually)
        if (r_res.status == SandboxStatus::RUNTIME_ERROR) passed = true;
        else failure_reason = "Expected RE (Seccomp kill), got " + std::to_string((int)r_res.status);
    }
    else if (name.find("filesystem") != std::string::npos) {
        // Should run OK but output should NOT contain sensitive info
        if (r_res.status == SandboxStatus::OK) {
            std::ifstream out("/tmp/test.out");
            std::string output((std::istreambuf_iterator<char>(out)), std::istreambuf_iterator<char>());
            if (output.find("root:x:0:0") != std::string::npos) {
                passed = false;
                failure_reason = "Leaked /etc/passwd!";
            } else {
                passed = true;
            }
        } else {
             // If it crashed trying to read, that's also fine (secure)
             passed = true;
        }
    }
    else {
        // General check
        if (r_res.status == expected_status) passed = true;
        else failure_reason = "Expected status " + std::to_string((int)expected_status) + " got " + std::to_string((int)r_res.status);
    }

    if (passed) {
        std::cout << GREEN << " [PASS]" << RESET << std::endl;
    } else {
        std::cout << RED << " [FAIL] " << failure_reason << RESET << std::endl;
    }
    
    sandbox.Cleanup("test_" + name);
}

// Declare extern
void LoadConfig(const std::string& path);

int main() {
    std::cout << "=== Security Integration Test ===" << std::endl;
    
    // 1. Load defaults from config.yaml
    // Note: config.yaml is copied to build directory by CMake
    if (fs::exists("config.yaml")) {
        LoadConfig("config.yaml");
    } else if (fs::exists("../config.yaml")) {
        LoadConfig("../config.yaml");
    } else {
        std::cerr << "Warning: config.yaml not found, using manual defaults." << std::endl;
        strncpy(deep_oj::g_runner_config.compiler_path, "/usr/bin/g++", sizeof(deep_oj::g_runner_config.compiler_path)-1);
        // Ensure minimal mounts manually if config missing? 
        // Best rely on LoadConfig.
    }

    // 2. Override for test
    strncpy(deep_oj::g_runner_config.workspace_root, "/tmp/deep_oj_test", sizeof(deep_oj::g_runner_config.workspace_root) - 1);
    deep_oj::g_runner_config.run_uid = 65534; // nobody
    deep_oj::g_runner_config.run_gid = 65534; // nogroup
    
    try {
        Sandbox sandbox(deep_oj::g_runner_config.workspace_root);

        // 1. Fork Bomb
        RunTest(sandbox, "fork_bomb", "tests/security/test_codes/fork_bomb.cpp", SandboxStatus::OK);
        
        // 2. Syscall Attack
        RunTest(sandbox, "syscall_attack", "tests/security/test_codes/syscall_attack.cpp", SandboxStatus::RUNTIME_ERROR);
        
        // 3. Filesystem Escape
        RunTest(sandbox, "filesystem_escape", "tests/security/test_codes/filesystem_escape.cpp", SandboxStatus::OK);
        
    } catch (const std::exception& e) {
        std::cerr << "Exception: " << e.what() << std::endl;
        return 1;
    }

    return 0;
}
