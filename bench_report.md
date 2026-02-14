# D2 Streams 压力口径采样报告（用于 D3 背压输入）

## 1. 采集命令与字段

采集脚本：`scripts/collect_stream_pressure.sh`  
脚本输出一行 JSON（末尾 `\n`），核心字段：

- `ts_ms`: 采样时刻毫秒时间戳
- `xlen`: `XLEN(stream)`，stream 长度
- `pending`: `XPENDING summary` 的 count
- `lag`: 来自 `XINFO GROUPS lag`，可能是数字，也可能是 `null`
- `lag_valid`: `lag` 是否可用（`true/false`）
- `oldest_age_ms`: 最老待处理消息年龄（毫秒）
- `oldest_age_source`: `pending | backlog | pending_stream_id | backlog_stream_id | none | unknown`
- `approx`: 该快照是否为近似值

`lag` 语义强调：`null != 0`。  
`null` 表示 Redis 未提供可用 lag（版本差异/字段为空），不是“没有积压”。

快速命令：

```bash
cd /home/diguo/Deep_OJ
STREAM_KEY="${JOB_STREAM_KEY:-deepoj:jobs}" \
GROUP="${JOB_STREAM_GROUP:-deepoj:workers}" \
REDIS_PASSWORD="${REDIS_PASSWORD}" \
bash scripts/collect_stream_pressure.sh
```

## 2. oldest_age_ms 口径

按优先级计算：

1. `pending > 0`：取最老 pending 的 entry（`XPENDING ... - + 1`），优先用消息字段 `enqueue_ts`。  
2. `pending == 0` 且 `lag_valid=true` 且 `lag>0`：用 `last-delivered-id` 之后第一条消息估算 backlog oldest（`XRANGE (last-delivered-id + COUNT 1`）。  
3. 其他：`oldest_age_ms=0`，`oldest_age_source=none`。

兜底策略：

- 若 `enqueue_ts` 缺失，回退使用 stream entry id 左侧毫秒部分（`*_stream_id`）。
- 使用 stream id 兜底或 backlog 推断时，`approx=true`。

## 3. 可复现采样步骤（本报告数据来源）

```bash
cd /home/diguo/Deep_OJ

PASS='deepoj_redis_change_me'
STREAM='deepoj:jobs'
GROUP='deepoj:workers'
CONSUMER='bench-consumer'

docker rm -f d2-redis-bench >/dev/null 2>&1 || true
docker run -d --name d2-redis-bench -p 6399:6379 \
  redis:7-alpine redis-server --save '' --appendonly no --requirepass "$PASS"

redis-cli -h 127.0.0.1 -p 6399 --no-auth-warning -a "$PASS" \
  XGROUP CREATE "$STREAM" "$GROUP" 0 MKSTREAM >/dev/null 2>&1 || true

# sample 1: baseline
REDIS_HOST=127.0.0.1 REDIS_PORT=6399 REDIS_PASSWORD="$PASS" STREAM_KEY="$STREAM" GROUP="$GROUP" \
  bash scripts/collect_stream_pressure.sh

# sample 2: backlog only
base_ts=$(( $(date +%s%3N) - 130000 ))
for i in $(seq 1 30); do
  ts=$((base_ts + i))
  redis-cli -h 127.0.0.1 -p 6399 --no-auth-warning -a "$PASS" \
    XADD "$STREAM" '*' job_id "bench-job-$i" enqueue_ts "$ts" payload_ref "task:payload:bench-job-$i" priority 0 >/dev/null
done
REDIS_HOST=127.0.0.1 REDIS_PORT=6399 REDIS_PASSWORD="$PASS" STREAM_KEY="$STREAM" GROUP="$GROUP" \
  bash scripts/collect_stream_pressure.sh

# sample 3: pending>0
redis-cli -h 127.0.0.1 -p 6399 --no-auth-warning -a "$PASS" \
  XREADGROUP GROUP "$GROUP" "$CONSUMER" COUNT 10 STREAMS "$STREAM" '>' >/dev/null
REDIS_HOST=127.0.0.1 REDIS_PORT=6399 REDIS_PASSWORD="$PASS" STREAM_KEY="$STREAM" GROUP="$GROUP" \
  bash scripts/collect_stream_pressure.sh
```

## 4. 三组真实样本

| ts (UTC) | xlen | pending | lag | lag_valid | oldest_age_ms | oldest_age_source | approx |
| --- | ---: | ---: | ---: | :---: | ---: | --- | :---: |
| 2026-02-14T17:36:09.852Z | 0 | 0 | 0 | true | 0 | none | false |
| 2026-02-14T17:36:10.079Z | 30 | 0 | 30 | true | 130220 | backlog | true |
| 2026-02-14T17:36:10.165Z | 30 | 10 | 20 | true | 130306 | pending | false |

原始输出：

```json
{"ts_ms":1771090569852,"stream_key":"deepoj:jobs","group":"deepoj:workers","xlen":0,"pending":0,"lag":0,"lag_valid":true,"oldest_age_ms":0,"oldest_age_source":"none","approx":false}
{"ts_ms":1771090570079,"stream_key":"deepoj:jobs","group":"deepoj:workers","xlen":30,"pending":0,"lag":30,"lag_valid":true,"oldest_age_ms":130220,"oldest_age_source":"backlog","approx":true}
{"ts_ms":1771090570165,"stream_key":"deepoj:jobs","group":"deepoj:workers","xlen":30,"pending":10,"lag":20,"lag_valid":true,"oldest_age_ms":130306,"oldest_age_source":"pending","approx":false}
```

## 5. 压力判断与调参建议

- `lag` 持续升高：消费落后，优先看 worker 并发/可用性。
- `pending` 与 `oldest_age_ms` 同升：任务卡在执行/确认链路，重点看 DB 延迟、judge 执行时长、reclaim 日志。
- `lag_valid=false`（`lag=null`）：先看 `XPENDING` + `last-delivered-id` + `XINFO STREAM`，不要把 null 当 0。
- `xlen` 接近 `JOB_STREAM_MAXLEN`：保留窗口过短风险升高，评估调大 `JOB_STREAM_MAXLEN` 或削峰。

排障方向（优先级）：

1. worker 存活与消费推进（`XINFO GROUPS`、worker 日志）。
2. DB claim/finalize/heartbeat 慢查询与超时。
3. reclaim 循环是否推进（`XPENDING` 长尾、`XAUTOCLAIM` 行为）。
4. Redis 延迟/网络抖动。

注：Prometheus 指标仍禁止 `job_id` label；逐任务定位用日志字段 `job_id/attempt_id/trace_id`。
