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
export WORKER_AUTH_TOKEN='deepoj_worker_token_change_me'

docker compose down -v --remove-orphans
docker compose up -d --build
docker compose ps

python3 tests/integration/test_e2e.py
```

预期：

- `docker compose ps` 中 `api/scheduler/worker/redis/postgres/minio/etcd` 为 `Up`
- `test_e2e.py` 输出 `Submitted, Job ID:`，并在轮询后出现 `Result: Accepted`（或可预期判题状态）

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
- Etcd 3.5（Worker 服务发现）

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
| Scheduler | `oj-scheduler` | `9091`(metrics), `50052`(预留) | `50052` | `docker compose logs --tail=100 scheduler`（应看到 `Scheduler started`）；metrics 见第 5 节 |
| Worker | `oj-worker` | `50051`(gRPC), `9092`(metrics) | 无映射 | `docker compose logs --tail=100 worker`（应看到 `gRPC server listening`） |
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
export WORKER_AUTH_TOKEN='deepoj_worker_token_change_me'

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
| 队列堆积（`queue:pending` 持续增长） | Worker 不可用、Scheduler 调度失败、下游执行慢 | `docker exec -it oj-redis redis-cli -a "$REDIS_PASSWORD" LLEN queue:pending`; `docker compose logs --tail=200 scheduler worker` | 先恢复 worker 可用性，再根据 CPU/内存扩容 worker 或降低提交速率 |
| Worker 不消费任务 | Etcd 注册失败、gRPC 不可达、worker 启动失败 | `docker compose logs --tail=200 worker`; `docker compose logs --tail=200 scheduler | rg -i "No workers|dispatch"` | 修复 `ETCD_ENDPOINTS/WORKER_ADDR`，确认 `oj-etcd` 可达，重启 worker/scheduler |
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

## 4.1 Redis Streams 验证（C1）

```bash
STREAM_KEY="${JOB_STREAM_KEY:-deepoj:jobs}"
docker exec -it oj-redis redis-cli -a "$REDIS_PASSWORD" XRANGE "$STREAM_KEY" - + COUNT 5
docker exec -it oj-redis redis-cli -a "$REDIS_PASSWORD" XINFO STREAM "$STREAM_KEY"
docker exec -it oj-redis redis-cli -a "$REDIS_PASSWORD" XINFO GROUPS "$STREAM_KEY"
docker exec -it oj-redis redis-cli -a "$REDIS_PASSWORD" XPENDING "$STREAM_KEY" "${JOB_STREAM_GROUP:-deepoj:workers}"
docker exec -it oj-redis redis-cli -a "$REDIS_PASSWORD" XPENDING "$STREAM_KEY" "${JOB_STREAM_GROUP:-deepoj:workers}" - + 10
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
- 当前仅消费 `>` 新消息，尚未引入自动 reclaim；PEL reclaim 会在 C4 引入。
- consumer 名称优先 `JOB_STREAM_CONSUMER`，否则复用 `WORKER_ID`，再回退 `hostname-pid`。重启后产生新 consumer 属预期，旧 consumer 的 PEL 在 C4 处理。

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
- Scheduler：`scheduler_queue_depth`、`scheduler_active_workers`、`submission_result_total`、`job_latency_seconds`
- Worker：`worker_task_total`、`worker_task_duration_seconds`、`worker_compile_duration_seconds`、`worker_download_duration_seconds`、`worker_unzip_duration_seconds`
- Worker（Judge 执行链路）：`judge_exec_duration_seconds{result}`、`judge_exec_inflight`、`judge_exec_total{result}`、`judge_verdict_total{verdict}`、`judge_protocol_errors_total{reason}`、`judge_output_truncated_total{stream}`
- Worker（Streams 消费链路）：`worker_stream_consume_total{status,reason}`、`worker_stream_consume_latency_ms`、`worker_stream_inflight`
- Worker（C3 claim/lease/fencing）：`worker_claim_total{status,reason}`、`worker_lease_heartbeat_total{status,reason}`、`worker_stale_attempt_total`

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

- `[1/3]`：输出 `queue:pending` / `queue:processing` / `stream:results` 长度
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
