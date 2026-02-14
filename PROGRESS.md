# Progress

## C1-补洞（已完成）

- 日期：2026-02-14
- 目标：C1 Streams 入队补洞收敛（不进入 C2）
- 基线 commit hash：`ff80b86`

### 完成项

- 入队顺序固定为 `SET payload -> XADD stream`。
- payload key 使用 `task:payload:<job_id>`，并增加 TTL 配置：`JOB_PAYLOAD_TTL_SEC`（默认 86400 秒）。
- payload 内容升级为 v1 envelope，包含固定字段：`schema_version=1`、`task_data_b64`。
- `XADD` 失败时执行 best-effort `DEL payload_ref`，返回 5xx，记录结构化日志（`job_id`/`trace_id`/`reason`）。
- 新增 API 侧指标：
  - `api_stream_enqueue_total{status=ok|error}`
  - `api_stream_enqueue_latency_ms`
- 日志补充 `stream_entry_id`（XADD 成功返回值）。

### 验收命令

```bash
cd /home/diguo/Deep_OJ/src/go
go test ./internal/api -run TestEnqueueJobToStream_XAddFailureDeletesPayload -v
go test ./internal/api -v

# 真实环境验收脚本（需 API/Redis 在线 + PROBLEM_ID）
PROBLEM_ID=<problem_id> REDIS_PASSWORD=<redis_password> \
  /home/diguo/Deep_OJ/scripts/verify_c1_stream_enqueue.sh
```

### 涉及文件

- `src/go/internal/api/stream_enqueue.go`
- `src/go/internal/api/stream_enqueue_test.go`
- `src/go/internal/api/handler.go`
- `src/go/internal/api/metrics.go`
- `src/go/internal/api/env.go`
- `src/go/internal/appconfig/config.go`
- `src/go/cmd/api/main.go`
- `scripts/verify_c1_stream_enqueue.sh`
- `DESIGN.md`
- `RUNBOOK.md`

## C2（已完成，含 C1 细节顺手收敛）

- 日期：2026-02-14
- 目标：Worker 使用 Redis Streams consumer group 消费，并实现 INV-2（DB 成功后才 XACK）
- 基线 commit hash：`ff80b86`

### 完成项

- Worker 新增 Streams Consumer Group 基线：
  - 启动时 `XGROUP CREATE <stream> <group> $ MKSTREAM`（BUSYGROUP 忽略）
  - 消费新消息：`XREADGROUP ... STREAMS <stream> >`
  - 默认配置：`JOB_STREAM_KEY=deepoj:jobs`、`JOB_STREAM_GROUP=deepoj:workers`
  - consumer 默认 `hostname-pid`（可通过 `JOB_STREAM_CONSUMER` 覆盖）
- INV-2 落地（`src/go/internal/worker/stream_consumer.go`）：
  - `missing_field` / `payload_missing_or_invalid` / `db_cas_reject` => `XACK`
  - `db_error` => **不 XACK**（保留在 PEL）
  - DB CAS 成功后才执行 `XACK`
  - `XACK` 失败记录 `reason=xack_error`，重试一次
- DB 新增 CAS 方法：`TryMarkSubmissionProcessing(job_id)`，仅 `state='pending'` 才推进到 `status='running', state='processing'`
- Worker 新增指标（低基数）：
  - `worker_stream_consume_total{status,reason}`
  - `worker_stream_consume_latency_ms`
  - `worker_stream_inflight`
- 新增 C2 单测：
  - DB 成功路径：验证最终调用 `XACK`
  - DB error 路径：验证不 `XACK`，reason=`db_error`

### C2 内顺手收敛 C1 小问题

- 配置语义收敛：文档与注释统一为“运行时从环境变量读取，config.yaml 仅回填缺省值（进程内）”。
- payload envelope v1 扩展：新增 `encoding=base64`、`content_type=application/json`；读取侧对缺失字段做兼容默认。
- `api_stream_enqueue_latency_ms` 确认为 Histogram，bucket 调整为毫秒级 `1/2/5/10/20/50/100/200/500/1000/2000`。
- `scripts/verify_c1_stream_enqueue.sh` 增强：
  - 校验 payload GET 非空
  - 校验 payload JSON 可解析
  - 校验 `schema_version` / `task_data_b64` / `encoding` / `content_type`

