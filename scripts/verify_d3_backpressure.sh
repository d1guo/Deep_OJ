#!/usr/bin/env bash
set -euo pipefail

API_BASE="${API_BASE:-http://127.0.0.1:18080}"
USERNAME="${USERNAME:-admin}"
PASSWORD="${PASSWORD:-password}"
EMAIL="${EMAIL:-${USERNAME}@example.com}"
SUBMIT_MAX="${SUBMIT_MAX:-800}"
SUBMIT_INTERVAL_SEC="${SUBMIT_INTERVAL_SEC:-0.05}"
RECOVERY_TIMEOUT_SEC="${RECOVERY_TIMEOUT_SEC:-60}"
RECOVERY_POLL_SEC="${RECOVERY_POLL_SEC:-2}"

JWT_SECRET="${JWT_SECRET:-dev_jwt_secret_change_me}"
ADMIN_USERS="${ADMIN_USERS:-admin}"
REDIS_PASSWORD="${REDIS_PASSWORD:-deepoj_redis_change_me}"
STREAM_KEY="${JOB_STREAM_KEY:-deepoj:jobs}"
STREAM_GROUP="${JOB_STREAM_GROUP:-deepoj:workers}"

TMP_DIR="$(mktemp -d)"
LAST_STATUS=""
LAST_BODY_FILE=""
LAST_HEADERS_FILE=""

