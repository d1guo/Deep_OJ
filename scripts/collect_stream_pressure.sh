#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  collect_stream_pressure.sh

Env:
  STREAM_KEY       Redis stream key (default: deepoj:jobs)
  GROUP            Redis consumer group (default: deepoj:workers)
  REDIS_HOST       Redis host (default: 127.0.0.1)
  REDIS_PORT       Redis port (default: 6379)
  REDIS_DB         Redis DB index (default: 0)
  REDIS_PASSWORD   Redis password (default: empty, no AUTH)
  REDIS_CLI        redis-cli binary (default: redis-cli)

Output:
  One-line JSON:
    ts_ms, xlen, pending, lag, lag_valid, oldest_age_ms, oldest_age_source, approx

Notes:
  - lag may be null when Redis reports NULL/empty lag.
  - oldest_age_ms is from pending oldest first; fallback to backlog oldest when pending is 0.
EOF
}

if [ "${1:-}" = "--help" ] || [ "${1:-}" = "-h" ]; then
  usage
  exit 0
fi
if [ "$#" -gt 0 ]; then
  echo "ERROR: unknown argument: $1" >&2
  usage >&2
  exit 2
fi

if [ "${STREAM_KEY+x}" = "x" ] && [ -z "${STREAM_KEY}" ]; then
  echo "ERROR: STREAM_KEY is empty; unset it or set a non-empty value." >&2
  exit 2
fi
if [ "${GROUP+x}" = "x" ] && [ -z "${GROUP}" ]; then
  echo "ERROR: GROUP is empty; unset it or set a non-empty value." >&2
  exit 2
fi

STREAM_KEY="${STREAM_KEY:-deepoj:jobs}"
GROUP="${GROUP:-deepoj:workers}"
REDIS_HOST="${REDIS_HOST:-127.0.0.1}"
REDIS_PORT="${REDIS_PORT:-6379}"
REDIS_DB="${REDIS_DB:-0}"
REDIS_PASSWORD="${REDIS_PASSWORD:-}"
REDIS_CLI="${REDIS_CLI:-redis-cli}"

if ! command -v "$REDIS_CLI" >/dev/null 2>&1; then
  echo "ERROR: redis-cli not found: $REDIS_CLI" >&2
  exit 2
fi

now_ms() {
  local ms
  ms="$(date +%s%3N 2>/dev/null || true)"
  if printf '%s' "$ms" | grep -Eq '^[0-9]+$'; then
    printf '%s\n' "$ms"
  else
    printf '%s\n' "$(( $(date +%s) * 1000 ))"
  fi
}

stream_id_ms_or_empty() {
  local entry_id="${1:-}"
  local ms="${entry_id%%-*}"
  if printf '%s' "$ms" | grep -Eq '^[0-9]+$'; then
    printf '%s\n' "$ms"
    return 0
  fi
  return 1
}

redis_raw() {
  if [ -n "$REDIS_PASSWORD" ]; then
    "$REDIS_CLI" -h "$REDIS_HOST" -p "$REDIS_PORT" -n "$REDIS_DB" --no-auth-warning -a "$REDIS_PASSWORD" --raw "$@"
  else
    "$REDIS_CLI" -h "$REDIS_HOST" -p "$REDIS_PORT" -n "$REDIS_DB" --raw "$@"
  fi
}

redis_csv() {
  if [ -n "$REDIS_PASSWORD" ]; then
    "$REDIS_CLI" -h "$REDIS_HOST" -p "$REDIS_PORT" -n "$REDIS_DB" --no-auth-warning -a "$REDIS_PASSWORD" --csv "$@"
  else
    "$REDIS_CLI" -h "$REDIS_HOST" -p "$REDIS_PORT" -n "$REDIS_DB" --csv "$@"
  fi
}

