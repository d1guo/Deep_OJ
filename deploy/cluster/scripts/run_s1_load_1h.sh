#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$REPO_ROOT"

mkdir -p artifacts/resume/manual

KEEP_TMP="${KEEP_TMP:-1}"
DURATION_SEC="${DURATION_SEC:-3600}"
WORKERS="${WORKERS:-20}"
SUBMIT_INTERVAL_SEC="${SUBMIT_INTERVAL_SEC:-0.05}"
OUT_LOG="${OUT_LOG:-artifacts/resume/manual/s1_load_1h.log}"

echo "[run] repo=$REPO_ROOT"
echo "[run] DURATION_SEC=$DURATION_SEC WORKERS=$WORKERS SUBMIT_INTERVAL_SEC=$SUBMIT_INTERVAL_SEC"
echo "[run] log=$OUT_LOG"

KEEP_TMP="$KEEP_TMP" DURATION_SEC="$DURATION_SEC" WORKERS="$WORKERS" SUBMIT_INTERVAL_SEC="$SUBMIT_INTERVAL_SEC" \
  bash scripts/submit_load_loop.sh | tee "$OUT_LOG"

echo
echo "[hint] success_ids path:"
grep 'success_ids=' "$OUT_LOG" | tail -n1 || true
