#!/usr/bin/env bash
set -euo pipefail

API_HEALTH_URL="${API_HEALTH_URL:-http://127.0.0.1:18080/api/v1/health}"
PROM_READY_URL="${PROM_READY_URL:-http://127.0.0.1:19090/-/ready}"
GRAFANA_HEALTH_URL="${GRAFANA_HEALTH_URL:-http://127.0.0.1:13000/api/health}"
PROM_TARGETS_URL="${PROM_TARGETS_URL:-http://127.0.0.1:19090/api/v1/targets}"

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "ERROR: missing command: $1"
    exit 1
  }
}

require_cmd curl
require_cmd jq

echo "[1/4] api health"
curl -fsS "$API_HEALTH_URL" | jq .

echo "[2/4] prometheus ready"
curl -fsS "$PROM_READY_URL"
echo

echo "[3/4] grafana health"
curl -fsS "$GRAFANA_HEALTH_URL" | jq .

echo "[4/4] prometheus targets"
curl -sS "$PROM_TARGETS_URL" | jq '.data.activeTargets[] | {job:.labels.job,health:.health,lastError:.lastError}'

echo "control healthcheck done"
