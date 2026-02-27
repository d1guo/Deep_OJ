# Deep-OJ 云上压测单页傻瓜教程（从拿到新服务器到跑 1 小时）

## 0. 先读这一段（30 秒）

你现在只需要看这一份文档：`docs/low_cost_cloud_cluster.md`。

- 当前执行口径：4 角色节点 + 1 小时压测。
- 4 角色节点：`control` / `stateful` / `minio` / `worker`。
- 一机一角色组（不是严格一机一容器）。
- 目标：从 0 到 1 完成部署、健康检查、1 小时压测、worker 扩容观察。

本页所有命令都会标注“在哪台机器执行”。

## 1. 你需要准备什么（开工前）

### 1.1 最低资源建议

- `node-control`：2C4G（API + Scheduler + Prometheus + Grafana）
- `node-stateful`：4C8G（PostgreSQL + Redis）
- `node-minio`：2C4G（MinIO）
- `node-worker-1`：4C8G（常驻 worker）
- 可选扩容：`node-worker-2..4`

### 1.2 机器要求

- OS：Ubuntu（建议 22.04+）
- 你有 sudo 权限
- 所有节点在同一 VPC，可私网互通
- 你可 SSH 到所有节点

### 1.3 一次性变量模板（先填好）

先在你本地管理机准备一份变量清单（后面会一直用）：

```bash
# 在你本地管理机执行（不是服务器）
cat > cluster.env <<'VARS'
CONTROL_IP=10.0.1.10
STATEFUL_IP=10.0.1.11
MINIO_IP=10.0.1.12
WORKER1_IP=10.0.1.21
WORKER2_IP=10.0.1.22
WORKER3_IP=10.0.1.23
WORKER4_IP=10.0.1.24

POSTGRES_PASSWORD=replace_me_pg
REDIS_PASSWORD=replace_me_redis
MINIO_ROOT_USER=deepoj_minio_user
MINIO_ROOT_PASSWORD=replace_me_minio
JWT_SECRET=replace_me_jwt
GRAFANA_ADMIN_USER=admin
GRAFANA_ADMIN_PASSWORD=replace_me_grafana
VARS
```

## 2. 从拿到新服务器开始（A-J）

### Step A：给机器定角色（control/stateful/minio/worker）

这一步在干什么：把每台机器的职责固定，避免后续在错误节点执行命令。  
为什么现在做：角色没定好，后面配置里的 IP 和 compose 文件会全乱。  
在哪台机器执行：在你本地管理机（记录即可）。

复制即用命令：

```bash
# 在你本地管理机执行
source cluster.env
printf "%-16s %s\n" \
  node-control "$CONTROL_IP" \
  node-stateful "$STATEFUL_IP" \
  node-minio "$MINIO_IP" \
  node-worker-1 "$WORKER1_IP" \
  node-worker-2 "$WORKER2_IP" \
  node-worker-3 "$WORKER3_IP" \
  node-worker-4 "$WORKER4_IP"
```

成功标志：你有一张“角色 -> IP”清单。  
失败先查什么：IP 是否重复、是否有空值。

---

### Step B：每台机器安装 Docker + Compose

这一步在干什么：把所有节点统一到同一容器运行时环境。  
为什么现在做：后续所有步骤都依赖 `docker compose`。  
在哪台机器执行：所有节点都执行一次（control/stateful/minio/worker）。

复制即用命令：

```bash
# 在每台服务器执行
sudo apt-get update
sudo apt-get install -y ca-certificates curl gnupg lsb-release

sudo install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg | sudo gpg --dearmor -o /etc/apt/keyrings/docker.gpg
sudo chmod a+r /etc/apt/keyrings/docker.gpg

echo \
  "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu \
  $(. /etc/os-release && echo "$VERSION_CODENAME") stable" | \
  sudo tee /etc/apt/sources.list.d/docker.list >/dev/null

sudo apt-get update
sudo apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin

sudo usermod -aG docker "$USER"
newgrp docker || true

docker --version
docker compose version
```

成功标志：`docker --version` 和 `docker compose version` 有输出。  
失败先查什么：
- `docker: permission denied` -> 重新登录 shell 或 `newgrp docker`
- apt 报错 -> 检查网络和 Docker 源是否添加成功

