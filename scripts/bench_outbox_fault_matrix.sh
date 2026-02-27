#!/usr/bin/env bash
set -euo pipefail

# 脚本用途：
# 1) 对 outbox 链路执行可复现故障注入（Redis 重启 / Dispatcher 崩溃 / Redis 抖动）。
# 2) 统计每轮 job 的投递恢复耗时（RTO）、丢失率、重复率。
# 3) 产出 CSV/JSON/Markdown，总结可直接填入简历。

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

API_BASE="${API_BASE:-http://127.0.0.1:18080}"
USERNAME="${USERNAME:-admin}"
PASSWORD="${PASSWORD:-password}"
EMAIL="${EMAIL:-${USERNAME}@example.com}"
TOKEN="${TOKEN:-}"
PROBLEM_ID="${PROBLEM_ID:-}"

POSTGRES_CONTAINER="${POSTGRES_CONTAINER:-oj-postgres}"
POSTGRES_DB="${POSTGRES_DB:-deep_oj}"
POSTGRES_USER="${POSTGRES_USER:-deep_oj}"
POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-}"

REDIS_CONTAINER="${REDIS_CONTAINER:-oj-redis}"
REDIS_PASSWORD="${REDIS_PASSWORD:-}"
STREAM_KEY="${JOB_STREAM_KEY:-deepoj:jobs}"
STREAM_SCAN_COUNT="${STREAM_SCAN_COUNT:-50000}"

API_CONTAINER="${API_CONTAINER:-oj-api}"

REDIS_RESTART_ROUNDS="${REDIS_RESTART_ROUNDS:-5}"
DISPATCHER_CRASH_ROUNDS="${DISPATCHER_CRASH_ROUNDS:-5}"
REDIS_JITTER_ROUNDS="${REDIS_JITTER_ROUNDS:-5}"

REDIS_OUTAGE_SEC="${REDIS_OUTAGE_SEC:-5}"
DISPATCHER_DOWNTIME_SEC="${DISPATCHER_DOWNTIME_SEC:-5}"
REDIS_JITTER_PAUSE_SEC="${REDIS_JITTER_PAUSE_SEC:-2}"

WAIT_DISPATCH_SEC="${WAIT_DISPATCH_SEC:-120}"
WAIT_TERMINAL_SEC="${WAIT_TERMINAL_SEC:-120}"
HTTP_MAX_TIME_SEC="${HTTP_MAX_TIME_SEC:-20}"
KEEP_TMP="${KEEP_TMP:-0}"

STAMP="$(date +%Y%m%d_%H%M%S)"
OUT_DIR="${OUT_DIR:-$ROOT_DIR/artifacts/resume/outbox_fault_matrix_${STAMP}}"
TMP_DIR="$(mktemp -d)"
CSV_FILE="$OUT_DIR/results.csv"
SUMMARY_JSON="$OUT_DIR/summary.json"
SUMMARY_MD="$OUT_DIR/summary.md"

LAST_HTTP_STATUS="<none>"
LAST_BODY_FILE=""
LAST_HEADERS_FILE=""

usage() {
  cat <<'USAGE'
Usage: scripts/bench_outbox_fault_matrix.sh

Required env:
  POSTGRES_PASSWORD       PostgreSQL password
  REDIS_PASSWORD          Redis password

Optional env:
  API_BASE                default: http://127.0.0.1:18080
  USERNAME/PASSWORD/EMAIL default: admin/password/admin@example.com
  TOKEN                   pre-existing JWT (skip register/login)
  PROBLEM_ID              existing problem id (skip auto upload)

  REDIS_RESTART_ROUNDS    default: 5
  DISPATCHER_CRASH_ROUNDS default: 5
  REDIS_JITTER_ROUNDS     default: 5

  REDIS_OUTAGE_SEC        default: 5
  DISPATCHER_DOWNTIME_SEC default: 5
  REDIS_JITTER_PAUSE_SEC  default: 2

  WAIT_DISPATCH_SEC       default: 120
  WAIT_TERMINAL_SEC       default: 120
  STREAM_SCAN_COUNT       default: 50000
  OUT_DIR                 default: artifacts/resume/outbox_fault_matrix_<timestamp>
USAGE
}

