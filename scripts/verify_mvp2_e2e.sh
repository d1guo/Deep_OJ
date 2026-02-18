#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

API_BASE="${API_BASE:-http://127.0.0.1:18080}"
USERNAME="${USERNAME:-admin}"
PASSWORD="${PASSWORD:-password}"
EMAIL="${EMAIL:-${USERNAME}@example.com}"
POLL_TIMEOUT_SEC="${POLL_TIMEOUT_SEC:-90}"
API_READY_TIMEOUT_SEC="${API_READY_TIMEOUT_SEC:-90}"
KEEP_TMP="${KEEP_TMP:-0}"

POSTGRES_CONTAINER="${POSTGRES_CONTAINER:-oj-postgres}"
POSTGRES_DB="${POSTGRES_DB:-deep_oj}"
POSTGRES_USER="${POSTGRES_USER:-deep_oj}"
POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-deepoj_pg_password_change_me}"

TMP_DIR="$(mktemp -d)"
FAILED_STEP="init"
LAST_HTTP_STATUS="<none>"
LAST_HTTP_BODY_FILE=""
READINESS_HEALTH_STATUS="<none>"
READINESS_HEALTH_BODY_FILE=""
READINESS_LAST_SOURCE="<none>"
HAS_RG=0

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
  if [[ "$HAS_RG" == "1" ]]; then
    rg -n "$pattern"
  else
    grep -nE "$pattern"
  fi
}

compose_service_exists() {
  local service="$1"
  local svc
  while IFS= read -r svc; do
    [[ "$svc" == "$service" ]] && return 0
  done < <(docker compose config --services 2>/dev/null || true)
  return 1
}

emit_diagnostics() {
  echo
  echo "========== diagnostics ==========" >&2
  echo "[docker compose ps]" >&2
  docker compose ps >&2 || true

  local services
  services="$(docker compose config --services 2>/dev/null || true)"
  if [[ -z "$services" ]]; then
    echo "[docker compose config --services] <none>" >&2
  else
    while IFS= read -r service; do
      [[ -z "$service" ]] && continue
      echo "[docker compose logs --tail=200 ${service}]" >&2
      docker compose logs --tail=200 "$service" >&2 || true
    done <<< "$services"
  fi
  echo "=================================" >&2
}

