#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

VERIFY_API_PORT="${VERIFY_API_PORT:-28080}"
API_BASE="${API_BASE:-http://127.0.0.1:${VERIFY_API_PORT}}"
USERNAME="${USERNAME:-admin}"
PASSWORD="${PASSWORD:-password}"
EMAIL="${EMAIL:-${USERNAME}@example.com}"
API_JWT_SECRET="${API_JWT_SECRET:-verify-g1-jwt-secret}"
WORKER_MINIO_ENDPOINT="${WORKER_MINIO_ENDPOINT:-oj-minio:9000}"
DISPATCH_RPC_TIMEOUT_MS="${DISPATCH_RPC_TIMEOUT_MS:-30000}"
COMPOSE_BUILD="${COMPOSE_BUILD:-1}"
POLL_TIMEOUT_SEC="${POLL_TIMEOUT_SEC:-180}"
POLL_INTERVAL_SEC="${POLL_INTERVAL_SEC:-1}"
RUNNING_WAIT_SEC="${RUNNING_WAIT_SEC:-90}"
PID_DISCOVERY_WAIT_SEC="${PID_DISCOVERY_WAIT_SEC:-30}"
CHILD_GONE_WAIT_SEC="${CHILD_GONE_WAIT_SEC:-3}"
LEAK_SETTLE_ROUNDS="${LEAK_SETTLE_ROUNDS:-12}"
LEAK_SETTLE_INTERVAL_SEC="${LEAK_SETTLE_INTERVAL_SEC:-1}"
LOG_TAIL_LINES="${LOG_TAIL_LINES:-200}"
KEEP_TMP="${KEEP_TMP:-0}"

TMP_DIR="$(mktemp -d)"
LAST_STATUS=""
LAST_BODY_FILE=""
LAST_HEADERS_FILE=""
WORKER_PS_FILE=""
KILL_BEFORE_FILE=""
KILL_AFTER_FILE=""
KILL_DEBUG_BEFORE_FILE=""
KILL_DEBUG_AFTER_FILE=""

# 默认 compose 保持受限 seccomp，本脚本额外叠加 verify override 仅在验收场景放开。
COMPOSE_FILES=(-f docker-compose.yml -f docker-compose.verify-g1.yml)
if [[ -f docker-compose.override.yml ]]; then
  COMPOSE_FILES+=(-f docker-compose.override.yml)
fi

RUNTIME_OVERRIDE_FILE="$TMP_DIR/verify_g1.runtime.override.yml"
cat >"$RUNTIME_OVERRIDE_FILE" <<YAML
services:
  api:
    ports:
      - "${VERIFY_API_PORT}:18080"
    environment:
      JWT_SECRET: "${API_JWT_SECRET}"
      ADMIN_USERS: "${USERNAME}"
  scheduler:
    ports: []
    environment:
      DISPATCH_RPC_TIMEOUT_MS: "${DISPATCH_RPC_TIMEOUT_MS}"
  worker:
    environment:
      MINIO_ENDPOINT: "${WORKER_MINIO_ENDPOINT}"
YAML
COMPOSE_FILES+=(-f "$RUNTIME_OVERRIDE_FILE")

