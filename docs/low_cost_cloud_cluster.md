# Deep-OJ 云集群部署与简历空格取数手册（腾讯云按量版）

## 0. 目标与结论

本手册是 Deep-OJ 云上落地与简历数据取数的唯一主文档，覆盖：

- 腾讯云按量计费集群如何部署
- 简历每个横线空格如何测、从哪里取值
- 限流与背压参数怎么调
- 按 S0-S6 一步一步执行的命令手册（每步含“目的/命令/产物”）

固定决策如下：

- 云平台：腾讯云（CVM + VPC + 安全组）
- 拓扑：1 台 API 直出公网，不上 Nginx（压测/取数阶段）
- 口径：标准口径（1 小时主压测 + Outbox 5/5/5 + Worker 1->4 对比）
- 文档归一：本文件为主；`deploy/cluster/README.md` 仅保留入口跳转

## 1. 部署拓扑（到底多少台）

### 1.1 节点数量与职责

| 节点 | 数量 | 规格建议 | 部署内容 |
|---|---:|---|---|
| `node-control` | 1 | 2C4G / 80GB | API + Scheduler + Prometheus + Grafana |
| `node-stateful` | 1 | 4C8G / 200GB | PostgreSQL + Redis |
| `node-minio` | 1 | 2C4G / 200GB | MinIO |
| `node-worker-1` | 1 | 4C8G / 120GB | 常驻 Worker |
| `node-worker-2..4` | 3（临时） | 4C8G / 120GB | 压测扩容 Worker |

结论：

- 常驻：4 台
- 压测扩容窗口：7 台
- API Server：1 台
- Nginx：不上（本阶段）

### 1.2 Nginx 是否要上

- 当前“简历数据取数”阶段：不上 Nginx，减少链路变量，降低排障成本。
- 仅当你要做“域名 + TLS + 80/443 展示”时再加 Nginx（可与 API 同机）。

### 1.3 网络与安全组（腾讯云）

- 所有节点放同一个 VPC 私网。
- 仅 `node-control` 开公网入口。
- 推荐开放策略：

| 端口 | 节点 | 来源 | 用途 |
|---|---|---|---|
| `22` | 全部 | 你的办公公网 IP | SSH |
| `18080` | node-control | `0.0.0.0/0` 或办公 IP | API |
| `19090` | node-control | 办公 IP 或不开放 | Prometheus（可选） |
| `13000` | node-control | 不开放公网，走 SSH 隧道 | Grafana |
| `5432` | node-stateful | VPC 内 node-control + worker 网段 | PostgreSQL |
| `6379` | node-stateful | VPC 内 node-control + worker 网段 | Redis |
| `9000/9001` | node-minio | VPC 内指定源 | MinIO API/Console |
| `50051/9092` | node-worker-* | VPC 内 | worker gRPC + metrics |

## 2. 目录与模板（本仓库）

- 分节点 Compose 模板：`deploy/cluster/`
- 控制面配置模板：`deploy/cluster/config.control.example.yaml`
- Worker 配置模板：`deploy/cluster/config.worker.example.yaml`
- Prometheus 模板：`deploy/cluster/prometheus.yml`

说明：

- `*.example.yaml` 是样例，复制成真实文件后再填私网 IP/密钥。
- `node-*.compose.yml` 是分节点编排文件，不是新项目。

## 3. 参数怎么调（限流 + 背压）

控制面配置文件（`config.control.yaml`）重点项：

- `api.limits.submit_rate_limit.ip_limit`
- `api.limits.submit_rate_limit.user_limit`
- `api.limits.submit_rate_limit.window_sec`
- `api.limits.submit_max_inflight`

Worker 配置文件（`config.worker.yaml`）重点项：

- `worker.stream.backpressure_sleep_ms`
- `worker.pool_size`

推荐三档：

| 档位 | submit_rate_limit (ip/user) | submit_max_inflight | worker.backpressure_sleep_ms | 用途 |
|---|---|---:|---:|---|
| 日常回归 | `1200 / 2400` | `5000` | `50` | 功能验证 |
| 主压测 | `200000 / 200000` | `30000` | `20~50` | 避免入口限流干扰吞吐 |
| 背压专项 | `200000 / 200000` | `200~500` | `50` | 快速触发 429/拥塞保护 |