fail_verify() {
  local step="$1"
  local status="${2:-$LAST_HTTP_STATUS}"
  local body_file="${3:-$LAST_HTTP_BODY_FILE}"
  local message="${4:-}"

  echo "ERROR: MVP-2 verify failed" >&2
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

wait_container_running() {
  local container="$1"
  local timeout_sec="$2"
  local deadline=$(( $(date +%s) + timeout_sec ))

  while true; do
    if [[ "$(docker inspect -f '{{.State.Running}}' "$container" 2>/dev/null || true)" == "true" ]]; then
      return 0
    fi
    if [[ $(date +%s) -ge "$deadline" ]]; then
      return 1
    fi
    sleep 1
  done
}

wait_api_ready() {
  local timeout_sec="$1"
  local deadline=$(( $(date +%s) + timeout_sec ))
  local health_body_file="$TMP_DIR/readiness_health_body.txt"
  local login_body_file="$TMP_DIR/readiness_login_body.txt"

  while true; do
    # A) health endpoint ready check
    local code
    code="$(curl -sS "$API_BASE/api/v1/health" -o "$health_body_file" -w '%{http_code}' || true)"
    READINESS_HEALTH_STATUS="${code:-<none>}"
    READINESS_HEALTH_BODY_FILE="$health_body_file"
    if [[ "$code" == "200" ]]; then
      READINESS_LAST_SOURCE="health"
      return 0
    fi

    # B) API log fallback (compose service first)
    if compose_service_exists "api"; then
      if docker compose logs --tail=200 api 2>&1 | rg_or_grep "API Server starting" >/dev/null 2>&1; then
        READINESS_LAST_SOURCE="compose_log"
        return 0
      fi
    fi

    # B2) docker log fallback (discover container by compose label)
    local api_container
    api_container="$(docker ps --filter label=com.docker.compose.service=api --format '{{.Names}}' | head -n1)"
    if [[ -n "$api_container" ]]; then
      if docker logs "$api_container" --tail 200 2>&1 | rg_or_grep "API Server starting" >/dev/null 2>&1; then
        READINESS_LAST_SOURCE="docker_log"
        return 0
      fi
    fi

    # C) login fallback
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

parse_job_id_from_smoke() {
  local log_file="$1"
  python3 - "$log_file" <<'PY'
import pathlib
import re
import sys

text = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8", errors="replace")
matches = re.findall(r"\bsubmitted job_id=([^\s]+)", text)
print(matches[-1] if matches else "")
PY
}

parse_smoke_http_status() {
  local log_file="$1"
  python3 - "$log_file" <<'PY'
import pathlib
import re
import sys

text = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8", errors="replace")
matches = re.findall(r"actual status:\s*([0-9]{3})", text)
print(matches[-1] if matches else "")
PY
}

extract_smoke_body() {
  local log_file="$1"
  local output_file="$2"
  python3 - "$log_file" "$output_file" <<'PY'
import pathlib
import re
import sys

src = pathlib.Path(sys.argv[1])
dst = pathlib.Path(sys.argv[2])
text = src.read_text(encoding="utf-8", errors="replace")
pattern = r"response body \(<=2KB\):\n"
matches = list(re.finditer(pattern, text))
if not matches:
    dst.write_text("<not found in smoke output>\n", encoding="utf-8")
    raise SystemExit(0)
body = text[matches[-1].end():].strip()
if not body:
    body = "<empty>"
dst.write_text(body + "\n", encoding="utf-8")
PY
}

extract_status_fields() {
  python3 -c '
import json
import sys

raw = sys.stdin.read()
obj = json.loads(raw)

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

echo "[1/7] start services"
FAILED_STEP="start services"
if ! docker compose up -d; then
  fail_verify "$FAILED_STEP" "<none>" "" "docker compose up -d failed"
fi

echo "[2/7] check core containers"
for container in oj-api oj-worker oj-scheduler oj-postgres oj-redis; do
  FAILED_STEP="wait container ${container}"
  if ! wait_container_running "$container" 90; then
    fail_verify "$FAILED_STEP" "<none>" "" "container not running within timeout"
  fi
  echo "container_running=${container}"
done

echo "[3/7] wait API health"
FAILED_STEP="wait api health"
if ! wait_api_ready "$API_READY_TIMEOUT_SEC"; then
  fail_verify "$FAILED_STEP" "${READINESS_HEALTH_STATUS:-<none>}" "${READINESS_HEALTH_BODY_FILE:-}" \
    "API readiness timeout after ${API_READY_TIMEOUT_SEC}s (sources tried: health -> compose_log/docker_log -> login)"
fi
echo "api_ready_source=${READINESS_LAST_SOURCE}"

echo "[4/7] run scripts/smoke_e2e.sh"
FAILED_STEP="run scripts/smoke_e2e.sh"
SMOKE_LOG="$TMP_DIR/smoke.log"
set +e
API_BASE="$API_BASE" USERNAME="$USERNAME" PASSWORD="$PASSWORD" EMAIL="$EMAIL" POLL_TIMEOUT_SEC="$POLL_TIMEOUT_SEC" \
  bash scripts/smoke_e2e.sh >"$SMOKE_LOG" 2>&1
SMOKE_EXIT=$?
set -e
cat "$SMOKE_LOG"

if [[ "$SMOKE_EXIT" -ne 0 ]]; then
  SMOKE_STATUS="$(parse_smoke_http_status "$SMOKE_LOG")"
  SMOKE_BODY_FILE="$TMP_DIR/smoke_body.txt"
  extract_smoke_body "$SMOKE_LOG" "$SMOKE_BODY_FILE"
  fail_verify "$FAILED_STEP" "${SMOKE_STATUS:-<unknown>}" "$SMOKE_BODY_FILE" "scripts/smoke_e2e.sh exited with code ${SMOKE_EXIT}"
fi

JOB_ID="$(parse_job_id_from_smoke "$SMOKE_LOG")"
if [[ -z "$JOB_ID" ]]; then
  parse_body_file="$TMP_DIR/smoke_parse_error.txt"
  printf '%s\n' "$(tail -c 2048 "$SMOKE_LOG")" > "$parse_body_file"
  fail_verify "$FAILED_STEP" "<none>" "$parse_body_file" "failed to parse job_id from smoke output"
fi
echo "extracted_job_id=$JOB_ID"

echo "[5/7] API re-check /api/v1/status/{job_id}"
FAILED_STEP="login for api status check"
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

FAILED_STEP="query api status by job_id"
STATUS_RESPONSE="$(http_request GET "$API_BASE/api/v1/status/$JOB_ID" "200" \
  -H "Authorization: Bearer $TOKEN")"
STATUS_FIELDS_FILE="$TMP_DIR/status_fields.txt"
printf '%s' "$STATUS_RESPONSE" | extract_status_fields > "$STATUS_FIELDS_FILE"
cat "$STATUS_FIELDS_FILE"

API_STATUS="$(awk -F= '$1=="status" {print $2}' "$STATUS_FIELDS_FILE" | head -n1 | tr -d '\r')"
API_STATE="$(awk -F= '$1=="state" {print $2}' "$STATUS_FIELDS_FILE" | head -n1 | tr -d '\r')"
API_VERDICT="$(awk -F= '$1=="verdict" {print $2}' "$STATUS_FIELDS_FILE" | head -n1 | tr -d '\r')"
API_ATTEMPT_ID="$(awk -F= '$1=="attempt_id" {print $2}' "$STATUS_FIELDS_FILE" | head -n1 | tr -d '\r')"
API_TRACE_ID="$(awk -F= '$1=="trace_id" {print $2}' "$STATUS_FIELDS_FILE" | head -n1 | tr -d '\r')"

echo "api_status_check: status=${API_STATUS} state=${API_STATE} verdict=${API_VERDICT} attempt_id=${API_ATTEMPT_ID} trace_id=${API_TRACE_ID}"

if [[ "$API_STATUS" != "Finished" ]]; then
  fail_verify "$FAILED_STEP" "$LAST_HTTP_STATUS" "$LAST_HTTP_BODY_FILE" "API status must be Finished, got: ${API_STATUS:-<empty>}"
fi

echo "[6/7] Postgres落库校验"
JOB_ID_SQL="${JOB_ID//\'/\'\'}"
FAILED_STEP="db count by job_id"
if ! DB_COUNT_RAW="$(docker exec -e PGPASSWORD="$POSTGRES_PASSWORD" "$POSTGRES_CONTAINER" \
  psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -t -A \
  -c "SELECT COUNT(*) FROM submissions WHERE job_id = '${JOB_ID_SQL}';" 2>&1)"; then
  db_body_file="$TMP_DIR/db_count_error.txt"
  printf '%s\n' "$DB_COUNT_RAW" > "$db_body_file"
  fail_verify "$FAILED_STEP" "N/A" "$db_body_file" "Postgres count query failed"
fi
DB_COUNT="$(printf '%s' "$DB_COUNT_RAW" | tr -d '[:space:]')"
if [[ "$DB_COUNT" != "1" ]]; then
  db_body_file="$TMP_DIR/db_count_unexpected.txt"
  {
    echo "job_id=$JOB_ID"
    echo "db_count=$DB_COUNT"
  } > "$db_body_file"
  fail_verify "$FAILED_STEP" "N/A" "$db_body_file" "expected exactly 1 row in submissions"
fi

FAILED_STEP="db row fetch by job_id"
DB_TSV_QUERY="SELECT job_id, attempt_id, status, COALESCE(NULLIF(result->>'verdict',''), result->>'status', '') AS verdict, updated_at FROM submissions WHERE job_id = '${JOB_ID_SQL}';"
if ! DB_TSV_RAW="$(docker exec -e PGPASSWORD="$POSTGRES_PASSWORD" "$POSTGRES_CONTAINER" \
  psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -t -A -F $'\t' \
  -c "$DB_TSV_QUERY" 2>&1)"; then
  db_body_file="$TMP_DIR/db_tsv_error.txt"
  printf '%s\n' "$DB_TSV_RAW" > "$db_body_file"
  fail_verify "$FAILED_STEP" "N/A" "$db_body_file" "Postgres row query failed"
fi

DB_TSV_LINE="$(printf '%s\n' "$DB_TSV_RAW" | sed '/^[[:space:]]*$/d' | head -n1)"
DB_TSV_LINES="$(printf '%s\n' "$DB_TSV_RAW" | sed '/^[[:space:]]*$/d' | wc -l | tr -d ' ')"
if [[ "$DB_TSV_LINES" != "1" ]]; then
  db_body_file="$TMP_DIR/db_rows_unexpected.txt"
  printf '%s\n' "$DB_TSV_RAW" > "$db_body_file"
  fail_verify "$FAILED_STEP" "N/A" "$db_body_file" "expected exactly 1 data row from submissions query"
fi

DB_STATUS="$(printf '%s\n' "$DB_TSV_LINE" | awk -F'\t' '{print $3}')"
DB_VERDICT="$(printf '%s\n' "$DB_TSV_LINE" | awk -F'\t' '{print $4}')"
if ! is_terminal_status "$DB_STATUS" && ! is_terminal_status "$DB_VERDICT"; then
  db_body_file="$TMP_DIR/db_status_not_terminal.txt"
  {
    echo "job_id=$JOB_ID"
    echo "status=$DB_STATUS"
    echo "verdict=$DB_VERDICT"
  } > "$db_body_file"
  fail_verify "$FAILED_STEP" "N/A" "$db_body_file" "DB row is not terminal by status/verdict"
fi

FAILED_STEP="print db evidence row"
DB_TABLE_QUERY="SELECT job_id, attempt_id, status, COALESCE(NULLIF(result->>'verdict',''), result->>'status', '') AS verdict, updated_at FROM submissions WHERE job_id = '${JOB_ID_SQL}';"
if ! DB_TABLE_OUTPUT="$(docker exec -e PGPASSWORD="$POSTGRES_PASSWORD" "$POSTGRES_CONTAINER" \
  psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -P pager=off \
  -c "$DB_TABLE_QUERY" 2>&1)"; then
  db_body_file="$TMP_DIR/db_table_error.txt"
  printf '%s\n' "$DB_TABLE_OUTPUT" > "$db_body_file"
  fail_verify "$FAILED_STEP" "N/A" "$db_body_file" "failed to print DB evidence row"
fi

echo "$DB_TABLE_OUTPUT"

echo "[7/7] MVP-2 verify passed"
echo "pass_criteria: api.status=Finished and db.count=1 with terminal status/verdict"
echo "job_id=$JOB_ID"