### 验收命令

```bash
cd /home/diguo/Deep_OJ/src/go
go test ./internal/worker -run 'TestStreamConsumer|TestDecodeStreamTaskPayload' -v
go test ./internal/api -run 'TestEnqueueJobToStream|TestEnqueueJobToStreamOrReply5xx' -v
go test ./...

# C1 stream 入队验证脚本（需 API/Redis 在线 + PROBLEM_ID）
PROBLEM_ID=<problem_id> REDIS_PASSWORD=<redis_password> \
  /home/diguo/Deep_OJ/scripts/verify_c1_stream_enqueue.sh
```

### 涉及文件

- `src/go/internal/worker/stream_consumer.go`
- `src/go/internal/worker/stream_consumer_test.go`
- `src/go/internal/worker/metrics.go`
- `src/go/internal/worker/config.go`
- `src/go/cmd/worker/main.go`
- `src/go/internal/repository/postgres.go`
- `src/go/internal/appconfig/config.go`
- `src/go/internal/api/stream_enqueue.go`
- `src/go/internal/api/handler.go`
- `src/go/internal/api/metrics.go`
- `src/go/cmd/api/main.go`
- `scripts/verify_c1_stream_enqueue.sh`
- `config.yaml`
- `docker-compose.yml`
- `DESIGN.md`
- `RUNBOOK.md`

## C3（已完成，含 C2 不足点顺手修复）

- 日期：2026-02-14
- 目标：引入 DB attempt_id/lease fencing，完成 claim->heartbeat->judge->fenced write-back->XACK 闭环
- 基线 commit hash：`ff80b86`

### 完成项

- DB schema 扩展（`sql/migrations/006_add_attempt_lease_and_error_fields.sql`）：
  - `attempt_id BIGINT NOT NULL DEFAULT 0`
  - `lease_owner TEXT`
  - `lease_until TIMESTAMPTZ`
  - `error_code TEXT` / `error_message TEXT`（poison 终态记录）
- Worker C3 闭环（`src/go/internal/worker/stream_consumer.go`）：
  - `XREADGROUP` 后先做 DB claim，原子推进 `pending -> running/processing` 并返回新 `attempt_id`
  - claim reject（`db_claim_reject`）=> `XACK`
  - claim db error（`db_error`）=> 不 `XACK`
  - 执行期间 heartbeat 刷 lease；`lease_lost`/`db_error_heartbeat` 会取消本地执行
  - judge 执行透传 claim 返回的 `attempt_id`（通过 context 传到 `executor.Execute`）
  - 结果落库使用 fencing SQL：`WHERE job_id AND attempt_id AND status='running'`
  - fenced 0 行 => `stale_attempt`（不覆盖新 attempt）
  - 仅 fenced 写回成功后 `XACK`
- C2 顺手收敛：
  - poison（`missing_field` / `payload_missing_or_invalid`）先标 DB `System Error + error_code`，再 `XACK`
  - consumer 命名支持 `JOB_STREAM_CONSUMER`，否则复用 `WORKER_ID`，最后回退 `hostname-pid`
  - `XGROUP CREATE ... MKSTREAM` 的 `BUSYGROUP` 保持幂等忽略
- 新增指标（低基数）：
  - `worker_claim_total{status,reason}`
  - `worker_lease_heartbeat_total{status,reason}`
  - `worker_stale_attempt_total`

### 验收命令

