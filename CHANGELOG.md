# 更新日志

本项目的重要变更将记录在此。

## [Unreleased] - 2026-02-13

### 安全
- 启动时强制配置 `JWT_SECRET`，移除不安全默认值。
- 实现 OAuth state 生成与回调校验，并支持配置 TTL。
- 题目管理接口新增管理员鉴权中间件。
- 强化沙箱隔离：清理附加组并设置 `PR_SET_NO_NEW_PRIVS`。
- 默认禁用宿主机 checker，需显式开启。

### 可靠性
- Worker 执行结果使用 DB fencing finalize + XACK，保证“先落库后确认”。
- reclaim 链路基于 `XAUTOCLAIM` 与 DB 租约 CAS，支持崩溃恢复。
- API outbox 投递支持退避重试，降低短暂故障下的丢单风险。
- 缓存反序列化失败时回退到 DB。

### API 行为
- 提交请求体大小限制 + 代码长度限制可配置。
- 提交默认/最大时间与内存限制可配置。
- 缓存命中会创建提交记录并返回结构化结果。
- 题目包上传限制大小，采用流式 hash 避免 OOM。

### Worker & Scheduler
- Scheduler 收敛为控制面（发现与指标），不再承担派单数据面。
- Worker 仅保留 Streams 消费执行链路。
- 指标端口与采样频率可配置。
- checker/cleanup 超时可配置。

### Sandbox/Core
- 运行/编译 tmpfs 大小、编译日志大小、最大错误数可配置。
- cgroup pids 限制可配置。

### 数据/基础设施
- TestcaseHash 升级为 SHA-256（API + DB）。
- Redis/Postgres 连接池与超时统一移入配置。

### 配置
- 引入统一 `config.yaml`（API/Scheduler/Worker/基础设施参数），支持环境变量覆盖。
- 新增 `APP_CONFIG` 指定配置路径。