cleanup() {
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT

usage() {
  cat <<USAGE
Usage: scripts/verify_d3_backpressure.sh

Environment overrides:
  API_BASE               (default: http://127.0.0.1:18080)
  USERNAME               (default: admin)
  PASSWORD               (default: password)
  EMAIL                  (default: <USERNAME>@example.com)
  SUBMIT_MAX             (default: 800)
  SUBMIT_INTERVAL_SEC    (default: 0.05)
  RECOVERY_TIMEOUT_SEC   (default: 60)
  RECOVERY_POLL_SEC      (default: 2)

  JWT_SECRET             (default: dev_jwt_secret_change_me)
  ADMIN_USERS            (default: admin)
  REDIS_PASSWORD         (default: deepoj_redis_change_me)
  JOB_STREAM_KEY         (default: deepoj:jobs)
  JOB_STREAM_GROUP       (default: deepoj:workers)
USAGE
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi

if ! command -v curl >/dev/null 2>&1; then
  echo "ERROR: curl not found" >&2
  exit 2
fi
if ! command -v python3 >/dev/null 2>&1; then
  echo "ERROR: python3 not found" >&2
  exit 2
fi
if ! command -v docker >/dev/null 2>&1; then
  echo "ERROR: docker not found" >&2
  exit 2
fi

print_body_snippet() {
  local file="$1"
  if [[ ! -f "$file" || ! -s "$file" ]]; then
    echo "<empty>"
    return
  fi
  local sz
  sz="$(wc -c < "$file" | tr -d ' ')"
  if [[ "$sz" -le 2048 ]]; then
    cat "$file"
    return
  fi
  head -c 2048 "$file"
  echo
  echo "... [truncated, total ${sz} bytes]"
}

compose_service_exists() {
  local svc="$1"
  docker compose config --services 2>/dev/null | grep -qx "$svc"
}

emit_diagnostics() {
  echo
  echo "========== diagnostics ==========" >&2
  echo "[compose ps]" >&2
  docker compose ps >&2 || true

  for svc in api worker scheduler redis; do
    if compose_service_exists "$svc"; then
      echo "[logs: $svc --tail=200]" >&2
      docker compose logs --tail=200 "$svc" >&2 || true
    fi
  done

  if [[ -x "scripts/collect_stream_pressure.sh" ]]; then
    echo "[stream pressure snapshot]" >&2
    STREAM_KEY="$STREAM_KEY" GROUP="$STREAM_GROUP" REDIS_PASSWORD="$REDIS_PASSWORD" \
      bash scripts/collect_stream_pressure.sh >&2 || true
  fi
  echo "=================================" >&2
}

cleanup_conflicting_containers() {
  local name
  for name in oj-api oj-worker oj-scheduler oj-redis oj-postgres oj-minio; do
    if docker ps -a --format '{{.Names}}' | grep -qx "$name"; then
      docker rm -f "$name" >/dev/null 2>&1 || true
    fi
  done
}

fatal() {
  local msg="$1"
  echo "ERROR: $msg" >&2
  emit_diagnostics
  exit 1
}

fatal_request() {
  local method="$1"
  local url="$2"
  local expected="$3"
  local status="$4"
  local body_file="$5"

  echo "ERROR: HTTP request assertion failed" >&2
  echo "  METHOD: $method" >&2
  echo "  URL: $url" >&2
  echo "  expected: $expected" >&2
  echo "  actual: ${status:-<none>}" >&2
  echo "  body (<=2KB):" >&2
  print_body_snippet "$body_file" >&2
  emit_diagnostics
  exit 1
}

http_request() {
  local method="$1"
  local url="$2"
  local expected_csv="$3"
  shift 3

  local body_file="$TMP_DIR/body_$(date +%s%N).txt"
  local headers_file="$TMP_DIR/headers_$(date +%s%N).txt"
  local status=""

  if ! status="$(curl -sS -X "$method" "$url" -D "$headers_file" -o "$body_file" -w '%{http_code}' "$@")"; then
    fatal_request "$method" "$url" "$expected_csv" "$status" "$body_file"
  fi

  LAST_STATUS="$status"
  LAST_BODY_FILE="$body_file"
  LAST_HEADERS_FILE="$headers_file"

  local matched=0
  local exp
  IFS=',' read -r -a exp <<< "$expected_csv"
  for code in "${exp[@]}"; do
    if [[ "$status" == "$code" ]]; then
      matched=1
      break
    fi
  done

  if [[ "$matched" -ne 1 ]]; then
    fatal_request "$method" "$url" "$expected_csv" "$status" "$body_file"
  fi
}

header_value() {
  local key="$1"
  python3 - "$key" "$LAST_HEADERS_FILE" <<'PY'
import pathlib
import sys

key = sys.argv[1].lower()
path = pathlib.Path(sys.argv[2])
for line in path.read_text(encoding="utf-8", errors="replace").splitlines():
    if ":" not in line:
        continue
    k, v = line.split(":", 1)
    if k.strip().lower() == key:
        print(v.strip())
        break
PY
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

wait_api_ready() {
  local deadline=$(( $(date +%s) + 60 ))
  while true; do
    if curl -sS -o /dev/null -w '%{http_code}' "$API_BASE/api/v1/health" | grep -q '^200$'; then
      return 0
    fi
    if [[ $(date +%s) -ge $deadline ]]; then
      return 1
    fi
    sleep 1
  done
}

ensure_stream_group() {
  local out
  if ! out="$(docker compose exec -T redis redis-cli -a "$REDIS_PASSWORD" --raw XGROUP CREATE "$STREAM_KEY" "$STREAM_GROUP" '$' MKSTREAM 2>&1)"; then
    if printf '%s' "$out" | grep -q 'BUSYGROUP'; then
      return 0
    fi
    fatal "failed to ensure stream group: $out"
  fi
}

export JWT_SECRET ADMIN_USERS REDIS_PASSWORD STREAM_KEY STREAM_GROUP

echo "[1/8] start core services (api/scheduler/redis/postgres/minio)"
cleanup_conflicting_containers
if ! docker compose up -d --build api scheduler redis postgres minio; then
  fatal "docker compose up failed"
fi

if ! wait_api_ready; then
  fatal "api not ready within 60s"
fi

echo "[2/8] ensure stream consumer group exists"
ensure_stream_group

echo "[3/8] stop worker to create backlog"
docker compose stop worker >/dev/null 2>&1 || true

echo "[4/8] register/login and get token"
REGISTER_BODY="$(USERNAME="$USERNAME" PASSWORD="$PASSWORD" EMAIL="$EMAIL" python3 - <<'PY'
import json, os
print(json.dumps({"username": os.environ["USERNAME"], "password": os.environ["PASSWORD"], "email": os.environ["EMAIL"]}, ensure_ascii=False))
PY
)"
http_request POST "$API_BASE/api/v1/auth/register" "201,409" \
  -H 'Content-Type: application/json' \
  -d "$REGISTER_BODY"

http_request POST "$API_BASE/api/v1/auth/login" "200" \
  -H 'Content-Type: application/json' \
  -d "$(USERNAME="$USERNAME" PASSWORD="$PASSWORD" python3 - <<'PY'
import json, os
print(json.dumps({"username": os.environ["USERNAME"], "password": os.environ["PASSWORD"]}, ensure_ascii=False))
PY
)"
auth_resp="$(cat "$LAST_BODY_FILE")"
TOKEN="$(printf '%s' "$auth_resp" | json_field token 2>/dev/null || true)"
if [[ -z "$TOKEN" ]]; then
  fatal "missing token in login response"
fi

echo "[5/8] upload minimal problem.zip"
problem_zip="$TMP_DIR/problem.zip"
python3 - "$problem_zip" <<'PY'
import sys, zipfile
with zipfile.ZipFile(sys.argv[1], "w", compression=zipfile.ZIP_DEFLATED) as zf:
    zf.writestr("1.in", "1 2\n")
    zf.writestr("1.out", "3\n")
PY

http_request POST "$API_BASE/api/v1/problems" "200" \
  -H "Authorization: Bearer $TOKEN" \
  -F 'title=D3 Backpressure Verify' \
  -F 'time_limit=1000' \
  -F 'memory_limit=128' \
  -F "file=@$problem_zip"
upload_resp="$(cat "$LAST_BODY_FILE")"
PROBLEM_ID="$(printf '%s' "$upload_resp" | json_field id 2>/dev/null || true)"
if ! [[ "$PROBLEM_ID" =~ ^[0-9]+$ ]]; then
  fatal "invalid problem id from upload response"
fi

echo "problem_id=$PROBLEM_ID"

echo "[6/8] submit loop until backpressure 429"
SOURCE_CODE="$(cat <<'CPP'
#include <iostream>
int main(){int a,b;std::cin>>a>>b;std::cout<<(a+b);return 0;}
CPP
)"
SUBMIT_BODY="$(PROBLEM_ID="$PROBLEM_ID" SOURCE_CODE="$SOURCE_CODE" python3 - <<'PY'
import json, os
print(json.dumps({
    "problem_id": int(os.environ["PROBLEM_ID"]),
    "language": 1,
    "code": os.environ["SOURCE_CODE"],
    "time_limit": 1000,
    "memory_limit": 65536,
}, ensure_ascii=False))
PY
)"

