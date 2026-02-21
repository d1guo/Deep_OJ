#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

API_BASE="${API_BASE:-http://127.0.0.1:18080}"
USERNAME="${USERNAME:-admin}"
PASSWORD="${PASSWORD:-password}"
EMAIL="${EMAIL:-${USERNAME}@example.com}"

API_READY_TIMEOUT_SEC="${API_READY_TIMEOUT_SEC:-90}"
WAIT_RUNNING_SEC="${WAIT_RUNNING_SEC:-60}"
CRASH_WAIT_SEC="${CRASH_WAIT_SEC:-2}"
POLL_TIMEOUT_SEC="${POLL_TIMEOUT_SEC:-220}"
KEEP_TMP="${KEEP_TMP:-0}"
COMPOSE_BUILD="${COMPOSE_BUILD:-1}"

WORKER_SERVICE="${WORKER_SERVICE:-worker}"
POSTGRES_CONTAINER="${POSTGRES_CONTAINER:-oj-postgres}"
POSTGRES_DB="${POSTGRES_DB:-deep_oj}"
POSTGRES_USER="${POSTGRES_USER:-deep_oj}"
POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-deepoj_pg_password_change_me}"
JOB_STREAM_KEY="${JOB_STREAM_KEY:-deepoj:jobs}"
JOB_STREAM_GROUP="${JOB_STREAM_GROUP:-deepoj:workers}"
REDIS_CONTAINER="${REDIS_CONTAINER:-oj-redis}"
REDIS_PASSWORD="${REDIS_PASSWORD:-deepoj_redis_change_me}"

TMP_DIR="$(mktemp -d)"
FAILED_STEP="init"
LAST_HTTP_STATUS="<none>"
LAST_HTTP_BODY_FILE=""
READINESS_HEALTH_STATUS="<none>"
READINESS_HEALTH_BODY_FILE=""
READINESS_LAST_SOURCE="<none>"
HAS_RG=0
COMPOSE_FILES=(-f docker-compose.yml)
if [[ -f docker-compose.verify-g1.yml ]]; then
  COMPOSE_FILES+=(-f docker-compose.verify-g1.yml)
fi
if [[ -f docker-compose.override.yml ]]; then
  COMPOSE_FILES+=(-f docker-compose.override.yml)
fi

