#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

COMPOSE_BUILD="${COMPOSE_BUILD:-1}"
KEEP_TMP="${KEEP_TMP:-0}"
TMP_DIR="$(mktemp -d)"
OVERRIDE_FILE="$TMP_DIR/docker-compose.b3.override.yml"
STREAM_KEY="${JOB_STREAM_KEY:-deepoj:jobs}"
POSTGRES_CONTAINER="${POSTGRES_CONTAINER:-oj-postgres}"
POSTGRES_DB="${POSTGRES_DB:-deep_oj}"
POSTGRES_USER="${POSTGRES_USER:-deep_oj}"
REDIS_CONTAINER="${REDIS_CONTAINER:-oj-redis}"
LOG_SINCE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

cleanup() {
  if [[ "$KEEP_TMP" == "1" ]]; then
    echo "KEEP_TMP=1, 保留临时目录: $TMP_DIR" >&2
    return
  fi
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT

require_cmd() {
  local cmd="$1"
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "ERROR: 缺少命令: $cmd" >&2
    exit 2
  fi
}

emit_diagnostics() {
  echo >&2
  echo "[docker compose ps]" >&2
  docker compose ps >&2 || true

  echo "[docker compose logs --tail=200 scheduler]" >&2
  docker compose logs --no-color --since "$LOG_SINCE" --tail=200 scheduler >&2 || true

  echo "[redis stream snippet <=2KB]" >&2
  if docker ps --format '{{.Names}}' | rg -qx "$REDIS_CONTAINER"; then
    docker exec "$REDIS_CONTAINER" redis-cli -a "$REDIS_PASSWORD" XREVRANGE "$STREAM_KEY" + - COUNT 20 2>/dev/null | head -c 2048 >&2 || true
    echo >&2
  else
    echo "容器不存在: $REDIS_CONTAINER" >&2
  fi
}

fatal() {
  local msg="$1"
  echo "ERROR: $msg" >&2
  emit_diagnostics
  exit 1
}

wait_scheduler_markers() {
  local timeout_sec="${1:-60}"
  local deadline=$(( $(date +%s) + timeout_sec ))

  while true; do
    local logs
    logs="$(docker compose logs --no-color --since "$LOG_SINCE" --tail=1200 scheduler 2>/dev/null || true)"

    if echo "$logs" | rg -q '(dispatch_mode=streams_only|"dispatch_mode":"streams_only")' \
      && echo "$logs" | rg -q '(control_plane_only=true|"control_plane_only":true)' \
      && echo "$logs" | rg -q '(legacy_loops_started=0|"legacy_loops_started":0)' \
      && echo "$logs" | rg -q '(control_plane_loops_started=repair,gc|"control_plane_loops_started":"repair,gc")'; then
      return 0
    fi

    if [[ $(date +%s) -ge "$deadline" ]]; then
      return 1
    fi
    sleep 1
  done
}

wait_default_disabled_logs() {
  local timeout_sec="${1:-60}"
  local deadline=$(( $(date +%s) + timeout_sec ))

  while true; do
    local logs
    logs="$(docker compose logs --no-color --since "$LOG_SINCE" --tail=1200 scheduler 2>/dev/null || true)"

    if echo "$logs" | rg -q '(event=repair_disabled|"event":"repair_disabled")' \
      && echo "$logs" | rg -q '(event=gc_disabled|"event":"gc_disabled")'; then
      return 0
    fi

    if [[ $(date +%s) -ge "$deadline" ]]; then
      return 1
    fi
    sleep 1
  done
}

wait_repair_xadd_log() {
  local job_id="$1"
  local timeout_sec="${2:-90}"
  local deadline=$(( $(date +%s) + timeout_sec ))

  while true; do
    local logs
    logs="$(docker compose logs --no-color --since "$LOG_SINCE" --tail=1500 scheduler 2>/dev/null || true)"

    if echo "$logs" | rg -q '(event=repair_xadd|"event":"repair_xadd")' \
      && echo "$logs" | rg -q --fixed-strings "$job_id"; then
      return 0
    fi

    if [[ $(date +%s) -ge "$deadline" ]]; then
      return 1
    fi
    sleep 1
  done
}

assert_stream_contains_job() {
  local job_id="$1"
  local stream_dump
  stream_dump="$(docker exec "$REDIS_CONTAINER" redis-cli -a "$REDIS_PASSWORD" XREVRANGE "$STREAM_KEY" + - COUNT 200 2>/dev/null || true)"
  if [[ -z "$stream_dump" ]]; then
    return 1
  fi
  echo "$stream_dump" | rg -q --fixed-strings "$job_id"
}

make_runtime_override() {
  cat > "$OVERRIDE_FILE" <<YAML
services:
  scheduler:
    environment:
      - SCHEDULER_REPAIR_ENABLED=true
      - SCHEDULER_REPAIR_INTERVAL_MS=1000
      - SCHEDULER_REPAIR_BATCH_SIZE=20
      - SCHEDULER_REPAIR_MIN_AGE_SEC=1
      - SCHEDULER_GC_ENABLED=false
YAML
}

insert_repair_candidate() {
  local job_id="$1"
  local sql="INSERT INTO submissions (job_id, code, language, time_limit, memory_limit, status, state, created_at, updated_at, attempt_id) VALUES ('${job_id}', E'#include <iostream>\\nint main(){return 0;}', 1, 1000, 65536, 'pending', 'pending', NOW() - INTERVAL '10 minutes', NOW() - INTERVAL '10 minutes', 0) ON CONFLICT (job_id) DO NOTHING;"

  docker exec \
    -e PGPASSWORD="$POSTGRES_PASSWORD" \
    "$POSTGRES_CONTAINER" \
    psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -c "$sql" >/dev/null
}

assert_no_control_plane_actions_before_override() {
  local logs
  logs="$(docker compose logs --no-color --since "$LOG_SINCE" --tail=1200 scheduler 2>/dev/null || true)"
  if echo "$logs" | rg -q '(event=repair_xadd|"event":"repair_xadd")'; then
    return 1
  fi
  if echo "$logs" | rg -q '(event=stream_trim|"event":"stream_trim")'; then
    return 1
  fi
  return 0
}

require_cmd docker
require_cmd bash
require_cmd rg

export JWT_SECRET="${JWT_SECRET:-dev_jwt_secret_change_me}"
export ADMIN_USERS="${ADMIN_USERS:-admin}"
export REDIS_PASSWORD="${REDIS_PASSWORD:-deepoj_redis_change_me}"
export POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-deepoj_pg_password_change_me}"
export MINIO_ROOT_USER="${MINIO_ROOT_USER:-deepoj_minio_user}"
export MINIO_ROOT_PASSWORD="${MINIO_ROOT_PASSWORD:-deepoj_minio_password_change_me}"

