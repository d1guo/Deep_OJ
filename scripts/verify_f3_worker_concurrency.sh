#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

API_BASE="${API_BASE:-http://127.0.0.1:18080}"
USERNAME="${USERNAME:-admin}"
PASSWORD="${PASSWORD:-password}"
EMAIL="${EMAIL:-${USERNAME}@example.com}"
WORKER_POOL_SIZE_REQUESTED="${WORKER_POOL_SIZE_REQUESTED:-2}"
WORKER_METRICS_PORT="${WORKER_METRICS_PORT:-9092}"
WORKER_MINIO_ENDPOINT="${WORKER_MINIO_ENDPOINT:-oj-minio:9000}"
API_JWT_SECRET="${API_JWT_SECRET:-verify-f3-jwt-secret}"
SUBMIT_MULTIPLIER="${SUBMIT_MULTIPLIER:-3}"
TIME_LIMIT_MS="${TIME_LIMIT_MS:-7000}"
MEMORY_LIMIT_KB="${MEMORY_LIMIT_KB:-65536}"
SAMPLE_WINDOW_SEC="${SAMPLE_WINDOW_SEC:-12}"
SAMPLE_INTERVAL_MS="${SAMPLE_INTERVAL_MS:-50}"
COMPOSE_BUILD="${COMPOSE_BUILD:-1}"
LOG_TAIL_LINES="${LOG_TAIL_LINES:-200}"
KEEP_TMP="${KEEP_TMP:-0}"

TMP_DIR="$(mktemp -d)"
LAST_STATUS=""
LAST_BODY_FILE=""
LAST_HEADERS_FILE=""
METRICS_SNIPPET_FILE=""

COMPOSE_FILES=(-f docker-compose.yml)
if [[ -f docker-compose.override.yml ]]; then
  COMPOSE_FILES+=(-f docker-compose.override.yml)
fi
RUNTIME_OVERRIDE_FILE="$TMP_DIR/verify_f3.runtime.override.yml"
cat >"$RUNTIME_OVERRIDE_FILE" <<YAML
services:
  api:
    environment:
      JWT_SECRET: "${API_JWT_SECRET}"
      ADMIN_USERS: "${USERNAME}"
  worker:
    environment:
      WORKER_POOL_SIZE: "${WORKER_POOL_SIZE_REQUESTED}"
      WORKER_METRICS_PORT: "${WORKER_METRICS_PORT}"
      MINIO_ENDPOINT: "${WORKER_MINIO_ENDPOINT}"
YAML
COMPOSE_FILES+=(-f "$RUNTIME_OVERRIDE_FILE")

