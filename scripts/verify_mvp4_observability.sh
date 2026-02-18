#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

API_BASE="${API_BASE:-http://127.0.0.1:18080}"
USERNAME="${USERNAME:-admin}"
PASSWORD="${PASSWORD:-password}"
EMAIL="${EMAIL:-${USERNAME}@example.com}"
API_BACKPRESSURE_ENABLED="${API_BACKPRESSURE_ENABLED:-false}"
JWT_SECRET="${JWT_SECRET:-dev_jwt_secret_change_me}"
ADMIN_USERS="${ADMIN_USERS:-admin}"
API_BACKPRESSURE_LAG_CRIT="${API_BACKPRESSURE_LAG_CRIT:-1000000}"
API_BACKPRESSURE_LAG_WARN="${API_BACKPRESSURE_LAG_WARN:-500000}"
API_BACKPRESSURE_PENDING_CRIT="${API_BACKPRESSURE_PENDING_CRIT:-1000000}"
API_BACKPRESSURE_PENDING_WARN="${API_BACKPRESSURE_PENDING_WARN:-500000}"
API_BACKPRESSURE_OLDEST_AGE_CRIT_MS="${API_BACKPRESSURE_OLDEST_AGE_CRIT_MS:-86400000}"
API_BACKPRESSURE_OLDEST_AGE_WARN_MS="${API_BACKPRESSURE_OLDEST_AGE_WARN_MS:-43200000}"
API_MINIO_ENDPOINT="${API_MINIO_ENDPOINT:-http://oj-minio:9000}"
WORKER_MINIO_ENDPOINT="${WORKER_MINIO_ENDPOINT:-oj-minio:9000}"

COMPOSE_BUILD="${COMPOSE_BUILD:-0}"
RUNTIME_OVERRIDE="${RUNTIME_OVERRIDE:-1}"
HOT_PATCH_GO_BINARIES="${HOT_PATCH_GO_BINARIES:-1}"
API_READY_TIMEOUT_SEC="${API_READY_TIMEOUT_SEC:-90}"
POLL_TIMEOUT_SEC="${POLL_TIMEOUT_SEC:-90}"
LOG_TAIL_LINES="${LOG_TAIL_LINES:-600}"
KEEP_TMP="${KEEP_TMP:-0}"

export API_BACKPRESSURE_ENABLED

TMP_DIR="$(mktemp -d)"
FAILED_STEP="init"
LAST_HTTP_STATUS="<none>"
LAST_HTTP_BODY_FILE=""
READINESS_HEALTH_STATUS="<none>"
READINESS_HEALTH_BODY_FILE=""
METRICS_SNIPPET_FILE=""
HAS_RG=0
RUNTIME_OVERRIDE_FILE=""
COMPOSE_FILES=(-f docker-compose.yml)
if [[ -f docker-compose.override.yml ]]; then
  COMPOSE_FILES+=(-f docker-compose.override.yml)
fi

if [[ "$RUNTIME_OVERRIDE" == "1" ]]; then
  RUNTIME_OVERRIDE_FILE="$TMP_DIR/verify_mvp4.runtime.override.yml"
  cat > "$RUNTIME_OVERRIDE_FILE" <<YAML
services:
  api:
    environment:
      JWT_SECRET: ${JWT_SECRET}
      ADMIN_USERS: ${ADMIN_USERS}
      MINIO_ENDPOINT: ${API_MINIO_ENDPOINT}
      API_BACKPRESSURE_ENABLED: "${API_BACKPRESSURE_ENABLED}"
      API_BACKPRESSURE_LAG_CRIT: "${API_BACKPRESSURE_LAG_CRIT}"
      API_BACKPRESSURE_LAG_WARN: "${API_BACKPRESSURE_LAG_WARN}"
      API_BACKPRESSURE_PENDING_CRIT: "${API_BACKPRESSURE_PENDING_CRIT}"
      API_BACKPRESSURE_PENDING_WARN: "${API_BACKPRESSURE_PENDING_WARN}"
      API_BACKPRESSURE_OLDEST_AGE_CRIT_MS: "${API_BACKPRESSURE_OLDEST_AGE_CRIT_MS}"
      API_BACKPRESSURE_OLDEST_AGE_WARN_MS: "${API_BACKPRESSURE_OLDEST_AGE_WARN_MS}"
  worker:
    environment:
      MINIO_ENDPOINT: ${WORKER_MINIO_ENDPOINT}