cleanup() {
  if [[ "$KEEP_TMP" == "1" ]]; then
    echo "KEEP_TMP=1, keep tmp dir: $TMP_DIR" >&2
    return
  fi
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT

print_body_snippet_from_file() {
  local body_file="$1"
  if [[ ! -f "$body_file" || ! -s "$body_file" ]]; then
    echo "<empty>"
    return
  fi

  local body_len
  body_len="$(wc -c < "$body_file" | tr -d ' ')"
  if [[ "$body_len" -le 2048 ]]; then
    cat "$body_file"
    return
  fi

  head -c 2048 "$body_file"
  echo
  echo "... [truncated, total ${body_len} bytes]"
}

rg_or_grep() {
  local pattern="$1"
  local file="${2:-}"
  if [[ "$HAS_RG" == "1" ]]; then
    if [[ -n "$file" ]]; then
      rg -n "$pattern" "$file"
    else
      rg -n "$pattern"
    fi
  else
    if [[ -n "$file" ]]; then
      grep -nE "$pattern" "$file"
    else
      grep -nE "$pattern"
    fi
  fi
}

compose_cmd() {
  docker compose "${COMPOSE_FILES[@]}" "$@"
}

compose_service_exists() {
  local service="$1"
  local svc
  while IFS= read -r svc; do
    [[ "$svc" == "$service" ]] && return 0
  done < <(compose_cmd config --services 2>/dev/null || true)
  return 1
}

service_running() {
  local service="$1"
  local svc
  while IFS= read -r svc; do
    [[ "$svc" == "$service" ]] && return 0
  done < <(compose_cmd ps --status running --services 2>/dev/null || true)
  return 1
}

wait_service_running() {
  local service="$1"
  local timeout_sec="$2"
  local deadline=$(( $(date +%s) + timeout_sec ))

  while true; do
    if service_running "$service"; then
      return 0
    fi
    if [[ $(date +%s) -ge "$deadline" ]]; then
      return 1
    fi
    sleep 1
  done
}

find_container_by_service() {
  local service="$1"
  docker ps --filter "label=com.docker.compose.service=${service}" --format '{{.Names}}' | head -n1
}

emit_diagnostics() {
  echo
  echo "========== diagnostics ==========" >&2
  echo "[docker compose ps]" >&2
  compose_cmd ps >&2 || true
  echo "[api readiness snapshot]" >&2
  echo "  source=${READINESS_LAST_SOURCE}" >&2
  echo "  health_status=${READINESS_HEALTH_STATUS}" >&2
  if [[ -n "${READINESS_HEALTH_BODY_FILE:-}" ]]; then
    echo "  health_body (<=2KB):" >&2
    print_body_snippet_from_file "$READINESS_HEALTH_BODY_FILE" >&2 || true
  fi

  local services
  services="$(compose_cmd config --services 2>/dev/null || true)"
  if [[ -z "$services" ]]; then
    echo "[docker compose config --services] <none>" >&2
  else
    while IFS= read -r service; do
      [[ -z "$service" ]] && continue
      echo "[docker compose logs --tail=200 ${service}]" >&2
      compose_cmd logs --tail=200 "$service" >&2 || true
    done <<< "$services"
  fi
  echo "=================================" >&2
}

fail_verify() {
  local step="$1"
  local status="${2:-$LAST_HTTP_STATUS}"
  local body_file="${3:-$LAST_HTTP_BODY_FILE}"
  local message="${4:-}"

  echo "ERROR: MVP-3 verify failed" >&2
  echo "  failed_step: $step" >&2
  echo "  http_status: ${status:-<none>}" >&2
  echo "  response_body (<=2KB):" >&2
  if [[ -n "$body_file" ]]; then
    print_body_snippet_from_file "$body_file" >&2
  else
    echo "<none>" >&2
  fi
  if [[ -n "$message" ]]; then
    echo "  message: $message" >&2
  fi

  emit_diagnostics
  exit 1
}

http_request() {
  local method="$1"
  local url="$2"
  local expected_csv="$3"
  shift 3

  local body_file="$TMP_DIR/http_body_$(date +%s%N).txt"
  local status=""

  if ! status="$(curl -sS -X "$method" "$url" -o "$body_file" -w '%{http_code}' "$@")"; then
    LAST_HTTP_STATUS="${status:-<none>}"
    LAST_HTTP_BODY_FILE="$body_file"
    fail_verify "$FAILED_STEP" "${status:-<none>}" "$body_file" "curl request failed: ${method} ${url}"
  fi

  LAST_HTTP_STATUS="$status"
  LAST_HTTP_BODY_FILE="$body_file"

  local matched=0
  local expected
  IFS=',' read -r -a expected <<< "$expected_csv"
  for code in "${expected[@]}"; do
    if [[ "$status" == "$code" ]]; then
      matched=1
      break
    fi
  done

  if [[ "$matched" -ne 1 ]]; then
    fail_verify "$FAILED_STEP" "$status" "$body_file" "unexpected HTTP status for ${method} ${url}, expected=${expected_csv}"
  fi

  cat "$body_file"
}

json_field() {
  local field_path="$1"
  python3 -c '
import json
import sys

field_path = sys.argv[1]
obj = json.loads(sys.stdin.read())
cur = obj
for part in field_path.split("."):
    if isinstance(cur, dict) and part in cur:
        cur = cur[part]
    else:
        raise KeyError(field_path)
if cur is None:
    raise KeyError(field_path)
if isinstance(cur, (dict, list)):
    print(json.dumps(cur, ensure_ascii=False, separators=(",", ":")))
else:
    print(cur)
' "$field_path"
}

require_json_field() {
  local field_path="$1"
  local response_body="$2"
  local value=""

  if ! value="$(printf '%s' "$response_body" | json_field "$field_path" 2>/dev/null)"; then
    local body_file="$TMP_DIR/json_field_error.txt"
    printf '%s' "$response_body" > "$body_file"
    fail_verify "$FAILED_STEP" "$LAST_HTTP_STATUS" "$body_file" "missing or invalid JSON field: ${field_path}"
  fi

  if [[ -z "$value" ]]; then
    local body_file="$TMP_DIR/json_field_empty.txt"
    printf '%s' "$response_body" > "$body_file"
    fail_verify "$FAILED_STEP" "$LAST_HTTP_STATUS" "$body_file" "empty JSON field: ${field_path}"
  fi

  printf '%s' "$value"
}

is_terminal_status() {
  local status="${1,,}"
  case "$status" in
    finished|accepted|"wrong answer"|"compile error"|"system error"|"time limit"|"time limit exceeded"|"memory limit"|"memory limit exceeded"|"output limit"|"output limit exceeded"|"runtime error")
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

is_int() {
  [[ "$1" =~ ^[0-9]+$ ]]
}

parse_xpending_count() {
  local raw="$1"
  local count
  count="$(printf '%s\n' "$raw" | awk '/^[0-9]+$/ {print; exit}')"
  if [[ -z "$count" ]]; then
    return 1
  fi
  printf '%s' "$count"
}

wait_api_ready() {
  local timeout_sec="$1"
  local deadline=$(( $(date +%s) + timeout_sec ))
  local health_body_file="$TMP_DIR/readiness_health_body.txt"
  local login_body_file="$TMP_DIR/readiness_login_body.txt"

  while true; do
    local code
    code="$(curl -sS "$API_BASE/api/v1/health" -o "$health_body_file" -w '%{http_code}' || true)"
    READINESS_HEALTH_STATUS="${code:-<none>}"
    READINESS_HEALTH_BODY_FILE="$health_body_file"
    if [[ "$code" == "200" ]]; then
      READINESS_LAST_SOURCE="health"
      return 0
    fi

    if compose_service_exists "api"; then
      if compose_cmd logs --tail=200 api 2>&1 | rg_or_grep "API Server starting" >/dev/null 2>&1; then
        READINESS_LAST_SOURCE="compose_log"
        return 0
      fi
    fi

    local api_container
    api_container="$(find_container_by_service api)"
    if [[ -n "$api_container" ]]; then
      if docker logs "$api_container" --tail 200 2>&1 | rg_or_grep "API Server starting" >/dev/null 2>&1; then
        READINESS_LAST_SOURCE="docker_log"
        return 0
      fi
    fi

    local login_body
    login_body="$(USERNAME="$USERNAME" PASSWORD="$PASSWORD" python3 - <<'PY'
import json
import os

print(json.dumps({"username": os.environ["USERNAME"], "password": os.environ["PASSWORD"]}, ensure_ascii=False))
PY
)"
    local login_status
    login_status="$(curl -sS -X POST "$API_BASE/api/v1/auth/login" \
      -H 'Content-Type: application/json' \
      -d "$login_body" \
      -o "$login_body_file" \
      -w '%{http_code}' || true)"
    if [[ "$login_status" == "200" ]]; then
      local login_token
      login_token="$(python3 -c '
import json
import sys
obj = json.loads(sys.stdin.read())
token = obj.get("token")
if isinstance(token, str) and token:
    print(token)
' < "$login_body_file" 2>/dev/null || true)"
      if [[ -n "$login_token" ]]; then
        READINESS_LAST_SOURCE="login"
        return 0
      fi
    fi

    if [[ $(date +%s) -ge "$deadline" ]]; then
      return 1
    fi
    sleep 1
  done
}

