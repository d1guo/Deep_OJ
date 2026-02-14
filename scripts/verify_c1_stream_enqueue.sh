#!/usr/bin/env bash
set -euo pipefail

API_BASE="${API_BASE:-http://127.0.0.1:18080}"
STREAM_KEY="${JOB_STREAM_KEY:-deepoj:jobs}"
REDIS_CONTAINER="${REDIS_CONTAINER:-oj-redis}"
REDIS_PASSWORD="${REDIS_PASSWORD:-}"
USERNAME="${USERNAME:-admin}"
PASSWORD="${PASSWORD:-password}"
PROBLEM_ID="${PROBLEM_ID:-}"

if [ -z "$PROBLEM_ID" ]; then
  echo "PROBLEM_ID is required" >&2
  exit 1
fi

if [ -z "$REDIS_PASSWORD" ]; then
  echo "REDIS_PASSWORD is required" >&2
  exit 1
fi

TOKEN="$(curl -sS -X POST "$API_BASE/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"$USERNAME\",\"password\":\"$PASSWORD\"}" \
  | python3 -c 'import sys,json;print(json.load(sys.stdin).get("token",""))')"

if [ -z "$TOKEN" ]; then
  echo "failed to fetch auth token" >&2
  exit 1
fi

JOB_ID="$(curl -sS -X POST "$API_BASE/api/v1/submit" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d "$(cat <<JSON
{
  \"problem_id\": $PROBLEM_ID,
  \"language\": 1,
  \"code\": \"#include <iostream>\\nint main(){int a,b;std::cin>>a>>b;std::cout<<(a+b);return 0;}\",
  \"time_limit\": 1000,
  \"memory_limit\": 128
}
JSON
)" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("job_id",""))')"

if [ -z "$JOB_ID" ]; then
  echo "submit failed: missing job_id" >&2
  exit 1
fi

XRANGE_RAW="$(docker exec -i "$REDIS_CONTAINER" redis-cli -a "$REDIS_PASSWORD" --raw XREVRANGE "$STREAM_KEY" + - COUNT 200)"

PARSED="$(printf '%s\n' "$XRANGE_RAW" | python3 - "$JOB_ID" <<'PY'
import json
import re
import sys

job_id = sys.argv[1]
lines = [line for line in sys.stdin.read().splitlines() if line.strip()]
entries = []
i = 0
while i < len(lines):
    entry_id = lines[i]
    i += 1
    fields = {}
    while i + 1 < len(lines) and not re.fullmatch(r"\d+-\d+", lines[i]):
        key = lines[i]
        val = lines[i + 1]
        fields[key] = val
        i += 2
    entries.append((entry_id, fields))

hit = None
for entry_id, fields in entries:
    if fields.get("job_id") == job_id:
        hit = (entry_id, fields)
        break
if not hit:
    raise SystemExit("job_id not found in stream")

entry_id, fields = hit
required = ["job_id", "enqueue_ts", "payload_ref", "priority"]
for key in required:
    if key not in fields:
        raise SystemExit(f"missing stream field: {key}")

enqueue_ts = fields["enqueue_ts"]
if not enqueue_ts.isdigit() or len(enqueue_ts) < 13:
    raise SystemExit(f"enqueue_ts is not millisecond timestamp: {enqueue_ts}")

print(json.dumps({
    "stream_entry_id": entry_id,
    "payload_ref": fields["payload_ref"],
    "enqueue_ts": enqueue_ts,
}, ensure_ascii=True))
PY
)"

STREAM_ENTRY_ID="$(printf '%s' "$PARSED" | python3 -c 'import sys,json;print(json.load(sys.stdin)["stream_entry_id"])')"
PAYLOAD_REF="$(printf '%s' "$PARSED" | python3 -c 'import sys,json;print(json.load(sys.stdin)["payload_ref"])')"

PAYLOAD_RAW="$(docker exec -i "$REDIS_CONTAINER" redis-cli -a "$REDIS_PASSWORD" --raw GET "$PAYLOAD_REF")"
if [ -z "$PAYLOAD_RAW" ]; then
  echo "payload GET returned empty value for key: $PAYLOAD_REF" >&2
  exit 2
fi
printf '%s\n' "$PAYLOAD_RAW" | python3 - <<'PY'
import json
import sys
raw = sys.stdin.read()
try:
    payload = json.loads(raw)
except json.JSONDecodeError as exc:
    raise SystemExit(f"payload is not valid JSON: {exc}")

if payload.get("schema_version") != 1:
    raise SystemExit("payload schema_version != 1")
if "task_data_b64" not in payload:
    raise SystemExit("payload missing task_data_b64")
if payload.get("encoding", "base64") != "base64":
    raise SystemExit("payload encoding must be base64")
if payload.get("content_type", "application/json") != "application/json":
    raise SystemExit("payload content_type must be application/json")
print("payload JSON/envelope verified")
PY

echo "OK stream_entry_id=$STREAM_ENTRY_ID job_id=$JOB_ID stream_key=$STREAM_KEY payload_ref=$PAYLOAD_REF"