YAML
  COMPOSE_FILES+=(-f "$RUNTIME_OVERRIDE_FILE")
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
  shift || true
  if [[ "$HAS_RG" == "1" ]]; then
    rg -n -- "$pattern" "$@"
  else
    grep -nE "$pattern" "$@"
  fi
}

rg_or_grep_q() {
  local pattern="$1"
  shift || true
  if [[ "$HAS_RG" == "1" ]]; then
    rg -q -- "$pattern" "$@"
  else
    grep -qE "$pattern" "$@"
  fi
}

compose_cmd() {
  docker compose "${COMPOSE_FILES[@]}" "$@"
}

find_container_by_service() {
  local service="$1"
  docker ps --filter "label=com.docker.compose.service=${service}" --format '{{.Names}}' | head -n1
}

sync_local_go_binaries() {
  if [[ "$HOT_PATCH_GO_BINARIES" != "1" ]]; then
    return 0
  fi
  if ! command -v go >/dev/null 2>&1; then
    echo "hot_patch_go_binaries=skip(no_go)"
    return 0
  fi

  local api_container worker_container scheduler_container
  api_container="$(find_container_by_service api)"
  worker_container="$(find_container_by_service worker)"
  scheduler_container="$(find_container_by_service scheduler)"

  if [[ -z "$api_container" || -z "$worker_container" || -z "$scheduler_container" ]]; then
    fail_verify "$FAILED_STEP" "<none>" "" "cannot find api/worker/scheduler containers for hot patch"
  fi

  local bin_dir="$TMP_DIR/hotpatch_bin"
  mkdir -p "$bin_dir"

  if ! (cd src/go && CGO_ENABLED=0 go build -o "$bin_dir/oj_api" ./cmd/api \
    && CGO_ENABLED=0 go build -o "$bin_dir/oj_scheduler" ./cmd/scheduler \
    && CGO_ENABLED=0 go build -o "$bin_dir/oj_worker" ./cmd/worker); then
    fail_verify "$FAILED_STEP" "<none>" "" "failed to build local Go binaries for hot patch"
  fi

  if ! docker cp "$bin_dir/oj_api" "$api_container:/app/oj_api"; then
    fail_verify "$FAILED_STEP" "<none>" "" "failed to copy oj_api into container"
  fi
  if ! docker cp "$bin_dir/oj_scheduler" "$scheduler_container:/app/oj_scheduler"; then
    fail_verify "$FAILED_STEP" "<none>" "" "failed to copy oj_scheduler into container"
  fi
  if ! docker cp "$bin_dir/oj_worker" "$worker_container:/app/oj_worker"; then
    fail_verify "$FAILED_STEP" "<none>" "" "failed to copy oj_worker into container"
  fi
  if ! docker restart "$api_container" "$scheduler_container" "$worker_container" >/dev/null; then
    fail_verify "$FAILED_STEP" "<none>" "" "failed to restart containers after hot patch"
  fi
  echo "hot_patch_go_binaries=ok"
}