extract_status_fields() {
  python3 -c '
import json
import sys

obj = json.loads(sys.stdin.read())

def pick(*paths):
    for path in paths:
        cur = obj
        ok = True
        for part in path.split("."):
            if isinstance(cur, dict) and part in cur:
                cur = cur[part]
            else:
                ok = False
                break
        if ok and cur is not None:
            if isinstance(cur, (dict, list)):
                return json.dumps(cur, ensure_ascii=False, separators=(",", ":"))
            return str(cur)
    return "<none>"

print("status=" + pick("status", "data.status"))
print("state=" + pick("state", "data.state"))
print("verdict=" + pick("verdict", "data.verdict", "data.status"))
print("attempt_id=" + pick("attempt_id", "data.attempt_id"))
print("trace_id=" + pick("trace_id", "data.trace_id"))
'
}

for cmd in curl python3 docker; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    fail_verify "dependency check" "<none>" "" "missing command: $cmd"
  fi
done
if command -v rg >/dev/null 2>&1; then
  HAS_RG=1
elif ! command -v grep >/dev/null 2>&1; then
  fail_verify "dependency check" "<none>" "" "missing command: rg or grep"
fi

echo "[1/10] start services"
FAILED_STEP="start services"
if [[ "$COMPOSE_BUILD" == "1" ]]; then
  if ! compose_cmd up -d --build; then
    fail_verify "$FAILED_STEP" "<none>" "" "docker compose up -d --build failed"
  fi
else
  if ! compose_cmd up -d; then
    fail_verify "$FAILED_STEP" "<none>" "" "docker compose up -d failed"
  fi
fi

echo "[2/10] wait core services running"
for service in api "$WORKER_SERVICE" scheduler postgres redis; do
  FAILED_STEP="wait service ${service} running"
  if ! wait_service_running "$service" 90; then
    fail_verify "$FAILED_STEP" "<none>" "" "service not running within timeout"
  fi
  echo "service_running=${service}"
done

echo "[3/10] wait API readiness"
FAILED_STEP="wait api readiness"
if ! wait_api_ready "$API_READY_TIMEOUT_SEC"; then
  fail_verify "$FAILED_STEP" "${READINESS_HEALTH_STATUS:-<none>}" "${READINESS_HEALTH_BODY_FILE:-}" \
    "API readiness timeout after ${API_READY_TIMEOUT_SEC}s"
fi
echo "api_ready_source=${READINESS_LAST_SOURCE}"

echo "[4/10] register/login/upload minimal problem.zip"
FAILED_STEP="register"
REGISTER_BODY="$(USERNAME="$USERNAME" PASSWORD="$PASSWORD" EMAIL="$EMAIL" python3 - <<'PY'
import json
import os

