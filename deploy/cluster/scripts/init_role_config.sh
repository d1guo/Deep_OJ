#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "Usage: $0 <control|stateful|minio|worker>"
  exit 1
fi

ROLE="$1"
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

if [[ ! -f ".env" ]]; then
  cp .env.example .env
  echo "[init] created .env from .env.example"
else
  echo "[skip] .env already exists"
fi

case "$ROLE" in
  control)
    if [[ ! -f "config.control.yaml" ]]; then
      cp config.control.example.yaml config.control.yaml
      echo "[init] created config.control.yaml"
    else
      echo "[skip] config.control.yaml already exists"
    fi
    echo "TODO: fill private IP/password in config.control.yaml and prometheus.yml"
    ;;
  worker)
    if [[ ! -f "config.worker.yaml" ]]; then
      cp config.worker.example.yaml config.worker.yaml
      echo "[init] created config.worker.yaml"
    else
      echo "[skip] config.worker.yaml already exists"
    fi
    echo "TODO: set unique worker.id / worker.addr / worker.stream.consumer in config.worker.yaml"
    ;;
  stateful|minio)
    echo "[init] no role-specific yaml required for $ROLE"
    ;;
  *)
    echo "ERROR: unsupported role: $ROLE"
    exit 1
    ;;
esac

echo "init done: role=$ROLE"