cleanup() {
  docker unpause "$REDIS_CONTAINER" >/dev/null 2>&1 || true
  docker start "$REDIS_CONTAINER" >/dev/null 2>&1 || true
  docker start "$API_CONTAINER" >/dev/null 2>&1 || true
  if [[ "$KEEP_TMP" == "1" ]]; then
    echo "KEEP_TMP=1, tmp_dir=$TMP_DIR" >&2
    return
  fi
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT

fatal() {
  local msg="$1"
  echo "ERROR: $msg" >&2
  if [[ -n "${LAST_BODY_FILE:-}" && -f "$LAST_BODY_FILE" ]]; then
    echo "last_http_status=$LAST_HTTP_STATUS" >&2
    echo "last_http_body(<=2KB):" >&2
    head -c 2048 "$LAST_BODY_FILE" >&2 || true
    echo >&2
  fi
  exit 1
}

require_cmd() {
  local cmd="$1"
  command -v "$cmd" >/dev/null 2>&1 || fatal "missing command: $cmd"
}

now_ms() {
  local ms
  ms="$(date +%s%3N 2>/dev/null || true)"
  if [[ "$ms" =~ ^[0-9]+$ ]]; then
    printf '%s\n' "$ms"
    return
  fi
  printf '%s\n' "$(( $(date +%s) * 1000 ))"
}

sql_escape() {
  printf '%s' "$1" | sed "s/'/''/g"
}

psql_query() {
  local query="$1"
  docker exec -e PGPASSWORD="$POSTGRES_PASSWORD" -i "$POSTGRES_CONTAINER" \
    psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -t -A -F $'\t' \
    -c "$query"
}

http_request() {
  local method="$1"
  local url="$2"
  local expected_csv="$3"
  shift 3

  local body_file="$TMP_DIR/http_body_$(date +%s%N).txt"
  local headers_file="$TMP_DIR/http_headers_$(date +%s%N).txt"
  local status

  if ! status="$(curl -sS --max-time "$HTTP_MAX_TIME_SEC" -X "$method" "$url" \
    -D "$headers_file" -o "$body_file" -w '%{http_code}' "$@")"; then
    LAST_HTTP_STATUS="${status:-<none>}"
    LAST_BODY_FILE="$body_file"
    LAST_HEADERS_FILE="$headers_file"
    fatal "request failed: $method $url"
  fi

  LAST_HTTP_STATUS="$status"
  LAST_BODY_FILE="$body_file"
  LAST_HEADERS_FILE="$headers_file"

  local matched=0
  local code
  IFS=',' read -r -a codes <<<"$expected_csv"
  for code in "${codes[@]}"; do
    if [[ "$status" == "$code" ]]; then
      matched=1
      break
    fi
  done
  if [[ "$matched" -ne 1 ]]; then
    fatal "unexpected status for $method $url, expected=$expected_csv actual=$status"
  fi
}

json_field() {
  local field="$1"
  python3 - "$field" <<'PY'
import json
import sys

field = sys.argv[1]
obj = json.loads(sys.stdin.read())
cur = obj
for part in field.split("."):
    if isinstance(cur, dict) and part in cur:
        cur = cur[part]
    else:
        raise SystemExit(2)
if isinstance(cur, (dict, list)):
    print(json.dumps(cur, ensure_ascii=False, separators=(",", ":")))
else:
    print(cur)
PY
}

wait_api_ready() {
  local deadline=$(( $(date +%s) + 120 ))
  while true; do
    if curl -sS --max-time 5 -o /dev/null -w '%{http_code}' "$API_BASE/api/v1/health" | grep -q '^200$'; then
      return 0
    fi
    if [[ $(date +%s) -ge "$deadline" ]]; then
      return 1
    fi
    sleep 1
  done
}

wait_redis_ready() {
  local deadline=$(( $(date +%s) + 120 ))
  while true; do
    if docker exec -i "$REDIS_CONTAINER" redis-cli -a "$REDIS_PASSWORD" --raw PING 2>/dev/null | grep -q '^PONG$'; then
      return 0
    fi
    if [[ $(date +%s) -ge "$deadline" ]]; then
      return 1
    fi
    sleep 1
  done
}

register_and_login() {
  if [[ -n "$TOKEN" ]]; then
    return 0
  fi

  local register_payload
  register_payload="$(USERNAME="$USERNAME" PASSWORD="$PASSWORD" EMAIL="$EMAIL" python3 - <<'PY'
import json
import os
print(json.dumps({
    "username": os.environ["USERNAME"],
    "password": os.environ["PASSWORD"],
    "email": os.environ["EMAIL"],
}, ensure_ascii=False))
PY
)"

  http_request POST "$API_BASE/api/v1/auth/register" "201,409,429" \
    -H 'Content-Type: application/json' \
    -d "$register_payload"

  local login_payload
  login_payload="$(USERNAME="$USERNAME" PASSWORD="$PASSWORD" python3 - <<'PY'
import json
import os
print(json.dumps({
    "username": os.environ["USERNAME"],
    "password": os.environ["PASSWORD"],
}, ensure_ascii=False))
PY
)"

  local attempt
  for attempt in $(seq 1 90); do
    http_request POST "$API_BASE/api/v1/auth/login" "200,429" \
      -H 'Content-Type: application/json' \
      -d "$login_payload"
    if [[ "$LAST_HTTP_STATUS" == "200" ]]; then
      TOKEN="$(cat "$LAST_BODY_FILE" | json_field token 2>/dev/null || true)"
      [[ -n "$TOKEN" ]] || fatal "login succeeded but token missing"
      return 0
    fi
    sleep 1
  done
  fatal "login failed after retries"
}