echo "submit body: $SUBMIT_BODY"

backpressure_hit=0
backpressure_reason=""
backpressure_retry_after=""
for i in $(seq 1 "$SUBMIT_MAX"); do
  http_request POST "$API_BASE/api/v1/submit" "200,429" \
    -H "Authorization: Bearer $TOKEN" \
    -H 'Content-Type: application/json' \
    -d "$SUBMIT_BODY"
  resp="$(cat "$LAST_BODY_FILE")"

  echo "submit[$i] status=$LAST_STATUS"

  if [[ "$LAST_STATUS" == "429" ]]; then
    backpressure_retry_after="$(header_value 'Retry-After' | tr -d '\r' || true)"
    if [[ -z "$backpressure_retry_after" ]]; then
      fatal "429 returned without Retry-After header"
    fi

    err_value="$(printf '%s' "$resp" | json_field error 2>/dev/null || true)"
    backpressure_reason="$(printf '%s' "$resp" | json_field reason 2>/dev/null || true)"
    if [[ "$err_value" != "backpressure" || -z "$backpressure_reason" ]]; then
      fatal "429 response is not backpressure json"
    fi

    echo "EVIDENCE_429: status=429 retry_after=$backpressure_retry_after reason=$backpressure_reason" 
    backpressure_hit=1
    break
  fi

  sleep "$SUBMIT_INTERVAL_SEC"
done

if [[ "$backpressure_hit" -ne 1 ]]; then
  fatal "did not observe backpressure 429 within SUBMIT_MAX=$SUBMIT_MAX"
fi

echo "[7/8] resume worker"
docker compose up -d worker >/dev/null

echo "[8/8] wait for recovery (no more 429)"
recovered=0
deadline=$(( $(date +%s) + RECOVERY_TIMEOUT_SEC ))
while [[ $(date +%s) -lt $deadline ]]; do
  http_request POST "$API_BASE/api/v1/submit" "200,202,429" \
    -H "Authorization: Bearer $TOKEN" \
    -H 'Content-Type: application/json' \
    -d "$SUBMIT_BODY"
  resp="$(cat "$LAST_BODY_FILE")"

  if [[ "$LAST_STATUS" != "429" ]]; then
    reason="$(printf '%s' "$resp" | json_field status 2>/dev/null || true)"
    echo "EVIDENCE_RECOVERED: status=$LAST_STATUS submit_status=${reason:-unknown}"
    recovered=1
    break
  fi

  echo "recovery probe status=429; waiting..."
  sleep "$RECOVERY_POLL_SEC"
done

if [[ "$recovered" -ne 1 ]]; then
  fatal "worker resumed but submit still 429 after ${RECOVERY_TIMEOUT_SEC}s"
fi

echo "D3 backpressure verification passed"