# Parse XINFO GROUPS --raw output as key/value lines.
# Sets: parsed_group_found parsed_pending parsed_last_delivered parsed_lag parsed_lag_seen
parse_group_info() {
  local raw="$1"
  local target_group="$2"
  local key val current_group=""

  parsed_group_found=0
  parsed_pending=""
  parsed_last_delivered=""
  parsed_lag=""
  parsed_lag_seen=0

  while IFS= read -r key && IFS= read -r val; do
    if [ "$key" = "name" ]; then
      current_group="$val"
    fi
    if [ "$current_group" = "$target_group" ]; then
      parsed_group_found=1
      case "$key" in
        pending) parsed_pending="$val" ;;
        last-delivered-id) parsed_last_delivered="$val" ;;
        lag)
          parsed_lag="$val"
          parsed_lag_seen=1
          ;;
      esac
    fi
  done <<<"$raw"
}

if ! ping_out="$(redis_raw PING 2>&1)"; then
  echo "ERROR: redis unreachable at ${REDIS_HOST}:${REDIS_PORT} (PING failed): $ping_out" >&2
  exit 1
fi
if [ "$ping_out" != "PONG" ]; then
  echo "ERROR: unexpected PING response from redis: $ping_out" >&2
  exit 1
fi

if ! xlen="$(redis_raw XLEN "$STREAM_KEY" 2>&1)"; then
  echo "ERROR: XLEN failed for stream=$STREAM_KEY: $xlen" >&2
  exit 1
fi
if ! printf '%s' "$xlen" | grep -Eq '^[0-9]+$'; then
  echo "ERROR: XLEN returned non-integer value: $xlen" >&2
  exit 1
fi

if ! groups_raw="$(redis_raw XINFO GROUPS "$STREAM_KEY" 2>&1)"; then
  echo "ERROR: XINFO GROUPS failed for stream=$STREAM_KEY: $groups_raw" >&2
  exit 1
fi

if [ -z "$groups_raw" ]; then
  echo "ERROR: stream not found or has no groups: stream=$STREAM_KEY" >&2
  exit 1
fi

parse_group_info "$groups_raw" "$GROUP"
if [ "$parsed_group_found" -ne 1 ]; then
  echo "ERROR: group not found in XINFO GROUPS output: group=$GROUP stream=$STREAM_KEY" >&2
  exit 1
fi

pending="$parsed_pending"
if ! printf '%s' "$pending" | grep -Eq '^[0-9]+$'; then
  echo "ERROR: invalid pending in XINFO GROUPS for group=$GROUP: $pending" >&2
  exit 1
fi

last_delivered_id="$parsed_last_delivered"
if [ -z "$last_delivered_id" ]; then
  last_delivered_id="0-0"
fi

lag="null"
lag_valid="false"
if [ "$parsed_lag_seen" -eq 1 ]; then
  lag_norm="$(printf '%s' "$parsed_lag" | tr '[:upper:]' '[:lower:]')"
  if printf '%s' "$parsed_lag" | grep -Eq '^[0-9]+$'; then
    lag="$parsed_lag"
    lag_valid="true"
  elif [ "$lag_norm" = "null" ] || [ "$lag_norm" = "(nil)" ] || [ -z "$parsed_lag" ]; then
    lag="null"
    lag_valid="false"
  else
    lag="null"
    lag_valid="false"
  fi
fi

if ! xpending_summary="$(redis_raw XPENDING "$STREAM_KEY" "$GROUP" 2>&1)"; then
  echo "ERROR: XPENDING summary failed for stream=$STREAM_KEY group=$GROUP: $xpending_summary" >&2
  exit 1
fi

pending_summary="$(printf '%s\n' "$xpending_summary" | sed -n '1p')"
if ! printf '%s' "$pending_summary" | grep -Eq '^[0-9]+$'; then
  echo "ERROR: XPENDING summary returned non-integer pending count: $pending_summary" >&2
  exit 1
fi

oldest_age_ms=0
oldest_age_source="none"
approx="false"
ts_ms="$(now_ms)"

extract_enqueue_ts_from_entry_raw() {
  # Sets globals: entry_id parsed_enqueue_ts parsed_enqueue_ts_valid
  local raw="$1"
  entry_id="$(printf '%s\n' "$raw" | sed -n '1p')"
  parsed_enqueue_ts="$(printf '%s\n' "$raw" | awk 'NR>1 {if (prev=="enqueue_ts"){print $0; exit} prev=$0}')"
  if printf '%s' "$parsed_enqueue_ts" | grep -Eq '^[0-9]+$'; then
    parsed_enqueue_ts_valid=1
  else
    parsed_enqueue_ts_valid=0
  fi
}