---

### Step C：每台机器拉代码并初始化 `.env`

这一步在干什么：把模板配置复制到本机可编辑文件。  
为什么现在做：后续配置和启动都在 `~/Deep_OJ/deploy/cluster` 目录。  
在哪台机器执行：所有节点。

复制即用命令：

```bash
# 在每台服务器执行
git clone https://github.com/d1guo/Deep_OJ.git ~/Deep_OJ || true
cd ~/Deep_OJ/deploy/cluster
cp -n .env.example .env
```

把密码写入 `.env`（示例，按你的真实值替换）：

```bash
# 在每台服务器执行（先把变量替换成你自己的）
cd ~/Deep_OJ/deploy/cluster
sed -i 's/^POSTGRES_PASSWORD=.*/POSTGRES_PASSWORD=replace_me_pg/' .env
sed -i 's/^REDIS_PASSWORD=.*/REDIS_PASSWORD=replace_me_redis/' .env
sed -i 's/^MINIO_ROOT_PASSWORD=.*/MINIO_ROOT_PASSWORD=replace_me_minio/' .env
sed -i 's/^GRAFANA_ADMIN_PASSWORD=.*/GRAFANA_ADMIN_PASSWORD=replace_me_grafana/' .env
```

成功标志：`ls .env` 存在，且关键密码不是默认占位符。  
失败先查什么：`sed` 后是否把特殊字符写坏（有特殊字符建议手工编辑 `.env`）。

---

### Step D：配置 control 节点（核心）

这一步在干什么：把 control 节点改成能连到真实 stateful/minio/worker。  
为什么现在做：control 负责 API、调度、观测；地址错一个，全链路都通不了。  
在哪台机器执行：只在 `node-control`。

复制即用命令：

```bash
# 在 node-control 执行
cd ~/Deep_OJ/deploy/cluster
cp -n config.control.example.yaml config.control.yaml
```

必须改的字段（打开 `config.control.yaml` 逐条改）：

1. `api.database_url` -> 指向 `STATEFUL_IP:5432`
2. `api.redis_url` -> 指向 `STATEFUL_IP:6379`
3. `api.minio.endpoint` -> 指向 `http://MINIO_IP:9000`
4. `api.auth.jwt_secret` -> 你的 JWT_SECRET
5. `scheduler.database_url` -> 指向 `STATEFUL_IP:5432`
6. `scheduler.redis_url` -> 指向 `STATEFUL_IP:6379`
7. `scheduler.workers.addresses` -> 列出全部 worker 私网地址（至少 worker-1）

Prometheus targets 也要改：

```bash
# 在 node-control 执行
cd ~/Deep_OJ/deploy/cluster
# 编辑 prometheus.yml，把 worker targets 改成真实 worker 私网IP:9092
```

建议改完后自检：

```bash
# 在 node-control 执行
cd ~/Deep_OJ/deploy/cluster
rg -n '10\.0\.1\.|change_me|127\.0\.0\.1:9091|10\.0\.1\.21:9092' config.control.yaml prometheus.yml
```

成功标志：`config.control.yaml` 不再有示例 IP/密码；`workers.addresses` 是你的实际地址。  
失败先查什么：
- `database_url` 密码和 `.env` 是否一致
- `workers.addresses` 端口是否是 `50051`
- `prometheus.yml` 中 worker 端口是否是 `9092`

---

### Step E：配置每台 worker 节点

这一步在干什么：给每台 worker 唯一身份，并接到同一个 stateful/minio。  
为什么现在做：worker 身份冲突会导致消费冲突或观测难排查。  
在哪台机器执行：每台 `node-worker-*`。

复制即用命令：

```bash
# 在每台 node-worker-* 执行
cd ~/Deep_OJ/deploy/cluster
cp -n config.worker.example.yaml config.worker.yaml
```

必须改的字段：

1. 唯一字段（每台不同）：
- `worker.id`
- `worker.addr`（例如 `10.0.1.21:50051`）
- `worker.stream.consumer`

2. 共享基础地址（所有 worker 一样）：
- `worker.database_url` -> `STATEFUL_IP:5432`
- `worker.redis_url` -> `STATEFUL_IP:6379`
- `worker.minio.endpoint` -> `http://MINIO_IP:9000`