cleanup() {
  if [[ "$KEEP_TMP" == "1" ]]; then
    echo "KEEP_TMP=1, retain tmp directory: $TMP_DIR" >&2
    return
  fi
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT

compose_cmd() {
  docker compose "${COMPOSE_FILES[@]}" "$@"
}

print_body_snippet() {
  local file="$1"
  if [[ ! -f "$file" || ! -s "$file" ]]; then
    echo "<empty>"
    return
  fi
  local size
  size="$(wc -c < "$file" | tr -d ' ')"
  if [[ "$size" -le 2048 ]]; then
    cat "$file"
    return
  fi
  head -c 2048 "$file"
  echo
  echo "... [truncated, total ${size} bytes]"
}

emit_diagnostics() {
  echo "[compose ps]" >&2
  compose_cmd ps >&2 || true
  echo "[worker logs tail]" >&2
  compose_cmd logs --tail="$LOG_TAIL_LINES" worker >&2 || true
  if [[ -n "${METRICS_SNIPPET_FILE:-}" ]]; then
    echo "[metrics snippet <=2KB]" >&2
    print_body_snippet "$METRICS_SNIPPET_FILE" >&2 || true
  fi
}

fatal() {
  local message="$1"
  echo "ERROR: $message" >&2
  emit_diagnostics
  exit 1
}

require_cmd() {
  local cmd="$1"
  if ! command -v "$cmd" >/dev/null 2>&1; then
    fatal "missing command: $cmd"
  fi
}

http_request() {
  local method="$1"
  local url="$2"
  local expected_csv="$3"
  shift 3

  local body_file="$TMP_DIR/http_body_$(date +%s%N).txt"
  local headers_file="$TMP_DIR/http_headers_$(date +%s%N).txt"
  local status
  if ! status="$(curl -sS -X "$method" "$url" -D "$headers_file" -o "$body_file" -w '%{http_code}' "$@")"; then
    LAST_STATUS="${status:-<none>}"
    LAST_BODY_FILE="$body_file"
    LAST_HEADERS_FILE="$headers_file"
    fatal "request failed: $method $url"
  fi

  LAST_STATUS="$status"
  LAST_BODY_FILE="$body_file"
  LAST_HEADERS_FILE="$headers_file"

  local matched=0
  local expected
  IFS=',' read -r -a expected <<<"$expected_csv"
  for code in "${expected[@]}"; do
    if [[ "$status" == "$code" ]]; then
      matched=1
      break
    fi
  done
  if [[ "$matched" -ne 1 ]]; then
    echo "unexpected HTTP status: method=$method url=$url expected=$expected_csv actual=$status" >&2
    echo "response body (<=2KB):" >&2
    print_body_snippet "$body_file" >&2
    fatal "http assertion failed"
  fi
}

json_field() {
  local field_path="$1"
  python3 -c '
import json
import sys

field = sys.argv[1]
obj = json.loads(sys.stdin.read())
cur = obj
for part in field.split("."):
    if isinstance(cur, dict) and part in cur:
        cur = cur[part]
    else:
        raise KeyError(field)
if cur is None:
    raise KeyError(field)
if isinstance(cur, (dict, list)):
    print(json.dumps(cur, ensure_ascii=False, separators=(",", ":")))
else:
    print(cur)
' "$field_path"
}

wait_api_ready() {
  local deadline=$(( $(date +%s) + 120 ))
  local consecutive_ok=0
  while true; do
    if curl -sS -o /dev/null -w '%{http_code}' "$API_BASE/api/v1/health" | grep -q '^200$'; then
      consecutive_ok=$(( consecutive_ok + 1 ))
      if [[ "$consecutive_ok" -ge 3 ]]; then
        return 0
      fi
    else
      consecutive_ok=0
    fi
    if [[ $(date +%s) -ge "$deadline" ]]; then
      return 1
    fi
    sleep 1
  done
}

login_with_retry() {
  local body="$1"
  local retries=15
  local delay_sec=1
  local attempt

  for attempt in $(seq 1 "$retries"); do
    local body_file="$TMP_DIR/login_body_$(date +%s%N).txt"
    local headers_file="$TMP_DIR/login_headers_$(date +%s%N).txt"
    local status=""
    if status="$(curl -sS -X POST "$API_BASE/api/v1/auth/login" -H 'Content-Type: application/json' -D "$headers_file" -o "$body_file" -w '%{http_code}' -d "$body")"; then
      LAST_STATUS="$status"
      LAST_BODY_FILE="$body_file"
      LAST_HEADERS_FILE="$headers_file"
      if [[ "$status" == "200" ]]; then
        return 0
      fi
      if [[ "$status" == "401" || "$status" == "403" ]]; then
        return 1
      fi
    fi
    sleep "$delay_sec"
  done
  return 1
}

find_container_by_service() {
  local service="$1"
  docker ps -a --filter "label=com.docker.compose.service=${service}" --format '{{.Names}}' | head -n1
}

fetch_worker_metrics() {
  local worker_container="$1"
  local out_file="$2"
  local raw_file="$TMP_DIR/worker_metrics_raw.txt"

  if ! docker exec "$worker_container" bash -lc "set -euo pipefail; exec 3<>/dev/tcp/127.0.0.1/${WORKER_METRICS_PORT}; printf 'GET /metrics HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n' >&3; cat <&3" >"$raw_file" 2>/dev/null; then
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
    if len(parts) == 2:
        headers, body = parts
    else:
        headers, body = "", raw

status_line = headers.splitlines()[0] if headers else ""
if status_line and " 200 " not in status_line:
    raise SystemExit(2)

out.write_text(body, encoding="utf-8")
PY
}

metric_max_value() {
  local metric_name="$1"
  local metric_file="$2"
  python3 - "$metric_name" "$metric_file" <<'PY'
import pathlib
import re
import sys

name = re.escape(sys.argv[1])
text = pathlib.Path(sys.argv[2]).read_text(encoding="utf-8", errors="replace")
pattern = re.compile(rf"^{name}(?:\{{[^}}]*\}})?\s+([-+]?[0-9]*\.?[0-9]+)$")
values = []
for line in text.splitlines():
    m = pattern.match(line.strip())
    if m:
        values.append(float(m.group(1)))
if not values:
    print("")
else:
    print(max(values))
PY
}

metric_counter_by_source() {
  local metric_file="$1"
  local source="$2"
  python3 - "$metric_file" "$source" <<'PY'
import pathlib
import re
import sys

text = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8", errors="replace")
source = re.escape(sys.argv[2])
pattern = re.compile(rf'^worker_stream_backpressure_total\{{[^}}]*source="{source}"[^}}]*\}}\s+([-+]?[0-9]*\.?[0-9]+)$')
value = 0.0
for line in text.splitlines():
    m = pattern.match(line.strip())
    if m:
        value = float(m.group(1))
print(value)
PY
}

float_max() {
  python3 - "$1" "$2" <<'PY'
import sys
a = float(sys.argv[1])
b = float(sys.argv[2])
print(max(a, b))
PY
}

float_sub() {
  python3 - "$1" "$2" <<'PY'
import sys
left = float(sys.argv[1])
right = float(sys.argv[2])
print(left - right)
PY
}

sample_metrics_during_load() {
  local worker_container="$1"
  local output_dir="$2"
  local rounds
  local sleep_sec
  local max_seen="0"
  local max_exec_seen="0"
  local max_stream_seen="0"
  local successful_samples=0
  local inflight=""
  local stream_inflight=""

  rounds=$(( SAMPLE_WINDOW_SEC * 1000 / SAMPLE_INTERVAL_MS ))
  if [[ "$rounds" -lt 1 ]]; then
    rounds=1
  fi

  sleep_sec="$(python3 - "$SAMPLE_INTERVAL_MS" <<'PY'
import sys
ms = int(sys.argv[1])
print(f"{ms/1000:.3f}")
PY
)"

  for i in $(seq 1 "$rounds"); do
    local metrics_file="$output_dir/worker_metrics_${i}.txt"
    if fetch_worker_metrics "$worker_container" "$metrics_file"; then
      successful_samples=$(( successful_samples + 1 ))
      cp "$metrics_file" "$output_dir/latest_metrics.txt"
      inflight="$(metric_max_value "exec_inflight" "$metrics_file")"
      stream_inflight="$(metric_max_value "worker_stream_inflight" "$metrics_file")"
      if [[ -n "$inflight" ]]; then
        max_exec_seen="$(float_max "$max_exec_seen" "$inflight")"
        max_seen="$(float_max "$max_seen" "$inflight")"
      fi
      if [[ -n "$stream_inflight" ]]; then
        max_stream_seen="$(float_max "$max_stream_seen" "$stream_inflight")"
        max_seen="$(float_max "$max_seen" "$stream_inflight")"
      fi
    fi
    sleep "$sleep_sec"
  done

  echo "$max_seen" > "$output_dir/max_inflight_seen.txt"
  echo "$max_exec_seen" > "$output_dir/max_exec_inflight_seen.txt"
  echo "$max_stream_seen" > "$output_dir/max_stream_inflight_seen.txt"
  if [[ "$successful_samples" -lt 1 ]]; then
    return 1
  fi
}