emit_diagnostics() {
  echo
  echo "========== diagnostics ==========" >&2
  echo "[docker compose ps]" >&2
  compose_cmd ps >&2 || true
  echo "[api readiness snapshot]" >&2
  echo "  health_status=${READINESS_HEALTH_STATUS}" >&2
  if [[ -n "${READINESS_HEALTH_BODY_FILE:-}" ]]; then
    echo "  health_body (<=2KB):" >&2
    print_body_snippet_from_file "$READINESS_HEALTH_BODY_FILE" >&2 || true
  fi

  if [[ -n "${METRICS_SNIPPET_FILE:-}" ]]; then
    echo "[metrics snippet <=2KB]" >&2
    print_body_snippet_from_file "$METRICS_SNIPPET_FILE" >&2 || true
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

  echo "ERROR: MVP-4 verify failed" >&2
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

wait_api_ready() {
  local timeout_sec="$1"
  local deadline=$(( $(date +%s) + timeout_sec ))
  local health_body_file="$TMP_DIR/readiness_health_body.txt"

  while true; do
    local code
    code="$(curl -sS "$API_BASE/api/v1/health" -o "$health_body_file" -w '%{http_code}' || true)"
    READINESS_HEALTH_STATUS="${code:-<none>}"
    READINESS_HEALTH_BODY_FILE="$health_body_file"
    if [[ "$code" == "200" ]]; then
      return 0
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

fetch_metrics_from_container() {
  local container="$1"
  local port="$2"
  local out_file="$3"
  local raw_file="$TMP_DIR/metrics_raw_${container}_${port}.txt"
  local err_file="$TMP_DIR/metrics_err_${container}_${port}.txt"

  if ! docker exec "$container" bash -lc "set -euo pipefail; exec 3<>/dev/tcp/127.0.0.1/${port}; printf 'GET /metrics HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n' >&3; cat <&3" >"$raw_file" 2>"$err_file"; then
    return 1
  fi

  python3 - "$raw_file" "$out_file" <<'PY'
import pathlib
import sys

raw = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8", errors="replace")
out_path = pathlib.Path(sys.argv[2])

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

out_path.write_text(body, encoding="utf-8")
PY
}

for cmd in curl python3 docker bash; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    fail_verify "dependency check" "<none>" "" "missing command: $cmd"
  fi
done
if command -v rg >/dev/null 2>&1; then
  HAS_RG=1
elif ! command -v grep >/dev/null 2>&1; then
  fail_verify "dependency check" "<none>" "" "missing command: rg or grep"
fi

echo "[1/9] start services"
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
sync_local_go_binaries

echo "[2/9] wait API readiness"
FAILED_STEP="wait api readiness"
if ! wait_api_ready "$API_READY_TIMEOUT_SEC"; then
  fail_verify "$FAILED_STEP" "${READINESS_HEALTH_STATUS:-<none>}" "${READINESS_HEALTH_BODY_FILE:-}" \
    "API readiness timeout after ${API_READY_TIMEOUT_SEC}s"
fi

echo "[3/9] run smoke_e2e"
FAILED_STEP="run smoke e2e"
SMOKE_LOG="$TMP_DIR/smoke_e2e.log"
if ! API_BASE="$API_BASE" USERNAME="$USERNAME" PASSWORD="$PASSWORD" EMAIL="$EMAIL" \
  POLL_STATUS=1 POLL_TIMEOUT_SEC="$POLL_TIMEOUT_SEC" bash scripts/smoke_e2e.sh >"$SMOKE_LOG" 2>&1; then
  LAST_HTTP_BODY_FILE="$SMOKE_LOG"
  fail_verify "$FAILED_STEP" "<none>" "$SMOKE_LOG" "scripts/smoke_e2e.sh failed"
fi

JOB_ID="$(parse_job_id_from_smoke "$SMOKE_LOG")"
if [[ -z "$JOB_ID" ]]; then
  LAST_HTTP_BODY_FILE="$SMOKE_LOG"
  fail_verify "parse job_id from smoke output" "<none>" "$SMOKE_LOG" "cannot parse job_id from smoke output"
fi
echo "extracted_job_id=$JOB_ID"

echo "[4/9] query API status and trace"
FAILED_STEP="login for status query"
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

STATUS_FIELDS="$(printf '%s' "$STATUS_RESPONSE" | extract_status_fields)"
printf '%s\n' "$STATUS_FIELDS"

TRACE_ID="$(printf '%s' "$STATUS_FIELDS" | awk -F= '/^trace_id=/{print $2}')"
ATTEMPT_ID="$(printf '%s' "$STATUS_FIELDS" | awk -F= '/^attempt_id=/{print $2}')"
STATUS_VALUE="$(printf '%s' "$STATUS_FIELDS" | awk -F= '/^status=/{print $2}')"
VERDICT_VALUE="$(printf '%s' "$STATUS_FIELDS" | awk -F= '/^verdict=/{print $2}')"

if [[ -z "$TRACE_ID" || "$TRACE_ID" == "<none>" ]]; then
  local_body="$TMP_DIR/status_no_trace.txt"
  printf '%s' "$STATUS_RESPONSE" > "$local_body"
  fail_verify "$FAILED_STEP" "$LAST_HTTP_STATUS" "$local_body" "status response has no trace_id"
fi
echo "trace_id=$TRACE_ID"

echo "[5/9] scrape metrics from components"
FAILED_STEP="scrape metrics"
API_METRICS="$TMP_DIR/metrics_api.prom"
WORKER_METRICS="$TMP_DIR/metrics_worker.prom"
SCHEDULER_METRICS="$TMP_DIR/metrics_scheduler.prom"
METRICS_COMBINED="$TMP_DIR/metrics_combined.prom"
METRICS_OK_COUNT=0
METRICS_COMPONENTS=""

API_CONTAINER="$(find_container_by_service api)"
if [[ -n "$API_CONTAINER" ]] && fetch_metrics_from_container "$API_CONTAINER" "18080" "$API_METRICS"; then
  METRICS_OK_COUNT=$((METRICS_OK_COUNT + 1))
  METRICS_COMPONENTS="${METRICS_COMPONENTS}api "
elif curl -sS "$API_BASE/metrics" -o "$API_METRICS"; then
  METRICS_OK_COUNT=$((METRICS_OK_COUNT + 1))
  METRICS_COMPONENTS="${METRICS_COMPONENTS}api "
else
  : >"$API_METRICS"
fi

WORKER_CONTAINER="$(find_container_by_service worker)"
if [[ -n "$WORKER_CONTAINER" ]] && fetch_metrics_from_container "$WORKER_CONTAINER" "9092" "$WORKER_METRICS"; then
  METRICS_OK_COUNT=$((METRICS_OK_COUNT + 1))
  METRICS_COMPONENTS="${METRICS_COMPONENTS}worker "
else
  : >"$WORKER_METRICS"
fi

SCHEDULER_CONTAINER="$(find_container_by_service scheduler)"
if [[ -n "$SCHEDULER_CONTAINER" ]] && fetch_metrics_from_container "$SCHEDULER_CONTAINER" "9091" "$SCHEDULER_METRICS"; then
  METRICS_OK_COUNT=$((METRICS_OK_COUNT + 1))
  METRICS_COMPONENTS="${METRICS_COMPONENTS}scheduler "
else
  : >"$SCHEDULER_METRICS"
fi

cat "$API_METRICS" "$WORKER_METRICS" "$SCHEDULER_METRICS" > "$METRICS_COMBINED"
METRICS_SNIPPET_FILE="$METRICS_COMBINED"

if [[ "$METRICS_OK_COUNT" -lt 2 ]]; then
  fail_verify "$FAILED_STEP" "<none>" "$METRICS_COMBINED" "metrics endpoint reachable components < 2"
fi
echo "metrics_reachable_components=${METRICS_COMPONENTS}"

echo "[6/9] check required metric names"
FAILED_STEP="check metric names"
required_metrics=(
  "submit_total"
  "submit_429_total"
  "request_duration_seconds"
  "exec_inflight"
  "exec_total"
  "exec_duration_seconds"
  "verdict_total"
  "reclaim_total"
  "xack_total"
  "xautoclaim_errors_total"
  "stream_lag"
  "stream_backlog"
  "oldest_age_seconds"
  "control_plane_only"
  "legacy_loops_started"
)
for metric in "${required_metrics[@]}"; do
  if ! rg_or_grep_q "(^# (HELP|TYPE) ${metric}(\\s|$)|^${metric}(\\{|\\s))" "$METRICS_COMBINED"; then
    fail_verify "$FAILED_STEP" "<none>" "$METRICS_COMBINED" "missing metric: ${metric}"
  fi
done
echo "metrics_required_names=ok"

echo "[7/9] ensure no job_id metric label"
FAILED_STEP="check metric labels"
if rg_or_grep_q 'job_id="' "$METRICS_COMBINED"; then
  fail_verify "$FAILED_STEP" "<none>" "$METRICS_COMBINED" "forbidden high-cardinality label found: job_id"
fi
echo "metrics_job_id_label=absent"

echo "[8/9] check trace-correlated full-chain events"
FAILED_STEP="check log correlation"
LOG_FILE="$TMP_DIR/log_chain.log"
compose_cmd logs --tail="$LOG_TAIL_LINES" api worker scheduler >"$LOG_FILE" 2>&1 || true

CHAIN_RESULT="$TMP_DIR/chain_result.txt"
if ! python3 - "$LOG_FILE" "$TRACE_ID" "$JOB_ID" "$CHAIN_RESULT" <<'PY'
import pathlib
import sys

log_file = pathlib.Path(sys.argv[1])
trace_id = sys.argv[2]
job_id = sys.argv[3]
out_file = pathlib.Path(sys.argv[4])

required = {"enqueue", "claim", "start_exec", "xack_ok"}
completion_alternatives = {"finish_exec", "db_finalize_ok", "db_finalize_stale", "db_finalize_rejected"}
finalize_alternatives = {"db_finalize_ok", "db_finalize_stale", "db_finalize_rejected"}
found = set()
found_completion = set()
found_finalize = set()

for line in log_file.read_text(encoding="utf-8", errors="replace").splitlines():
    if trace_id not in line or job_id not in line:
        continue
    if '"component"' not in line and "component=" not in line:
        continue
    for event in required:
        if f'"event":"{event}"' in line or f"event={event}" in line:
            found.add(event)
    for event in completion_alternatives:
        if f'"event":"{event}"' in line or f"event={event}" in line:
            found_completion.add(event)
    for event in finalize_alternatives:
        if f'"event":"{event}"' in line or f"event={event}" in line:
            found_finalize.add(event)

missing = sorted(required - found)
if not found_completion:
    missing.append("finish_exec|db_finalize_ok|db_finalize_stale|db_finalize_rejected")
if not found_finalize:
    missing.append("db_finalize_ok|db_finalize_stale|db_finalize_rejected")

if missing:
    out_file.write_text("missing_events=" + ",".join(missing) + "\n", encoding="utf-8")
    raise SystemExit(1)

out_file.write_text(
    "found_events=" + ",".join(sorted(found)) + "\n"
    + "found_completion=" + ",".join(sorted(found_completion)) + "\n"
    + "found_finalize=" + ",".join(sorted(found_finalize)) + "\n",
    encoding="utf-8",
)
PY
then
  fail_verify "$FAILED_STEP" "<none>" "$CHAIN_RESULT" "trace_id full-chain events not found"
fi
cat "$CHAIN_RESULT"

echo "[9/9] done"
echo "EVIDENCE_METRICS: components=${METRICS_COMPONENTS}"
echo "EVIDENCE_LOG_CHAIN: trace_id=${TRACE_ID} job_id=${JOB_ID}"
echo "EVIDENCE_NO_HIGH_CARDINALITY: job_id_label_absent=1"
echo "MVP-4 verify passed"
