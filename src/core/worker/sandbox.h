#ifndef DEEP_OJ_SANDBOX_H
#define DEEP_OJ_SANDBOX_H

#include <string>

namespace deep_oj
{
    /**
     * @brief 编译结果结构体
     * 包含编译是否成功、可执行文件路径以及错误信息
     */
    struct CompileResult
    {
        bool success = false;            // 编译是否成功
        std::string exe_path;           // 生成的可执行文件路径（仅当 success 为 true 时有效）
        std::string error_message;      // 错误信息（编译错误或系统错误）
    };

    /**
     * @brief 运行结果结构体
     */
    enum class SandboxStatus {
        OK = 0,                 // 0: OK
        TIME_LIMIT_EXCEEDED,    // 1: TLE
        MEMORY_LIMIT_EXCEEDED,  // 2: MLE
        OUTPUT_LIMIT_EXCEEDED,  // 3: OLE
        RUNTIME_ERROR,          // 4: RE
        SYSTEM_ERROR            // 5: SE
    };

    struct RunResult
    {
        SandboxStatus status = SandboxStatus::OK;
        int time_used = 0;      // ms
        long memory_used = 0;   // KB
        int exit_code = 0;      // 只有 status != SYSTEM_ERROR 时有效
        std::string error_message; // 系统错误详情
    };

    /**
     * @brief 沙箱类
     * 负责管理临时目录、代码文件写入以及安全的编译执行
     */
    class Sandbox
    {
    public:
        /**
         * @brief 构造函数
         * @param temp_dir 临时工作的根目录（例如 /tmp/deep_oj）
         * @throw std::runtime_error 如果无法创建根目录
         */
        explicit Sandbox(const std::string& temp_dir = "/tmp/deep_oj");

        /**
         * @brief 执行编译任务
         * 为每个请求创建独立的子目录，将代码写入并调用 g++ 进行编译
         *
         * @param request_id 请求的唯一标识 ID，用于生成子目录名
         * @param source_code 待编译的 C++ 源代码
         * @return CompileResult 包含编译结果
         */
        CompileResult Compile(const std::string& request_id, const std::string& source_code);

        /**
         * @brief 运行可执行文件并进行监控
         * @param exe_path 可执行文件路径
         * @param time_limit_ms 时间限制 (毫秒)
         * @param memory_limit_kb 内存限制 (KB)
         * @return RunResult 运行结果
         */
        RunResult Run(const std::string& exe_path, 
                      const std::string& stdin_path,
                      const std::string& stdout_path,
                      const std::string& stderr_path,
                      int time_limit_ms, 
                      int memory_limit_kb);

        /**
         * @brief 清理请求临时目录
         * @param request_id 请求 ID
         */
        void Cleanup(const std::string& request_id);

    private:
        std::string temp_dir_; // 根临时目录路径
    };
}


#endif // DEEP_OJ_SANDBOX_H
