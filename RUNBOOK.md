# Deep-OJ RUNBOOK（A3）

## 0. 快速开始（One-shot）

最短路径：使用 `docker compose` 启动全栈并直接跑端到端脚本。

```bash
cd /home/diguo/Deep_OJ

export JWT_SECRET='deepoj_dev_jwt_secret_change_me'
export ADMIN_USERS='admin'
export REDIS_PASSWORD='deepoj_redis_change_me'
export POSTGRES_PASSWORD='deepoj_pg_password_change_me'
export MINIO_ROOT_USER='deepoj_minio_user'
export MINIO_ROOT_PASSWORD='deepoj_minio_password_change_me'

docker compose down -v --remove-orphans
docker compose up -d --build
docker compose ps

python3 tests/integration/test_e2e.py
```

预期：

- `docker compose ps` 中 `api/scheduler/worker/redis/postgres/minio` 为 `Up`
- `test_e2e.py` 输出 `Submitted, Job ID:`，并在轮询后出现 `Result: Accepted`（或可预期判题状态）

### 0.1 无 etcd 启动验证（B1）

系统已移除 etcd 运行时依赖。请使用以下脚本执行完整验收：

```bash
bash scripts/verify_b1_no_etcd.sh
```

### 0.2 无 legacy 数据面验证（B2）

系统已移除 legacy 数据面（Redis List + scheduler gRPC push + 旧回写链路），仅保留 Streams-only：

`API(outbox) -> Redis Stream -> Worker(XREADGROUP) -> DB finalize -> XACK -> XAUTOCLAIM reclaim`

验证命令：

```bash
bash scripts/verify_b2_no_legacy_dataplane.sh
```

### 0.3 B3 控制面 scheduler 验证

scheduler 已收敛为纯控制面，只保留两类循环：

- `RepairLoop`：扫描 DB 中需要补投递的任务并 `XADD` 到 `deepoj:jobs`
- `GcLoop`：执行 `XTRIM` 与 DB 清理计划/清理（默认 dry-run）

验证命令：

```bash
bash scripts/verify_b3_control_plane.sh
```

开启方式（建议优先环境变量覆盖）：

```bash
SCHEDULER_REPAIR_ENABLED=true \
SCHEDULER_REPAIR_INTERVAL_MS=1000 \
docker compose up -d scheduler
```

关键日志关键字：

- `event=repair_xadd`
- `event=stream_trim`
- `event=db_gc_plan`

关键指标：

- `scheduler_repair_total{result,reason}`
- `scheduler_stream_trim_total{result}`
- `scheduler_db_gc_total{result,reason}`

## 1. 环境与依赖

最低要求（生产建议高于此基线）：

- OS：Linux x86_64（需要 cgroup/namespace 能力）
- Docker Engine：24+（建议）
- Docker Compose Plugin：v2+
- Python：3.9+（用于 `tests/integration/test_e2e.py`）
- `curl`、`rg`（排障与观测命令）

本地源码构建（非 Docker）额外要求：

- Go：1.24+（见 `src/go/go.mod`）
- CMake：3.20+（见 `CMakeLists.txt`）
- C++ 编译器：支持 C++20（`g++`/`clang++`）
- 运行时中间件：
- Redis（兼容 compose 中 `redis:alpine`）
- PostgreSQL 15（compose 使用 `postgres:15-alpine`）
- MinIO（项目使用对象存储下载测试数据；非可选于完整判题链路）

### 1.1 本地运行 judge 并查看 JSON 输出

```bash
cd /home/diguo/Deep_OJ
cmake -S . -B build
cmake --build build -j

./build/judge_engine --self_test --job_id demo_job --attempt_id 0
```

可选：运行 Go 测试时指定 judge 路径：

```bash
export JUDGE_BIN=/home/diguo/Deep_OJ/build/judge_engine
cd /home/diguo/Deep_OJ/src/go && go test ./internal/worker -run TestJudgeSelfTest -v
```

配置读取语义：

