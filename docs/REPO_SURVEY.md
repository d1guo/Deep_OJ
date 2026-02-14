# Deep-OJ 仓库侦察报告（A1）

更新时间：2026-02-14  
范围：当前主干代码（Go API/Scheduler/Worker + SQL migrations + docs）

## 1. 调研方法与范围

使用的命令（可复现）：

```bash
rg -n "BRPopLPush|XReadGroup|XAck|XAdd|queue:|stream:|processing" src/go -g'*.go'
rg -n "CREATE TABLE|ALTER TABLE|FOREIGN KEY|INDEX" sql/migrations -g'*.sql'
rg -n "prometheus|CounterVec|Histogram|Gauge|slog" src/go -g'*.go'
```

重点代码路径：

- API 提交入口：`src/go/internal/api/handler.go`
- Scheduler 主循环：`src/go/cmd/scheduler/main.go`
- 结果确认：`src/go/internal/scheduler/ack_listener.go`
- Worker 执行与回传：`src/go/internal/worker/judge.go`
- DB 访问：`src/go/internal/repository/postgres.go`
- Redis 访问：`src/go/internal/repository/redis.go`

## 2. 队列实现现状

### 2.1 Redis key/stream 现状

- List 主队列：`queue:pending`
- List 处理中队列：`queue:processing`
- List 死信队列：`queue:dead`
- Result cache：`result:{job_id}`
- Result stream：`stream:results`（group: `results-group`）
- 任务辅助键：`task:worker:{job_id}`、`task:payload:{job_id}`、`task:processing_start:{job_id}`、`task:processing:zset`

### 2.2 执行链路（当前）

1. API 在 `src/go/internal/api/handler.go` 写 DB `submissions`（`state=pending`）后，`LPush queue:pending`。  
2. Scheduler 在 `src/go/cmd/scheduler/main.go` 用 `BRPopLPush(queue:pending -> queue:processing)` 拉取任务。  
3. Scheduler 通过 gRPC 调用 Worker，记录 assignment/payload/processing_start/zset。  
4. Worker 在 `src/go/internal/worker/judge.go` 执行后：`SETNX result:{job_id}` + `XADD stream:results`。  
5. ACK Listener 在 `src/go/internal/scheduler/ack_listener.go` 用 `XReadGroup` 消费 `stream:results`，更新 DB 后 `XAck`。  
6. ACK 成功后清理 `queue:processing` 对应元素与辅助键。  

结论：当前是“List 作为任务主队列 + Stream 作为结果回传”，尚未切换到“Streams + Consumer Group 作为主任务队列”。

## 3. PostgreSQL Schema 现状

迁移文件：

- `sql/migrations/001_init.sql`：`submissions` 基表
- `sql/migrations/002_add_problems_table.sql`：`problems`
- `sql/migrations/003_add_users_table.sql`：`users`
- `sql/migrations/004_add_submission_state_and_indexes.sql`：新增 `submissions.state`
- `sql/migrations/005_add_submission_foreign_keys.sql`：`submissions -> problems/users` FK

当前核心表：`submissions`

- 主键：`id BIGSERIAL`
- 业务键：`job_id VARCHAR(64) UNIQUE`
- 判题字段：`status VARCHAR(32)`, `state TEXT`, `result JSONB`
- 资源限制字段：`time_limit`, `memory_limit`
- 关联字段：`problem_id`, `user_id`

当前完成写入口径（代码现状）：

- `src/go/internal/repository/postgres.go` 使用  
  `UPDATE submissions SET ... state='done' WHERE job_id=$1 AND state != 'done'`
- 该实现有基本幂等能力，但尚未引入 `attempt_id` / `RUNNING` CAS fencing（后续任务处理）。

## 4. Worker 执行链路现状

入口与流程：

- gRPC 入口：`src/go/cmd/worker/main.go` 注册 `JudgeService.ExecuteTask`
- 任务执行：`src/go/internal/worker/judge.go`
- 执行器调用 C++：`src/go/internal/worker/executor.go`

关键步骤：

1. `ExecuteTask` 先做并发信号量限流（`PoolSize`）。  
2. 校验 `job_id` 白名单格式（正则）并准备 testcase。  
3. 调 C++ core 编译；逐测试点执行并比对输出。  
4. 汇总结果后 `reportResult`：写 `result:{job_id}`，再写 `stream:results`。  
5. 最后触发 cleanup（失败仅 warn，不中断主流程）。  

## 5. 可观测性现状

Metrics 文件：

- API：`src/go/internal/api/metrics.go`
- Scheduler：`src/go/internal/scheduler/metrics.go`
- Worker：`src/go/internal/worker/metrics.go`

已确认的标签：

- API：`method/path/status`、`language/status`
- Scheduler：`queue`、`status/language`
- Worker：`status`

现状结论：

- 指标定义中未使用 `job_id` label（符合“metrics 禁止 job_id label”约束）。
- 日志广泛使用 `slog`，链路关键日志带 `job_id` 和 `trace_id`（如 `handler.go`, `main.go`, `judge.go`）。
- 当前尚未看到统一日志采样/限流层；后续任务可补齐。

## 6. 现场探针脚本

已提供可复现脚本：`scripts/repo_survey_probe.sh`

用途：

- 采样 `queue:pending` / `queue:processing` / `stream:results` 深度
- 打印 PostgreSQL 表与 `submissions` 列定义
- 抽样 API/Scheduler/Worker `/metrics`