ensure_problem() {
  if [[ -n "$PROBLEM_ID" ]]; then
    return 0
  fi

  local zip_file="$TMP_DIR/problem.zip"
  python3 - "$zip_file" <<'PY'
import sys
import zipfile
with zipfile.ZipFile(sys.argv[1], "w", compression=zipfile.ZIP_DEFLATED) as zf:
    zf.writestr("1.in", "1 2\n")
    zf.writestr("1.out", "3\n")
PY

  http_request POST "$API_BASE/api/v1/problems" "200" \
    -H "Authorization: Bearer $TOKEN" \
    -F "title=Outbox Fault Matrix ${STAMP}" \
    -F "time_limit=1000" \
    -F "memory_limit=128" \
    -F "file=@${zip_file}"
  PROBLEM_ID="$(cat "$LAST_BODY_FILE" | json_field id 2>/dev/null || true)"
  [[ "$PROBLEM_ID" =~ ^[0-9]+$ ]] || fatal "invalid problem_id: ${PROBLEM_ID:-<empty>}"
}

submit_job() {
  local source_code
  source_code="$(cat <<'CPP'
#include <iostream>
int main(){int a,b;std::cin>>a>>b;std::cout<<(a+b)<<"\n";return 0;}
CPP
)"
  local body
  body="$(PROBLEM_ID="$PROBLEM_ID" SOURCE_CODE="$source_code" python3 - <<'PY'
import json
import os
print(json.dumps({
    "problem_id": int(os.environ["PROBLEM_ID"]),
    "language": 1,
    "code": os.environ["SOURCE_CODE"],
    "time_limit": 1000,
    "memory_limit": 65536,
}, ensure_ascii=False))
PY
)"
  http_request POST "$API_BASE/api/v1/submit" "200" \
    -H "Authorization: Bearer $TOKEN" \
    -H 'Content-Type: application/json' \
    -d "$body"
  cat "$LAST_BODY_FILE" | json_field job_id 2>/dev/null || true
}