- 进程运行参数以环境变量为准。
- `cmd/api` 与 `cmd/worker` 会读取 `config.yaml` 并仅在“环境变量未设置”时应用缺省值。

## 2. 服务清单与端口

| 组件 | 容器名 | 容器内端口 | 宿主机端口 | 健康检查方式 |
| --- | --- | --- | --- | --- |
| API | `oj-api` | `18080` | `18080` | `curl -fsS http://127.0.0.1:18080/api/v1/health` |
| Scheduler | `oj-scheduler` | `9091`(metrics) | 无映射 | `docker compose logs --tail=100 scheduler`（应看到 `控制面模式` 相关日志）；metrics 见第 5 节 |
| Worker | `oj-worker` | `9092`(metrics) | 无映射 | `docker compose logs --tail=100 worker`（应看到 `工作节点流消费器` 已启动） |
| Redis | `oj-redis` | `6379` | 无映射 | `docker exec -it oj-redis redis-cli -a "$REDIS_PASSWORD" PING` |
| PostgreSQL | `oj-postgres` | `5432` | 无映射 | `docker exec -it oj-postgres pg_isready -U deep_oj -d deep_oj` |
| MinIO | `oj-minio` | `9000`,`9001` | 无映射 | `docker run --rm --network deep-oj-net curlimages/curl:8.7.1 -fsS http://oj-minio:9000/minio/health/live` |
| Prometheus（外部） | N/A | 默认 `9090` | 视部署而定 | 本仓库未内置服务；外部 Prometheus 抓取 API/Scheduler/Worker metrics |

备注：

- 当前 `docker-compose.yml` 未暴露 `scheduler:9091`、`worker:9092` 到宿主机；主机侧排查建议通过 `docker compose logs` 或同网络临时容器访问。
- API `/metrics` 默认仅允许 loopback 访问（若未配置 `METRICS_TOKEN`）。

## 3. 端到端验收（提交 -> 执行 -> 查询结果）

下面是完整手工链路（`curl` + `python3` 解析 JSON），与当前仓库路由一致。

### 3.1 准备环境

```bash
cd /home/diguo/Deep_OJ
export API_BASE='http://127.0.0.1:18080'
export JWT_SECRET='deepoj_dev_jwt_secret_change_me'
export ADMIN_USERS='admin'
export REDIS_PASSWORD='deepoj_redis_change_me'
export POSTGRES_PASSWORD='deepoj_pg_password_change_me'
export MINIO_ROOT_USER='deepoj_minio_user'
export MINIO_ROOT_PASSWORD='deepoj_minio_password_change_me'

docker compose up -d --build
curl -fsS "$API_BASE/api/v1/health"
```

### 3.2 注册并登录

```bash
curl -sS -X POST "$API_BASE/api/v1/auth/register" \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"password","email":"admin@example.com"}' || true

TOKEN="$(curl -sS -X POST "$API_BASE/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"password"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')"

echo "TOKEN acquired: ${TOKEN:0:16}..."
```

### 3.3 创建题目并上传测试数据

```bash
python3 - <<'PY'
import zipfile
with zipfile.ZipFile("problem.zip", "w") as zf:
    zf.writestr("1.in", "1 2\n")
    zf.writestr("1.out", "3\n")
print("problem.zip generated")
PY

PROBLEM_ID="$(curl -sS -X POST "$API_BASE/api/v1/problems" \
  -H "Authorization: Bearer $TOKEN" \
  -F "title=A+B Problem" \
  -F "time_limit=1000" \
  -F "memory_limit=128" \
  -F "file=@problem.zip" \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["id"])')"

echo "PROBLEM_ID=$PROBLEM_ID"
```

### 3.4 提交代码

```bash
JOB_ID="$(curl -sS -X POST "$API_BASE/api/v1/submit" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d "$(cat <<JSON
{
  "problem_id": $PROBLEM_ID,
  "language": 1,
  "code": "#include <iostream>\nint main(){int a,b;std::cin>>a>>b;std::cout<<(a+b);return 0;}",
  "time_limit": 1000,
  "memory_limit": 128
}
JSON
)" | python3 -c 'import sys,json;print(json.load(sys.stdin)["job_id"])')"

echo "JOB_ID=$JOB_ID"
```

