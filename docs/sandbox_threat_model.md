# Sandbox 威胁模型

## 目标
隔离用户代码，防止逃逸、资源滥用和主机损害。

## 主要威胁
- **资源耗尽**：CPU/内存/进程数耗尽导致宿主不可用
- **文件系统逃逸**：访问宿主机敏感文件
- **系统调用攻击**：执行敏感 syscall（如 `execve`、`ptrace`、`socket`）
- **网络访问**：向外网发起请求或扫描
- **Log Bomb**：大量 stderr 输出导致磁盘耗尽

## 防护策略
- **Cgroups v2**：限制 CPU/Memory/PIDs/IO
- **Namespace**：隔离 PID/NET/IPC/UTS
- **Seccomp**：仅允许白名单 syscall
- **Chroot/Pivot Root**：文件系统隔离
- **资源限制**：`setrlimit` CPU/AS/FSIZE
- **日志限制**：`-fmax-errors` + stderr 文件大小限制

## 假设与非目标
- 不防御宿主 kernel 0-day
- 不承诺容器级多租户隔离（需配合 K8s/VM）

## 验证方式
参考 `tests/run_tests.sh` 安全用例（fork bomb / syscall / filesystem escape）