wait_outbox_delivery() {
  local job_id="$1"
  local timeout_sec="$2"
  local deadline=$(( $(date +%s) + timeout_sec ))
  local job_sql
  job_sql="$(sql_escape "$job_id")"

  OUTBOX_STREAM_KEY=""
  OUTBOX_ENTRY_ID=""
  OUTBOX_STATUS=""
  OUTBOX_DISPATCHED_MS="0"

  while true; do
    local row
    row="$(psql_query "SELECT COALESCE(stream_key,''), COALESCE(stream_entry_id,''), COALESCE(status,''), COALESCE((EXTRACT(EPOCH FROM dispatched_at)*1000)::bigint,0) FROM outbox_events WHERE job_id='${job_sql}' LIMIT 1;" | sed '/^[[:space:]]*$/d' | head -n1)"
    if [[ -n "$row" ]]; then
      OUTBOX_STREAM_KEY="$(printf '%s' "$row" | awk -F'\t' '{print $1}')"
      OUTBOX_ENTRY_ID="$(printf '%s' "$row" | awk -F'\t' '{print $2}')"
      OUTBOX_STATUS="$(printf '%s' "$row" | awk -F'\t' '{print $3}')"
      OUTBOX_DISPATCHED_MS="$(printf '%s' "$row" | awk -F'\t' '{print $4}')"
      if [[ -n "$OUTBOX_ENTRY_ID" && "$OUTBOX_DISPATCHED_MS" =~ ^[0-9]+$ && "$OUTBOX_DISPATCHED_MS" -gt 0 ]]; then
        return 0
      fi
    fi

    if [[ $(date +%s) -ge "$deadline" ]]; then
      return 1
    fi
    sleep 1
  done
}

wait_job_terminal() {
  local job_id="$1"
  local timeout_sec="$2"
  local deadline=$(( $(date +%s) + timeout_sec ))
  local status=""
  while true; do
    http_request GET "$API_BASE/api/v1/status/$job_id" "200" \
      -H "Authorization: Bearer $TOKEN"
    status="$(cat "$LAST_BODY_FILE" | json_field status 2>/dev/null || true)"
    if [[ "$status" == "Finished" || "$status" == "Accepted" || "$status" == "Wrong Answer" || "$status" == "Compile Error" || "$status" == "Runtime Error" || "$status" == "System Error" ]]; then
      printf '%s\n' "$status"
      return 0
    fi
    if [[ $(date +%s) -ge "$deadline" ]]; then
      printf '%s\n' "${status:-timeout}"
      return 1
    fi
    sleep 1
  done
}

stream_job_occurrences() {
  local stream_key="$1"
  local job_id="$2"
  local rows_file="$TMP_DIR/stream_rows_$(date +%s%N).csv"

  if ! docker exec -i "$REDIS_CONTAINER" redis-cli -a "$REDIS_PASSWORD" --csv \
    XREVRANGE "$stream_key" + - COUNT "$STREAM_SCAN_COUNT" >"$rows_file" 2>/dev/null; then
    echo "0"
    return 0
  fi

  python3 - "$rows_file" "$job_id" <<'PY'
import csv
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
target = sys.argv[2]
count = 0
for row in csv.reader(path.read_text(encoding="utf-8", errors="replace").splitlines()):
    if len(row) < 3:
        continue
    fields = row[1:]
    for i in range(0, len(fields) - 1, 2):
        if fields[i] == "job_id" and fields[i + 1] == target:
            count += 1
            break
print(count)
PY
}