float_gt() {
  python3 - "$1" "$2" <<'PY'
import sys
left = float(sys.argv[1])
right = float(sys.argv[2])
print("1" if left > right else "0")
PY
}

for cmd in curl python3 docker bash; do
  require_cmd "$cmd"
done

if ! [[ "$WORKER_POOL_SIZE_REQUESTED" =~ ^[0-9]+$ ]]; then
  fatal "WORKER_POOL_SIZE_REQUESTED must be a positive integer"
fi

echo "[1/7] start services"
if [[ "$COMPOSE_BUILD" == "1" ]]; then
  compose_cmd up -d --build || fatal "docker compose up -d --build failed"
else
  compose_cmd up -d || fatal "docker compose up -d failed"
fi

wait_api_ready || fatal "api did not become ready in time"

worker_container="$(find_container_by_service worker)"
if [[ -z "$worker_container" ]]; then
  fatal "failed to locate worker container"
fi
if [[ "$(docker inspect -f '{{.State.Running}}' "$worker_container" 2>/dev/null || true)" != "true" ]]; then
  fatal "worker container is not running: $worker_container"
fi

cpu_count="$(docker exec "$worker_container" sh -lc 'nproc 2>/dev/null || getconf _NPROCESSORS_ONLN 2>/dev/null || echo 1' | tr -d '[:space:]')"
if ! [[ "$cpu_count" =~ ^[0-9]+$ ]]; then
  fatal "invalid cpu_count from worker container: $cpu_count"