cleanup() {
  if [[ "$KEEP_TMP" == "1" ]]; then
    echo "KEEP_TMP=1, tmp directory retained: $TMP_DIR" >&2
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

  if [[ -n "${WORKER_PS_FILE:-}" && -f "$WORKER_PS_FILE" ]]; then
    echo "[worker ps snippet <=2KB]" >&2
    print_body_snippet "$WORKER_PS_FILE" >&2 || true
  fi

  if [[ -n "${KILL_BEFORE_FILE:-}" && -f "$KILL_BEFORE_FILE" ]]; then
    echo "[kill target snapshot before kill <=2KB]" >&2
    print_body_snippet "$KILL_BEFORE_FILE" >&2 || true
  fi
  if [[ -n "${KILL_AFTER_FILE:-}" && -f "$KILL_AFTER_FILE" ]]; then
    echo "[kill target snapshot after kill <=2KB]" >&2
    print_body_snippet "$KILL_AFTER_FILE" >&2 || true
  fi
  if [[ -n "${KILL_DEBUG_BEFORE_FILE:-}" && -f "$KILL_DEBUG_BEFORE_FILE" ]]; then
    echo "[kill target debug before kill <=2KB]" >&2
    print_body_snippet "$KILL_DEBUG_BEFORE_FILE" >&2 || true
  fi
  if [[ -n "${KILL_DEBUG_AFTER_FILE:-}" && -f "$KILL_DEBUG_AFTER_FILE" ]]; then
    echo "[kill target debug after kill <=2KB]" >&2
    print_body_snippet "$KILL_DEBUG_AFTER_FILE" >&2 || true
  fi

  if [[ -n "${LAST_BODY_FILE:-}" && -f "$LAST_BODY_FILE" ]]; then
    echo "[last response body <=2KB]" >&2
    print_body_snippet "$LAST_BODY_FILE" >&2 || true
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
  local code
  IFS=',' read -r -a codes <<<"$expected_csv"
  for code in "${codes[@]}"; do
    if [[ "$status" == "$code" ]]; then
      matched=1
      break
    fi
  done

  if [[ "$matched" -ne 1 ]]; then
    echo "unexpected HTTP status: method=$method url=$url expected=$expected_csv actual=$status" >&2
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

wait_worker_ready() {
  local deadline=$(( $(date +%s) + 120 ))
  local worker_container=""

  while true; do
    worker_container="$(find_container_by_service worker)"
    if [[ -n "$worker_container" ]]; then
      if [[ "$(docker inspect -f '{{.State.Running}}' "$worker_container" 2>/dev/null || true)" == "true" ]]; then
        printf '%s' "$worker_container"
        return 0
      fi
      compose_cmd up -d worker >/dev/null 2>&1 || true
    else
      compose_cmd up -d worker >/dev/null 2>&1 || true
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
    sleep 1
  done

  return 1
}

find_container_by_service() {
  local service="$1"
  docker ps -a --filter "label=com.docker.compose.service=${service}" --format '{{.Names}}' | head -n1
}

collect_worker_ps() {
  local worker_container="$1"
  local out_file="$2"
  docker exec "$worker_container" sh -lc 'ps -eo pid,ppid,pgid,cmd --no-headers 2>/dev/null || ps -ef' >"$out_file" 2>/dev/null
}

count_judge_processes() {
  local ps_file="$1"
  python3 - "$ps_file" <<'PY'
import pathlib
import re
import sys

text = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8", errors="replace")
count = 0
for line in text.splitlines():
    m = re.match(r"\s*(\d+)\s+(\d+)\s+(\d+)\s+(.*)$", line)
    if not m:
        continue
    cmd = m.group(4)
    if "judge_engine" in cmd:
        count += 1
print(count)
PY
}

judge_pids_csv() {
  local ps_file="$1"
  python3 - "$ps_file" <<'PY'
import pathlib
import re
import sys

text = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8", errors="replace")
pids = []
for line in text.splitlines():
    m = re.match(r"\s*(\d+)\s+(\d+)\s+(\d+)\s+(.*)$", line)
    if not m:
        continue
    pid = m.group(1)
    cmd = m.group(4)
    if "judge_engine" in cmd:
        pids.append(pid)
print(",".join(pids))
PY
}

find_job_judge_pid() {
  local ps_file="$1"
  local job_id="$2"
  local baseline_csv="$3"
  python3 - "$ps_file" "$job_id" "$baseline_csv" <<'PY'
import pathlib
import re
import sys

text = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8", errors="replace")
job_id = sys.argv[2]
baseline = {x for x in sys.argv[3].split(",") if x}

for line in text.splitlines():
    m = re.match(r"\s*(\d+)\s+(\d+)\s+(\d+)\s+(.*)$", line)
    if not m:
        continue
    pid = m.group(1)
    cmd = m.group(4)
    if "judge_engine" not in cmd:
        continue
    if "--compile" in cmd:
        continue
    if job_id not in cmd:
        continue
    if pid in baseline:
        continue
    print(pid)
    break
PY
}

children_by_ppid_csv() {
  local ps_file="$1"
  local parent_pid="$2"
  python3 - "$ps_file" "$parent_pid" <<'PY'
import pathlib
import re
import sys

text = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8", errors="replace")
parent = sys.argv[2]
children = []
for line in text.splitlines():
    m = re.match(r"\s*(\d+)\s+(\d+)\s+(\d+)\s+(.*)$", line)
    if not m:
        continue
    pid = m.group(1)
    ppid = m.group(2)
    if ppid == parent:
        children.append(pid)
print(",".join(children))
PY
}

count_marker_matches() {
  local ps_file="$1"
  local marker="$2"
  python3 - "$ps_file" "$marker" <<'PY'
import pathlib
import sys

text = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8", errors="replace")
marker = sys.argv[2]
if not marker:
    print(0)
    raise SystemExit(0)

count = 0
for line in text.splitlines():
    if marker in line:
        count += 1
print(count)
PY
}

pid_exists_in_worker() {
  local worker_container="$1"
  local pid="$2"
  docker exec "$worker_container" sh -lc "kill -0 $pid >/dev/null 2>&1"
}

wait_child_pids_gone() {
  local worker_container="$1"
  local child_csv="$2"
  local timeout_sec="$3"

  if [[ -z "$child_csv" ]]; then
    return 1
  fi

  local deadline=$(( $(date +%s) + timeout_sec ))
  while true; do
    local alive=0
    local child
    IFS=',' read -r -a child_pids <<<"$child_csv"
    for child in "${child_pids[@]}"; do
      [[ -z "$child" ]] && continue
      if pid_exists_in_worker "$worker_container" "$child"; then
        alive=1
        break
      fi
    done

    if [[ "$alive" -eq 0 ]]; then
      return 0
    fi

    if [[ $(date +%s) -ge "$deadline" ]]; then
      return 1
    fi
    sleep 1
  done
}

collect_pid_snapshot() {
  local worker_container="$1"
  local pid_csv="$2"
  local out_file="$3"
  docker exec "$worker_container" sh -lc '
csv="$1"
if [ -z "$csv" ]; then
  exit 0
fi
ps -o pid,ppid,pgid,stat,cmd -p "$csv" --no-headers 2>/dev/null || true
' sh "$pid_csv" >"$out_file" 2>/dev/null
}

collect_pid_debug() {
  local worker_container="$1"
  local pid_csv="$2"
  local out_file="$3"
  docker exec "$worker_container" sh -lc '
csv="$1"
for pid in $(printf "%s" "$csv" | tr "," " "); do
  [ -z "$pid" ] && continue
  echo "=== pid:$pid ==="
  if [ ! -d "/proc/$pid" ]; then
    echo "missing_proc=1"
    continue
  fi
  awk "/^(Name|State|PPid|NSpid):/{print}" "/proc/$pid/status" 2>/dev/null || true
  cg_rel="$(awk -F: '\''$1=="0"{print $3}'\'' "/proc/$pid/cgroup" 2>/dev/null | head -n1)"
  echo "cgroup_rel=${cg_rel:-}"
  if [ -n "$cg_rel" ]; then
    cg_dir="/sys/fs/cgroup$cg_rel"
    echo "cgroup_dir=$cg_dir"
    if [ -f "$cg_dir/cgroup.procs" ]; then
      echo "cgroup.procs:"
      cat "$cg_dir/cgroup.procs" 2>/dev/null || true
    fi
    if [ -f "$cg_dir/cgroup.events" ]; then
      echo "cgroup.events:"
      cat "$cg_dir/cgroup.events" 2>/dev/null || true
    fi
  fi
done
' sh "$pid_csv" >"$out_file" 2>/dev/null
}

is_terminal_status() {
  local status="${1,,}"
  case "$status" in
    finished|accepted|ac|wrong\ answer|wa|runtime\ error|re|time\ limit\ exceeded|tle|memory\ limit\ exceeded|mle|output\ limit\ exceeded|ole|compile\ error|ce|system\ error|se)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

is_running_status() {
  local status="${1,,}"
  case "$status" in
    running|judging|processing)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

POLL_TOP_STATUS=""
POLL_DATA_STATUS=""
POLL_VERDICT=""
POLL_ATTEMPT_ID="-1"

read_status_fields() {
  local body_file="$1"
  local fields_file="$TMP_DIR/status_fields_$(date +%s%N).txt"
  python3 - "$body_file" >"$fields_file" <<'PY'
import json
import pathlib
import sys

obj = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8", errors="replace"))
data = obj.get("data")
if not isinstance(data, dict):
    data = {}

def pick(*keys):
    for k in keys:
        v = data.get(k)
        if isinstance(v, str) and v.strip():
            return v.strip()
    return ""

print(f"top_status={obj.get('status', '')}")
print(f"data_status={pick('status')}")
print(f"verdict={pick('verdict')}")
print(f"attempt_id={data.get('attempt_id', -1)}")
PY

  POLL_TOP_STATUS="$(awk -F= '$1=="top_status"{print $2}' "$fields_file" | tr -d '\r')"
  POLL_DATA_STATUS="$(awk -F= '$1=="data_status"{print $2}' "$fields_file" | tr -d '\r')"
  POLL_VERDICT="$(awk -F= '$1=="verdict"{print $2}' "$fields_file" | tr -d '\r')"
  POLL_ATTEMPT_ID="$(awk -F= '$1=="attempt_id"{print $2}' "$fields_file" | tr -d '\r')"
}

fetch_job_status() {
  local job_id="$1"
  local token="$2"
  http_request GET "$API_BASE/api/v1/status/$job_id" "200" -H "Authorization: Bearer $token"
  read_status_fields "$LAST_BODY_FILE"
}

wait_job_running() {
  local job_id="$1"
  local token="$2"
  local timeout_sec="$3"
  local deadline=$(( $(date +%s) + timeout_sec ))

  while true; do
    fetch_job_status "$job_id" "$token"

    if is_running_status "$POLL_TOP_STATUS" || is_running_status "$POLL_DATA_STATUS"; then
      return 0
    fi

    if is_terminal_status "$POLL_TOP_STATUS" || is_terminal_status "$POLL_DATA_STATUS" || is_terminal_status "$POLL_VERDICT"; then
      fatal "job reached terminal state before running: job_id=$job_id top_status=$POLL_TOP_STATUS data_status=$POLL_DATA_STATUS verdict=$POLL_VERDICT"
    fi

    if [[ $(date +%s) -ge "$deadline" ]]; then
      fatal "job did not enter running state in ${timeout_sec}s: job_id=$job_id top_status=$POLL_TOP_STATUS data_status=$POLL_DATA_STATUS verdict=$POLL_VERDICT"
    fi
    sleep "$POLL_INTERVAL_SEC"
  done
}

wait_job_terminal() {
  local job_id="$1"
  local token="$2"
  local deadline=$(( $(date +%s) + POLL_TIMEOUT_SEC ))

  while true; do
    fetch_job_status "$job_id" "$token"

    if [[ "$POLL_TOP_STATUS" == "Finished" ]] || is_terminal_status "$POLL_TOP_STATUS" || is_terminal_status "$POLL_DATA_STATUS" || is_terminal_status "$POLL_VERDICT"; then
      return 0
    fi

    if [[ $(date +%s) -ge "$deadline" ]]; then
      fatal "job did not reach terminal status in ${POLL_TIMEOUT_SEC}s: job_id=$job_id top_status=$POLL_TOP_STATUS data_status=$POLL_DATA_STATUS verdict=$POLL_VERDICT"
    fi
    sleep "$POLL_INTERVAL_SEC"
  done
}

is_accepted_verdict() {
  local verdict="${1,,}"
  case "$verdict" in
    accepted|ac|ok)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

submit_code() {
  local problem_id="$1"
  local source_code="$2"
  local time_limit_ms="$3"
  local memory_limit_kb="$4"
  local token="$5"

  local body
  body="$(PROBLEM_ID="$problem_id" SOURCE_CODE="$source_code" TIME_LIMIT_MS="$time_limit_ms" MEMORY_LIMIT_KB="$memory_limit_kb" python3 - <<'PY'
import json
import os

print(json.dumps({
    "problem_id": int(os.environ["PROBLEM_ID"]),
    "language": 1,
    "code": os.environ["SOURCE_CODE"],
    "time_limit": int(os.environ["TIME_LIMIT_MS"]),
    "memory_limit": int(os.environ["MEMORY_LIMIT_KB"])
}, ensure_ascii=False))
PY
)"

  http_request POST "$API_BASE/api/v1/submit" "200" \
    -H "Authorization: Bearer $token" \
    -H 'Content-Type: application/json' \
    -d "$body"

  local job_id
  job_id="$(cat "$LAST_BODY_FILE" | json_field job_id 2>/dev/null || true)"
  if [[ -z "$job_id" ]]; then
    fatal "submit response missing job_id"
  fi

  printf '%s' "$job_id"
}

