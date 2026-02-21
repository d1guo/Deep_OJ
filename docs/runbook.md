# 运维 Runbook（简版）

## 1. Stream backlog 持续增长
1. 查看 `deepoj:jobs` 长度与消费组 pending：`XLEN` / `XPENDING`
2. 检查 Worker 进程是否正常、`XREADGROUP` 是否持续消费
3. 必要时扩容 Worker 或限流提交

## 2. Redis 异常
1. 检查 Redis 连通性与认证配置
2. Redis 不可用时，提交与消费都会受影响
3. 恢复后由 outbox + reclaim 机制继续推进

## 3. MinIO 不可用
1. Worker 下载测试数据失败，任务会进入错误分支
2. 检查 MinIO 状态、桶权限与对象完整性
3. 恢复后重试任务

## 4. 判题状态卡住
1. 检查 Worker 日志中的 `db_finalize_*` 与 `xack_*`
2. 校验 `submissions` 的 `state/status/attempt_id`
3. 如存在 PEL 堆积，观察 reclaim 是否在推进

## 5. Cgroups v2 未启用
1. 宿主机启用 cgroup v2（内核参数）
2. Docker 挂载 `/sys/fs/cgroup`
3. Worker 设置 `REQUIRE_CGROUPS_V2=1`
