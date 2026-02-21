# REPO_SURVEY（B2 后）

## 1. 调研方法与范围

- 代码检索：`rg -n "XADD|XREADGROUP|XACK|XAUTOCLAIM|outbox|stream" src/go scripts docs`
- 关键入口：
  - `src/go/internal/api/outbox_dispatcher.go`
  - `src/go/internal/worker/stream_consumer.go`
  - `src/go/cmd/scheduler/main.go`
- 只覆盖当前主链路：API(outbox) -> Stream -> Worker -> DB finalize -> XACK/reclaim。

## 2. 队列实现现状

- 当前唯一数据面是 Redis Stream：`deepoj:jobs`。
- API 端通过 outbox 分发器把事件写入 `deepoj:jobs`：`src/go/internal/api/outbox_dispatcher.go`。
- Worker 端通过消费组读取：`src/go/internal/worker/stream_consumer.go`（`XREADGROUP` + `XAUTOCLAIM`）。
- Scheduler 已不承载派单数据面，只保留控制面与指标：`src/go/cmd/scheduler/main.go`、`src/go/internal/scheduler/metrics.go`。

## 3. PostgreSQL Schema 现状

- 基础表结构来源：`sql/migrations/001_init.sql`。
- Outbox 表结构来源：`sql/migrations/007_add_outbox_events.sql`。
- 提交流程主仓储：`src/go/internal/repository/postgres.go`。
- Outbox 仓储：`src/go/internal/repository/postgres_outbox.go`。
- 关键表：`submissions`、`outbox_events`。

## 4. Worker 执行链路现状

- 流消息读取与 claim/reclaim：`src/go/internal/worker/stream_consumer.go`。
- 执行器调用链：`src/go/internal/worker/judge.go`。
- 最终写入采用 fenced finalize，成功后 `XACK`；异常由 reclaim 继续处理。

## 5. 可观测性现状

- API 指标定义：`src/go/internal/api/metrics.go`。
- Scheduler 指标定义：`src/go/internal/scheduler/metrics.go`（控制面指标）。
- Worker 指标定义：`src/go/internal/worker/metrics.go`。
- 采样脚本：`scripts/repo_survey_probe.sh`。