建议自检：

```bash
# 在每台 node-worker-* 执行
cd ~/Deep_OJ/deploy/cluster
rg -n 'worker.id|worker.addr|consumer|database_url|redis_url|minio:' config.worker.yaml
```

成功标志：每台 worker 的 id/addr/consumer 都不同。  
失败先查什么：`worker.addr` IP 是否写成别台机器地址。

---

### Step F：按依赖顺序启动（写死顺序）

这一步在干什么：按依赖链启动服务，减少反复失败重启。  
为什么现在做：先有 DB/Redis/MinIO，control/worker 才能正常连接。  
在哪台机器执行：按角色节点分别执行。

复制即用命令：

```bash
# 在 node-stateful 执行
cd ~/Deep_OJ/deploy/cluster
docker compose -f node-stateful.compose.yml up -d
```

```bash
# 在 node-minio 执行
cd ~/Deep_OJ/deploy/cluster
docker compose -f node-minio.compose.yml up -d
```

```bash
# 在 node-control 执行
cd ~/Deep_OJ/deploy/cluster
docker compose -f node-control.compose.yml up -d
```

```bash
# 在 node-worker-1 执行
cd ~/Deep_OJ/deploy/cluster
docker compose -f node-worker.compose.yml up -d
```

成功标志：每台 `docker compose ps` 都显示对应服务 `Up`。  
失败先查什么：
- `.env` 密码是否缺失
- `config.*.yaml` 是否存在且路径正确
- `docker compose logs --tail=200 <service>`

---

### Step G：control 节点健康检查（门禁）

这一步在干什么：确认 API/Prometheus/Grafana 和 targets 都可用。  
为什么现在做：这是开压测前门禁，不通过就不要跑流量。  
在哪台机器执行：`node-control`。

复制即用命令：

```bash
# 在 node-control 执行
curl -fsS http://127.0.0.1:18080/api/v1/health
curl -fsS http://127.0.0.1:19090/-/ready
curl -fsS http://127.0.0.1:13000/api/health
curl -sS 'http://127.0.0.1:19090/api/v1/targets' | jq '.data.activeTargets[] | {job:.labels.job,health:.health,lastError:.lastError}'
```

成功标志：
- `api/scheduler/worker` 全 `UP`
- `lastError` 为空

失败先查什么：
- worker target DOWN -> `node-worker` 的 `docker compose logs --tail=200 worker`
- scheduler DOWN -> `node-control` 的 `docker compose logs --tail=200 scheduler`
- api DOWN -> 优先查 DB/Redis 地址与密码

---

### Step H：跑 1 小时压测（固定口径）

这一步在干什么：产出主压测日志和累计任务数。  
为什么现在做：先拿单 worker 基线数据，后面扩容才有对照。  
在哪台机器执行：`node-control`。

复制即用命令：

```bash
# 在 node-control 执行
cd ~/Deep_OJ
mkdir -p artifacts/resume/manual
KEEP_TMP=1 DURATION_SEC=3600 WORKERS=20 SUBMIT_INTERVAL_SEC=0.05 \
bash scripts/submit_load_loop.sh | tee artifacts/resume/manual/s1_load_1h.log
```

取数命令：

```bash
# 在 node-control 执行
SUCCESS_IDS_FILE="$(grep 'success_ids=' artifacts/resume/manual/s1_load_1h.log | tail -n1 | cut -d= -f2-)"
echo "$SUCCESS_IDS_FILE"
wc -l < "$SUCCESS_IDS_FILE"
```

成功标志：
- 日志文件存在：`artifacts/resume/manual/s1_load_1h.log`
- 能拿到 `success_ids` 路径
- `wc -l` > 0

失败先查什么：
- 登录 429 -> 降低 `WORKERS` 或先传固定 `TOKEN`
- submit 429 太高 -> 看 `submit_429_total{reason=...}` 分类

---

### Step I：扩容 worker 到 2..4（可选）

这一步在干什么：扩容执行侧，观察吞吐是否上升。  
为什么现在做：验证瓶颈是不是 worker 并发。  
在哪台机器执行：`node-worker-2`、`node-worker-3`、`node-worker-4`；然后回 `node-control` 观测。

复制即用命令：