fi
if [[ "$cpu_count" -lt 1 ]]; then
  cpu_count=1
fi

requested="$WORKER_POOL_SIZE_REQUESTED"
if [[ "$requested" -lt 1 ]]; then
  requested=1
fi
max_concurrency="$requested"
if [[ "$max_concurrency" -gt "$cpu_count" ]]; then
  max_concurrency="$cpu_count"
fi
submit_total=$(( max_concurrency * SUBMIT_MULTIPLIER ))
if [[ "$submit_total" -lt 1 ]]; then
  submit_total=1
fi

echo "[2/7] register and login"
register_body="$(USERNAME="$USERNAME" PASSWORD="$PASSWORD" EMAIL="$EMAIL" python3 - <<'PY'
import json
import os
print(json.dumps({"username": os.environ["USERNAME"], "password": os.environ["PASSWORD"], "email": os.environ["EMAIL"]}, ensure_ascii=False))
PY
)"
http_request POST "$API_BASE/api/v1/auth/register" "201,409" -H 'Content-Type: application/json' -d "$register_body"

login_body="$(USERNAME="$USERNAME" PASSWORD="$PASSWORD" python3 - <<'PY'
import json
import os
print(json.dumps({"username": os.environ["USERNAME"], "password": os.environ["PASSWORD"]}, ensure_ascii=False))
PY
)"
if ! login_with_retry "$login_body"; then
  fatal "failed to login after retries"
fi
token="$(cat "$LAST_BODY_FILE" | json_field token 2>/dev/null || true)"
if [[ -z "$token" ]]; then
  fatal "failed to parse token from login response"
fi

echo "[3/7] upload testcase package"
problem_zip="$TMP_DIR/problem.zip"
python3 - "$problem_zip" <<'PY'
import sys
import zipfile

with zipfile.ZipFile(sys.argv[1], "w", compression=zipfile.ZIP_DEFLATED) as zf:
    zf.writestr("1.in", "1 2\n")
    zf.writestr("1.out", "3\n")
PY
http_request POST "$API_BASE/api/v1/problems" "200" \
  -H "Authorization: Bearer $token" \
  -F 'title=F3 Concurrency Verify' \
  -F "time_limit=${TIME_LIMIT_MS}" \
  -F 'memory_limit=128' \
  -F "file=@$problem_zip"
problem_id="$(cat "$LAST_BODY_FILE" | json_field id 2>/dev/null || true)"
if ! [[ "$problem_id" =~ ^[0-9]+$ ]]; then
  fatal "invalid problem id from upload response: $problem_id"
fi

echo "[4/7] submit long-running tasks: count=$submit_total max_concurrency=$max_concurrency"
echo "[5/7] sample worker metrics during load"
sample_dir="$TMP_DIR/metrics_samples"
mkdir -p "$sample_dir"
baseline_metrics_file="$sample_dir/baseline_metrics.txt"
fetch_worker_metrics "$worker_container" "$baseline_metrics_file" || fatal "failed to fetch baseline worker metrics"
baseline_read_pause_total="$(metric_counter_by_source "$baseline_metrics_file" "read_new")"
baseline_reclaim_pause_total="$(metric_counter_by_source "$baseline_metrics_file" "reclaim")"
sample_metrics_during_load "$worker_container" "$sample_dir" &
sampler_pid=$!

