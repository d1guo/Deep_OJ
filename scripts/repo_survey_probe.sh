#!/usr/bin/env bash
set -euo pipefail

REDIS_URL="${REDIS_URL:-redis://localhost:6379/0}"
DATABASE_URL="${DATABASE_URL:-postgres://postgres:postgres@localhost:5432/deepoj?sslmode=disable}"
API_METRICS_URL="${API_METRICS_URL:-http://localhost:8080/metrics}"
SCHEDULER_METRICS_URL="${SCHEDULER_METRICS_URL:-http://localhost:9091/metrics}"
WORKER_METRICS_URL="${WORKER_METRICS_URL:-http://localhost:9092/metrics}"

usage() {
  cat <<'EOF'
Usage: scripts/repo_survey_probe.sh

Read-only probe for A1 repo survey:
1) Redis Stream 深度
2) PostgreSQL schema snapshot
3) Metrics endpoint smoke check

Environment:
  REDIS_URL              (default redis://localhost:6379/0)
  DATABASE_URL           (default postgres://postgres:postgres@localhost:5432/deepoj?sslmode=disable)
  API_METRICS_URL        (default http://localhost:8080/metrics)
  SCHEDULER_METRICS_URL  (default http://localhost:9091/metrics)
  WORKER_METRICS_URL     (default http://localhost:9092/metrics)
EOF
}

pick_filter() {
  if command -v rg >/dev/null 2>&1; then
    echo "rg"
    return
  fi
  echo "grep -E"
}

filter_stream() {
  local pattern="$1"
  if [[ "$(pick_filter)" == "rg" ]]; then
    rg "${pattern}" || true
    return
  fi
  grep -E "${pattern}" || true
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi

echo "[1/3] Redis Stream depth"
if command -v redis-cli >/dev/null 2>&1; then
  redis-cli -u "${REDIS_URL}" XLEN deepoj:jobs || true
else
  echo "redis-cli not found; skip Redis probes."
fi

echo "[2/3] PostgreSQL schema snapshot"
if command -v psql >/dev/null 2>&1; then
  psql "${DATABASE_URL}" -c "SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename;" || true
  psql "${DATABASE_URL}" -c "SELECT column_name, data_type FROM information_schema.columns WHERE table_schema='public' AND table_name='submissions' ORDER BY ordinal_position;" || true
else
  echo "psql not found; skip PostgreSQL probes."
fi

echo "[3/3] Metrics endpoint smoke check"
if command -v curl >/dev/null 2>&1; then
  curl -fsS "${API_METRICS_URL}" | filter_stream "http_requests_total|submission_total" || true
  curl -fsS "${SCHEDULER_METRICS_URL}" | filter_stream "scheduler_active_workers|control_plane_only" || true
  curl -fsS "${WORKER_METRICS_URL}" | filter_stream "worker_task_total|worker_task_duration_seconds" || true
else
  echo "curl not found; skip metrics probes."
fi

echo "Repo survey probe finished."