```bash
# 在每台 node-worker-2..4 执行
cd ~/Deep_OJ/deploy/cluster
docker compose -f node-worker.compose.yml up -d
```

回 control 观察：

```bash
# 在 node-control 执行
curl -sS 'http://127.0.0.1:19090/api/v1/targets' | jq '.data.activeTargets[] | {job:.labels.job,instance:.labels.instance,health:.health,lastError:.lastError}'
curl -g -sS 'http://127.0.0.1:19090/api/v1/query?query=sum(rate(worker_task_total[1m]))' | jq '.data.result'
```

成功标志：worker targets 数量增加且 `UP`，`Jobs/s` 比基线更高。  
失败先查什么：新增 worker 的 `config.worker.yaml` 唯一字段是否冲突。

---

### Step J：停机/回滚（按角色）

这一步在干什么：标准化收尾或故障回滚。  
为什么现在做：避免临时测试环境失控持续扣费。  
在哪台机器执行：对应角色节点。

复制即用命令：

```bash
# 在 node-control 执行
cd ~/Deep_OJ/deploy/cluster
docker compose -f node-control.compose.yml down
```

```bash
# 在 node-stateful 执行
cd ~/Deep_OJ/deploy/cluster
docker compose -f node-stateful.compose.yml down
```

```bash
# 在 node-minio 执行
cd ~/Deep_OJ/deploy/cluster
docker compose -f node-minio.compose.yml down
```

```bash
# 在 node-worker-* 执行
cd ~/Deep_OJ/deploy/cluster
docker compose -f node-worker.compose.yml down
```

成功标志：各节点 `docker compose ps` 无运行容器。  
失败先查什么：是否在错误节点执行了错误 compose 文件。

## 3. 1 小时压测与取数（B01/B02/B16）

固定口径：

- 压测时长：`DURATION_SEC=3600`
- 输出日志：`artifacts/resume/manual/s1_load_1h.log`
- 累计任务：`wc -l < success_ids_file`
- 并发参数：压测命令中的 `WORKERS`

## 4. worker 扩容（1 -> 4）与观测

推荐流程：

1. 先只跑 `node-worker-1` 作为 baseline
2. 跑 1 小时压测并记下 `Jobs/s`、P95、P99
3. 拉起 `node-worker-2..4`
4. 再跑同口径压测
5. 对比吞吐与延迟

关键观测命令（在 node-control）：

```bash
curl -g -sS 'http://127.0.0.1:19090/api/v1/query?query=sum(rate(worker_task_total[1m]))' | jq '.data.result'
curl -g -sS 'http://127.0.0.1:19090/api/v1/query?query=histogram_quantile(0.95,sum by (le)(rate(job_e2e_duration_seconds_bucket[5m])))' | jq '.data.result'
curl -g -sS 'http://127.0.0.1:19090/api/v1/query?query=histogram_quantile(0.99,sum by (le)(rate(job_e2e_duration_seconds_bucket[5m])))' | jq '.data.result'
```

## 5. 常见报错速查（最小集合）

### 5.1 API 连不上 DB

现象：API 启动失败或 submit 5xx。  
先查：

```bash
# 在 node-control 执行
cd ~/Deep_OJ/deploy/cluster
docker compose -f node-control.compose.yml logs --tail=200 api
```

重点看 `database_url` 是否指向 `STATEFUL_IP:5432` 且密码一致。

### 5.2 API 连不上 Redis

现象：提交报队列错误。  
先查：

```bash
# 在 node-control 执行
cd ~/Deep_OJ/deploy/cluster
docker compose -f node-control.compose.yml logs --tail=200 api
```

### 5.3 worker 不消费

现象：submit 成功但任务长期 pending。  
先查：

```bash
# 在对应 node-worker 执行
cd ~/Deep_OJ/deploy/cluster
docker compose -f node-worker.compose.yml logs --tail=200 worker
```

重点看 `worker.id / worker.addr / worker.stream.consumer` 是否唯一。

### 5.4 Prometheus target DOWN

现象：看板没数据。  
先查：

```bash
# 在 node-control 执行
curl -sS 'http://127.0.0.1:19090/api/v1/targets' | jq '.data.activeTargets[] | {job:.labels.job,instance:.labels.instance,health:.health,lastError:.lastError}'
```