### 3.5 轮询结果

```bash
for i in $(seq 1 30); do
  RESP="$(curl -sS -H "Authorization: Bearer $TOKEN" "$API_BASE/api/v1/status/$JOB_ID")"
  STATUS="$(echo "$RESP" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("status",""))')"
  echo "[$i] status=$STATUS"
  if [ "$STATUS" = "Finished" ] || [ "$STATUS" = "Accepted" ] || [ "$STATUS" = "Wrong Answer" ]; then
    echo "$RESP"
    break
  fi
  sleep 1
done
```

## 4. 常见故障排查（现象 -> 可能原因 -> 定位命令 -> 解决方案）

| 现象 | 可能原因 | 定位命令 | 解决方案 |
| --- | --- | --- | --- |
| Redis 不可达，API 报 `QUEUE_ERROR` | `REDIS_PASSWORD` 不一致、Redis 容器未启动、网络不可达 | `docker compose ps redis`; `docker exec -it oj-redis redis-cli -a "$REDIS_PASSWORD" PING`; `docker compose logs --tail=200 api` | 统一 `REDIS_PASSWORD`，重启 `redis/api/scheduler/worker`，必要时 `docker compose up -d` |
| DB 连接池耗尽，接口变慢/5xx | `PG_MAX_CONNS` 太小、长事务、慢 SQL | `docker compose logs --tail=300 api | rg -i "database|timeout|too many"`; `docker exec -it oj-postgres psql -U deep_oj -d deep_oj -c "select * from pg_stat_activity;"` | 增大 `PG_MAX_CONNS`，排查慢查询，缩短事务并重启 API |
| Stream backlog 持续增长（`deepoj:jobs`） | Worker 消费异常、下游执行慢、Redis 压力高 | `docker exec -it oj-redis redis-cli -a "$REDIS_PASSWORD" XLEN deepoj:jobs`; `docker exec -it oj-redis redis-cli -a "$REDIS_PASSWORD" XPENDING deepoj:jobs deepoj:workers`; `docker compose logs --tail=200 worker` | 优先恢复 worker 消费能力，再按资源瓶颈扩容 worker 或限流 |
| Worker 不消费任务 | Worker 启动失败、流消费组配置错误、DB/Redis 依赖异常 | `docker compose logs --tail=200 worker`; `docker exec -it oj-redis redis-cli -a "$REDIS_PASSWORD" XINFO GROUPS deepoj:jobs` | 修复 `JOB_STREAM_*` 与依赖配置，确认消费组存在并可读写，重启 worker |
| Judge 出现超时/TLE 异常增多 | 用户代码死循环、时间限制过低、机器负载过高 | `docker compose logs --tail=300 worker | rg -i "Time Limit|timed out|command timed out"` | 调整题目时限、检查资源压力、必要时横向扩容 worker |
| Judge 出现 OOM/MLE 异常增多 | 代码内存占用过大、内存限制过低、容器内存紧张 | `docker compose logs --tail=300 worker | rg -i "Memory Limit|OOM|killed"` | 调整内存上限，检查宿主机内存，限制异常任务并重试 |
| 出现僵尸进程或残留判题进程 | kill/cleanup 未完整执行、worker 异常中断 | `docker exec -it oj-worker ps -eo pid,ppid,stat,cmd | rg "judge_engine| Z "` | 重启 worker 清理现场；后续按任务 G1/G2 加强进程组 kill 与幂等清理 |
| stdout/stderr 大输出导致任务卡死 | 子进程输出过大导致 pipe 满；或 drain/截断异常 | `docker compose logs --tail=400 worker | rg -i "truncated_stdout|truncated_stderr|pipe|timeout|Wait"` | 检查日志字段 `truncated_stdout/truncated_stderr` 与 `stdout_len/stderr_len`；必要时调整 `JUDGE_STDOUT_LIMIT_BYTES`/`JUDGE_STDERR_LIMIT_BYTES` |
| cgroup 清理失败或权限报错 | 宿主机未启用 cgroup v2、挂载权限不足 | `docker compose logs --tail=300 worker | rg -i "cgroup|permission|cleanup"`; `test -f /sys/fs/cgroup/cgroup.controllers && echo ok` | 启用 cgroup v2，保留 compose 中 `/sys/fs/cgroup` 挂载与必要 capabilities |
| metrics 维度爆炸（高基数） | 错误新增了 `job_id` 等高基数 label | `cd src/go && go test ./internal/survey -v`; `rg -n '"job_id"' src/go/internal/*/metrics.go` | 移除高基数 label，仅保留低基数字段；用日志追 job，不在 metrics 打 `job_id` |
| 时钟漂移导致超时/重试判断异常 | 宿主机或容器时间不同步 | `date -u`; `docker exec -it oj-api date -u`; `docker exec -it oj-worker date -u` | 统一 NTP，同步宿主机时间，避免跨节点时间偏差 |
| 磁盘写爆（workspace/日志膨胀） | `data/workspace`、日志、Docker 卷膨胀 | `df -h`; `du -sh data/workspace`; `docker system df` | 清理历史工作目录和无用镜像卷，设置日志轮转与容量阈值 |
| MinIO 拉取失败（Worker 下载 testcase 失败） | MinIO 不可达、凭证错误、对象缺失 | `docker compose logs --tail=300 worker | rg -i "minio|download|Prepare testcases failed"`; `docker run --rm --network deep-oj-net curlimages/curl:8.7.1 -fsS http://oj-minio:9000/minio/health/live` | 校验 `MINIO_*` 环境变量、确认对象存在、恢复 MinIO 后重试任务 |
| API `/metrics` 返回 403/401 | 未配置 `METRICS_TOKEN` 时非 loopback 访问；或 token 错误 | `curl -i http://127.0.0.1:18080/metrics`; `docker compose logs --tail=100 api | rg -i "forbidden|unauthorized"` | 本机 loopback 抓取，或设置 `METRICS_TOKEN` 并带 `Authorization: Bearer <token>` |

