#!/usr/bin/env bash
set -euo pipefail

API_BASE="${API_BASE:-http://127.0.0.1:18080}"
USERNAME="${USERNAME:-admin}"
PASSWORD="${PASSWORD:-password}"
EMAIL="${EMAIL:-${USERNAME}@example.com}"
POLL_STATUS="${POLL_STATUS:-1}"
POLL_TIMEOUT_SEC="${POLL_TIMEOUT_SEC:-20}"

TMP_DIR="$(mktemp -d)"
LAST_HTTP_STATUS=""
cleanup() {
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT

usage() {
  cat <<USAGE
Usage: scripts/smoke_e2e.sh

Environment overrides:
  API_BASE          (default: http://127.0.0.1:18080)
  USERNAME          (default: admin)
  PASSWORD          (default: password)
  EMAIL             (default: <USERNAME>@example.com)
  POLL_STATUS       (default: 1, set 0 to skip polling)
  POLL_TIMEOUT_SEC  (default: 20)

Note: upload endpoint is protected by AdminMiddleware, so USERNAME must be in ADMIN_USERS.
USAGE
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi

print_body_snippet_from_file() {
  local body_file="$1"
  local body_len
  body_len="$(wc -c < "$body_file" | tr -d ' ')"
  if [[ "$body_len" -le 2048 ]]; then
    cat "$body_file" >&2
    return
  fi

  head -c 2048 "$body_file" >&2
  echo >&2
  echo "... [truncated, total ${body_len} bytes]" >&2
}

fail_with_http_error() {
  local method="$1"
  local url="$2"
  local expected="$3"
  local status="$4"
  local body_file="$5"

  echo "ERROR: request failed" >&2
  echo "  method: $method" >&2
  echo "  url: $url" >&2
  echo "  expected status: $expected" >&2
  echo "  actual status: ${status:-<none>}" >&2
  echo "  response body (<=2KB):" >&2
  if [[ -s "$body_file" ]]; then
    print_body_snippet_from_file "$body_file"
  else
    echo "<empty>" >&2
  fi
  exit 1
}

fail_with_body_text() {
  local message="$1"
  local body_text="$2"
  local tmp_file="$TMP_DIR/resp_parse_error.txt"
  printf '%s' "$body_text" > "$tmp_file"

  echo "ERROR: $message" >&2
  echo "  response body (<=2KB):" >&2
  print_body_snippet_from_file "$tmp_file"
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
    fail_with_http_error "$method" "$url" "$expected_csv" "$status" "$body_file"
  fi

  LAST_HTTP_STATUS="$status"

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
    fail_with_http_error "$method" "$url" "$expected_csv" "$status" "$body_file"
  fi

  cat "$body_file"
}

json_field() {
  local field_path="$1"
  python3 -c '
import json
import sys

field_path = sys.argv[1]
raw = sys.stdin.read()
obj = json.loads(raw)
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
    fail_with_body_text "missing or invalid JSON field: $field_path" "$response_body"
  fi

  if [[ -z "$value" ]]; then
    fail_with_body_text "empty JSON field: $field_path" "$response_body"
  fi

  printf '%s' "$value"
}

echo "[1/5] register user"
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
REGISTER_RESPONSE="$(http_request POST "$API_BASE/api/v1/auth/register" "201,409" \
  -H 'Content-Type: application/json' \
  -d "$REGISTER_BODY")"
if [[ "$LAST_HTTP_STATUS" == "201" ]]; then
  echo "registered username=$USERNAME"
else
  echo "username already exists, continue: $USERNAME"
fi


echo "[2/5] login"
LOGIN_BODY="$(USERNAME="$USERNAME" PASSWORD="$PASSWORD" python3 - <<'PY'
import json
import os

print(json.dumps({
    "username": os.environ["USERNAME"],
    "password": os.environ["PASSWORD"],
}, ensure_ascii=False))
PY
)"
LOGIN_RESPONSE="$(http_request POST "$API_BASE/api/v1/auth/login" "200" \
  -H 'Content-Type: application/json' \
  -d "$LOGIN_BODY")"
TOKEN="$(require_json_field token "$LOGIN_RESPONSE")"

echo "[3/5] upload minimal problem.zip"
PROBLEM_ZIP="$TMP_DIR/problem.zip"
python3 - "$PROBLEM_ZIP" <<'PY'
import sys
import zipfile

zip_path = sys.argv[1]
with zipfile.ZipFile(zip_path, "w", compression=zipfile.ZIP_DEFLATED) as zf:
    zf.writestr("1.in", "1 2\n")
    zf.writestr("1.out", "3\n")
PY

UPLOAD_RESPONSE="$(http_request POST "$API_BASE/api/v1/problems" "200" \
  -H "Authorization: Bearer $TOKEN" \
  -F "title=Smoke Test A+B" \
  -F "time_limit=1000" \
  -F "memory_limit=128" \
  -F "file=@$PROBLEM_ZIP")"
PROBLEM_ID="$(require_json_field id "$UPLOAD_RESPONSE")"
if ! [[ "$PROBLEM_ID" =~ ^[0-9]+$ ]]; then
  fail_with_body_text "problem id is not numeric: $PROBLEM_ID" "$UPLOAD_RESPONSE"
fi

echo "uploaded problem_id=$PROBLEM_ID"

echo "[4/5] submit code"
SOURCE_CODE="$(cat <<'CPP'
#include <iostream>
int main(){int a,b;std::cin>>a>>b;std::cout<<(a+b);return 0;}
CPP
)"
SUBMIT_BODY="$(PROBLEM_ID="$PROBLEM_ID" SOURCE_CODE="$SOURCE_CODE" python3 - <<'PY'
import json
import os

problem_id = int(os.environ["PROBLEM_ID"])
payload = {
    "problem_id": problem_id,
    "language": 1,
    "code": os.environ["SOURCE_CODE"],
    "time_limit": 1000,
    "memory_limit": 65536,
}
print(json.dumps(payload, ensure_ascii=False))
PY
)"