print(json.dumps({
    "username": os.environ["USERNAME"],
    "password": os.environ["PASSWORD"],
    "email": os.environ["EMAIL"],
}, ensure_ascii=False))
PY
)"
_="$(http_request POST "$API_BASE/api/v1/auth/register" "201,409" \
  -H 'Content-Type: application/json' \
  -d "$REGISTER_BODY")"

FAILED_STEP="login"
LOGIN_BODY="$(USERNAME="$USERNAME" PASSWORD="$PASSWORD" python3 - <<'PY'
import json
import os

print(json.dumps({"username": os.environ["USERNAME"], "password": os.environ["PASSWORD"]}, ensure_ascii=False))
PY
)"
LOGIN_RESPONSE="$(http_request POST "$API_BASE/api/v1/auth/login" "200" \
  -H 'Content-Type: application/json' \
  -d "$LOGIN_BODY")"
TOKEN="$(require_json_field token "$LOGIN_RESPONSE")"

PROBLEM_ZIP="$TMP_DIR/problem.zip"
python3 - "$PROBLEM_ZIP" <<'PY'
import sys
import zipfile

with zipfile.ZipFile(sys.argv[1], "w", compression=zipfile.ZIP_DEFLATED) as zf:
    zf.writestr("1.in", "1 2\n")
    zf.writestr("1.out", "3\n")
PY

FAILED_STEP="upload problem"
UPLOAD_RESPONSE="$(http_request POST "$API_BASE/api/v1/problems" "200" \
  -H "Authorization: Bearer $TOKEN" \
  -F 'title=MVP3 Crash Recover Test' \
  -F 'time_limit=30000' \
  -F 'memory_limit=128' \
  -F "file=@$PROBLEM_ZIP")"
PROBLEM_ID="$(require_json_field id "$UPLOAD_RESPONSE")"
if ! is_int "$PROBLEM_ID"; then
  body_file="$TMP_DIR/problem_id_invalid.txt"
  printf '%s\n' "$UPLOAD_RESPONSE" > "$body_file"
  fail_verify "$FAILED_STEP" "$LAST_HTTP_STATUS" "$body_file" "problem id is not numeric"
fi
echo "EVIDENCE_PROBLEM_ID: problem_id=$PROBLEM_ID"

echo "[5/10] submit long-running code"
SOURCE_CODE="$(cat <<'CPP'
#include <chrono>
#include <iostream>
int main(){
  int a,b;
  if(!(std::cin>>a>>b)) return 0;
  auto start = std::chrono::steady_clock::now();
  volatile unsigned long long spin = 0;
  while (std::chrono::duration_cast<std::chrono::milliseconds>(std::chrono::steady_clock::now() - start).count() < 8000) {
    spin ^= 1ULL;
  }
  std::cout << (a+b);
  return static_cast<int>(spin & 0ULL);
}
CPP
)"
SUBMIT_BODY="$(PROBLEM_ID="$PROBLEM_ID" SOURCE_CODE="$SOURCE_CODE" python3 - <<'PY'
import json
import os

print(json.dumps({
    "problem_id": int(os.environ["PROBLEM_ID"]),
    "language": 1,
    "code": os.environ["SOURCE_CODE"],
    "time_limit": 25000,
    "memory_limit": 65536,
}, ensure_ascii=False))
PY
)"
FAILED_STEP="submit"
SUBMIT_RESPONSE="$(http_request POST "$API_BASE/api/v1/submit" "200" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d "$SUBMIT_BODY")"
JOB_ID="$(require_json_field job_id "$SUBMIT_RESPONSE")"
JOB_ID_SQL="${JOB_ID//\'/\'\'}"
echo "EVIDENCE_JOB: job_id=$JOB_ID"

echo "[6/10] wait running -> SIGKILL worker -> restart"
FAILED_STEP="wait job running before crash"
RUNNING_DEADLINE=$(( $(date +%s) + WAIT_RUNNING_SEC ))
ATTEMPT_BEFORE=""
while true; do
  DB_RUN_QUERY="SELECT status, attempt_id FROM submissions WHERE job_id = '${JOB_ID_SQL}';"
  if ! DB_RUN_RAW="$(docker exec -e PGPASSWORD="$POSTGRES_PASSWORD" "$POSTGRES_CONTAINER" \
    psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -t -A -F $'\t' \
    -c "$DB_RUN_QUERY" 2>&1)"; then
    body_file="$TMP_DIR/db_wait_running_error.txt"
    printf '%s\n' "$DB_RUN_RAW" > "$body_file"
    fail_verify "$FAILED_STEP" "N/A" "$body_file" "query running state failed"
  fi

  DB_RUN_LINE="$(printf '%s\n' "$DB_RUN_RAW" | sed '/^[[:space:]]*$/d' | head -n1)"
  if [[ -n "$DB_RUN_LINE" ]]; then
    CUR_STATUS="$(printf '%s\n' "$DB_RUN_LINE" | awk -F'\t' '{print $1}')"
    CUR_ATTEMPT="$(printf '%s\n' "$DB_RUN_LINE" | awk -F'\t' '{print $2}')"
    if [[ "${CUR_STATUS,,}" == "running" ]]; then
      ATTEMPT_BEFORE="$CUR_ATTEMPT"
      break
    fi
    if is_terminal_status "$CUR_STATUS"; then
      body_file="$TMP_DIR/job_finished_before_crash.txt"
      {
        echo "job_id=$JOB_ID"
        echo "status=$CUR_STATUS"
        echo "attempt_id=$CUR_ATTEMPT"
      } > "$body_file"
      fail_verify "$FAILED_STEP" "N/A" "$body_file" "job reached terminal status before crash injection"
    fi
  fi

  if [[ $(date +%s) -ge "$RUNNING_DEADLINE" ]]; then
    body_file="$TMP_DIR/wait_running_timeout.txt"
    {
      echo "job_id=$JOB_ID"
      echo "last_db_line=${DB_RUN_LINE:-<empty>}"
    } > "$body_file"
    fail_verify "$FAILED_STEP" "N/A" "$body_file" "wait running timeout after ${WAIT_RUNNING_SEC}s"
  fi
  sleep 1