## 4.1 Redis Streams 验证（C1/C4）

```bash
STREAM_KEY="${JOB_STREAM_KEY:-deepoj:jobs}"
MAXLEN="${JOB_STREAM_MAXLEN:-200000}"
docker exec -it oj-redis redis-cli -a "$REDIS_PASSWORD" XRANGE "$STREAM_KEY" - + COUNT 5
docker exec -it oj-redis redis-cli -a "$REDIS_PASSWORD" XLEN "$STREAM_KEY"
docker exec -it oj-redis redis-cli -a "$REDIS_PASSWORD" XINFO STREAM "$STREAM_KEY"
docker exec -it oj-redis redis-cli -a "$REDIS_PASSWORD" XINFO GROUPS "$STREAM_KEY"
docker exec -it oj-redis redis-cli -a "$REDIS_PASSWORD" XPENDING "$STREAM_KEY" "${JOB_STREAM_GROUP:-deepoj:workers}"
docker exec -it oj-redis redis-cli -a "$REDIS_PASSWORD" XPENDING "$STREAM_KEY" "${JOB_STREAM_GROUP:-deepoj:workers}" - + 10
# 谨慎使用：会把超时 pending entry 转移到当前 consumer（用于 C4 reclaim 排障）
MIN_IDLE_MS=$(( (${JOB_LEASE_SEC:-60} + ${JOB_RECLAIM_GRACE_SEC:-15}) * 1000 ))
docker exec -it oj-redis redis-cli -a "$REDIS_PASSWORD" XAUTOCLAIM "$STREAM_KEY" "${JOB_STREAM_GROUP:-deepoj:workers}" "${JOB_STREAM_CONSUMER:-debug-consumer}" "$MIN_IDLE_MS" 0-0 COUNT 10
# 手动补救清理（幂等）：按近似长度裁剪
docker exec -it oj-redis redis-cli -a "$REDIS_PASSWORD" XTRIM "$STREAM_KEY" MAXLEN "~" "$MAXLEN"
```

