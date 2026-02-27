#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

echo "[check] node=$(hostname)"
echo "[check] project_root=$ROOT_DIR"

if ! command -v docker >/dev/null 2>&1; then
  echo "ERROR: docker not found. Install Docker Engine first."
  exit 1
fi

if ! docker info >/dev/null 2>&1; then
  echo "ERROR: docker daemon unavailable or permission denied."
  echo "Hint: ensure current user can access docker.sock or use sudo."
  exit 1
fi

if ! docker compose version >/dev/null 2>&1; then
  echo "ERROR: docker compose plugin not found."
  exit 1
fi

echo "[ok] docker version: $(docker --version)"
echo "[ok] compose version: $(docker compose version --short 2>/dev/null || docker compose version | head -n1)"

if [[ ! -f "$ROOT_DIR/.env" ]]; then
  echo "WARN: $ROOT_DIR/.env not found."
  echo "Run: cp $ROOT_DIR/.env.example $ROOT_DIR/.env"
else
  echo "[ok] found $ROOT_DIR/.env"
fi

echo "prerequisite check passed"
