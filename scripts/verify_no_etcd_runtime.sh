#!/usr/bin/env bash
set -euo pipefail

# 脚本用途：
# 1) 启动 compose 并确认运行时完全不依赖 etcd。
# 2) 串行执行端到端、崩溃恢复、可观测性与基础 CI 验收。
# 3) 在环境满足时附加执行“进程树清理”安全验收。
# 失败时会自动打印 compose 诊断日志，便于快速定位问题。

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

# 诊断与通用执行函数
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
  echo "========== diagnostics ==========" >&2
  echo "[docker compose ps]" >&2
  docker compose ps >&2 || true

  for service in api worker scheduler; do
    if compose_service_exists "$service"; then
      echo "[docker compose logs --tail=200 $service]" >&2
      docker compose logs --tail=200 "$service" >&2 || true
    fi
  done
  echo "=================================" >&2
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

assert_no_etcd_service_defined() {
  if docker compose config --services 2>/dev/null | grep -qx 'etcd'; then
    fatal "compose 配置中仍存在 etcd 服务"
  fi
}

assert_no_etcd_service_running() {
  if docker compose ps --services 2>/dev/null | grep -qx 'etcd'; then
    fatal "compose 运行列表中存在 etcd 服务"
  fi
  if docker compose ps 2>/dev/null | grep -Eiq '(^|[[:space:]])(etcd|oj-etcd)([[:space:]]|$)'; then
    fatal "compose ps 输出中检测到 etcd/oj-etcd"
  fi
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

# 可选执行“进程树清理”验收（依赖额外权限，可能在部分环境不满足）
run_optional_process_cleanup() {
  if [[ ! -f scripts/verify_worker_process_cleanup.sh ]]; then
    echo "[7/8] 跳过进程清理验收：未找到 scripts/verify_worker_process_cleanup.sh"
    return 0
  fi

  if [[ -f scripts/verify_sandbox_prerequisites.sh ]]; then
    echo "[7/8] 检查进程清理验收前置条件"
    if ! bash scripts/verify_sandbox_prerequisites.sh; then
      echo "[7/8] 跳过进程清理验收：当前环境不满足前置条件" >&2
      return 0
    fi
  else
    echo "[7/8] 未找到 verify_sandbox_prerequisites.sh，按可运行处理并直接执行进程清理验收"
  fi

  echo "[7/8] 执行 scripts/verify_worker_process_cleanup.sh"
  if ! bash scripts/verify_worker_process_cleanup.sh; then
    fatal "脚本执行失败: scripts/verify_worker_process_cleanup.sh"
  fi
}

require_cmd docker
require_cmd bash

export JWT_SECRET="${JWT_SECRET:-dev_jwt_secret_change_me}"
export ADMIN_USERS="${ADMIN_USERS:-admin}"
export REDIS_PASSWORD="${REDIS_PASSWORD:-deepoj_redis_change_me}"
export POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-deepoj_pg_password_change_me}"
export MINIO_ROOT_USER="${MINIO_ROOT_USER:-deepoj_minio_user}"
export MINIO_ROOT_PASSWORD="${MINIO_ROOT_PASSWORD:-deepoj_minio_password_change_me}"

# 主流程：
# 先做架构红线检查（无 etcd），再串行跑功能验收，最后尝试安全验收。
echo "[1/8] docker compose up -d --build"
assert_no_etcd_service_defined
if ! docker compose up -d --build; then
  fatal "docker compose up 失败"
fi

echo "[2/8] docker compose ps 断言不存在 etcd"
assert_no_etcd_service_defined
assert_no_etcd_service_running

run_script_step "[3/8] 执行 scripts/verify_end_to_end_flow.sh" "scripts/verify_end_to_end_flow.sh"
run_script_step "[4/8] 执行 scripts/verify_crash_recovery.sh" "scripts/verify_crash_recovery.sh"
run_script_step "[5/8] 执行 scripts/verify_observability_pipeline.sh" "scripts/verify_observability_pipeline.sh"
run_script_step "[6/8] 执行 scripts/verify_ci.sh" "scripts/verify_ci.sh"
run_optional_process_cleanup

echo "[8/8] 无 etcd 启动验证通过"
