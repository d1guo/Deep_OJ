#!/usr/bin/env bash
set -euo pipefail

case "${1:-}" in
  worker_kill)
    docker kill oj-worker || true
    echo "Killed oj-worker"
    ;;
  redis_down)
    docker stop oj-redis || true
    echo "Stopped oj-redis"
    ;;
  minio_down)
    docker stop oj-minio || true
    echo "Stopped oj-minio"
    ;;
  *)
    echo "Usage: $0 {worker_kill|redis_down|minio_down}"
    exit 1
    ;;
esac