echo "[1/6] 启动 compose（COMPOSE_BUILD=$COMPOSE_BUILD）"
if [[ "$COMPOSE_BUILD" == "1" ]]; then
  docker compose up -d --build || fatal "docker compose up --build 失败"
else
  docker compose up -d || fatal "docker compose up 失败"
fi

echo "[2/6] 断言 scheduler 启动日志包含 control_plane_only=true 与 legacy_loops_started=0"
if ! wait_scheduler_markers 90; then
  fatal "scheduler 启动标记校验失败"
fi

echo "[3/6] 断言默认 repair/gc 不执行实际动作（disabled）"
if ! wait_default_disabled_logs 90; then
  fatal "未检测到 repair_disabled/gc_disabled 日志"
fi
if ! assert_no_control_plane_actions_before_override; then
  fatal "默认配置下出现了 repair_xadd 或 stream_trim 动作"
fi

echo "[4/6] runtime override 打开 repair，并制造可 repair 样例"
make_runtime_override
if [[ "$COMPOSE_BUILD" == "1" ]]; then
  docker compose -f docker-compose.yml -f "$OVERRIDE_FILE" up -d --build scheduler || fatal "应用 scheduler runtime override 失败"
else
  docker compose -f docker-compose.yml -f "$OVERRIDE_FILE" up -d scheduler || fatal "应用 scheduler runtime override 失败"
fi

if ! wait_scheduler_markers 90; then
  fatal "override 后 scheduler 启动标记校验失败"
fi

REPAIR_JOB_ID="b3-repair-$(date +%s)-$RANDOM"
insert_repair_candidate "$REPAIR_JOB_ID"

echo "[5/6] 验证 repair_xadd 日志与 Redis Stream 中出现 job_id"
if ! wait_repair_xadd_log "$REPAIR_JOB_ID" 90; then
  fatal "未检测到 repair_xadd 日志: $REPAIR_JOB_ID"
fi
if ! assert_stream_contains_job "$REPAIR_JOB_ID"; then
  fatal "Redis Stream 中未找到 repair job_id: $REPAIR_JOB_ID"
fi

echo "[6/6] B3 控制面验证通过"
echo "EVIDENCE_B3_MARKERS: control_plane_only=true legacy_loops_started=0"
echo "EVIDENCE_B3_DISABLED: repair_disabled=1 gc_disabled=1"
echo "EVIDENCE_B3_REPAIR: job_id=$REPAIR_JOB_ID stream=$STREAM_KEY"