run_fault_round() {
  local scenario="$1"
  local round="$2"
  local job_id=""
  local recovery_ms=""
  local dispatched_ms="0"
  local rto_ms="-1"
  local delivered="0"
  local stream_key="$STREAM_KEY"
  local stream_occurrences="0"
  local duplicate_entries="0"
  local final_status="unknown"

  case "$scenario" in
    redis_restart)
      docker stop "$REDIS_CONTAINER" >/dev/null
      job_id="$(submit_job)"
      [[ -n "$job_id" ]] || fatal "submit returned empty job_id in redis_restart round=$round"
      sleep "$REDIS_OUTAGE_SEC"
      docker start "$REDIS_CONTAINER" >/dev/null
      wait_redis_ready || fatal "redis not ready after restart, round=$round"
      recovery_ms="$(now_ms)"
      ;;
    dispatcher_crash)
      job_id="$(submit_job)"
      [[ -n "$job_id" ]] || fatal "submit returned empty job_id in dispatcher_crash round=$round"
      docker stop "$API_CONTAINER" >/dev/null
      sleep "$DISPATCHER_DOWNTIME_SEC"
      docker start "$API_CONTAINER" >/dev/null
      wait_api_ready || fatal "api not ready after dispatcher crash round=$round"
      recovery_ms="$(now_ms)"
      ;;
    redis_jitter)
      docker pause "$REDIS_CONTAINER" >/dev/null
      job_id="$(submit_job)"
      [[ -n "$job_id" ]] || fatal "submit returned empty job_id in redis_jitter round=$round"
      sleep "$REDIS_JITTER_PAUSE_SEC"
      docker unpause "$REDIS_CONTAINER" >/dev/null
      wait_redis_ready || fatal "redis not ready after unpause, round=$round"
      recovery_ms="$(now_ms)"
      ;;
    *)
      fatal "unknown scenario: $scenario"
      ;;
  esac

  if wait_outbox_delivery "$job_id" "$WAIT_DISPATCH_SEC"; then
    delivered="1"
    dispatched_ms="$OUTBOX_DISPATCHED_MS"
    if [[ "$OUTBOX_DISPATCHED_MS" =~ ^[0-9]+$ && "$recovery_ms" =~ ^[0-9]+$ ]]; then
      rto_ms=$(( OUTBOX_DISPATCHED_MS - recovery_ms ))
      if [[ "$rto_ms" -lt 0 ]]; then
        rto_ms=0
      fi
    fi
    if [[ -n "$OUTBOX_STREAM_KEY" ]]; then
      stream_key="$OUTBOX_STREAM_KEY"
    fi
    stream_occurrences="$(stream_job_occurrences "$stream_key" "$job_id")"
    if [[ "$stream_occurrences" =~ ^[0-9]+$ && "$stream_occurrences" -gt 1 ]]; then
      duplicate_entries=$(( stream_occurrences - 1 ))
    fi
  fi

  if [[ "$delivered" == "1" ]]; then
    final_status="$(wait_job_terminal "$job_id" "$WAIT_TERMINAL_SEC" || true)"
  fi

  printf '%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s\n' \
    "$scenario" "$round" "$job_id" "${recovery_ms:-0}" "$dispatched_ms" "$rto_ms" "$delivered" \
    "$stream_key" "${stream_occurrences:-0}" "$duplicate_entries" "$final_status" >>"$CSV_FILE"
}