```bash
cd /home/diguo/Deep_OJ/src/go
go test ./internal/worker -run 'TestStreamConsumer|TestDecodeStreamTaskPayload' -v
go test ./internal/api -run 'TestEnqueueJobToStream|TestEnqueueJobToStreamOrReply5xx' -v
go test ./...

STREAM_KEY="${JOB_STREAM_KEY:-deepoj:jobs}"
GROUP="${JOB_STREAM_GROUP:-deepoj:workers}"
docker exec -it oj-redis redis-cli -a "$REDIS_PASSWORD" XINFO GROUPS "$STREAM_KEY"
docker exec -it oj-redis redis-cli -a "$REDIS_PASSWORD" XPENDING "$STREAM_KEY" "$GROUP" - + 10
```

### 涉及文件

- `sql/migrations/006_add_attempt_lease_and_error_fields.sql`
- `src/go/internal/repository/postgres.go`
- `src/go/internal/model/model.go`
- `src/go/internal/worker/stream_consumer.go`
- `src/go/internal/worker/stream_consumer_test.go`
- `src/go/internal/worker/attempt_context.go`
- `src/go/internal/worker/judge.go`
- `src/go/internal/worker/metrics.go`
- `src/go/internal/worker/config.go`
- `src/go/cmd/worker/main.go`
- `src/go/internal/appconfig/config.go`
- `config.yaml`
- `docker-compose.yml`
- `DESIGN.md`
- `RUNBOOK.md`

## C4（已完成，含 C3 高风险洞顺手修复）

- 日期：2026-02-14
- 目标：引入 PEL reclaim（`XAUTOCLAIM`）并落地 INV-4 分流；同时修复 attempt mismatch 提前 return 导致 PEL/DB 卡住的问题
- 基线 commit hash：`c1ea148`

### 完成项

- Worker 新增 reclaim loop（`src/go/internal/worker/stream_consumer.go`）：
  - 周期配置：`JOB_RECLAIM_INTERVAL_SEC`（默认 5s）
  - 批量配置：`JOB_RECLAIM_COUNT`（默认 16）
  - idle 阈值：`JOB_LEASE_SEC + JOB_RECLAIM_GRACE_SEC`（默认 75s）
  - 执行 `XAUTOCLAIM` 回收 PEL pending entry
- DB 新增 reclaim CAS（`src/go/internal/repository/postgres.go`）：
  - `ReclaimSubmissionForRun(job_id, lease_owner, lease_sec)`
  - 仅当 `status='running' AND state='processing' AND lease_until < NOW()` 时 reclaim 成功，并 `attempt_id = attempt_id + 1`
  - 返回稳定决策枚举：`claimed / done_or_stale / lease_still_valid / not_found`
- INV-4 分流落地：
  - `claimed`：执行 judge，fenced finalize 成功后 `XACK`
  - `done_or_stale` / `not_found`：业务性失败，直接 `XACK`
  - `lease_still_valid`：不执行、不 `XACK`（保留给原 consumer）
  - `db_error`：不 `XACK`（留在 PEL 下次重试）
- C3 高风险洞修复：
  - 执行结束发现 `finalResult.attempt_id` 与本次 claim attempt 不一致时，不再提前 `return`
  - 强制覆盖为本次 claim/reclaim attempt，再走 fenced finalize
  - fenced 0 行按 `stale_attempt` 处理并 `XACK`，避免 PEL 卡死
- C4 可观测性：
  - 指标：`worker_reclaim_total{status,reason}`、`worker_reclaim_latency_ms`、`worker_reclaim_inflight`
  - 日志字段统一：`trace_id/job_id/attempt_id/stream_entry_id/group/consumer/reason`

### 验收命令

```bash
cd /home/diguo/Deep_OJ/src/go
go test ./internal/worker -run 'Test.*Reclaim|Test.*AutoClaim|TestStreamConsumer' -v

STREAM_KEY="${JOB_STREAM_KEY:-deepoj:jobs}"
GROUP="${JOB_STREAM_GROUP:-deepoj:workers}"
CONSUMER="${JOB_STREAM_CONSUMER:-debug-consumer}"

docker exec -it oj-redis redis-cli -a "$REDIS_PASSWORD" XINFO GROUPS "$STREAM_KEY"
docker exec -it oj-redis redis-cli -a "$REDIS_PASSWORD" XPENDING "$STREAM_KEY" "$GROUP"
docker exec -it oj-redis redis-cli -a "$REDIS_PASSWORD" XPENDING "$STREAM_KEY" "$GROUP" - + 10
docker exec -it oj-redis redis-cli -a "$REDIS_PASSWORD" XAUTOCLAIM "$STREAM_KEY" "$GROUP" "$CONSUMER" 75000 0-0 COUNT 10
```