## 4. 简历空格全量编号表（B01-B24）

同一实验序号可覆盖多个空格。所有空格都必须可追溯到命令输出或指标文件。

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

## 5. 手把手流程（S0-S6）

下面所有步骤默认以仓库根目录 `~/Deep_OJ` 为基准。每个步骤都包含“目的 + 命令 + 产物”。

### S0. 集群落地（4 台常驻）

#### S0-1 代码和环境准备（每台）
目的：统一版本与密钥入口。

```bash
git clone https://github.com/d1guo/deep_oj.git ~/Deep_OJ
cd ~/Deep_OJ/deploy/cluster
cp .env.example .env
```

产物：每台机器的 `deploy/cluster/.env`。

#### S0-2 配置落盘
目的：把样例配置变成真实配置（私网 IP/密码/worker.id）。

```bash
# node-control
cd ~/Deep_OJ/deploy/cluster
cp config.control.example.yaml config.control.yaml

# node-worker-1..4
cd ~/Deep_OJ/deploy/cluster
cp config.worker.example.yaml config.worker.yaml
```

补充：按你的私网地址编辑 `prometheus.yml` 中 `worker` targets。

产物：`config.control.yaml`、`config.worker.yaml`、已更新的 `prometheus.yml`。

#### S0-3 按顺序启动
目的：先状态，再控制，再计算，避免依赖竞态。

```bash
# node-stateful
cd ~/Deep_OJ/deploy/cluster
docker compose -f node-stateful.compose.yml up -d

# node-minio
cd ~/Deep_OJ/deploy/cluster
docker compose -f node-minio.compose.yml up -d

# node-control
cd ~/Deep_OJ/deploy/cluster
docker compose -f node-control.compose.yml up -d

# node-worker-1
cd ~/Deep_OJ/deploy/cluster
docker compose -f node-worker.compose.yml up -d
```

产物：4 台常驻节点容器全部运行。

#### S0-4 健康检查（node-control）
目的：确认 API/Prometheus/Grafana 就绪。

```bash
curl -fsS http://127.0.0.1:18080/api/v1/health
curl -fsS http://127.0.0.1:19090/-/ready
curl -fsS http://127.0.0.1:13000/api/health
```

产物：3 个健康检查返回 200。

### S1. 1 小时主压测（填 B01/B02/B16）

目的：一次实验产出压测时长、累计任务、并发参数。

```bash
cd ~/Deep_OJ
mkdir -p artifacts/resume/manual
KEEP_TMP=1 DURATION_SEC=3600 WORKERS=20 SUBMIT_INTERVAL_SEC=0.05 \
  bash scripts/submit_load_loop.sh | tee artifacts/resume/manual/s1_load_1h.log
```

取值命令：

```bash
# 找 success_ids 文件路径
SUCCESS_IDS_FILE="$(grep 'success_ids=' artifacts/resume/manual/s1_load_1h.log | tail -n1 | cut -d= -f2-)"
echo "$SUCCESS_IDS_FILE"

# 统计累计任务
wc -l < "$SUCCESS_IDS_FILE"
```

产物：

- `artifacts/resume/manual/s1_load_1h.log`
- `success_ids` 文件（路径由脚本输出）

### S2. 扩容对比（填 B13-B15/B17-B23/B18-B20）

#### S2-1 基线轮（仅 1 台 Worker）
目的：得到 baseline 吞吐/QPS/延迟。

```bash
cd ~/Deep_OJ
KEEP_TMP=1 DURATION_SEC=1800 WORKERS=20 SUBMIT_INTERVAL_SEC=0.05 \
  bash scripts/submit_load_loop.sh | tee artifacts/resume/manual/s2_base_load.log
bash scripts/collect_resume_metrics.sh | tee artifacts/resume/manual/s2_base_collect.log
```

