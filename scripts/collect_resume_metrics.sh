#!/usr/bin/env bash
set -euo pipefail

# 脚本用途：
# 1) 从 Prometheus/DB/脚本产物收集简历常用指标。
# 2) 统一导出 CSV + Markdown，避免手工抄写口径不一致。
# 3) 兼容“部分数据缺失”场景，缺失字段标记为 NA。

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

PROM_BASE="${PROM_BASE:-http://127.0.0.1:19090}"
API_BASE="${API_BASE:-http://127.0.0.1:18080}"
METRICS_TOKEN="${METRICS_TOKEN:-deepoj_metrics_token_dev}"

POSTGRES_CONTAINER="${POSTGRES_CONTAINER:-oj-postgres}"
POSTGRES_DB="${POSTGRES_DB:-deep_oj}"
POSTGRES_USER="${POSTGRES_USER:-deep_oj}"
POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-}"

SUCCESS_IDS_FILE="${SUCCESS_IDS_FILE:-}"
LOAD_RUN_SECONDS="${LOAD_RUN_SECONDS:-}"
BASELINE_JOBS_PER_SEC="${BASELINE_JOBS_PER_SEC:-}"
SCALED_JOBS_PER_SEC="${SCALED_JOBS_PER_SEC:-}"
OUTBOX_SUMMARY_FILE="${OUTBOX_SUMMARY_FILE:-}"
PROCESS_CLEANUP_LOG_FILE="${PROCESS_CLEANUP_LOG_FILE:-}"
GO_COVERAGE_FUNC_FILE="${GO_COVERAGE_FUNC_FILE:-}"
CPP_COVERAGE_SUMMARY_FILE="${CPP_COVERAGE_SUMMARY_FILE:-}"

STAMP="$(date +%Y%m%d_%H%M%S)"
OUT_DIR="${OUT_DIR:-$ROOT_DIR/artifacts/resume/metrics_${STAMP}}"
TMP_DIR="$(mktemp -d)"

CSV_FILE="$OUT_DIR/resume_metrics.csv"
MD_FILE="$OUT_DIR/resume_metrics.md"
PROM_SNAPSHOT_CSV="$OUT_DIR/prom_snapshot.csv"
METRICS_RAW="$TMP_DIR/metrics_combined.txt"

