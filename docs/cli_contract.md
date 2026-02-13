# C++ Core CLI 契约（V3）

**目标**：确保 Go 管控层与 C++ 执行层的边界稳定，参数固定、输出可解析、向后兼容。

## 通用约定
- 所有输出为 **单行 JSON**（stdout）。
- JSON 必含字段：`schema_version`、`status`。
- 非 0 退出码代表系统级异常（I/O、配置缺失、参数错误等）。
- `schema_version` 当前为 `1`，后续兼容时 **新增字段只增不删**。

## 运行模式（Run）
**用途**：运行已编译的二进制程序。

### 参数
- `-c <path>`：可执行文件路径（必填）
- `-i <path>`：stdin 文件路径（必填）
- `-o <path>`：stdout 输出文件路径（必填）
- `-e <path>`：stderr 输出文件路径（可选）
- `-t <ms>`：时间限制（默认 1000）
- `-m <kb>`：内存限制（默认 131072）
- `-w <path>`：工作目录（可选）
- `-C <path>`：config.yaml 路径（必填）

### 返回 JSON
```json
{
  "schema_version": 1,
  "status": "Finished|Time Limit Exceeded|Memory Limit Exceeded|Output Limit Exceeded|Runtime Error",
  "time_used": 12,
  "memory_used": 2048,
  "exit_code": 0,
  "error": "optional"
}
```

## 编译模式（Compile）
**用途**：在 C++ 沙箱内编译源码。

### 参数
- `--compile`
- `-s <path>`：源码路径（必填）
- `-r <request_id>`：请求 ID（必填）
- `-C <path>`：config.yaml 路径（必填）

### 返回 JSON
```json
{
  "schema_version": 1,
  "status": "Compiled|Compile Error",
  "exe_path": "/tmp/deep_oj/<request_id>/main",
  "error": "optional"
}
```

## 清理模式（Cleanup）
**用途**：清理编译产生的临时目录。

### 参数
- `--cleanup`
- `-r <request_id>`：请求 ID（必填）
- `-C <path>`：config.yaml 路径（必填）

### 返回 JSON
```json
{
  "schema_version": 1,
  "status": "Cleaned",
  "request_id": "<request_id>"
}
```

## 错误码（建议）
- `Compile Error`：编译失败（源码问题）
- `System Error`：系统错误（配置/权限/资源）
- `Runtime Error`：运行错误（程序异常）

> 兼容性策略：旧字段保持，新字段只增不删。