#### S2-2 启动临时 Worker（node-worker-2..4）
目的：进入 4 Worker 扩容状态。

```bash
cd ~/Deep_OJ/deploy/cluster
docker compose -f node-worker.compose.yml up -d
```

#### S2-3 扩容轮（4 台 Worker）
目的：得到 scaled 吞吐/QPS/延迟。

```bash
cd ~/Deep_OJ
KEEP_TMP=1 DURATION_SEC=1800 WORKERS=20 SUBMIT_INTERVAL_SEC=0.05 \
  bash scripts/submit_load_loop.sh | tee artifacts/resume/manual/s2_scaled_load.log
bash scripts/collect_resume_metrics.sh | tee artifacts/resume/manual/s2_scaled_collect.log
```

#### S2-4 计算扩容倍数
目的：得到 B23。

```bash
python3 - <<'PY'
import csv,glob
files=sorted(glob.glob('artifacts/resume/metrics_*/resume_metrics.csv'))
print('latest metrics files:', files[-2], files[-1])
def get(path,key):
    with open(path, encoding='utf-8') as f:
        for r in csv.DictReader(f):
            if r['key']==key:
                return float(r['value'])
    raise RuntimeError(f'{key} not found in {path}')
b=get(files[-2],'jobs_per_sec_peak_5m')
s=get(files[-1],'jobs_per_sec_peak_5m')
print('baseline_jobs_per_sec_peak_5m=', b)
print('scaled_jobs_per_sec_peak_5m=', s)
print('scale_factor=', round(s/b,4))
PY
```

产物：

- 基线和扩容两轮的 `resume_metrics.csv`
- `scale_factor`

### S3. 进程清理专项（填 B03）

说明：该脚本依赖“单机 compose 进程可见”。建议在临时 `node-lab`（单机部署）执行，不在分节点集群直接执行。

目的：验证 FD/僵尸进程无泄漏。

```bash
cd ~/Deep_OJ
bash scripts/verify_worker_process_cleanup.sh | tee artifacts/resume/manual/s3_process_cleanup.log
grep 'EVIDENCE_PROCESS_CLEANUP_NO_LEAK' artifacts/resume/manual/s3_process_cleanup.log
```

产物：`EVIDENCE_PROCESS_CLEANUP_NO_LEAK ... leaked=0`。

### S4. Outbox 故障矩阵（填 B04-B09）

说明：同样建议在 `node-lab` 执行（脚本会本机 `docker exec` `api/redis/postgres` 容器）。

目的：得到 Redis 重启/Dispatcher 崩溃/网络抖动下的丢失率、重复率、RTO。

```bash
cd ~/Deep_OJ
POSTGRES_PASSWORD=<pg_pwd> REDIS_PASSWORD=<redis_pwd> \
REDIS_RESTART_ROUNDS=5 DISPATCHER_CRASH_ROUNDS=5 REDIS_JITTER_ROUNDS=5 \
bash scripts/bench_outbox_fault_matrix.sh | tee artifacts/resume/manual/s4_outbox.log
```

取值口径：

- B04/B05/B06：直接填 `5/5/5`
- B07：`summary.json.loss_rate`
- B08：`summary.json.duplicate_rate`
- B09：`summary.json.rto_ms.p95 / 1000`

### S5. 缓存冷热对比（填 B10-B12）

#### S5-1 清空 worker 本地缓存（所有 worker）
目的：制造冷启动窗口。

```bash
sudo rm -rf /var/lib/deepoj/workspace/cases/*
sudo rm -rf /var/lib/deepoj/workspace/cases_unzipped/*
```

#### S5-2 连续跑 30 分钟
目的：前 15 分钟冷窗口，后 15 分钟热窗口。

```bash
cd ~/Deep_OJ
DURATION_SEC=1800 WORKERS=20 SUBMIT_INTERVAL_SEC=0.1 \
bash scripts/submit_load_loop.sh | tee artifacts/resume/manual/s5_cache_30m.log
```

#### S5-3 PromQL 取数（node-control）
目的：计算命中率、MinIO 带宽下降、平均拉取耗时下降。

