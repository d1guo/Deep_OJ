#include "sandbox.h"
#include <iostream>
#include <cassert>
#include <string>

using namespace deep_oj;

int main() {
    try {
        // 使用临时目录进行测试
        Sandbox sandbox("/tmp/deep_oj_compile_test_env");

        std::string req_id = "test_syntax_error_001";
        
        // 这是一个典型的语法错误代码：缺少分号
        std::string bad_code = R"(
            #include <iostream>
            int main() {
                std::cout << "This should fail compilation" // <--- Missing semicolon
                return 0;
            }
        )";

        std::cout << "========================================" << std::endl;
        std::cout << "测试场景: 提交无法编译的代码 (期待失败)" << std::endl;
        std::cout << "========================================" << std::endl;

        auto result = sandbox.Compile(req_id, bad_code);

        if (result.success == false) {
            std::cout << "\n[PASS] 测试通过！编译不仅失败了，而且被我们捕获了。\n" << std::endl;
            std::cout << "错误信息 (Sandbox 返回): " << std::endl;
            std::cout << "----------------------------------------" << std::endl;
            std::cout << result.error_message << std::endl;
            std::cout << "----------------------------------------" << std::endl;
            
            // 简单验证错误信息里是否包含 g++ 的报错关键词 (例如 "error" 或 "expected")
            if (result.error_message.find("error") != std::string::npos || 
                result.error_message.find("expected") != std::string::npos) {
                std::cout << "[PASS] 错误日志中包含 g++ 的具体报错内容。" << std::endl;
            } else {
                std::cout << "[WARN] 错误日志似乎没有包含 g++ 的标准报错，请人工检查。" << std::endl;
            }

        } else {
            std::cout << "\n[FAIL] 测试失败！代码居然编译成功了？" << std::endl;
            std::cout << "这不科学，或者环境里的 g++ 过于宽容。" << std::endl;
            sandbox.Cleanup(req_id);
            return 1;
        }

        // 清理环境
        sandbox.Cleanup(req_id);

    } catch (const std::exception& e) {
        std::cerr << "Test Exception: " << e.what() << std::endl;
        return 1;
    }

    return 0;
}
