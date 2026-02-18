# Deep-OJ

Deep-OJ 是一个面向在线评测场景的分布式判题系统，采用 Go + C++ 混合实现。

当前代码状态（以仓库现状为准，2026-02-18）已经从“Scheduler 主导数据面”演进到“Worker 直连 Streams 数据面”。

## 架构是否大改了

是，核心执行链路已经明显调整。

| 维度 | 旧链路（历史） | 新链路（当前主线） |
| --- | --- | --- |
| 入队 | API 直接写 Redis List | API 写 DB（`submissions + outbox_events`），由 outbox dispatcher 投递 Redis Stream |
| 消费 | Scheduler `BRPopLPush` 后 gRPC 推给 Worker | Worker 自己 `XREADGROUP` 消费 `deepoj:jobs` |
| 重试/回收 | Scheduler + 队列回推 | Worker `XAUTOCLAIM` reclaim + DB reclaim CAS |
| 最终落库 | Scheduler ACK Listener 回写 DB | Worker 侧按 attempt fencing 写 DB，成功后才 `XACK` |
| 真相源 | Redis + DB 混合语义 | DB 状态机为唯一真相源 |

## Scheduler 现在的作用

Scheduler 目前不再是主数据面的唯一入口，更接近控制面和兼容层：

1. 继续做 Worker 发现相关能力（Etcd watch、活性视角、指标）。
2. 保留 legacy 分发链路（`queue:pending -> queue:processing -> gRPC Dispatch`），用于兼容旧路径或回退。
3. 保留 ACK Listener / Watchdog / Slow Path 等守护逻辑，主要服务于 legacy 语义和运维兜底。
4. 提供 Scheduler 侧 metrics（队列深度、活跃 worker 等）。

结论：主链路已经不再依赖 Scheduler 来“拉任务并分发”，但仓库里仍保留了可运行的 legacy 逻辑，属于过渡期双轨并存。

## 当前主链路架构

```mermaid
flowchart LR
    Client[Client] --> API[API Server]
    API -->|TX: submissions + outbox_events| PG[(PostgreSQL)]
    API -->|Outbox Dispatcher XADD| JOBS[(Redis Stream: deepoj:jobs)]

    JOBS -->|XREADGROUP| Worker[Worker Stream Consumer]
    Worker -->|claim/reclaim/finalize (attempt fencing)| PG
    Worker -->|run judge_engine| Judge[C++ Sandbox]
    Worker -->|SET result:{job_id}| Redis[(Redis)]

    Worker -->|XADD stream:results (兼容)| Results[(Redis Stream: stream:results)]
    Scheduler[Scheduler] -->|XREADGROUP (兼容)| Results
    Scheduler -->|Etcd watch + metrics| Etcd[(Etcd)]
```

## 核心不变量

1. DB 是唯一状态机真相源，Streams 只负责驱动执行。
2. `claim -> DB CAS(RUNNING)` 成功后才能执行判题。
3. 只有 DB finalize 成功后才允许 `XACK`（禁止先 ACK 后落库）。
4. reclaim 使用 `XAUTOCLAIM`，并按业务失败/系统失败分流处理。
5. finalize 使用 attempt fencing，旧 attempt 不允许覆盖新 attempt 结果。

## 组件职责

| 组件 | 主要职责 |
| --- | --- |
| API (`src/go/cmd/api`) | 鉴权、限流、参数校验、写提交、写 outbox |
| Outbox Dispatcher (`src/go/internal/api/outbox_dispatcher.go`) | 扫描 outbox，投递 `deepoj:jobs` |
| Worker (`src/go/cmd/worker`) | 消费 Streams、执行判题、租约心跳、reclaim、fenced finalize |
| Judge Engine (`src/core/worker`) | 编译/运行/资源隔离/结果输出 |
| Scheduler (`src/go/cmd/scheduler`) | 控制面能力 + legacy 兼容逻辑 |
| PostgreSQL | 提交状态机、attempt/lease/fencing、outbox 记录 |
| Redis | Streams、payload 缓存、结果缓存 |
| Etcd | Worker 注册发现 |

## 快速开始

### 1. 依赖

- Linux（推荐 Ubuntu 22.04+）
- Docker + Docker Compose
- Python 3（运行集成脚本）

### 2. 启动

```bash
cd /home/diguo/Deep_OJ

export JWT_SECRET='deepoj_dev_jwt_secret_change_me'
export ADMIN_USERS='admin'
export REDIS_PASSWORD='deepoj_redis_change_me'
export POSTGRES_PASSWORD='deepoj_pg_password_change_me'
export MINIO_ROOT_USER='deepoj_minio_user'
export MINIO_ROOT_PASSWORD='deepoj_minio_password_change_me'
export WORKER_AUTH_TOKEN='deepoj_worker_token_change_me'

docker compose up -d --build
docker compose ps
```

### 3. 端到端验证

```bash
python3 tests/integration/test_e2e.py
```

或使用仓库脚本：

```bash
bash scripts/verify_mvp2_e2e.sh
```

## 常用接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/v1/auth/register` | 注册 |
| POST | `/api/v1/auth/login` | 登录 |
| POST | `/api/v1/problems` | 创建题目（管理员） |
| DELETE | `/api/v1/problems/:id` | 删除题目（管理员） |
| POST | `/api/v1/submit` | 提交代码 |
| GET | `/api/v1/status/:job_id` | 查询状态/结果 |
| GET | `/api/v1/health` | 健康检查 |
| GET | `/metrics` | 指标 |

## 关键配置项

| 配置 | 默认值 | 说明 |
| --- | --- | --- |
| `OUTBOX_ENABLED` | `true` | 是否启用事务 outbox |
| `JOB_STREAM_KEY` | `deepoj:jobs` | 主任务 Stream |
| `JOB_STREAM_GROUP` | `deepoj:workers` | Worker consumer group |
| `JOB_LEASE_SEC` | `60` | 任务租约秒数 |
| `JOB_HEARTBEAT_SEC` | `10` | 租约续期心跳间隔 |
| `JOB_RECLAIM_INTERVAL_SEC` | `5` | reclaim 扫描周期 |
| `JOB_STREAM_MAXLEN` | `200000` | Stream 近似长度上限 |

## 相关文档

- 设计约束与时序：`DESIGN.md`
- 运行排障：`RUNBOOK.md`
- 能力演进与验收证据：`TASK.md`

## License

MIT
