#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

if ! command -v go >/dev/null 2>&1; then
  echo "ERROR: 未找到 go 命令" >&2
  exit 2
fi
if ! command -v bash >/dev/null 2>&1; then
  echo "ERROR: 未找到 bash 命令" >&2
  exit 2
fi

echo "[1/3] go test ./..."
(
  cd src/go
  go test ./...
)

echo "[2/3] go vet ./..."
(
  cd src/go
  go vet ./...
)

echo "[3/3] 脚本语法检查（bash -n）"
while IFS= read -r script; do
  bash -n "$script"
done < <(find scripts -maxdepth 1 -type f -name '*.sh' | sort)

echo "verify_ci: PASS"
