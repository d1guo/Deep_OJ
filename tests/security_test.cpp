#include "sandbox.h"
#include <iostream>
#include <vector>
#include <fstream>
#include <chrono>
#include <thread>
#include <cassert>

using namespace deep_oj;

// Color codes
const std::string RESET = "\033[0m";
const std::string RED = "\033[31m";
const std::string GREEN = "\033[32m";
const std::string YELLOW = "\033[33m";

void PrintResult(const std::string& name, bool pass, const std::string& msg = "") {
    std::cout << "[" << (pass ? GREEN + "PASS" : RED + "FAIL") << RESET << "] " 
              << name << (msg.empty() ? "" : " - " + msg) << std::endl;
}

void RunTest(Sandbox& sandbox, std::string name, std::string code, SandboxStatus expected_status, int time_limit = 1000, int mem_limit = 128 * 1024) {
    std::string req_id = "test_" + name;
    
    // 1. Compile
    auto compile_res = sandbox.Compile(req_id, code);
    if (!compile_res.success) {
        if (expected_status == SandboxStatus::SYSTEM_ERROR) {
             // Sometimes we test compile failures? No, mostly runtime.
             // If compilation fails unexpectedly
             PrintResult(name, false, "Compilation Failed: " + compile_res.error_message);
             return;
        }
        // Maybe the test code itself is invalid
        PrintResult(name, false, "Compilation Failed: " + compile_res.error_message);
        return;
    }

    // 2. Run
    auto run_res = sandbox.Run(compile_res.exe_path, time_limit, mem_limit);

    // 3. Verify
    bool status_match = (run_res.status == expected_status);
    bool msg_match = true;

    if (status_match) {
        PrintResult(name, true, "Status matched");
    } else {
        std::string status_str;
        switch(run_res.status) {
            case SandboxStatus::OK: status_str = "OK"; break;
            case SandboxStatus::TIME_LIMIT_EXCEEDED: status_str = "TLE"; break;
            case SandboxStatus::MEMORY_LIMIT_EXCEEDED: status_str = "MLE"; break;
            case SandboxStatus::OUTPUT_LIMIT_EXCEEDED: status_str = "OLE"; break;
            case SandboxStatus::RUNTIME_ERROR: status_str = "RE"; break;
            case SandboxStatus::SYSTEM_ERROR: status_str = "SE"; break;
        }
        PrintResult(name, false, "Expected " + std::to_string((int)expected_status) + ", Got " + status_str + " (" + run_res.error_message + ")");
    }
}

int main() {
    std::cout << "=== Starting Security & Stability Tests ===" << std::endl;

    try {
        Sandbox sandbox("/tmp/deep_oj_tests");

        // 1. Baseline
        std::string code_ok = R"(
            #include <iostream>
            int main() {
                int a = 1, b = 2;
                std::cout << a + b << std::endl; 
                return 0;
            }
        )";
        RunTest(sandbox, "Baseline_OK", code_ok, SandboxStatus::OK);

        // 2. Timeout (TLE)
        std::string code_tle = R"(
            int main() {
                while(true) {}
                return 0;
            }
        )";
        RunTest(sandbox, "Time_Limit", code_tle, SandboxStatus::TIME_LIMIT_EXCEEDED);

        // 3. Memory (MLE)
        std::string code_mle = R"(
            #include <vector>
            #include <cstring>
            int main() {
                // Try to allocate 200MB (limit is 128MB)
                // We must touch memory to trigger RSS growth
                std::vector<char*> ptrs;
                for(int i=0; i<200; ++i) {
                    char* p = new char[1024*1024];
                    std::memset(p, 0, 1024*1024);
                    ptrs.push_back(p);
                }
                return 0;
            }
        )";
        RunTest(sandbox, "Memory_Limit", code_mle, SandboxStatus::MEMORY_LIMIT_EXCEEDED);

        // 4. File Read Attack (/etc/passwd)
        // Note: In our sandbox, /etc is NOT mounted. 
        // So checking if we can read host's /etc/passwd
        std::string code_read_etc = R"(
            #include <fstream>
            #include <iostream>
            int main() {
                std::ifstream f("/etc/passwd");
                if (f.good()) {
                    // Start reading
                    std::string line;
                    if (std::getline(f, line)) {
                        // If we read something, return 0 (Success = BAD for security)
                        return 0; 
                    }
                }
                // Failed to read (Good)
                return 1;
            }
        )";
        // Expect RUNTIME_ERROR (exit code 1) because it fails to find file
        RunTest(sandbox, "Hack_Read_Etc", code_read_etc, SandboxStatus::RUNTIME_ERROR);

        // 5. Root Traversal (../../)
        // Try to escape chroot/pivot_root
        std::string code_traverse = R"(
            #include <fstream>
            int main() {
                std::ifstream f("../../../../../etc/passwd");
                if (f.good()) return 0; // Bad
                return 1; // Good
            }
        )";
        RunTest(sandbox, "Hack_Traverse", code_traverse, SandboxStatus::RUNTIME_ERROR);

        // 6. Write to Root
        // Try to write a file in / 
        std::string code_write = R"(
            #include <fstream>
            int main() {
                std::ofstream f("/hacked.txt");
                if (f.good()) return 0; // Write success (Bad)
                return 1; // Write failed (Good)
            }
        )";
        RunTest(sandbox, "Hack_Write_Root", code_write, SandboxStatus::RUNTIME_ERROR);

        // 7. Fork Bomb
        // Limit is set to 1 via RLIMIT_NPROC (or low number)
        std::string code_fork = R"(
            #include <unistd.h>
            int main() {
                while(1) {
                    fork();
                }
                return 0;
            }
        )";
        // This might cause RE (cannot fork) or SE logic depending on how it's caught
        // Usually, fork() fails returning -1, creating a loop.
        // It will eventually TLE if it just loops failing forks.
        // Or if it crashes.
        RunTest(sandbox, "Hack_Fork_Bomb", code_fork, SandboxStatus::TIME_LIMIT_EXCEEDED); // Or RUNTIME_ERROR

    } catch (const std::exception& e) {
        std::cerr << "Test Suite Exception: " << e.what() << std::endl;
        return 1;
    }

    std::cout << "=== Tests Completed ===" << std::endl;
    return 0;
}