done

if ! is_int "$ATTEMPT_BEFORE"; then
  body_file="$TMP_DIR/attempt_before_invalid.txt"
  printf 'attempt_before=%s\n' "$ATTEMPT_BEFORE" > "$body_file"
  fail_verify "$FAILED_STEP" "N/A" "$body_file" "attempt_before is not numeric"
fi

echo "EVIDENCE_PRE_CRASH: status=running attempt_id=$ATTEMPT_BEFORE"
CRASH_TS="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"

FAILED_STEP="capture XPENDING before kill"
if ! XPENDING_BEFORE_RAW="$(docker exec "$REDIS_CONTAINER" redis-cli -a "$REDIS_PASSWORD" --raw XPENDING "$JOB_STREAM_KEY" "$JOB_STREAM_GROUP" 2>&1)"; then
  body_file="$TMP_DIR/xpending_before_error.txt"
  printf '%s\n' "$XPENDING_BEFORE_RAW" > "$body_file"
  fail_verify "$FAILED_STEP" "N/A" "$body_file" "redis XPENDING before kill failed"
fi
if ! XPENDING_BEFORE="$(parse_xpending_count "$XPENDING_BEFORE_RAW")"; then
  body_file="$TMP_DIR/xpending_before_parse_error.txt"
  printf '%s\n' "$XPENDING_BEFORE_RAW" > "$body_file"
  fail_verify "$FAILED_STEP" "N/A" "$body_file" "failed to parse XPENDING before kill"
fi
if ! is_int "$XPENDING_BEFORE"; then
  body_file="$TMP_DIR/xpending_before_not_int.txt"
  printf 'pending_before=%s\n' "$XPENDING_BEFORE" > "$body_file"
  fail_verify "$FAILED_STEP" "N/A" "$body_file" "pending_before is not numeric"
fi

FAILED_STEP="kill worker"
if ! compose_cmd kill -s SIGKILL "$WORKER_SERVICE" >/dev/null 2>&1; then
  worker_container="$(find_container_by_service "$WORKER_SERVICE")"
  if [[ -z "$worker_container" ]]; then
    fail_verify "$FAILED_STEP" "N/A" "" "failed to find worker container for SIGKILL"
  fi
  if ! docker kill -s KILL "$worker_container" >/dev/null 2>&1; then
    fail_verify "$FAILED_STEP" "N/A" "" "failed to SIGKILL worker"
  fi
fi

FAILED_STEP="capture XPENDING after kill before restart"
if ! XPENDING_AFTER_RAW="$(docker exec "$REDIS_CONTAINER" redis-cli -a "$REDIS_PASSWORD" --raw XPENDING "$JOB_STREAM_KEY" "$JOB_STREAM_GROUP" 2>&1)"; then
  body_file="$TMP_DIR/xpending_after_error.txt"
  printf '%s\n' "$XPENDING_AFTER_RAW" > "$body_file"
  fail_verify "$FAILED_STEP" "N/A" "$body_file" "redis XPENDING after kill failed"
fi
if ! XPENDING_AFTER="$(parse_xpending_count "$XPENDING_AFTER_RAW")"; then
  body_file="$TMP_DIR/xpending_after_parse_error.txt"
  printf '%s\n' "$XPENDING_AFTER_RAW" > "$body_file"
  fail_verify "$FAILED_STEP" "N/A" "$body_file" "failed to parse XPENDING after kill"
fi
if ! is_int "$XPENDING_AFTER"; then
  body_file="$TMP_DIR/xpending_after_not_int.txt"
  printf 'pending_after=%s\n' "$XPENDING_AFTER" > "$body_file"
  fail_verify "$FAILED_STEP" "N/A" "$body_file" "pending_after is not numeric"