一键校验脚本（提交一次 job 并检查 `job_id/enqueue_ts/payload_ref/priority` 与 payload schema）：

```bash
PROBLEM_ID=<problem_id> REDIS_PASSWORD=<redis_password> \
  bash scripts/verify_c1_stream_enqueue.sh
```

粗算 backlog/lag（运维口径）：

- `XLEN <stream>`：流总长度（近似“待处理积压规模”上界）。
- `XINFO GROUPS <stream>`：
- 关注 `pending`（PEL 未确认消息数）与 `lag`（组相对尾部落后量，Redis 新版本可用）。
- `XPENDING <stream> <group>`：查看 PEL 概况（总 pending/最小和最大消息 ID/每个 consumer 的 pending 分布）。
- `XPENDING <stream> <group> - + 10`：查看具体卡住消息和所属 consumer（定位单个卡住任务）。
- `XAUTOCLAIM <stream> <group> <consumer> <min-idle-ms> 0-0 COUNT N`：手动观察 reclaim 候选 entry（注意该命令会变更 entry 所属 consumer）。
- Worker 已启用 C4 reclaim loop：周期执行 `XAUTOCLAIM`，并使用 `next_start_id` 游标跨轮推进；仅在 DB reclaim CAS 成功后才执行并最终 `XACK`。
- `min-idle-ms` 建议不低于 `(lease_sec + reclaim_grace_sec) * 1000`，避免误抢占仍在心跳窗口内的任务。
- C6 默认在 API 入队时使用 `XADD ... MAXLEN ~ <JOB_STREAM_MAXLEN>`（默认 `200000`）。

何时需要调大 `JOB_STREAM_MAXLEN`：

- `XLEN` 长时间接近 `JOB_STREAM_MAXLEN`；
- `XINFO GROUPS` 中 `pending/lag` 持续上升；
- `XPENDING` 显示大量未确认消息且无法快速回落。

建议将 `JOB_STREAM_MAXLEN` 设置为明显大于峰值 backlog 的数量级，并结合实际入队 QPS 按“保留窗口”估算：
- `window_seconds ≈ maxlen / enqueue_qps`
- consumer 名称优先 `JOB_STREAM_CONSUMER`，否则复用 `WORKER_ID`，再回退 `hostname-pid`。重启后产生新 consumer 属预期，旧 consumer 的 PEL 在 C4 处理。

## 4.2 Outbox 验证与排障（D1）

默认行为（`OUTBOX_ENABLED=true`）：

- API 提交成功仅依赖 DB 事务（`submissions + outbox_events`）；
- Redis 暂不可用时，outbox 会保留 `pending`，待 dispatcher 自动补投递。

验证命令（容器部署）：

```bash
STREAM_KEY="${JOB_STREAM_KEY:-deepoj:jobs}"
POSTGRES_DB="${POSTGRES_DB:-deep_oj}"
POSTGRES_USER="${POSTGRES_USER:-deep_oj}"

# 查看 outbox pending 数与最近错误
docker exec -e PGPASSWORD="$POSTGRES_PASSWORD" -it oj-postgres \
  psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" \
  -c "SELECT status, COUNT(*) FROM outbox_events GROUP BY status ORDER BY status;"
docker exec -e PGPASSWORD="$POSTGRES_PASSWORD" -it oj-postgres \
  psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" \
  -c "SELECT id, job_id, attempts, next_attempt_at, last_error, dispatched_at, stream_entry_id FROM outbox_events ORDER BY id DESC LIMIT 20;"

# 查看 stream 是否收到补投递消息
docker exec -it oj-redis redis-cli -a "$REDIS_PASSWORD" XRANGE "$STREAM_KEY" - + COUNT 10
```

Redis down 场景预期：

1. 停 Redis 后提交：API 仍返回 200 + `job_id`。
2. `outbox_events` 出现 `pending` 行，`attempts` 递增，`last_error` 记录 Redis 失败原因。
3. Redis 恢复后，`dispatched_at/stream_entry_id` 被回填，`XRANGE` 可见对应 `job_id`。

