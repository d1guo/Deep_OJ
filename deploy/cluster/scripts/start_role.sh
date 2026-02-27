#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "Usage: $0 <control|minio|stateful|worker>"
  exit 1
fi

ROLE="$1"
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

case "$ROLE" in
  control)
    FILE="node-control.compose.yml"
    ;;
  minio)
    FILE="node-minio.compose.yml"
    ;;
  stateful)
    FILE="node-stateful.compose.yml"
    ;;
  worker)
    FILE="node-worker.compose.yml"
    ;;
  *)
    echo "ERROR: unsupported role: $ROLE"
    exit 1
    ;;
esac

echo "[start] role=$ROLE compose_file=$FILE"
docker compose -f "$FILE" up -d
docker compose -f "$FILE" ps
