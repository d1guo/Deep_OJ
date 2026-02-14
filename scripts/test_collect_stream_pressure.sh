#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="$SCRIPT_DIR/collect_stream_pressure.sh"

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

FAKE_REDIS="$TMP_DIR/redis-cli"
cat >"$FAKE_REDIS" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

scenario="${FAKE_REDIS_SCENARIO:-}"
if [ -z "$scenario" ]; then
  echo "fake redis-cli requires FAKE_REDIS_SCENARIO" >&2
  exit 10
fi

args=("$@")
cmd=()
i=0
while [ "$i" -lt "${#args[@]}" ]; do
  case "${args[$i]}" in
    -h|-p|-n|-a)
      i=$((i+2))
      ;;
    --raw|--csv|--no-auth-warning)
      i=$((i+1))
      ;;
    *)
      cmd=("${args[@]:$i}")
      break
      ;;
  esac
done

if [ "${#cmd[@]}" -eq 0 ]; then
  echo "empty command" >&2
  exit 11
fi

op="${cmd[0]}"

case "$scenario:$op" in
  lag_null:PING)
    echo "PONG"
    ;;
  lag_null:XLEN)
    echo "12"
    ;;
  lag_null:XINFO)
    cat <<'EOT'
name
deepoj:workers
consumers
1
pending
0
last-delivered-id
1700000000100-0
entries-read
99
lag

EOT
    ;;
  lag_null:XPENDING)
    cat <<'EOT'
0
0-0
0-0

EOT
    ;;

  pending:PING)
    echo "PONG"
    ;;
  pending:XLEN)
    echo "30"
    ;;
  pending:XINFO)
    cat <<'EOT'
name
deepoj:workers
consumers
1
pending
2
last-delivered-id
1700000000000-0
entries-read
20
lag
10
EOT
    ;;
  pending:XPENDING)
    if [ "${#cmd[@]}" -eq 3 ]; then
      cat <<'EOT'
2
1700000001000-0
1700000001001-0
consumer-a
2
EOT
    else
      cat <<'EOT'
1700000001000-0
consumer-a
80
1
EOT
    fi
    ;;
  pending:XRANGE)
    cat <<'EOT'
1700000001000-0
job_id
job-p-1
enqueue_ts
1700000001000
payload_ref
task:payload:job-p-1
priority
0
EOT
    ;;

  backlog:PING)
    echo "PONG"
    ;;
  backlog:XLEN)
    echo "40"
    ;;
  backlog:XINFO)
    cat <<'EOT'
name
deepoj:workers
consumers
1
pending
0
last-delivered-id
1700000000000-0
entries-read
50
lag
4
EOT
    ;;
  backlog:XPENDING)
    cat <<'EOT'
0
0-0
0-0

EOT
    ;;
  backlog:XRANGE)
    cat <<'EOT'
1700000002000-0
job_id
job-b-1
enqueue_ts
1700000001500
payload_ref
task:payload:job-b-1
priority
0
EOT
    ;;

  *)
    echo "unsupported scenario/op: $scenario:$op" >&2
    exit 12
    ;;
esac
EOF
chmod +x "$FAKE_REDIS"

if ! "$SCRIPT" --help | grep -q "Usage:"; then
  echo "FAIL: --help does not print usage" >&2
  exit 1
fi

if STREAM_KEY="" "$SCRIPT" >/tmp/collect_stream_pressure.out 2>/tmp/collect_stream_pressure.err; then
  echo "FAIL: expected non-zero exit when STREAM_KEY is empty" >&2
  exit 1
fi
if ! grep -q "STREAM_KEY is empty" /tmp/collect_stream_pressure.err; then
  echo "FAIL: expected clear STREAM_KEY empty message" >&2
  cat /tmp/collect_stream_pressure.err >&2
  exit 1
fi

run_case() {
  local scenario="$1"
  PATH="$TMP_DIR:$PATH" \
    FAKE_REDIS_SCENARIO="$scenario" \
    STREAM_KEY="deepoj:jobs" \
    GROUP="deepoj:workers" \
    REDIS_HOST="127.0.0.1" \
    REDIS_PORT="6379" \
    REDIS_DB="0" \
    REDIS_PASSWORD="" \
    "$SCRIPT"
}

lag_null_json="$(run_case lag_null)"
python3 - <<'PY' "$lag_null_json"
import json
import sys
d = json.loads(sys.argv[1])
assert d["lag"] is None, d
assert d["lag_valid"] is False, d
assert d["oldest_age_source"] == "none", d
assert d["approx"] is False, d
print("PASS: lag_null")
PY

pending_json="$(run_case pending)"
python3 - <<'PY' "$pending_json"
import json
import sys
d = json.loads(sys.argv[1])
assert d["oldest_age_source"] == "pending", d
assert d["approx"] is False, d
assert d["pending"] > 0, d
assert isinstance(d["lag"], int), d
print("PASS: pending_source")
PY

backlog_json="$(run_case backlog)"
python3 - <<'PY' "$backlog_json"
import json
import sys
d = json.loads(sys.argv[1])
assert d["oldest_age_source"] == "backlog", d
assert d["approx"] is True, d
assert d["pending"] == 0, d
assert d["lag_valid"] is True and d["lag"] > 0, d
print("PASS: backlog_source")
PY

echo "PASS"