cleanup() {
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT

usage() {
  cat <<'USAGE'
Usage: scripts/collect_resume_metrics.sh

Optional env:
  PROM_BASE                   default: http://127.0.0.1:19090
  API_BASE                    default: http://127.0.0.1:18080
  METRICS_TOKEN               default: deepoj_metrics_token_dev
  POSTGRES_PASSWORD           if set, collect DB submission count

  SUCCESS_IDS_FILE            submit_load_loop success ids file
  LOAD_RUN_SECONDS            total load runtime in seconds
  BASELINE_JOBS_PER_SEC       baseline Jobs/s (for scale factor)
  SCALED_JOBS_PER_SEC         scaled Jobs/s (for scale factor)
  OUTBOX_SUMMARY_FILE         summary.json from bench_outbox_fault_matrix.sh
  PROCESS_CLEANUP_LOG_FILE    log file containing EVIDENCE_PROCESS_CLEANUP_NO_LEAK
  GO_COVERAGE_FUNC_FILE       go coverage func report
  CPP_COVERAGE_SUMMARY_FILE   C++ coverage summary report
USAGE
}

require_cmd() {
  local cmd="$1"
  command -v "$cmd" >/dev/null 2>&1 || {
    echo "ERROR: missing command: $cmd" >&2
    exit 1
  }
}

find_container_by_service() {
  local service="$1"
  if ! command -v docker >/dev/null 2>&1; then
    return 0
  fi
  docker ps --filter "label=com.docker.compose.service=${service}" --format '{{.Names}}' | head -n1
}

prom_query_scalar() {
  local query="$1"
  local default="${2:-NA}"
  local resp_file="$TMP_DIR/prom_$(date +%s%N).json"

  if ! curl -g -sS -G "$PROM_BASE/api/v1/query" --data-urlencode "query=$query" -o "$resp_file"; then
    echo "$default"
    return 0
  fi

  python3 - "$resp_file" "$default" <<'PY'
import json
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
default = sys.argv[2]
try:
    obj = json.loads(path.read_text(encoding="utf-8"))
    if obj.get("status") != "success":
        print(default)
        raise SystemExit(0)
    result = obj.get("data", {}).get("result", [])
    if not result:
        print(default)
        raise SystemExit(0)
    value = result[0].get("value", [])
    if len(value) < 2:
        print(default)
        raise SystemExit(0)
    print(value[1])
except Exception:
    print(default)
PY
}

fetch_metrics_from_container() {
  local container="$1"
  local port="$2"
  local out_file="$3"
  local raw_file="$TMP_DIR/raw_${container}_${port}.txt"

  if ! docker exec "$container" bash -lc "set -euo pipefail; exec 3<>/dev/tcp/127.0.0.1/${port}; printf 'GET /metrics HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n' >&3; cat <&3" >"$raw_file" 2>/dev/null; then
    return 1
  fi

  python3 - "$raw_file" "$out_file" <<'PY'
import pathlib
import sys

raw = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8", errors="replace")
out = pathlib.Path(sys.argv[2])
parts = raw.split("\r\n\r\n", 1)
if len(parts) == 2:
    headers, body = parts
else:
    parts = raw.split("\n\n", 1)
    headers, body = parts if len(parts) == 2 else ("", raw)
status_line = headers.splitlines()[0] if headers else ""
if status_line and " 200 " not in status_line:
    raise SystemExit(2)
out.write_text(body, encoding="utf-8")
PY
}

collect_metric_names_count() {
  : >"$METRICS_RAW"
  local api_metrics="$TMP_DIR/api_metrics.txt"
  local scheduler_metrics="$TMP_DIR/scheduler_metrics.txt"
  local worker_metrics="$TMP_DIR/worker_metrics.txt"

  curl -sS -H "Authorization: Bearer $METRICS_TOKEN" "$API_BASE/metrics" >"$api_metrics" 2>/dev/null || true

  if command -v docker >/dev/null 2>&1; then
    local scheduler_container worker_container
    scheduler_container="$(find_container_by_service scheduler)"
    worker_container="$(find_container_by_service worker)"
    if [[ -n "$scheduler_container" ]]; then
      fetch_metrics_from_container "$scheduler_container" "9091" "$scheduler_metrics" || true
    fi
    if [[ -n "$worker_container" ]]; then
      fetch_metrics_from_container "$worker_container" "9092" "$worker_metrics" || true
    fi
  fi

  cat "$api_metrics" "$scheduler_metrics" "$worker_metrics" >"$METRICS_RAW" 2>/dev/null || true
  python3 - "$METRICS_RAW" <<'PY'
import pathlib
import re
import sys

path = pathlib.Path(sys.argv[1])
names = set()
for line in path.read_text(encoding="utf-8", errors="replace").splitlines():
    s = line.strip()
    if not s or s.startswith("#"):
        continue
    m = re.match(r"^([a-zA-Z_:][a-zA-Z0-9_:]*)", s)
    if m:
        names.add(m.group(1))
print(len(names))
PY
}

db_submission_count() {
  if [[ -z "$POSTGRES_PASSWORD" ]]; then
    echo "NA"
    return 0
  fi
  docker exec -e PGPASSWORD="$POSTGRES_PASSWORD" -i "$POSTGRES_CONTAINER" \
    psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -t -A \
    -c "SELECT COUNT(*) FROM submissions;" 2>/dev/null | tr -d '[:space:]' || echo "NA"
}

maybe_count_lines() {
  local file="$1"
  if [[ -n "$file" && -f "$file" ]]; then
    wc -l <"$file" | tr -d ' '
  else
    echo "NA"
  fi
}

extract_go_coverage_pct() {
  local file="$1"
  if [[ -n "$file" && -f "$file" ]]; then
    awk '/^total:/ {print $3}' "$file" | tail -n1
    return 0
  fi
  local latest
  latest="$(ls -1t artifacts/coverage/*/go/coverage.func.txt 2>/dev/null | head -n1 || true)"
  if [[ -n "$latest" && -f "$latest" ]]; then
    awk '/^total:/ {print $3}' "$latest" | tail -n1
    return 0
  fi
  echo "NA"
}

extract_cpp_coverage_pct() {
  local file="$1"
  if [[ -n "$file" && -f "$file" ]]; then
    python3 - "$file" <<'PY'
import pathlib
import re
import sys

text = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8", errors="replace")
for pat in [r"lines:\s*([0-9]+(?:\.[0-9]+)?%)", r"TOTAL.*?([0-9]+(?:\.[0-9]+)?%)"]:
    m = re.search(pat, text, re.IGNORECASE | re.DOTALL)
    if m:
        print(m.group(1))
        raise SystemExit(0)
print("NA")
PY
    return 0
  fi
  local latest
  latest="$(ls -1t artifacts/coverage/*/cpp/coverage.summary.txt 2>/dev/null | head -n1 || true)"
  if [[ -n "$latest" && -f "$latest" ]]; then
    extract_cpp_coverage_pct "$latest"
    return 0
  fi
  echo "NA"
}

extract_outbox_summary() {
  local file="$1"
  if [[ -z "$file" ]]; then
    file="$(ls -1t artifacts/resume/outbox_fault_matrix_*/summary.json 2>/dev/null | head -n1 || true)"
  fi
  if [[ -z "$file" || ! -f "$file" ]]; then
    echo "NA,NA,NA,NA,NA"
    return 0
  fi
  python3 - "$file" <<'PY'
import json
import pathlib
import sys

obj = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
loss = obj.get("loss_rate", "NA")
dup = obj.get("duplicate_rate", "NA")
rto = obj.get("rto_ms", {})
p95 = rto.get("p95", "NA")
p99 = rto.get("p99", "NA")
maxv = rto.get("max", "NA")
print(f"{loss},{dup},{p95},{p99},{maxv}")
PY
}

extract_process_cleanup_leak_flag() {
  local file="$1"
  if [[ -z "$file" ]]; then
    file="$(ls -1t artifacts/*/verify_worker_process_cleanup.sh.log 2>/dev/null | head -n1 || true)"
  fi
  if [[ -z "$file" || ! -f "$file" ]]; then
    echo "NA"
    return 0
  fi
  local line
  line="$(grep -E 'EVIDENCE_PROCESS_CLEANUP_NO_LEAK' "$file" | tail -n1 || true)"
  if [[ -z "$line" ]]; then
    echo "NA"
    return 0
  fi
  echo "$line" | sed -n 's/.*leaked=\([0-9]\+\).*/\1/p'
}

calc_scale_factor() {
  local baseline="$1"
  local scaled="$2"
  if [[ ! "$baseline" =~ ^[0-9]+(\.[0-9]+)?$ ]] || [[ ! "$scaled" =~ ^[0-9]+(\.[0-9]+)?$ ]]; then
    echo "NA"
    return 0
  fi
  python3 - "$baseline" "$scaled" <<'PY'
import sys
b = float(sys.argv[1])
s = float(sys.argv[2])
if b <= 0:
    print("NA")
else:
    print(f"{s/b:.4f}")
PY
}

write_outputs() {
  local submit_qps="$1"
  local jobs_s="$2"
  local p95_ms="$3"
  local p99_ms="$4"
  local submit_qps_peak="$5"
  local jobs_s_peak="$6"
  local stream_lag_peak="$7"
  local stream_backlog_peak="$8"
  local outbox_pending_peak="$9"
  local inflight_peak="${10}"
  local metric_count="${11}"
  local db_submissions="${12}"
  local success_ids="${13}"
  local run_hours="${14}"
  local outbox_loss_rate="${15}"
  local outbox_dup_rate="${16}"
  local outbox_rto_p95="${17}"
  local outbox_rto_p99="${18}"
  local outbox_rto_max="${19}"
  local leak_flag="${20}"
  local go_cov="${21}"
  local cpp_cov="${22}"
  local scale_factor="${23}"

  cat >"$CSV_FILE" <<CSV
key,value
submit_qps,$submit_qps
jobs_per_sec,$jobs_s
e2e_p95_ms,$p95_ms
e2e_p99_ms,$p99_ms
submit_qps_peak_5m,$submit_qps_peak
jobs_per_sec_peak_5m,$jobs_s_peak
stream_lag_peak_5m,$stream_lag_peak
stream_backlog_peak_5m,$stream_backlog_peak
outbox_pending_peak_5m,$outbox_pending_peak
worker_stream_inflight_peak_5m,$inflight_peak
metric_names_count,$metric_count
db_submissions_total,$db_submissions
success_ids_count,$success_ids
load_run_hours,$run_hours
outbox_loss_rate,$outbox_loss_rate
outbox_duplicate_rate,$outbox_dup_rate
outbox_rto_p95_ms,$outbox_rto_p95
outbox_rto_p99_ms,$outbox_rto_p99
outbox_rto_max_ms,$outbox_rto_max
process_cleanup_leaked,$leak_flag
go_coverage_total,$go_cov
cpp_coverage_total,$cpp_cov
scale_factor,$scale_factor
CSV

  cat >"$PROM_SNAPSHOT_CSV" <<CSV
metric,query,value
submit_qps,"sum(rate(http_requests_total{path=\"/api/v1/submit\",status=~\"2..\"}[1m]))",$submit_qps
jobs_per_sec,"sum(rate(worker_task_total[1m]))",$jobs_s
e2e_p95_ms,"1000*histogram_quantile(0.95, sum(rate(worker_task_duration_seconds_bucket[5m])) by (le))",$p95_ms
e2e_p99_ms,"1000*histogram_quantile(0.99, sum(rate(worker_task_duration_seconds_bucket[5m])) by (le))",$p99_ms
CSV

  cat >"$MD_FILE" <<MD
# Resume Metrics Snapshot

- generated_at: $(date -u +"%Y-%m-%dT%H:%M:%SZ")
- prom_base: \`$PROM_BASE\`
- api_base: \`$API_BASE\`

## Core Throughput

| Metric | Value |
|---|---|
| submit_qps | $submit_qps |
| jobs_per_sec | $jobs_s |
| e2e_p95_ms | $p95_ms |
| e2e_p99_ms | $p99_ms |
| submit_qps_peak_5m | $submit_qps_peak |
| jobs_per_sec_peak_5m | $jobs_s_peak |

## Reliability / Queue

| Metric | Value |
|---|---|
| stream_lag_peak_5m | $stream_lag_peak |
| stream_backlog_peak_5m | $stream_backlog_peak |
| outbox_pending_peak_5m | $outbox_pending_peak |
| outbox_loss_rate | $outbox_loss_rate |
| outbox_duplicate_rate | $outbox_dup_rate |
| outbox_rto_p95_ms | $outbox_rto_p95 |
| outbox_rto_p99_ms | $outbox_rto_p99 |
| outbox_rto_max_ms | $outbox_rto_max |

## Load / Cleanup / Coverage

| Metric | Value |
|---|---|
| success_ids_count | $success_ids |
| db_submissions_total | $db_submissions |
| load_run_hours | $run_hours |
| process_cleanup_leaked | $leak_flag |
| metric_names_count | $metric_count |
| go_coverage_total | $go_cov |
| cpp_coverage_total | $cpp_cov |
| scale_factor | $scale_factor |

## Resume Fill Hints

- 压测时长（小时）：$run_hours
- 累计任务数（优先 success_ids_count）：$success_ids
- Jobs/s（峰值可取 jobs_per_sec_peak_5m）：$jobs_s_peak
- submit QPS（峰值）：$submit_qps_peak
- E2E P95/P99（ms）：$p95_ms / $p99_ms
- Outbox 丢失率 / 重复率 / RTO P95：$outbox_loss_rate / $outbox_dup_rate / $outbox_rto_p95
- 进程清理泄漏标记（0=未泄漏）：$leak_flag
MD
}

for cmd in curl python3 awk sed wc date; do
  require_cmd "$cmd"
done

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi

mkdir -p "$OUT_DIR"

submit_qps="$(prom_query_scalar 'sum(rate(http_requests_total{path="/api/v1/submit",status=~"2.."}[1m]))' 'NA')"
jobs_per_sec="$(prom_query_scalar 'sum(rate(worker_task_total[1m]))' 'NA')"
e2e_p95_ms="$(prom_query_scalar '1000*histogram_quantile(0.95, sum(rate(worker_task_duration_seconds_bucket[5m])) by (le))' 'NA')"
e2e_p99_ms="$(prom_query_scalar '1000*histogram_quantile(0.99, sum(rate(worker_task_duration_seconds_bucket[5m])) by (le))' 'NA')"

submit_qps_peak_5m="$(prom_query_scalar 'max_over_time((sum(rate(http_requests_total{path="/api/v1/submit",status=~"2.."}[1m])))[5m:30s])' 'NA')"
jobs_per_sec_peak_5m="$(prom_query_scalar 'max_over_time((sum(rate(worker_task_total[1m])))[5m:30s])' 'NA')"
stream_lag_peak_5m="$(prom_query_scalar 'max_over_time(stream_lag[5m])' 'NA')"
stream_backlog_peak_5m="$(prom_query_scalar 'max_over_time(stream_backlog[5m])' 'NA')"
outbox_pending_peak_5m="$(prom_query_scalar 'max_over_time(api_outbox_pending[5m])' 'NA')"
worker_stream_inflight_peak_5m="$(prom_query_scalar 'max_over_time(worker_stream_inflight[5m])' 'NA')"

metric_names_count="$(collect_metric_names_count)"
db_submissions_total="$(db_submission_count)"
success_ids_count="$(maybe_count_lines "$SUCCESS_IDS_FILE")"

load_run_hours="NA"
if [[ "$LOAD_RUN_SECONDS" =~ ^[0-9]+$ ]]; then
  load_run_hours="$(python3 - "$LOAD_RUN_SECONDS" <<'PY'
import sys
seconds = int(sys.argv[1])
print(f"{seconds/3600:.4f}")
PY
)"
fi

IFS=',' read -r outbox_loss_rate outbox_duplicate_rate outbox_rto_p95_ms outbox_rto_p99_ms outbox_rto_max_ms \
  <<<"$(extract_outbox_summary "$OUTBOX_SUMMARY_FILE")"

process_cleanup_leaked="$(extract_process_cleanup_leak_flag "$PROCESS_CLEANUP_LOG_FILE")"
go_coverage_total="$(extract_go_coverage_pct "$GO_COVERAGE_FUNC_FILE")"
cpp_coverage_total="$(extract_cpp_coverage_pct "$CPP_COVERAGE_SUMMARY_FILE")"
scale_factor="$(calc_scale_factor "$BASELINE_JOBS_PER_SEC" "$SCALED_JOBS_PER_SEC")"

write_outputs \
  "$submit_qps" "$jobs_per_sec" "$e2e_p95_ms" "$e2e_p99_ms" \
  "$submit_qps_peak_5m" "$jobs_per_sec_peak_5m" \
  "$stream_lag_peak_5m" "$stream_backlog_peak_5m" "$outbox_pending_peak_5m" "$worker_stream_inflight_peak_5m" \
  "$metric_names_count" "$db_submissions_total" "$success_ids_count" "$load_run_hours" \
  "$outbox_loss_rate" "$outbox_duplicate_rate" "$outbox_rto_p95_ms" "$outbox_rto_p99_ms" "$outbox_rto_max_ms" \
  "$process_cleanup_leaked" "$go_coverage_total" "$cpp_coverage_total" "$scale_factor"

echo "resume_metrics_csv=$CSV_FILE"
echo "resume_metrics_md=$MD_FILE"
echo "prom_snapshot_csv=$PROM_SNAPSHOT_CSV"
