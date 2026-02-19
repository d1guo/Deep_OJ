#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

IMAGE="${IMAGE:-deep-oj:v3}"
HOST_WORKSPACE="${HOST_WORKSPACE:-$ROOT_DIR/data/workspace}"
REQUEST_PREFIX="${REQUEST_PREFIX:-g1_prereq}"
KEEP_TMP="${KEEP_TMP:-0}"
LOG_TRUNCATE_BYTES="${LOG_TRUNCATE_BYTES:-2048}"

TMP_DIR="$(mktemp -d)"
SRC_FILE="$TMP_DIR/g1_prereq.cpp"

cleanup() {
  if [[ "$KEEP_TMP" == "1" ]]; then
    echo "KEEP_TMP=1, tmp directory retained: $TMP_DIR" >&2
    return
  fi
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT

fatal() {
  echo "ERROR: $*" >&2
  exit 1
}

require_cmd() {
  local cmd="$1"
  if ! command -v "$cmd" >/dev/null 2>&1; then
    fatal "missing command: $cmd"
  fi
}

print_snippet() {
  local text="$1"
  local max_len="$2"
  if [[ ${#text} -le "$max_len" ]]; then
    printf '%s\n' "$text"
    return
  fi
  printf '%s\n' "${text:0:max_len}"
  echo "... [truncated, total ${#text} bytes]"
}

run_compile() {
  local mode="$1"
  shift || true
  local request_id="${REQUEST_PREFIX}_${mode}_$(date +%s%N)"

  docker run --rm \
    -v "$HOST_WORKSPACE:/data/workspace" \
    -v "$SRC_FILE:/tmp/g1_prereq.cpp:ro" \
    --cap-add SYS_ADMIN \
    --cap-add SYS_RESOURCE \
    --cap-add SYS_CHROOT \
    --cap-add SETUID \
    --cap-add SETGID \
    "$@" \
    "$IMAGE" \
    /app/judge_engine --compile -s /tmp/g1_prereq.cpp -r "$request_id" -C /app/config.yaml 2>&1
}

for cmd in bash docker grep mktemp; do
  require_cmd "$cmd"
done

echo "[1/6] check docker daemon and image"
docker info >/dev/null 2>&1 || fatal "docker daemon unavailable"
docker image inspect "$IMAGE" >/dev/null 2>&1 || fatal "image not found: $IMAGE (build first: docker build -t $IMAGE .)"

security_options="$(docker info --format '{{json .SecurityOptions}}' 2>/dev/null || echo "[]")"
cgroup_version="$(docker info --format '{{.CgroupVersion}}' 2>/dev/null || echo "unknown")"
default_unconfined=0
if printf '%s' "$security_options" | grep -q 'profile=unconfined'; then
  default_unconfined=1
fi

echo "[2/6] check workspace path and host write permission"
mkdir -p "$HOST_WORKSPACE"
touch "$HOST_WORKSPACE/.g1_prereq_write_test" || fatal "workspace not writable: $HOST_WORKSPACE"
rm -f "$HOST_WORKSPACE/.g1_prereq_write_test"

cat >"$SRC_FILE" <<'CPP'
#include <iostream>
int main() { std::cout << "ok\n"; return 0; }
CPP

echo "[3/6] check container access to binary/config/workspace"
docker run --rm \
  -v "$HOST_WORKSPACE:/data/workspace" \
  --cap-add SYS_ADMIN \
  --cap-add SYS_RESOURCE \
  --cap-add SYS_CHROOT \
  --cap-add SETUID \
  --cap-add SETGID \
  "$IMAGE" \
  bash -lc 'test -x /app/judge_engine && test -r /app/config.yaml && test -w /data/workspace' \
  >/dev/null || fatal "container cannot access /app/judge_engine, /app/config.yaml, or /data/workspace"

echo "[4/6] compile with default seccomp"
set +e
default_output="$(run_compile default)"
default_rc=$?
set -e

default_compiled=0
default_pivot_failed=0
if printf '%s' "$default_output" | grep -q '"status":"Compiled"'; then
  default_compiled=1
fi
if printf '%s' "$default_output" | grep -q 'pivot_root_failed' && printf '%s' "$default_output" | grep -q 'step=pivot_root'; then
  default_pivot_failed=1
fi
if [[ "$default_compiled" -eq 0 && "$default_pivot_failed" -eq 0 ]]; then
  echo "[default seccomp output <=${LOG_TRUNCATE_BYTES}B]" >&2
  print_snippet "$default_output" "$LOG_TRUNCATE_BYTES" >&2
  fatal "default seccomp compile was neither Compiled nor pivot_root_failed"
fi

echo "[5/6] compile with seccomp=unconfined"
set +e
unconfined_output="$(run_compile unconfined --security-opt seccomp=unconfined)"
unconfined_rc=$?
set -e

unconfined_compiled=0
if printf '%s' "$unconfined_output" | grep -q '"status":"Compiled"'; then
  unconfined_compiled=1
fi
if [[ "$unconfined_compiled" -ne 1 ]]; then
  echo "[unconfined output <=${LOG_TRUNCATE_BYTES}B]" >&2
  print_snippet "$unconfined_output" "$LOG_TRUNCATE_BYTES" >&2
  fatal "seccomp=unconfined compile did not return status=Compiled"
fi

echo "[6/6] evidence"
echo "EVIDENCE_G1_PREREQ_WORKSPACE: host_workspace=${HOST_WORKSPACE} writable=1"
echo "EVIDENCE_G1_PREREQ_SECCOMP: cgroup_version=${cgroup_version} default_unconfined=${default_unconfined} security_options=${security_options}"
echo "EVIDENCE_G1_PREREQ_DEFAULT_COMPILE: rc=${default_rc} compiled=${default_compiled} pivot_root_failed=${default_pivot_failed}"
echo "EVIDENCE_G1_PREREQ_UNCONFINED_COMPILE: rc=${unconfined_rc} compiled=${unconfined_compiled}"
if [[ "$default_unconfined" -eq 1 ]]; then
  echo "NOTE: docker default seccomp is unconfined; default compile may return status=Compiled instead of pivot_root_failed."
fi
echo "G1 prereq passed"