for cmd in bash curl docker python3; do
  require_cmd "$cmd"
done

echo "[1/9] start services"
if [[ "$COMPOSE_BUILD" == "1" ]]; then
  compose_cmd up -d --build || fatal "docker compose up -d --build failed"
else
  compose_cmd up -d || fatal "docker compose up -d failed"
fi

wait_api_ready || fatal "api did not become ready in time"

worker_container="$(wait_worker_ready || true)"
if [[ -z "$worker_container" ]]; then
  fatal "worker container did not become running in time"
fi

echo "[2/9] register and login"
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
  fatal "failed to parse login token"
fi

echo "[3/9] upload testcase package"
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
  -F 'title=G1 Kill All Verify' \
  -F 'time_limit=2000' \
  -F 'memory_limit=128' \
  -F "file=@$problem_zip"

problem_id="$(cat "$LAST_BODY_FILE" | json_field id 2>/dev/null || true)"
if ! [[ "$problem_id" =~ ^[0-9]+$ ]]; then
  fatal "invalid problem id from upload response: $problem_id"
fi

echo "[4/9] capture worker baseline"
WORKER_PS_FILE="$TMP_DIR/worker_ps_baseline.txt"
collect_worker_ps "$worker_container" "$WORKER_PS_FILE" || fatal "failed to collect worker baseline process list"
baseline_judge_count="$(count_judge_processes "$WORKER_PS_FILE")"
baseline_judge_pids="$(judge_pids_csv "$WORKER_PS_FILE")"