fi
echo "EVIDENCE_PEL: stream=$JOB_STREAM_KEY group=$JOB_STREAM_GROUP pending_before=$XPENDING_BEFORE pending_after=$XPENDING_AFTER"
if [[ "$XPENDING_AFTER" -le 0 ]]; then
  body_file="$TMP_DIR/pel_not_proven.txt"
  {
    echo "stream=$JOB_STREAM_KEY"
    echo "group=$JOB_STREAM_GROUP"
    echo "pending_before=$XPENDING_BEFORE"
    echo "pending_after=$XPENDING_AFTER"
  } > "$body_file"
  fail_verify "$FAILED_STEP" "N/A" "$body_file" "PEL pending evidence not strong enough after kill"
fi

sleep "$CRASH_WAIT_SEC"
FAILED_STEP="restart worker"
if ! compose_cmd up -d "$WORKER_SERVICE" >/dev/null 2>&1; then
  fail_verify "$FAILED_STEP" "N/A" "" "docker compose up -d worker failed"
fi
if ! wait_service_running "$WORKER_SERVICE" 90; then
  fail_verify "$FAILED_STEP" "N/A" "" "worker not running after restart"
fi
echo "EVIDENCE_CRASH: signal=SIGKILL crash_ts=$CRASH_TS crash_wait_sec=$CRASH_WAIT_SEC worker_restarted=1"

echo "[7/10] poll /status until Finished"
FAILED_STEP="poll status after restart"
POLL_DEADLINE=$(( $(date +%s) + POLL_TIMEOUT_SEC ))
FINAL_API_STATUS=""
FINAL_API_STATE=""
FINAL_API_VERDICT=""
FINAL_API_ATTEMPT=""
FINAL_API_TRACE=""
while true; do
  STATUS_RESPONSE="$(http_request GET "$API_BASE/api/v1/status/$JOB_ID" "200" \
    -H "Authorization: Bearer $TOKEN")"

  STATUS_FIELDS_FILE="$TMP_DIR/status_fields_after_restart.txt"
  printf '%s' "$STATUS_RESPONSE" | extract_status_fields > "$STATUS_FIELDS_FILE"

  CUR_API_STATUS="$(awk -F= '$1=="status" {print $2}' "$STATUS_FIELDS_FILE" | head -n1 | tr -d '\r')"
  CUR_API_STATE="$(awk -F= '$1=="state" {print $2}' "$STATUS_FIELDS_FILE" | head -n1 | tr -d '\r')"
  CUR_API_VERDICT="$(awk -F= '$1=="verdict" {print $2}' "$STATUS_FIELDS_FILE" | head -n1 | tr -d '\r')"
  CUR_API_ATTEMPT="$(awk -F= '$1=="attempt_id" {print $2}' "$STATUS_FIELDS_FILE" | head -n1 | tr -d '\r')"
  CUR_API_TRACE="$(awk -F= '$1=="trace_id" {print $2}' "$STATUS_FIELDS_FILE" | head -n1 | tr -d '\r')"
  echo "poll_status=$CUR_API_STATUS verdict=$CUR_API_VERDICT attempt_id=$CUR_API_ATTEMPT"

  if [[ "$CUR_API_STATUS" == "Finished" ]]; then
    FINAL_API_STATUS="$CUR_API_STATUS"
    FINAL_API_STATE="$CUR_API_STATE"
    FINAL_API_VERDICT="$CUR_API_VERDICT"
    FINAL_API_ATTEMPT="$CUR_API_ATTEMPT"
    FINAL_API_TRACE="$CUR_API_TRACE"
    break
  fi

  if [[ $(date +%s) -ge "$POLL_DEADLINE" ]]; then
    body_file="$TMP_DIR/poll_timeout_body.txt"
    printf '%s\n' "$STATUS_RESPONSE" > "$body_file"
    fail_verify "$FAILED_STEP" "$LAST_HTTP_STATUS" "$body_file" "poll timeout after ${POLL_TIMEOUT_SEC}s"
  fi
  sleep 1
done

echo "EVIDENCE_FINAL_API: status=$FINAL_API_STATUS state=$FINAL_API_STATE verdict=$FINAL_API_VERDICT attempt_id=$FINAL_API_ATTEMPT trace_id=$FINAL_API_TRACE"
if ! is_int "$FINAL_API_ATTEMPT"; then
  body_file="$TMP_DIR/final_api_attempt_invalid.txt"
  printf 'final_api_attempt=%s\n' "$FINAL_API_ATTEMPT" > "$body_file"
  fail_verify "$FAILED_STEP" "N/A" "$body_file" "final API attempt_id is not numeric"
fi