重点看：
- `prometheus.yml` worker targets 是否写了正确私网 IP
- worker 9092 是否可达

## 6. 简历空格映射（B01-B24）

| 空格编号 | 对应简历空格 | 实验序号 | 取数方法（命令/来源） | 填写口径 |
|---|---|---|---|---|
| B01 | 持续压测 `__` 小时 | S1 | `DURATION_SEC=3600` + `submit_load_loop.sh` 摘要 `运行=...s` | `秒/3600` |
| B02 | 累计任务 `__` 次 | S1 | `success_ids` 文件行数 | `wc -l` |
| B03 | FD/僵尸泄漏 `__` | S3 | `verify_worker_process_cleanup.sh` 日志 | `leaked=0` 填 `0` |
| B04 | Redis 重启 `__` 次 | S4 | `REDIS_RESTART_ROUNDS` | 环境变量实值 |
| B05 | Dispatcher 崩溃 `__` 次 | S4 | `DISPATCHER_CRASH_ROUNDS` | 环境变量实值 |
| B06 | 网络抖动 `__` 组 | S4 | `REDIS_JITTER_ROUNDS` | 环境变量实值 |
| B07 | 丢失率 `__` | S4 | `summary.json.loss_rate` | 小数或百分比二选一并统一 |
| B08 | 重复率 `__` | S4 | `summary.json.duplicate_rate` | 同 B07 |
| B09 | RTO `__` 秒 | S4 | `summary.json.rto_ms.p95` | `/1000` 后填秒 |
| B10 | 缓存命中率 `__` | S5 | PromQL：`hit/(hit+miss)` | 百分比 |
| B11 | MinIO 带宽下降 `__%` | S5 | 冷热窗口 `worker_minio_download_bytes_total` 对比 | `(cold-warm)/cold*100%` |
| B12 | 平均拉取耗时下降 `__%` | S5 | 冷热窗口 `worker_testcase_prepare_duration_seconds` 均值对比 | `(cold-warm)/cold*100%` |
| B13 | 在 `__` 台集群 | S2 | 拓扑盘点 | 扩容窗口填 `7` |
| B14 | （`__` 核/`__` GB）核数 | S2 | 规格盘点 | Worker 规格填 `4` |
| B15 | （`__` 核/`__` GB）内存 | S2 | 规格盘点 | Worker 规格填 `8` |
| B16 | 并发 `__` | S1/S2 | `submit_load_loop.sh` 的 `WORKERS` | 填实测并发参数 |
| B17 | 吞吐 `__` Jobs/s | S2 | `collect_resume_metrics.sh` 的 `jobs_per_sec_peak_5m` | 峰值口径 |
| B18 | P95 `__` ms | S2 | `collect_resume_metrics.sh` 的 `e2e_p95_ms` | ms |
| B19 | P99 `__` ms | S2 | `collect_resume_metrics.sh` 的 `e2e_p99_ms` | ms |
| B20 | 提交接口稳态 `__` QPS | S2 | `collect_resume_metrics.sh` 的 `submit_qps_peak_5m` | QPS |
| B21 | Worker 从 `__` 台 | S2 | 扩容前节点数 | `1` |
| B22 | Worker 到 `__` 台 | S2 | 扩容后节点数 | `4` |
| B23 | 吞吐提升 `__` 倍 | S2 | `scaled_jobs / baseline_jobs` | 倍数 |
| B24 | 沙箱模块单测覆盖率 `__%` | S6 | `collect_coverage.sh` 产物（C++ 覆盖率） | 百分比 |

## 附录 A：背压口径速记

- `submit QPS`：入口成功速率（submit 返回 200）
- `Jobs/s`：端到端完成速率
- `submit_429_total{reason="rate_limit"}`：入口限流
- `submit_429_total{reason="inflight_cap"}`：活跃任务上限保护
- `submit_429_total{reason="backpressure"}`：队列背压拒绝

## 附录 B：严格“一机一服务”后续怎么做

这不是当前执行路径。若后续要做：

1. 把 `node-control.compose.yml` 拆成 `api/scheduler/prometheus/grafana` 4 份
2. 每台机器只起一个 service
3. 全部配置改成对应私网地址
4. 重做健康检查和观测 targets 清单
