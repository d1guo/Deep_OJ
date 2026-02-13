# 数据一致性策略

## 目标
- PostgreSQL 是 **最终权威**（最终一致）。
- Redis 仅作 **缓存/队列/中间态**。
- 任意结果丢失时可由 SlowPath 回收。

## 提交流程
1. API 写 PostgreSQL：`state=pending`。
2. API 推任务到 Redis Pending 队列。
3. Scheduler 取出任务，写 `processing_start` + ZSET。
4. Worker 执行并写 `result:<job_id>`（Redis）+ Stream 结果。
5. Scheduler 消费 Stream 更新 PostgreSQL：`state=done` + `status=判题结果`。

## 读路径
- `/status` 先查 Redis Result（热点），未命中回退 PostgreSQL。
- PostgreSQL 为权威：如果 Redis 丢失，仍可查询结果。

## 幂等与恢复
- Result 写入 Redis 使用 `SETNX`，重复上报会被忽略。
- Scheduler 更新数据库采用 `state != done` 条件，避免重复覆盖。
- SlowPath 定期扫描 DB `state=pending` 且超时的任务，尝试重入队。

## 风险与兜底
- Redis PubSub 可能丢消息，因此采用 Redis Stream + ACK。
- Watchdog/SlowPath 使用 ZSET 追踪超时任务，避免全量扫描。