echo "[8/10] reclaim evidence"
FAILED_STEP="collect reclaim evidence"
WORKER_LOGS_SINCE_FILE="$TMP_DIR/worker_logs_since_crash.log"
compose_cmd logs --since "$CRASH_TS" --tail=800 "$WORKER_SERVICE" > "$WORKER_LOGS_SINCE_FILE" 2>&1 || true
RECLAIM_MATCHES="$(rg_or_grep "DB reclaim success|reclaim_claimed|XAUTOCLAIM|reclaim" "$WORKER_LOGS_SINCE_FILE" || true)"
RECLAIM_MATCH_COUNT="$(printf '%s\n' "$RECLAIM_MATCHES" | sed '/^[[:space:]]*$/d' | wc -l | tr -d ' ')"

RECLAIM_EVIDENCE_SOURCE=""
if [[ "${RECLAIM_MATCH_COUNT:-0}" -gt 0 ]]; then
  RECLAIM_EVIDENCE_SOURCE="log"
elif [[ "$FINAL_API_ATTEMPT" -gt "$ATTEMPT_BEFORE" ]]; then
  RECLAIM_EVIDENCE_SOURCE="attempt_bump"
else
  body_file="$TMP_DIR/reclaim_evidence_missing.txt"
  {
    echo "attempt_before=$ATTEMPT_BEFORE"
    echo "final_api_attempt=$FINAL_API_ATTEMPT"
    echo "reclaim_match_count=${RECLAIM_MATCH_COUNT:-0}"
  } > "$body_file"
  fail_verify "$FAILED_STEP" "N/A" "$body_file" "reclaim evidence not found"
fi

echo "EVIDENCE_RECLAIM: source=$RECLAIM_EVIDENCE_SOURCE log_match_count=${RECLAIM_MATCH_COUNT:-0} attempt_before=$ATTEMPT_BEFORE attempt_after_api=$FINAL_API_ATTEMPT"
if [[ "${RECLAIM_MATCH_COUNT:-0}" -gt 0 ]]; then
  printf '%s\n' "$RECLAIM_MATCHES" | head -n 10
fi

echo "[9/10] DB final verify + attempt align"
FAILED_STEP="db count by job_id"
if ! DB_COUNT_RAW="$(docker exec -e PGPASSWORD="$POSTGRES_PASSWORD" "$POSTGRES_CONTAINER" \
  psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -t -A \
  -c "SELECT COUNT(*) FROM submissions WHERE job_id = '${JOB_ID_SQL}';" 2>&1)"; then
  body_file="$TMP_DIR/db_count_error.txt"
  printf '%s\n' "$DB_COUNT_RAW" > "$body_file"
  fail_verify "$FAILED_STEP" "N/A" "$body_file" "DB count query failed"
fi
DB_COUNT="$(printf '%s' "$DB_COUNT_RAW" | tr -d '[:space:]')"
if [[ "$DB_COUNT" != "1" ]]; then
  body_file="$TMP_DIR/db_count_not_one.txt"
  {
    echo "job_id=$JOB_ID"
    echo "db_count=$DB_COUNT"
  } > "$body_file"
  fail_verify "$FAILED_STEP" "N/A" "$body_file" "expected DB count == 1"
fi

FAILED_STEP="db final row fetch"
DB_TSV_QUERY="SELECT job_id, attempt_id, status, COALESCE(NULLIF(result->>'verdict',''), result->>'status', '') AS verdict, updated_at FROM submissions WHERE job_id = '${JOB_ID_SQL}';"
if ! DB_TSV_RAW="$(docker exec -e PGPASSWORD="$POSTGRES_PASSWORD" "$POSTGRES_CONTAINER" \
  psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -t -A -F $'\t' \
  -c "$DB_TSV_QUERY" 2>&1)"; then
  body_file="$TMP_DIR/db_tsv_error.txt"
  printf '%s\n' "$DB_TSV_RAW" > "$body_file"
  fail_verify "$FAILED_STEP" "N/A" "$body_file" "DB tsv query failed"
fi
DB_TSV_LINE="$(printf '%s\n' "$DB_TSV_RAW" | sed '/^[[:space:]]*$/d' | head -n1)"
if [[ -z "$DB_TSV_LINE" ]]; then
  body_file="$TMP_DIR/db_tsv_empty.txt"
  printf '%s\n' "$DB_TSV_RAW" > "$body_file"
  fail_verify "$FAILED_STEP" "N/A" "$body_file" "DB final row is empty"
fi
DB_JOB_ID="$(printf '%s\n' "$DB_TSV_LINE" | awk -F'\t' '{print $1}')"
DB_ATTEMPT_ID="$(printf '%s\n' "$DB_TSV_LINE" | awk -F'\t' '{print $2}')"
DB_STATUS="$(printf '%s\n' "$DB_TSV_LINE" | awk -F'\t' '{print $3}')"
DB_VERDICT="$(printf '%s\n' "$DB_TSV_LINE" | awk -F'\t' '{print $4}')"