```bash
# 1) 热窗口命中率
curl -g -sG 'http://127.0.0.1:19090/api/v1/query' \
  --data-urlencode 'query=100 * sum(increase(worker_testcase_cache_total{result="hit"}[15m])) / sum(increase(worker_testcase_cache_total[15m]))'

# 2) MinIO 下载字节（热窗口）
curl -g -sG 'http://127.0.0.1:19090/api/v1/query' \
  --data-urlencode 'query=sum(increase(worker_minio_download_bytes_total[15m]))'

# 3) MinIO 下载字节（冷窗口：向前偏移 15m）
curl -g -sG 'http://127.0.0.1:19090/api/v1/query' \
  --data-urlencode 'query=sum(increase(worker_minio_download_bytes_total[15m] offset 15m))'

# 4) testcase prepare 平均耗时（热窗口）
curl -g -sG 'http://127.0.0.1:19090/api/v1/query' \
  --data-urlencode 'query=sum(increase(worker_testcase_prepare_duration_seconds_sum[15m])) / sum(increase(worker_testcase_prepare_duration_seconds_count[15m]))'

# 5) testcase prepare 平均耗时（冷窗口）
curl -g -sG 'http://127.0.0.1:19090/api/v1/query' \
  --data-urlencode 'query=sum(increase(worker_testcase_prepare_duration_seconds_sum[15m] offset 15m)) / sum(increase(worker_testcase_prepare_duration_seconds_count[15m] offset 15m))'
```

计算公式：

- B11：`(cold_bytes - warm_bytes) / cold_bytes * 100%`
- B12：`(cold_avg - warm_avg) / cold_avg * 100%`

### S6. 覆盖率（填 B24）

目的：生成 Go + C++ 覆盖率结果文件，读取 C++ 覆盖率填空。

```bash
cd ~/Deep_OJ
CPP_COVERAGE=1 bash scripts/collect_coverage.sh | tee artifacts/resume/manual/s6_coverage.log
```

产物：

- `artifacts/coverage/<timestamp>/summary.md`
- `artifacts/coverage/<timestamp>/cpp/coverage.summary.txt`

## 6. 产物目录约定

为便于审计，建议统一归档到 `artifacts/resume/manual/`：

- `s1_load_1h.log`
- `s2_base_load.log`
- `s2_base_collect.log`
- `s2_scaled_load.log`
- `s2_scaled_collect.log`
- `s3_process_cleanup.log`
- `s4_outbox.log`
- `s5_cache_30m.log`
- `s6_coverage.log`

## 7. 常见失败与排查

### 7.1 脚本执行节点不对

症状：`docker exec` 找不到 `oj-api/oj-redis/oj-postgres`。

原因：`verify_worker_process_cleanup.sh`、`bench_outbox_fault_matrix.sh` 依赖“单机 compose 同机容器名”，不适合直接在分节点集群控制机执行。

处理：在临时 `node-lab`（单机 compose）执行 S3/S4。

### 7.2 429 原因混淆（限流 vs inflight_cap）

症状：压测中出现 429，但吞吐没有上去。

处理：

- 主压测阶段先把 `submit_rate_limit` 放宽到 `200000/200000`
- 再把 `submit_max_inflight` 提到 `30000`
- 观察 `submit_429_total{reason=...}` 与 `api_backpressure_reject_total`

### 7.3 Grafana/Prometheus 不应暴露公网

建议：

- 不开放 `13000/19090` 公网
- 使用 SSH 隧道查看

## 8. 验收清单（文档层）

- 本文可直接指导从租机到取数全流程
- B01-B24 全部可映射命令或指标
- 同一实验序号可覆盖多个空格，避免重复劳动
- `deploy/cluster/README.md` 仅保留入口跳转，不再双份维护

## 9. 相关文件

- 集群模板目录：`deploy/cluster/`
- 控制面样例：`deploy/cluster/config.control.example.yaml`
- Worker 样例：`deploy/cluster/config.worker.example.yaml`
- Prometheus 样例：`deploy/cluster/prometheus.yml`
- 项目总览：`README.md`