echo "[5/9] submit long-running task and force kill judge_engine"
long_run_code="$(cat <<'CPP'
#include <chrono>
#include <cstdint>
#include <iostream>

int main() {
  volatile std::uint64_t spin = 0;
  auto end_at = std::chrono::steady_clock::now() + std::chrono::seconds(12);
  while (std::chrono::steady_clock::now() < end_at) {
    spin += 1;
  }
  std::cout << (spin % 1000) << "\n";
  return 0;
}
CPP
)"

kill_job_id="$(submit_code "$problem_id" "$long_run_code" 15000 65536 "$token")"
wait_job_running "$kill_job_id" "$token" "$RUNNING_WAIT_SEC"

judge_pid=""
child_pids_csv=""
target_pids_csv=""
pid_deadline=$(( $(date +%s) + PID_DISCOVERY_WAIT_SEC ))
while true; do
  WORKER_PS_FILE="$TMP_DIR/worker_ps_for_kill_$(date +%s%N).txt"
  collect_worker_ps "$worker_container" "$WORKER_PS_FILE" || true

  judge_pid="$(find_job_judge_pid "$WORKER_PS_FILE" "$kill_job_id" "$baseline_judge_pids")"
  if [[ -n "$judge_pid" ]]; then
    child_pids_csv="$(children_by_ppid_csv "$WORKER_PS_FILE" "$judge_pid")"
    if [[ -n "$child_pids_csv" ]]; then
      break
    fi
  fi

  if [[ $(date +%s) -ge "$pid_deadline" ]]; then
    fatal "failed to locate judge_engine pid and children for job_id=$kill_job_id"
	  fi
	  sleep 1