可复现脚本：

```bash
bash scripts/verify_d1_outbox.sh
```

迁移与回滚（手动）：

```bash
# 应用迁移（新环境随 sql/migrations 自动初始化）
# 对已运行环境手动执行：
docker exec -i -e PGPASSWORD="$POSTGRES_PASSWORD" oj-postgres \
  psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" < sql/migrations/007_add_outbox_events.sql

# 回滚
docker exec -e PGPASSWORD="$POSTGRES_PASSWORD" -it oj-postgres \
  psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" \
  -c "DROP INDEX IF EXISTS idx_outbox_pending_retry; DROP INDEX IF EXISTS idx_outbox_job_id; DROP TABLE IF EXISTS outbox_events;"
```

## 4.3 Streams 压力快照（D2）

该快照用于 D3 背压判断输入。采集命令：

```bash
cd /home/diguo/Deep_OJ
STREAM_KEY="${JOB_STREAM_KEY:-deepoj:jobs}" \
GROUP="${JOB_STREAM_GROUP:-deepoj:workers}" \
REDIS_PASSWORD="${REDIS_PASSWORD}" \
bash scripts/collect_stream_pressure.sh
```

输出（单行 JSON）字段：
- `xlen`：`XLEN` 结果
- `pending`：`XPENDING summary` count
- `lag` / `lag_valid`：`XINFO GROUPS lag`（允许 `null`，`null != 0`）
- `oldest_age_ms` + `oldest_age_source`：优先 pending oldest，其次 backlog oldest，最后 none
- `approx`：是否使用近似（backlog 推断或 stream id 兜底）

示例：

```json
{"ts_ms":1771090570165,"stream_key":"deepoj:jobs","group":"deepoj:workers","xlen":30,"pending":10,"lag":20,"lag_valid":true,"oldest_age_ms":130306,"oldest_age_source":"pending","approx":false}
```

当 `lag=null` 时的排障步骤（不要按 0 处理）：

```bash
STREAM_KEY="${JOB_STREAM_KEY:-deepoj:jobs}"
GROUP="${JOB_STREAM_GROUP:-deepoj:workers}"

docker exec -it oj-redis redis-cli -a "$REDIS_PASSWORD" XINFO GROUPS "$STREAM_KEY"
docker exec -it oj-redis redis-cli -a "$REDIS_PASSWORD" XPENDING "$STREAM_KEY" "$GROUP"
docker exec -it oj-redis redis-cli -a "$REDIS_PASSWORD" XPENDING "$STREAM_KEY" "$GROUP" - + 10
docker exec -it oj-redis redis-cli -a "$REDIS_PASSWORD" XINFO STREAM "$STREAM_KEY"
```

判读建议：
- `lag_valid=true` 且 `lag` 持续升高：消费跟不上入队。
- `pending` 与 `oldest_age_ms` 同升：存在卡单，优先查 DB 慢查询、judge 时延、reclaim。
- `oldest_age_source=backlog` 且 `approx=true`：说明当前没有 pending，但 backlog 正在累积。
- `xlen` 长期贴近 `JOB_STREAM_MAXLEN`：评估调大保留窗口或削峰。

## 5. 可观测性

### 5.1 指标入口

- API：`http://127.0.0.1:18080/metrics`（默认仅 loopback 可访问）
- Scheduler：容器内 `:9091/metrics`（compose 未映射宿主机）
- Worker：容器内 `:9092/metrics`（compose 未映射宿主机）

容器网络内抓取示例：

```bash
docker run --rm --network deep-oj-net curlimages/curl:8.7.1 -sS http://oj-scheduler:9091/metrics | head
docker run --rm --network deep-oj-net curlimages/curl:8.7.1 -sS http://oj-worker:9092/metrics | head
```

### 5.2 关键指标列表（禁止 `job_id` label）

