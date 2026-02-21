# Deep-OJ Failure Modes（Baseline）

本文件先记录 A1 阶段已确认的关键故障模式，后续按任务队列持续扩展。

## 1. Redis 不可用（提交路径）

- 触发点：`src/go/internal/api/handler.go` 提交阶段 `LPush queue:pending` 失败
- 现象：`/submit` 返回 `QUEUE_ERROR`
- 定位：API 日志关键字 `Redis 推送错误`
- 恢复：恢复 Redis 后重提；后续建议 Outbox（D1）避免丢单

## 2. Scheduler 无可用 Worker

- 触发点：`src/go/cmd/scheduler/main.go` `selectWorker` 失败
- 现象：任务被 `RequeueTask(queue:processing -> queue:pending)` 循环重试
- 定位：日志关键字 `No workers available`
- 恢复：恢复 worker 可达（`WORKER_ADDR/WORKER_ADDRS`），观察 pending 回落

## 3. Worker 执行完成但结果未入 Stream

- 触发点：`src/go/internal/worker/judge.go` `XADD stream:results` 连续失败
- 现象：状态卡在 processing
- 定位：日志关键字 `Redis stream add failed`
- 恢复：修复 Redis 后重试；当前会删除 `result:{job_id}` 以避免脏缓存

## 4. ACK 消费失败

- 触发点：`src/go/internal/scheduler/ack_listener.go` `processResult` 失败
- 现象：消息留在 pending entries list，不 `XACK`
- 定位：日志关键字 `Result processing failed, keep pending`
- 恢复：修复 DB/JSON 解析问题后，listener 重启可继续消费 pending

## 5. Worker 崩溃导致 processing 残留

- 触发点：任务在 `queue:processing`，worker 心跳丢失
- 现象：任务长期不完成
- 定位：`task:processing:zset` 超时项、Watchdog 日志 `Worker dead, requeuing task`
- 恢复：Watchdog/SlowPath 会回收并重试；需检查是否触发重复执行风险

## 6. DB 已 done，重复结果回写

- 触发点：旧结果重复投递
- 现象：当前 SQL 条件 `state != 'done'` 下被忽略
- 定位：日志关键字 `Duplicate result ignored (already done)`
- 恢复：无需人工处理；后续需升级到 attempt_id fencing（C5）