done

target_pids_csv="$judge_pid"
if [[ -n "$child_pids_csv" ]]; then
  target_pids_csv="$judge_pid,$child_pids_csv"
fi
KILL_BEFORE_FILE="$TMP_DIR/kill_target_before.txt"
KILL_DEBUG_BEFORE_FILE="$TMP_DIR/kill_target_debug_before.txt"
collect_pid_snapshot "$worker_container" "$target_pids_csv" "$KILL_BEFORE_FILE" || true
collect_pid_debug "$worker_container" "$target_pids_csv" "$KILL_DEBUG_BEFORE_FILE" || true

docker exec "$worker_container" sh -lc "kill -9 $judge_pid" || fatal "failed to kill judge_engine pid=$judge_pid"

if ! wait_child_pids_gone "$worker_container" "$child_pids_csv" "$CHILD_GONE_WAIT_SEC"; then
  KILL_AFTER_FILE="$TMP_DIR/kill_target_after.txt"
  KILL_DEBUG_AFTER_FILE="$TMP_DIR/kill_target_debug_after.txt"
  collect_pid_snapshot "$worker_container" "$target_pids_csv" "$KILL_AFTER_FILE" || true
  collect_pid_debug "$worker_container" "$target_pids_csv" "$KILL_DEBUG_AFTER_FILE" || true
  WORKER_PS_FILE="$TMP_DIR/worker_ps_after_kill.txt"
  collect_worker_ps "$worker_container" "$WORKER_PS_FILE" || true
  fatal "child processes still alive after parent kill: judge_pid=$judge_pid child_pids=$child_pids_csv"
fi

KILL_AFTER_FILE="$TMP_DIR/kill_target_after.txt"
KILL_DEBUG_AFTER_FILE="$TMP_DIR/kill_target_debug_after.txt"
collect_pid_snapshot "$worker_container" "$target_pids_csv" "$KILL_AFTER_FILE" || true
collect_pid_debug "$worker_container" "$target_pids_csv" "$KILL_DEBUG_AFTER_FILE" || true