- API：`http_requests_total`、`http_request_duration_seconds`、`submission_total`
- Scheduler：`control_plane_only`、`legacy_loops_started`、`scheduler_active_workers`、`scheduler_repair_total{result,reason}`、`scheduler_stream_trim_total{result}`、`scheduler_db_gc_total{result,reason}`
- Worker：`worker_task_total`、`worker_task_duration_seconds`、`worker_compile_duration_seconds`、`worker_download_duration_seconds`、`worker_unzip_duration_seconds`
- Worker（Judge 执行链路）：`judge_exec_duration_seconds{result}`、`judge_exec_inflight`、`judge_exec_total{result}`、`judge_verdict_total{verdict}`、`judge_protocol_errors_total{reason}`、`judge_output_truncated_total{stream}`
- Worker（Streams 消费链路）：`worker_stream_consume_total{status,reason}`、`worker_stream_consume_latency_ms`、`worker_stream_inflight`
- Worker（C3 claim/lease/fencing）：`worker_claim_total{status,reason}`、`worker_lease_heartbeat_total{status,reason}`、`worker_stale_attempt_total`
- Worker（C4 reclaim）：`worker_reclaim_total{status,reason}`、`worker_reclaim_latency_ms`、`worker_reclaim_inflight`

约束：

- Metrics 标签禁止使用 `job_id`（高基数）。
- 单个任务追踪使用日志字段，不在指标上挂高基数 ID。

### 5.3 日志字段规范与单 Job 定位

建议日志字段：

- `job_id`
- `attempt_id`
- `stream_entry_id`
- `group`
- `consumer`
- `trace_id`
- `request_id`（API 入口）

定位单个 job：

```bash
JOB_ID="<your_job_id>"
docker compose logs --since=30m api scheduler worker | rg "$JOB_ID|trace_id|attempt_id"
```

通过 trace_id 串联日志：

```bash
TRACE_ID="<trace_id>"
docker compose logs --since=30m worker | rg "$TRACE_ID"
```

## 6. 探针脚本

使用脚本：`scripts/repo_survey_probe.sh`

### 6.1 用法

```bash
bash scripts/repo_survey_probe.sh --help
bash scripts/repo_survey_probe.sh
```

可配置参数（环境变量）：

- `REDIS_URL`（默认 `redis://localhost:6379/0`）
- `DATABASE_URL`（默认 `postgres://postgres:postgres@localhost:5432/deepoj?sslmode=disable`）
- `API_METRICS_URL`（默认 `http://localhost:8080/metrics`）
- `SCHEDULER_METRICS_URL`（默认 `http://localhost:9091/metrics`）
- `WORKER_METRICS_URL`（默认 `http://localhost:9092/metrics`）

### 6.2 输出解释

- `[1/3]`：输出 `deepoj:jobs` 的 `XLEN`
- `[2/3]`：输出 PG 表列表与 `submissions` 字段定义
- `[3/3]`：输出核心 metrics 名称匹配结果

### 6.3 失败处理

- Redis 失败：优先检查 URL/密码、容器网络与 `redis-cli` 连通性
- DB 失败：校验 `DATABASE_URL` 用户密码与数据库名
- Metrics 失败：确认目标端口是否已暴露；未暴露时改用容器网络内探测

## 7. 安全与运行边界（简版）

当前边界与前提：

- Worker 依赖 Linux 内核能力（cgroup/namespace/seccomp 相关）
- `docker-compose.yml` 为 worker 配置了 `cap_add` 与 `/sys/fs/cgroup` 挂载
- API `/metrics` 有访问控制（loopback 或 `METRICS_TOKEN`）

运行前提检查（建议）：

- 宿主机启用 cgroup v2
- 最小化容器权限（当前为可运行优先，仍有收敛空间）
- 明确生产环境 secrets 管理（`JWT_SECRET`、`REDIS_PASSWORD`、`POSTGRES_PASSWORD`、`MINIO_*`）

TODO（后续任务收敛）：

- 收敛 worker 最小 capabilities（避免过宽权限）
- 完整固化 seccomp/cgroup 策略与降级路径
- 将 kill/清理幂等规则与故障演练流程写入发布前检查