if ! is_int "$DB_ATTEMPT_ID"; then
  body_file="$TMP_DIR/db_attempt_invalid.txt"
  printf 'db_attempt_id=%s\n' "$DB_ATTEMPT_ID" > "$body_file"
  fail_verify "$FAILED_STEP" "N/A" "$body_file" "DB attempt_id is not numeric"
fi
if [[ "$DB_ATTEMPT_ID" != "$FINAL_API_ATTEMPT" ]]; then
  body_file="$TMP_DIR/attempt_mismatch_api_db.txt"
  {
    echo "job_id=$JOB_ID"
    echo "api_attempt_id=$FINAL_API_ATTEMPT"
    echo "db_attempt_id=$DB_ATTEMPT_ID"
  } > "$body_file"
  fail_verify "$FAILED_STEP" "N/A" "$body_file" "API and DB attempt_id mismatch"
fi
if [[ "$DB_ATTEMPT_ID" -le "$ATTEMPT_BEFORE" ]]; then
  body_file="$TMP_DIR/attempt_not_bumped.txt"
  {
    echo "job_id=$JOB_ID"
    echo "attempt_before=$ATTEMPT_BEFORE"
    echo "attempt_after=$DB_ATTEMPT_ID"
  } > "$body_file"
  fail_verify "$FAILED_STEP" "N/A" "$body_file" "attempt_id did not bump after crash/reclaim"
fi
if ! is_terminal_status "$DB_STATUS" && ! is_terminal_status "$DB_VERDICT"; then
  body_file="$TMP_DIR/db_not_terminal.txt"
  {
    echo "job_id=$JOB_ID"
    echo "db_status=$DB_STATUS"
    echo "db_verdict=$DB_VERDICT"
  } > "$body_file"
  fail_verify "$FAILED_STEP" "N/A" "$body_file" "DB final row is not terminal"
fi

echo "EVIDENCE_ATTEMPT_ALIGNMENT: job_id=$DB_JOB_ID attempt_before=$ATTEMPT_BEFORE attempt_after=$DB_ATTEMPT_ID api_attempt=$FINAL_API_ATTEMPT"

FAILED_STEP="print db evidence row"
DB_TABLE_QUERY="SELECT job_id, attempt_id, status, COALESCE(NULLIF(result->>'verdict',''), result->>'status', '') AS verdict, updated_at FROM submissions WHERE job_id = '${JOB_ID_SQL}';"
if ! DB_TABLE_OUTPUT="$(docker exec -e PGPASSWORD="$POSTGRES_PASSWORD" "$POSTGRES_CONTAINER" \
  psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -P pager=off \
  -c "$DB_TABLE_QUERY" 2>&1)"; then
  body_file="$TMP_DIR/db_table_error.txt"
  printf '%s\n' "$DB_TABLE_OUTPUT" > "$body_file"
  fail_verify "$FAILED_STEP" "N/A" "$body_file" "failed to print DB evidence row"
fi
echo "$DB_TABLE_OUTPUT"

echo "[10/10] fencing probe (stale attempt write must be blocked)"
FAILED_STEP="fencing probe"
FENCING_QUERY="WITH stale_write AS (UPDATE submissions SET updated_at = updated_at WHERE job_id = '${JOB_ID_SQL}' AND attempt_id = ${ATTEMPT_BEFORE} AND attempt_id <> ${DB_ATTEMPT_ID} RETURNING 1) SELECT COUNT(*) FROM stale_write;"
if ! FENCING_RAW="$(docker exec -e PGPASSWORD="$POSTGRES_PASSWORD" "$POSTGRES_CONTAINER" \
  psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -t -A \
  -c "$FENCING_QUERY" 2>&1)"; then
  body_file="$TMP_DIR/fencing_query_error.txt"
  printf '%s\n' "$FENCING_RAW" > "$body_file"
  fail_verify "$FAILED_STEP" "N/A" "$body_file" "fencing probe query failed"
fi
STALE_WRITE_ROWS="$(printf '%s' "$FENCING_RAW" | tr -d '[:space:]')"
if [[ "$STALE_WRITE_ROWS" != "0" ]]; then
  body_file="$TMP_DIR/fencing_failed.txt"
  {
    echo "job_id=$JOB_ID"
    echo "stale_attempt=$ATTEMPT_BEFORE"
    echo "final_attempt=$DB_ATTEMPT_ID"
    echo "stale_write_rows=$STALE_WRITE_ROWS"
  } > "$body_file"
  fail_verify "$FAILED_STEP" "N/A" "$body_file" "stale attempt write was not blocked"
fi
echo "EVIDENCE_FENCING: job_id=$JOB_ID stale_attempt=$ATTEMPT_BEFORE final_attempt=$DB_ATTEMPT_ID stale_write_rows=$STALE_WRITE_ROWS"

echo "MVP-3 verify passed"
echo "pass_criteria: worker crash(no xack) -> reclaim -> Finished, attempt bump, stale attempt write blocked"
