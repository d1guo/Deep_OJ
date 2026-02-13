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
- 结果流写入加入可配置重试与退避，保证 SetNX+XAdd 一致性。
- ACK 仅在 DB 同步成功后确认，失败消息保持 pending 可重放。
- Scheduler 无可用 Worker 时立即回滚任务到 pending。
- 缓存反序列化失败时回退到 DB。

### API 行为
- 提交请求体大小限制 + 代码长度限制可配置。
- 提交默认/最大时间与内存限制可配置。
- 缓存命中会创建提交记录并返回结构化结果。
- 题目包上传限制大小，采用流式 hash 避免 OOM。

### Worker & Scheduler
- `ETCD_ENDPOINTS` 支持逗号分隔；新增 dial 超时与 lease TTL 配置。
- 指标端口与采样频率可配置。
- 队列超时、watchdog、slow-path 周期、重试 TTL 可配置。
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
