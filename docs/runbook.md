# 运维 Runbook（简版）

## 1. Worker 无响应 / 任务堆积
1. 查看 `queue:processing` 长度与 `processing:zset` 超时任务
2. 检查 Worker 进程与 gRPC 端口是否存活
3. 如 Worker 崩溃：Watchdog/SlowPath 会自动重入队

## 2. Redis 异常
1. 检查 Redis 连接与慢查询
2. 如 Redis 不可用：提交与队列暂停（API 返回 5xx）
3. 恢复后：SlowPath 扫描 DB 重建队列

## 3. MinIO 不可用
1. Worker 下载测试数据失败 -> 任务失败
2. 检查 MinIO 状态与桶权限
3. 恢复后重试任务（重入队）

## 4. 判题结果丢失
1. 检查 Redis Stream `stream:results` 与消费者组
2. 确认 DB 是否已写入 `state=done`
3. 如 Redis 丢失：DB 仍为权威结果

## 5. Cgroups v2 未启用
1. 宿主机启用 cgroup v2（内核参数）
2. Docker 启动时挂载 `/sys/fs/cgroup`
3. Worker 设置 `REQUIRE_CGROUPS_V2=1`
