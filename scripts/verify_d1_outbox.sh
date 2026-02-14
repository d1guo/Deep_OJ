#!/usr/bin/env bash
set -euo pipefail

API_BASE="${API_BASE:-http://127.0.0.1:18080}"
STREAM_KEY="${JOB_STREAM_KEY:-deepoj:jobs}"
REDIS_CONTAINER="${REDIS_CONTAINER:-oj-redis}"
POSTGRES_CONTAINER="${POSTGRES_CONTAINER:-oj-postgres}"
POSTGRES_DB="${POSTGRES_DB:-deep_oj}"
POSTGRES_USER="${POSTGRES_USER:-deep_oj}"
POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-}"
REDIS_PASSWORD="${REDIS_PASSWORD:-}"
USERNAME="${USERNAME:-admin}"
PASSWORD="${PASSWORD:-password}"
PROBLEM_ID="${PROBLEM_ID:-}"
WAIT_SECONDS="${WAIT_SECONDS:-30}"

if [ -z "$PROBLEM_ID" ]; then
  echo "PROBLEM_ID is required" >&2
  exit 1
fi
if [ -z "$POSTGRES_PASSWORD" ]; then
  echo "POSTGRES_PASSWORD is required" >&2
  exit 1
fi
if [ -z "$REDIS_PASSWORD" ]; then
  echo "REDIS_PASSWORD is required" >&2
  exit 1
fi

cleanup() {
  if ! docker ps --format '{{.Names}}' | grep -qx "$REDIS_CONTAINER"; then
    docker start "$REDIS_CONTAINER" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

TOKEN="$(curl -sS -X POST "$API_BASE/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"$USERNAME\",\"password\":\"$PASSWORD\"}" \
  | python3 -c 'import sys,json;print(json.load(sys.stdin).get("token",""))')"
if [ -z "$TOKEN" ]; then
  echo "failed to fetch auth token" >&2
  exit 2
fi

echo "[1/5] stop redis to simulate outage: $REDIS_CONTAINER"
docker stop "$REDIS_CONTAINER" >/dev/null

echo "[2/5] submit while redis is down"
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
  echo "submit failed: missing job_id (expected success in outbox mode)" >&2
  exit 3
fi
echo "submitted job_id=$JOB_ID"

echo "[3/5] verify outbox pending row exists"
STATUS="$(docker exec -e PGPASSWORD="$POSTGRES_PASSWORD" -i "$POSTGRES_CONTAINER" \
  psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -tAc \
  "SELECT status FROM outbox_events WHERE job_id='${JOB_ID}' LIMIT 1;" | tr -d '[:space:]')"
if [ "$STATUS" != "pending" ]; then
  echo "expected outbox status=pending while redis down, got: ${STATUS:-<empty>}" >&2
  exit 4
fi

echo "[4/5] restore redis and wait for dispatcher"
docker start "$REDIS_CONTAINER" >/dev/null
deadline=$(( $(date +%s) + WAIT_SECONDS ))
while true; do
  delivered="$(docker exec -e PGPASSWORD="$POSTGRES_PASSWORD" -i "$POSTGRES_CONTAINER" \
    psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -tAc \
    "SELECT COALESCE(stream_entry_id,'') FROM outbox_events WHERE job_id='${JOB_ID}' AND dispatched_at IS NOT NULL LIMIT 1;" | tr -d '[:space:]')"
  if [ -n "$delivered" ]; then
    STREAM_ENTRY_ID="$delivered"
    break
  fi
  if [ "$(date +%s)" -ge "$deadline" ]; then
    echo "dispatcher did not deliver within ${WAIT_SECONDS}s" >&2
    exit 5
  fi
  sleep 1
done

echo "[5/5] verify stream contains job"
XRANGE_RAW="$(docker exec -i "$REDIS_CONTAINER" redis-cli -a "$REDIS_PASSWORD" --raw XREVRANGE "$STREAM_KEY" + - COUNT 200)"
if ! printf '%s\n' "$XRANGE_RAW" | grep -q "$JOB_ID"; then
  echo "stream does not contain job_id=$JOB_ID after delivery" >&2
  exit 6
fi

echo "OK D1 verified: job_id=$JOB_ID stream_entry_id=$STREAM_ENTRY_ID"