all_gone=1
wait_job_terminal "$kill_job_id" "$token"
kill_job_status="$POLL_TOP_STATUS"
kill_job_verdict="$POLL_VERDICT"

echo "[6/9] submit follow-up AC task"
ac_code="$(cat <<'CPP'
#include <iostream>
int main() {
  int a, b;
  std::cin >> a >> b;
  std::cout << (a + b) << "\n";
  return 0;
}
CPP
)"
ac_job_id="$(submit_code "$problem_id" "$ac_code" 2000 65536 "$token")"
wait_job_terminal "$ac_job_id" "$token"
ac_top_status="$POLL_TOP_STATUS"
ac_data_status="$POLL_DATA_STATUS"
ac_verdict="$POLL_VERDICT"

if [[ "$ac_top_status" != "Finished" ]]; then
  fatal "follow-up task not finished: job_id=$ac_job_id top_status=$ac_top_status data_status=$ac_data_status verdict=$ac_verdict"
fi

ac_verdict_check="$ac_verdict"
if [[ -z "$ac_verdict_check" ]]; then
  ac_verdict_check="$ac_data_status"
fi
if [[ -z "$ac_verdict_check" ]]; then
  ac_verdict_check="$ac_top_status"
fi

if ! is_accepted_verdict "$ac_verdict_check"; then
  fatal "follow-up task not accepted: job_id=$ac_job_id verdict=$ac_verdict data_status=$ac_data_status top_status=$ac_top_status"
fi

normalized_ac_verdict="Accepted"

echo "[7/9] verify no leak"
leak_ok=0
final_judge_count="$baseline_judge_count"
marker_matches=0

for _ in $(seq 1 "$LEAK_SETTLE_ROUNDS"); do
  WORKER_PS_FILE="$TMP_DIR/worker_ps_final_$(date +%s%N).txt"
  collect_worker_ps "$worker_container" "$WORKER_PS_FILE" || true

  final_judge_count="$(count_judge_processes "$WORKER_PS_FILE")"
  marker_matches="$(count_marker_matches "$WORKER_PS_FILE" "$kill_job_id")"
  if [[ "$final_judge_count" -le "$baseline_judge_count" && "$marker_matches" -eq 0 ]]; then
    leak_ok=1
    break
  fi
  sleep "$LEAK_SETTLE_INTERVAL_SEC"
done

if [[ "$leak_ok" -ne 1 ]]; then
  fatal "leak check failed: baseline_judge=$baseline_judge_count final_judge=$final_judge_count marker_matches=$marker_matches"
fi

echo "[8/9] evidence"
echo "EVIDENCE_G1_PARENT_KILL: judge_pid=${judge_pid} child_pids=${child_pids_csv} all_gone=${all_gone} job_id=${kill_job_id} status=${kill_job_status:-unknown} verdict=${kill_job_verdict:-unknown}"
echo "EVIDENCE_G1_KILL_BEFORE_PS:"
if [[ -n "$KILL_BEFORE_FILE" && -s "$KILL_BEFORE_FILE" ]]; then
  cat "$KILL_BEFORE_FILE"
else
  echo "<empty>"
fi
echo "EVIDENCE_G1_KILL_AFTER_PS:"
if [[ -n "$KILL_AFTER_FILE" && -s "$KILL_AFTER_FILE" ]]; then
  cat "$KILL_AFTER_FILE"
else
  echo "<none>"
fi
echo "EVIDENCE_G1_KILL_BEFORE_CGROUP:"
if [[ -n "$KILL_DEBUG_BEFORE_FILE" && -s "$KILL_DEBUG_BEFORE_FILE" ]]; then
  cat "$KILL_DEBUG_BEFORE_FILE"
else
  echo "<empty>"
fi
echo "EVIDENCE_G1_KILL_AFTER_CGROUP:"
if [[ -n "$KILL_DEBUG_AFTER_FILE" && -s "$KILL_DEBUG_AFTER_FILE" ]]; then
  cat "$KILL_DEBUG_AFTER_FILE"
else
  echo "<none>"
fi
echo "EVIDENCE_G1_NO_LEAK: baseline_judge=${baseline_judge_count} final_judge=${final_judge_count} leaked=0"
echo "EVIDENCE_G1_STILL_WORKS: job_id=${ac_job_id} status=${ac_top_status} verdict=${normalized_ac_verdict}"

echo "[9/9] done"
echo "G1 verify passed"
