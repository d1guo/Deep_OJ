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