if [ "$pending_summary" -gt 0 ]; then
  if ! oldest_pending_raw="$(redis_raw XPENDING "$STREAM_KEY" "$GROUP" - + 1 2>&1)"; then
    echo "ERROR: XPENDING detail failed for stream=$STREAM_KEY group=$GROUP: $oldest_pending_raw" >&2
    exit 1
  fi
  oldest_entry_id="$(printf '%s\n' "$oldest_pending_raw" | sed -n '1p')"
  if [ -z "$oldest_entry_id" ]; then
    echo "ERROR: failed to parse oldest pending entry id from XPENDING detail output" >&2
    exit 1
  fi

  if ! entry_raw="$(redis_raw XRANGE "$STREAM_KEY" "$oldest_entry_id" "$oldest_entry_id" COUNT 1 2>&1)"; then
    echo "ERROR: XRANGE failed for oldest pending entry=$oldest_entry_id: $entry_raw" >&2
    exit 1
  fi

  if [ -n "$entry_raw" ]; then
    extract_enqueue_ts_from_entry_raw "$entry_raw"
  else
    entry_id="$oldest_entry_id"
    parsed_enqueue_ts=""
    parsed_enqueue_ts_valid=0
  fi

  if [ "$parsed_enqueue_ts_valid" -eq 1 ]; then
    base_enqueue_ts="$parsed_enqueue_ts"
    oldest_age_source="pending"
  else
    if base_enqueue_ts="$(stream_id_ms_or_empty "$entry_id")"; then
      oldest_age_source="pending_stream_id"
      approx="true"
    else
      oldest_age_source="unknown"
      approx="true"
      base_enqueue_ts=""
    fi
  fi

  if [ -n "${base_enqueue_ts:-}" ]; then
    oldest_age_ms=$(( ts_ms - base_enqueue_ts ))
    if [ "$oldest_age_ms" -lt 0 ]; then
      oldest_age_ms=0
    fi
  else
    oldest_age_ms=0
  fi
elif [ "$lag_valid" = "true" ] && [ "$lag" -gt 0 ]; then
  if ! backlog_raw="$(redis_raw XRANGE "$STREAM_KEY" "($last_delivered_id" + COUNT 1 2>&1)"; then
    echo "ERROR: XRANGE failed for backlog inference after id=$last_delivered_id: $backlog_raw" >&2
    exit 1
  fi
  approx="true"
  if [ -z "$backlog_raw" ]; then
    oldest_age_source="unknown"
    oldest_age_ms=0
  else
    extract_enqueue_ts_from_entry_raw "$backlog_raw"
    if [ "$parsed_enqueue_ts_valid" -eq 1 ]; then
      base_enqueue_ts="$parsed_enqueue_ts"
      oldest_age_source="backlog"
    else
      if base_enqueue_ts="$(stream_id_ms_or_empty "$entry_id")"; then
        oldest_age_source="backlog_stream_id"
      else
        oldest_age_source="unknown"
        base_enqueue_ts=""
      fi
    fi

    if [ -n "${base_enqueue_ts:-}" ]; then
      oldest_age_ms=$(( ts_ms - base_enqueue_ts ))
      if [ "$oldest_age_ms" -lt 0 ]; then
        oldest_age_ms=0
      fi
    else
      oldest_age_ms=0
    fi
  fi
fi

printf '{"ts_ms":%s,"stream_key":"%s","group":"%s","xlen":%s,"pending":%s,"lag":%s,"lag_valid":%s,"oldest_age_ms":%s,"oldest_age_source":"%s","approx":%s}\n' \
  "$ts_ms" "$STREAM_KEY" "$GROUP" "$xlen" "$pending_summary" "$lag" "$lag_valid" "$oldest_age_ms" "$oldest_age_source" "$approx"
