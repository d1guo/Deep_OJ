# Deep-OJ Failure Modes（Streams-only）

本文件记录当前 Streams-only 数据面（API outbox -> Redis Stream -> Worker -> DB finalize -> XACK -> reclaim）的关键故障模式。

## 1. Redis 不可用（提交或出队阶段）

- 触发点：API 写 outbox/入流或 Worker `XREADGROUP` 失败
- 现象：提交后状态长期 pending，或 worker 日志持续报 Redis 错误
- 定位：`docker compose logs --tail=200 api worker | rg -i "redis|xreadgroup|xadd"`
- 恢复：恢复 Redis 连通后重试；outbox 会继续补投未投递事件

## 2. Worker 无法 claim 任务

- 触发点：`ClaimSubmissionForRun` 返回拒绝或 DB 异常
- 现象：消息被读到但无法进入执行，状态不推进
- 定位：worker 日志关键字 `DB claim`、`db_claim_reject`
- 恢复：检查 DB 可用性与 submissions 行状态，恢复后由后续消费/reclaim 继续推进

## 3. Worker 执行完成但 finalize 失败

- 触发点：`FinalizeSubmissionWithFence` 失败
- 现象：日志出现 `db_finalize_error`，消息未成功收敛
- 定位：`docker compose logs --tail=300 worker | rg -i "db_finalize"`
- 恢复：优先修复 DB 问题；消息仍在流中，后续可由 reclaim 继续处理

## 4. XACK 失败导致 PEL 堆积

- 触发点：执行或拒绝分支中 `XACK` 失败
- 现象：`XPENDING` 持续增长，lag 上升
- 定位：`redis-cli XPENDING deepoj:jobs deepoj:workers` 与 worker 日志 `xack_error`
- 恢复：恢复 Redis 后，worker reclaim loop 会继续回收并确认

## 5. Worker 崩溃或租约丢失

- 触发点：worker 进程崩溃、心跳中断、lease owner 变化
- 现象：任务进入 pending entries list，原执行中断
- 定位：日志关键字 `lease_lost`、`reclaim_claimed`，以及 `XAUTOCLAIM` 相关指标
- 恢复：重启 worker；reclaim 机制按 `min-idle-ms` 规则重新接管

## 6. 重复执行与陈旧结果

- 触发点：网络抖动或重启导致同一 job 被多次尝试
- 现象：旧 attempt finalize 被拒绝（stale/already_finished）
- 定位：日志关键字 `db_finalize_rejected`、`db_finalize_stale`
- 恢复：无需人工干预；fencing 机制保证最终态单写入
