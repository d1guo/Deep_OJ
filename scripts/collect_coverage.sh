#!/usr/bin/env bash
set -euo pipefail

# 脚本用途：
# 1) 采集 Go 覆盖率（coverprofile + total）。
# 2) 在工具可用时采集 C++ 覆盖率（gcovr 或 lcov）。
# 3) 统一输出到 artifacts/coverage/<timestamp>/。

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

STAMP="$(date +%Y%m%d_%H%M%S)"
OUT_DIR="${OUT_DIR:-$ROOT_DIR/artifacts/coverage/${STAMP}}"
GO_OUT_DIR="$OUT_DIR/go"
CPP_OUT_DIR="$OUT_DIR/cpp"
TMP_DIR="$(mktemp -d)"

CPP_COVERAGE="${CPP_COVERAGE:-1}"
CPP_BUILD_DIR="${CPP_BUILD_DIR:-$TMP_DIR/build-cpp-coverage}"
KEEP_TMP="${KEEP_TMP:-0}"

GO_TOTAL_PCT="NA"
GO_STATUS="skipped"
CPP_TOTAL_PCT="NA"
CPP_STATUS="skipped"

cleanup() {
  if [[ "$KEEP_TMP" == "1" ]]; then
    echo "KEEP_TMP=1, tmp_dir=$TMP_DIR" >&2
    return
  fi
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT

usage() {
  cat <<'USAGE'
Usage: scripts/collect_coverage.sh

Optional env:
  OUT_DIR         default: artifacts/coverage/<timestamp>
  CPP_COVERAGE    default: 1 (0 to skip C++ coverage)
  CPP_BUILD_DIR   custom C++ coverage build directory
USAGE
}

require_cmd() {
  local cmd="$1"
  command -v "$cmd" >/dev/null 2>&1 || {
    echo "ERROR: missing command: $cmd" >&2
    exit 1
  }
}

collect_go_with_local_toolchain() {
  mkdir -p "$GO_OUT_DIR"
  (
    cd "$ROOT_DIR/src/go"
    go test ./... -covermode=atomic -coverprofile="$GO_OUT_DIR/coverage.out"
    go tool cover -func="$GO_OUT_DIR/coverage.out" >"$GO_OUT_DIR/coverage.func.txt"
  )
}

collect_go_with_docker_toolchain() {
  mkdir -p "$GO_OUT_DIR"
  require_cmd docker
  docker run --rm \
    -v "$ROOT_DIR:/workspace" \
    -w /workspace \
    golang:1.24 sh -lc '
      export PATH=/usr/local/go/bin:$PATH
      cd src/go
      /usr/local/go/bin/go test ./... -covermode=atomic -coverprofile=/workspace/'"${GO_OUT_DIR#$ROOT_DIR/}"'/coverage.out
      /usr/local/go/bin/go tool cover -func=/workspace/'"${GO_OUT_DIR#$ROOT_DIR/}"'/coverage.out > /workspace/'"${GO_OUT_DIR#$ROOT_DIR/}"'/coverage.func.txt
    '
}

parse_go_total_pct() {
  local func_file="$GO_OUT_DIR/coverage.func.txt"
  if [[ -f "$func_file" ]]; then
    GO_TOTAL_PCT="$(awk '/^total:/ {print $3}' "$func_file" | tail -n1)"
  fi
}

collect_cpp_with_gcovr() {
  mkdir -p "$CPP_OUT_DIR"
  cmake -S "$ROOT_DIR" -B "$CPP_BUILD_DIR" \
    -DCMAKE_BUILD_TYPE=Debug \
    -DCMAKE_C_FLAGS="--coverage -O0" \
    -DCMAKE_CXX_FLAGS="--coverage -O0"
  cmake --build "$CPP_BUILD_DIR" -j"$(nproc)"
  ctest --test-dir "$CPP_BUILD_DIR" --output-on-failure || true

  gcovr -r "$ROOT_DIR" "$CPP_BUILD_DIR" \
    --txt "$CPP_OUT_DIR/coverage.summary.txt" \
    --xml-pretty "$CPP_OUT_DIR/coverage.xml" \
    --json-summary "$CPP_OUT_DIR/coverage.json"

  CPP_TOTAL_PCT="$(python3 - "$CPP_OUT_DIR/coverage.summary.txt" <<'PY'
import pathlib
import re
import sys
text = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8", errors="replace")
for pat in [r"lines:\s*([0-9]+(?:\.[0-9]+)?%)", r"TOTAL.*?([0-9]+(?:\.[0-9]+)?%)"]:
    m = re.search(pat, text, re.IGNORECASE | re.DOTALL)
    if m:
        print(m.group(1))
        raise SystemExit(0)
print("NA")
PY
)"
}

collect_cpp_with_lcov() {
  mkdir -p "$CPP_OUT_DIR"
  cmake -S "$ROOT_DIR" -B "$CPP_BUILD_DIR" \
    -DCMAKE_BUILD_TYPE=Debug \
    -DCMAKE_C_FLAGS="--coverage -O0" \
    -DCMAKE_CXX_FLAGS="--coverage -O0"
  cmake --build "$CPP_BUILD_DIR" -j"$(nproc)"
  ctest --test-dir "$CPP_BUILD_DIR" --output-on-failure || true

  lcov --capture --directory "$CPP_BUILD_DIR" --output-file "$CPP_OUT_DIR/coverage.info" >/dev/null
  lcov --summary "$CPP_OUT_DIR/coverage.info" >"$CPP_OUT_DIR/coverage.summary.txt"
  CPP_TOTAL_PCT="$(awk '/lines\.+:/ {print $2}' "$CPP_OUT_DIR/coverage.summary.txt" | head -n1)"
}

write_summary() {
  cat >"$OUT_DIR/summary.md" <<MD
# Coverage Summary

- generated_at: $(date -u +"%Y-%m-%dT%H:%M:%SZ")
- go_status: $GO_STATUS
- go_total: $GO_TOTAL_PCT
- cpp_status: $CPP_STATUS
- cpp_total: $CPP_TOTAL_PCT

## Artifacts

- go_profile: $GO_OUT_DIR/coverage.out
- go_func: $GO_OUT_DIR/coverage.func.txt
- cpp_summary: $CPP_OUT_DIR/coverage.summary.txt
MD
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi

mkdir -p "$OUT_DIR"

echo "[1/3] collect Go coverage"
if command -v go >/dev/null 2>&1; then
  collect_go_with_local_toolchain
  GO_STATUS="ok"
elif command -v docker >/dev/null 2>&1; then
  collect_go_with_docker_toolchain
  GO_STATUS="ok"
else
  GO_STATUS="failed"
  echo "ERROR: neither go nor docker is available for Go coverage collection" >&2
  exit 1
fi
parse_go_total_pct

echo "[2/3] collect C++ coverage"
if [[ "$CPP_COVERAGE" != "1" ]]; then
  CPP_STATUS="skipped"
elif ! command -v cmake >/dev/null 2>&1 || ! command -v ctest >/dev/null 2>&1; then
  CPP_STATUS="skipped"
  echo "WARN: cmake/ctest missing, skip C++ coverage"
elif command -v gcovr >/dev/null 2>&1; then
  collect_cpp_with_gcovr
  CPP_STATUS="ok"
elif command -v lcov >/dev/null 2>&1; then
  collect_cpp_with_lcov
  CPP_STATUS="ok"
else
  CPP_STATUS="skipped"
  echo "WARN: gcovr/lcov missing, skip C++ coverage"
fi

echo "[3/3] write summary"
write_summary
echo "coverage_summary=$OUT_DIR/summary.md"