echo "submit body: $SUBMIT_BODY"

SUBMIT_RESPONSE="$(http_request POST "$API_BASE/api/v1/submit" "200" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d "$SUBMIT_BODY")"
JOB_ID="$(require_json_field job_id "$SUBMIT_RESPONSE")"
echo "submitted job_id=$JOB_ID"

if [[ "$POLL_STATUS" != "1" ]]; then
  echo "[5/5] poll skipped (POLL_STATUS=$POLL_STATUS)"
  exit 0
fi

echo "[5/5] poll status (timeout=${POLL_TIMEOUT_SEC}s)"
deadline_at=$(( $(date +%s) + POLL_TIMEOUT_SEC ))
last_response=''
while true; do
  STATUS_RESPONSE="$(http_request GET "$API_BASE/api/v1/status/$JOB_ID" "200" \
    -H "Authorization: Bearer $TOKEN")"
  last_response="$STATUS_RESPONSE"

  STATUS_VALUE="$(printf '%s' "$STATUS_RESPONSE" | json_field status 2>/dev/null || true)"
  if [[ -z "$STATUS_VALUE" ]]; then
    fail_with_body_text "missing or invalid status field in /status response" "$STATUS_RESPONSE"
  fi
  VERDICT_VALUE="$(printf '%s' "$STATUS_RESPONSE" | json_field data.verdict 2>/dev/null || true)"

  echo "status=$STATUS_VALUE verdict=$VERDICT_VALUE"

  if [[ "$STATUS_VALUE" == "Finished" || "$STATUS_VALUE" == "Accepted" || "$VERDICT_VALUE" == "AC" || "$VERDICT_VALUE" == "Accepted" ]]; then
    echo "final response: $STATUS_RESPONSE"
    exit 0
  fi

  if [[ $(date +%s) -ge $deadline_at ]]; then
    fail_with_body_text "poll timeout after ${POLL_TIMEOUT_SEC}s" "$last_response"
  fi

  sleep 1
done
