# 运维 Runbook（简版）

## 1. Stream backlog 持续增长
1. 查看 `deepoj:jobs` 长度与消费组 pending：`XLEN` / `XPENDING`
2. 检查 Worker 进程是否正常、`XREADGROUP` 是否持续消费
3. 必要时扩容 Worker 或限流提交

## 2. Redis 异常
1. 检查 Redis 连通性与认证配置
2. Redis 不可用时，提交与消费都会受影响
3. 恢复后由 outbox + reclaim 机制继续推进

## 3. MinIO 不可用
1. Worker 下载测试数据失败，任务会进入错误分支
2. 检查 MinIO 状态、桶权限与对象完整性
3. 恢复后重试任务

## 4. 判题状态卡住
1. 检查 Worker 日志中的 `db_finalize_*` 与 `xack_*`
2. 校验 `submissions` 的 `state/status/attempt_id`
3. 如存在 PEL 堆积，观察 reclaim 是否在推进

## 5. Cgroups v2 未启用
1. 宿主机启用 cgroup v2（内核参数）
2. Docker 挂载 `/sys/fs/cgroup`（hybrid 模式下确认容器内可见 `/sys/fs/cgroup/unified`）
3. 容器内检查：`test -f /sys/fs/cgroup/unified/cgroup.controllers || test -f /sys/fs/cgroup/cgroup.controllers`
4. Worker 设置 `REQUIRE_CGROUPS_V2=1`

## 6. Prometheus/Grafana 观测栈
1. 启动：
   `docker compose -f docker-compose.yml -f docker-compose.observability.yml up -d --build`
2. 健康检查：
   `curl -fsS http://127.0.0.1:19090/-/ready`
   `curl -fsS http://127.0.0.1:13000/api/health`
3. 抓取状态：
   `curl -sS 'http://127.0.0.1:19090/api/v1/targets' | jq '.data.activeTargets[] | {job:.labels.job,health:.health,lastError:.lastError}'`
4. API 指标鉴权：
   `curl -sS -H "Authorization: Bearer deepoj_metrics_token_dev" http://127.0.0.1:18080/metrics | head`

## 7. 观测常见故障
1. Prometheus `api` 目标 `DOWN`：
   检查 `config.docker.yaml` 的 `metrics_token` 与 `docker/observability/prometheus/prometheus.yml` Bearer token 是否一致。
2. Grafana 无数据：
   检查 `http://127.0.0.1:13000/api/health`、`http://127.0.0.1:19090/-/ready`，以及数据源是否为 `Prometheus`。
3. 端口冲突：
   默认使用 `19090`（Prometheus）和 `13000`（Grafana）。