for i in $(seq 1 "$submit_total"); do
  source_code="$(cat <<CPP
#include <cstdint>
int main() {
  volatile std::uint64_t x = ${i};
  while (true) {
    x = x * 1664525ULL + 1013904223ULL;
  }
  return 0;
}
CPP
)"
  submit_body="$(PROBLEM_ID="$problem_id" SOURCE_CODE="$source_code" TIME_LIMIT_MS="$TIME_LIMIT_MS" MEMORY_LIMIT_KB="$MEMORY_LIMIT_KB" python3 - <<'PY'
import json
import os
print(json.dumps({
    "problem_id": int(os.environ["PROBLEM_ID"]),
    "language": 1,
    "code": os.environ["SOURCE_CODE"],
    "time_limit": int(os.environ["TIME_LIMIT_MS"]),
    "memory_limit": int(os.environ["MEMORY_LIMIT_KB"]),
}, ensure_ascii=False))
PY
)"
  http_request POST "$API_BASE/api/v1/submit" "200" \
    -H "Authorization: Bearer $token" \
    -H 'Content-Type: application/json' \
    -d "$submit_body"
done

if ! wait "$sampler_pid"; then
  fatal "failed to sample worker metrics during load"
fi
METRICS_SNIPPET_FILE="$sample_dir/latest_metrics.txt"
if [[ ! -f "$METRICS_SNIPPET_FILE" ]]; then
  fatal "metrics sample output is missing"
fi
max_inflight_seen="$(cat "$sample_dir/max_inflight_seen.txt" 2>/dev/null || true)"
max_exec_inflight_seen="$(cat "$sample_dir/max_exec_inflight_seen.txt" 2>/dev/null || true)"
max_stream_inflight_seen="$(cat "$sample_dir/max_stream_inflight_seen.txt" 2>/dev/null || true)"
if [[ -z "$max_inflight_seen" ]]; then
  fatal "failed to read max inflight from sampling results"
fi

echo "[6/7] verify limits and backpressure counters"
if [[ "$(float_gt "$max_inflight_seen" "$max_concurrency")" == "1" ]]; then
  fatal "max inflight exceeds max_concurrency: max_inflight_seen=$max_inflight_seen max_concurrency=$max_concurrency"
fi
if [[ "$(float_gt "0.1" "$max_inflight_seen")" == "1" ]]; then
  fatal "max inflight did not rise above zero, workload may not have been executed: max_inflight_seen=$max_inflight_seen max_exec_inflight_seen=${max_exec_inflight_seen:-0} max_stream_inflight_seen=${max_stream_inflight_seen:-0}"
fi

read_pause_total="$(metric_counter_by_source "$METRICS_SNIPPET_FILE" "read_new")"
reclaim_pause_total="$(metric_counter_by_source "$METRICS_SNIPPET_FILE" "reclaim")"
read_pause_delta="$(float_sub "$read_pause_total" "$baseline_read_pause_total")"
reclaim_pause_delta="$(float_sub "$reclaim_pause_total" "$baseline_reclaim_pause_total")"
if [[ "$(float_gt "$read_pause_delta" "0")" != "1" && "$(float_gt "$reclaim_pause_delta" "0")" != "1" ]]; then
  fatal "backpressure counters did not increase in this run: read_new_delta=$read_pause_delta reclaim_delta=$reclaim_pause_delta (baseline_read_new=$baseline_read_pause_total baseline_reclaim=$baseline_reclaim_pause_total final_read_new=$read_pause_total final_reclaim=$reclaim_pause_total)"
fi

echo "[7/7] evidence"
echo "EVIDENCE_F3_LIMIT: max_concurrency=${max_concurrency} max_inflight_seen=${max_inflight_seen} max_exec_inflight_seen=${max_exec_inflight_seen:-0} max_stream_inflight_seen=${max_stream_inflight_seen:-0}"
echo "EVIDENCE_F3_BACKPRESSURE: read_pause_delta=${read_pause_delta} reclaim_pause_delta=${reclaim_pause_delta}"
