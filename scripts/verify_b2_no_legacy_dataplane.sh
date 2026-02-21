#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

KEEP_TMP="${KEEP_TMP:-0}"
TMP_DIR="$(mktemp -d)"

cleanup() {
  if [[ "$KEEP_TMP" == "1" ]]; then
    echo "KEEP_TMP=1, 保留临时目录: $TMP_DIR" >&2
    return
  fi
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT

compose_service_exists() {
  local service="$1"
  local svc
  while IFS= read -r svc; do
    [[ "$svc" == "$service" ]] && return 0
  done < <(docker compose config --services 2>/dev/null || true)
  return 1
}

emit_diagnostics() {
  echo >&2
  echo "========== 诊断信息 ==========" >&2
  echo "[docker compose ps]" >&2
  docker compose ps >&2 || true

  for service in api worker scheduler; do
    if compose_service_exists "$service"; then
      echo "[docker compose logs --tail=200 $service]" >&2
      docker compose logs --tail=200 "$service" >&2 || true
    fi
  done
  echo "================================" >&2
}

fatal() {
  local msg="$1"
  echo "ERROR: $msg" >&2
  emit_diagnostics
  exit 1
}

require_cmd() {
  local cmd="$1"
  if ! command -v "$cmd" >/dev/null 2>&1; then
    fatal "缺少命令: $cmd"
  fi
}

assert_no_legacy_scheduler_files() {
  local removed_files=(
    "src/go/internal/scheduler/dispatch.go"
    "src/go/internal/scheduler/ack_listener.go"
    "src/go/internal/scheduler/retry.go"
    "src/go/internal/scheduler/slow_path.go"
    "src/go/internal/scheduler/watchdog.go"
    "src/go/pkg/proto/judge_grpc.pb.go"
  )
  local f
  for f in "${removed_files[@]}"; do
    if [[ -e "$f" ]]; then
      fatal "仍存在 legacy 文件: $f"
    fi
  done
}

assert_no_legacy_keywords() {
  local keywords=(
    "QueuePending"
    "QueueProcessing"
    "TaskProcessingZSet"
    "BRPopLPush"
    "BRPOPLPUSH"
    "SubmitJob"
    "DispatchTask"
    "legacy_grpc_push"
    "dispatch_enabled"
    "queue:pending"
    "queue:processing"
    "stream:results"
    "results-group"
    "WORKER_AUTH_TOKEN"
    "ALLOW_INSECURE_WORKER_GRPC"
    "50052"
  )

  local kw
  for kw in "${keywords[@]}"; do
    if rg -n --fixed-strings \
      --glob '!scripts/verify_b2_no_legacy_dataplane.sh' \
      --glob '!*.diff' \
      -- "$kw" . >/dev/null; then
      echo "命中 legacy 关键字: $kw" >&2
      rg -n --fixed-strings \
        --glob '!scripts/verify_b2_no_legacy_dataplane.sh' \
        --glob '!*.diff' \
        -- "$kw" . >&2 || true
      fatal "存在 legacy 数据面关键字残留"
    fi
  done
}

run_script_step() {
  local label="$1"
  local script="$2"
  echo "$label"
  if [[ ! -f "$script" ]]; then
    fatal "脚本不存在: $script"
  fi
  if ! bash "$script"; then
    fatal "脚本执行失败: $script"
  fi
}

run_optional_g1() {
  if [[ ! -f scripts/verify_g1_kill_all.sh ]]; then
    echo "[7/8] 跳过 G1：未找到 scripts/verify_g1_kill_all.sh"
    return 0
  fi

  if [[ -f scripts/verify_g1_prereq.sh ]]; then
    echo "[7/8] 检查 G1 运行前置条件"
    if ! bash scripts/verify_g1_prereq.sh; then
      echo "[7/8] 跳过 G1：当前环境不满足前置条件" >&2
      return 0
    fi
  else
    echo "[7/8] 未找到 verify_g1_prereq.sh，按可运行处理并直接执行 G1"
  fi

  echo "[7/8] 执行 scripts/verify_g1_kill_all.sh"
  if ! bash scripts/verify_g1_kill_all.sh; then
    fatal "脚本执行失败: scripts/verify_g1_kill_all.sh"
  fi
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

echo "[1/8] docker compose up -d --build"
if ! docker compose up -d --build; then
  fatal "docker compose up 失败"
fi

echo "[2/8] 断言无 legacy 数据面残留"
assert_no_legacy_scheduler_files
assert_no_legacy_keywords

run_script_step "[3/8] 执行 scripts/verify_mvp2_e2e.sh" "scripts/verify_mvp2_e2e.sh"
run_script_step "[4/8] 执行 scripts/verify_mvp3_crash_recover.sh" "scripts/verify_mvp3_crash_recover.sh"
run_script_step "[5/8] 执行 scripts/verify_mvp4_observability.sh" "scripts/verify_mvp4_observability.sh"
run_script_step "[6/8] 执行 scripts/verify_ci.sh" "scripts/verify_ci.sh"
run_optional_g1

echo "[8/8] 无 legacy 数据面验证通过"