build_summary() {
  python3 - "$CSV_FILE" "$SUMMARY_JSON" "$SUMMARY_MD" <<'PY'
import csv
import json
import pathlib
import statistics
import sys
from datetime import datetime, timezone

csv_path = pathlib.Path(sys.argv[1])
json_path = pathlib.Path(sys.argv[2])
md_path = pathlib.Path(sys.argv[3])

rows = list(csv.DictReader(csv_path.read_text(encoding="utf-8").splitlines()))
total = len(rows)
delivered = sum(1 for r in rows if r.get("delivered") == "1")
lost = total - delivered
duplicate_entries = sum(int(r.get("duplicate_entries") or "0") for r in rows)
duplicate_jobs = sum(1 for r in rows if int(r.get("duplicate_entries") or "0") > 0)
rto = [int(r["rto_ms"]) for r in rows if int(r.get("rto_ms") or "-1") >= 0]

def pct(v):
    return round(v * 100.0, 4)

def percentile(data, p):
    if not data:
        return None
    if len(data) == 1:
        return data[0]
    data = sorted(data)
    k = (len(data) - 1) * p
    f = int(k)
    c = min(f + 1, len(data) - 1)
    if f == c:
        return data[f]
    return int(round(data[f] + (data[c] - data[f]) * (k - f)))

summary = {
    "generated_at_utc": datetime.now(timezone.utc).isoformat(),
    "total_jobs": total,
    "delivered_jobs": delivered,
    "lost_jobs": lost,
    "loss_rate": 0.0 if total == 0 else lost / total,
    "duplicate_entries": duplicate_entries,
    "duplicate_jobs": duplicate_jobs,
    "duplicate_rate": 0.0 if total == 0 else duplicate_entries / total,
    "rto_ms": {
        "count": len(rto),
        "min": min(rto) if rto else None,
        "p50": percentile(rto, 0.50),
        "p95": percentile(rto, 0.95),
        "p99": percentile(rto, 0.99),
        "max": max(rto) if rto else None,
        "mean": round(statistics.fmean(rto), 3) if rto else None,
    },
}

json_path.write_text(json.dumps(summary, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")

md = [
    "# Outbox Fault Matrix Summary",
    "",
    f"- generated_at_utc: {summary['generated_at_utc']}",
    f"- total_jobs: {summary['total_jobs']}",
    f"- delivered_jobs: {summary['delivered_jobs']}",
    f"- lost_jobs: {summary['lost_jobs']}",
    f"- loss_rate: {pct(summary['loss_rate'])}%",
    f"- duplicate_entries: {summary['duplicate_entries']}",
    f"- duplicate_jobs: {summary['duplicate_jobs']}",
    f"- duplicate_rate: {pct(summary['duplicate_rate'])}%",
    "",
    "## RTO (ms)",
    "",
    f"- count: {summary['rto_ms']['count']}",
    f"- min: {summary['rto_ms']['min']}",
    f"- p50: {summary['rto_ms']['p50']}",
    f"- p95: {summary['rto_ms']['p95']}",
    f"- p99: {summary['rto_ms']['p99']}",
    f"- max: {summary['rto_ms']['max']}",
    f"- mean: {summary['rto_ms']['mean']}",
    "",
]
md_path.write_text("\n".join(md), encoding="utf-8")
PY
}

for cmd in curl python3 docker awk sed date; do
  require_cmd "$cmd"
done

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi

[[ -n "$POSTGRES_PASSWORD" ]] || fatal "POSTGRES_PASSWORD is required"
[[ -n "$REDIS_PASSWORD" ]] || fatal "REDIS_PASSWORD is required"

mkdir -p "$OUT_DIR"
cat >"$CSV_FILE" <<'CSV'
scenario,round,job_id,recovery_ms,dispatched_ms,rto_ms,delivered,stream_key,stream_occurrences,duplicate_entries,final_status
CSV

wait_api_ready || fatal "api is not ready: $API_BASE"
wait_redis_ready || fatal "redis is not ready: $REDIS_CONTAINER"
register_and_login
ensure_problem

echo "run_fault_matrix: out_dir=$OUT_DIR"
echo "problem_id=$PROBLEM_ID"
echo "rounds: redis_restart=$REDIS_RESTART_ROUNDS dispatcher_crash=$DISPATCHER_CRASH_ROUNDS redis_jitter=$REDIS_JITTER_ROUNDS"

for i in $(seq 1 "$REDIS_RESTART_ROUNDS"); do
  echo "[redis_restart] round=$i"
  run_fault_round "redis_restart" "$i"
done
for i in $(seq 1 "$DISPATCHER_CRASH_ROUNDS"); do
  echo "[dispatcher_crash] round=$i"
  run_fault_round "dispatcher_crash" "$i"
done
for i in $(seq 1 "$REDIS_JITTER_ROUNDS"); do
  echo "[redis_jitter] round=$i"
  run_fault_round "redis_jitter" "$i"
done

build_summary
echo "summary_json=$SUMMARY_JSON"
echo "summary_md=$SUMMARY_MD"
echo "rows_csv=$CSV_FILE"
cat "$SUMMARY_MD"