### 涉及文件

- `src/go/internal/worker/stream_consumer.go`
- `src/go/internal/worker/stream_consumer_test.go`
- `src/go/internal/repository/postgres.go`
- `src/go/internal/worker/config.go`
- `src/go/internal/worker/metrics.go`
- `src/go/internal/appconfig/config.go`
- `src/go/cmd/worker/main.go`
- `config.yaml`
- `docker-compose.yml`
- `DESIGN.md`
- `RUNBOOK.md`

## D1（已完成）

- 日期：2026-02-15
- 目标：Outbox Pattern（API 同事务写 submissions + outbox_events；dispatcher 异步补投递 Streams）
- 基线 commit hash：`1ab79b2`

### 完成项

- 新增迁移：`sql/migrations/007_add_outbox_events.sql`
  - 建表 `outbox_events`（`job_id` 唯一、`payload`、`attempts`、`next_attempt_at`、`last_error`、`dispatched_at`、`stream_entry_id` 等）
  - 索引：`idx_outbox_pending_retry`、`idx_outbox_job_id`
  - SQL 文件内提供回滚语句注释
- Repository 新增 outbox 能力（`src/go/internal/repository/postgres_outbox.go`）：
  - `CreateSubmissionAndOutbox`：同事务写 `submissions + outbox_events`
  - `ClaimPendingOutboxEvents`：`FOR UPDATE SKIP LOCKED` claim + 退避更新
  - `MarkOutboxDispatched` / `MarkOutboxDispatchError` / `CountOutboxPending`
- API submit 改造（`src/go/internal/api/handler.go`）：
  - 默认 `OUTBOX_ENABLED=true` 走 outbox 事务路径，Redis 不可用时提交仍成功
  - `OUTBOX_ENABLED=false` 保留旧直投递路径（回滚开关）
- 新增 dispatcher（`src/go/internal/api/outbox_dispatcher.go`）：
  - 周期拉 pending outbox，写 Redis payload + XADD stream
  - 成功标记 delivered，失败记录 last_error 并可重试
  - 语义为 at-least-once，重复投递由 Worker 现有 claim/lease/fence 拦截
- API 新增低基数指标（`src/go/internal/api/metrics.go`）：
  - `api_outbox_dispatch_total{status,reason}`
  - `api_outbox_dispatch_latency_seconds`
  - `api_outbox_pending`
- API 进程启动 dispatcher（`src/go/cmd/api/main.go`）
- 新增验证脚本：`scripts/verify_d1_outbox.sh`

### 验收命令

```bash
cd /home/diguo/Deep_OJ/src/go
go test ./internal/api -v
go test ./... -count=1
go test ./internal/api -run 'TestOutbox_' -count=20 -v
```

```bash
# 需要 API/Redis/Postgres 在线与 PROBLEM_ID
PROBLEM_ID=<problem_id> \
POSTGRES_PASSWORD=<postgres_password> \
REDIS_PASSWORD=<redis_password> \
bash /home/diguo/Deep_OJ/scripts/verify_d1_outbox.sh
```

### 涉及文件

- `sql/migrations/007_add_outbox_events.sql`
- `src/go/internal/repository/postgres_outbox.go`
- `src/go/internal/api/handler.go`
- `src/go/internal/api/stream_enqueue.go`
- `src/go/internal/api/outbox_dispatcher.go`
- `src/go/internal/api/outbox_dispatcher_test.go`
- `src/go/internal/api/metrics.go`
- `src/go/internal/api/env.go`
- `src/go/cmd/api/main.go`
- `scripts/verify_d1_outbox.sh`
- `DESIGN.md`
- `RUNBOOK.md`
