#!/usr/bin/env bash
set -euo pipefail

# 脚本用途：
# 1) 登录并创建(或复用)题目后，持续向 /api/v1/submit 打流量。
# 2) 通过可配置 worker 数、提交间隔、持续时长快速制造观测数据。
# 3) 支持 mixed 模式，注入少量 WA/CE 结果，便于 Grafana 看到更丰富曲线。

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

API_BASE="${API_BASE:-http://127.0.0.1:18080}"
USERNAME="${USERNAME:-admin}"
PASSWORD="${PASSWORD:-password}"
EMAIL="${EMAIL:-${USERNAME}@example.com}"
PROBLEM_ID="${PROBLEM_ID:-}"
TOKEN="${TOKEN:-}"

WORKERS="${WORKERS:-2}"
DURATION_SEC="${DURATION_SEC:-300}"         # 0 表示无限持续，直到 Ctrl+C
SUBMIT_INTERVAL_SEC="${SUBMIT_INTERVAL_SEC:-0.2}"
SUMMARY_INTERVAL_SEC="${SUMMARY_INTERVAL_SEC:-5}"
MIXED_MODE="${MIXED_MODE:-0}"               # 0: 全 AC, 1: AC/WA/CE 混合
TIME_LIMIT_MS="${TIME_LIMIT_MS:-1000}"
MEMORY_LIMIT_KB="${MEMORY_LIMIT_KB:-65536}"
LOGIN_RETRY_MAX="${LOGIN_RETRY_MAX:-90}"
LOGIN_RETRY_INTERVAL_SEC="${LOGIN_RETRY_INTERVAL_SEC:-1}"
PROM_BASE="${PROM_BASE:-http://127.0.0.1:19090}"
PROM_DIAG_ENABLED="${PROM_DIAG_ENABLED:-1}" # 1: 输出 Prometheus 诊断
KEEP_TMP="${KEEP_TMP:-0}"

TMP_DIR="$(mktemp -d)"
SUCCESS_IDS_FILE="$TMP_DIR/success_job_ids.txt"
THROTTLE_FILE="$TMP_DIR/throttle_429.txt"
THROTTLE_RATE_LIMIT_FILE="$TMP_DIR/throttle_429_rate_limit.txt"
THROTTLE_INFLIGHT_CAP_FILE="$TMP_DIR/throttle_429_inflight_cap.txt"
THROTTLE_BACKPRESSURE_FILE="$TMP_DIR/throttle_429_backpressure.txt"
THROTTLE_OTHER_FILE="$TMP_DIR/throttle_429_other.txt"
ERROR_FILE="$TMP_DIR/errors.txt"
STOP_FILE="$TMP_DIR/stop.signal"
PIDS=()
START_TS="$(date +%s)"

# 预创建统计文件，避免并发首次写入时触发 "No such file or directory"。
: > "$SUCCESS_IDS_FILE"
: > "$THROTTLE_FILE"
: > "$THROTTLE_RATE_LIMIT_FILE"
: > "$THROTTLE_INFLIGHT_CAP_FILE"
: > "$THROTTLE_BACKPRESSURE_FILE"
: > "$THROTTLE_OTHER_FILE"
: > "$ERROR_FILE"

usage() {
  cat <<'USAGE'
Usage: scripts/submit_load_loop.sh

Env:
  API_BASE             default: http://127.0.0.1:18080
  USERNAME             default: admin
  PASSWORD             default: password
  EMAIL                default: <USERNAME>@example.com
  TOKEN                default: empty (if set, skip register/login)
  PROBLEM_ID           default: empty (auto upload minimal problem.zip)
  WORKERS              default: 2
  DURATION_SEC         default: 300, set 0 to run until Ctrl+C
  SUBMIT_INTERVAL_SEC  default: 0.2
  SUMMARY_INTERVAL_SEC default: 5
  MIXED_MODE           default: 0 (1 means AC/WA/CE mixed)
  TIME_LIMIT_MS        default: 1000
  MEMORY_LIMIT_KB      default: 65536
  LOGIN_RETRY_MAX      default: 90
  LOGIN_RETRY_INTERVAL_SEC default: 1
  PROM_BASE            default: http://127.0.0.1:19090
  PROM_DIAG_ENABLED    default: 1 (0 means skip Prometheus diagnostics)
  KEEP_TMP             default: 0 (set 1 to keep temp files)
USAGE
}

cleanup() {
  if [[ "$KEEP_TMP" == "1" ]]; then
    echo "KEEP_TMP=1, tmp_dir=$TMP_DIR"
    return
  fi
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT

on_signal() {
  touch "$STOP_FILE"
  for pid in "${PIDS[@]:-}"; do
    kill "$pid" >/dev/null 2>&1 || true
  done
}
trap on_signal INT TERM

require_cmd() {
  local cmd="$1"
  command -v "$cmd" >/dev/null 2>&1 || {
    echo "ERROR: missing command: $cmd" >&2
    exit 1
  }
}

print_body_snippet() {
  local body_file="$1"
  if [[ ! -f "$body_file" || ! -s "$body_file" ]]; then
    echo "<empty>"
    return
  fi
  local body_len
  body_len="$(wc -c < "$body_file" | tr -d ' ')"
  if [[ "$body_len" -le 512 ]]; then
    cat "$body_file"
    return
  fi
  head -c 512 "$body_file"
  echo
  echo "... [truncated, total ${body_len} bytes]"
}

prom_query_scalar() {
  local query="$1"
  local default="${2:-NaN}"
  local resp_file="$TMP_DIR/prom_query_$(date +%s%N).json"

  if [[ "$PROM_DIAG_ENABLED" != "1" ]]; then
    echo "$default"
    return 0
  fi

  if ! curl -g -sS -G "$PROM_BASE/api/v1/query" --data-urlencode "query=$query" -o "$resp_file"; then
    echo "$default"
    return 0
  fi

  python3 - "$resp_file" "$default" <<'PY'
import json
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
default = sys.argv[2]
try:
    obj = json.loads(path.read_text(encoding="utf-8"))
    if obj.get("status") != "success":
        print(default)
        raise SystemExit(0)
    result = obj.get("data", {}).get("result", [])
    if not result:
        print("0")
        raise SystemExit(0)
    value = result[0].get("value", [])
    if len(value) < 2:
        print(default)
        raise SystemExit(0)
    print(value[1])
except Exception:
    print(default)
PY
}

number_gt_zero() {
  local value="$1"
  awk -v v="$value" 'BEGIN { if ((v+0) > 0) print 1; else print 0 }'
}

http_request() {
  local method="$1"
  local url="$2"
  local expected_csv="$3"
  local body_file="$4"
  shift 4

  local status=""
  if ! status="$(curl -sS -X "$method" "$url" -o "$body_file" -w '%{http_code}' "$@")"; then
    echo "ERROR: request failed: $method $url" >&2
    return 1
  fi

  local matched=0
  local expected
  IFS=',' read -r -a expected <<< "$expected_csv"
  for code in "${expected[@]}"; do
    if [[ "$status" == "$code" ]]; then
      matched=1
      break
    fi
  done

  if [[ "$matched" -ne 1 ]]; then
    echo "ERROR: unexpected status $status for $method $url (expected=$expected_csv)" >&2
    print_body_snippet "$body_file" >&2
    return 1
  fi
}

register_and_login() {
  if [[ -n "$TOKEN" ]]; then
    return
  fi

  local register_body
  register_body="$(USERNAME="$USERNAME" PASSWORD="$PASSWORD" EMAIL="$EMAIL" python3 - <<'PY'
import json
import os
print(json.dumps({
    "username": os.environ["USERNAME"],
    "password": os.environ["PASSWORD"],
    "email": os.environ["EMAIL"],
}, ensure_ascii=False))
PY
)"

  local register_resp="$TMP_DIR/register_resp.json"
  http_request POST "$API_BASE/api/v1/auth/register" "201,409,429" "$register_resp" \
    -H 'Content-Type: application/json' \
    -d "$register_body" || exit 1

  local login_body
  login_body="$(USERNAME="$USERNAME" PASSWORD="$PASSWORD" python3 - <<'PY'
import json
import os
print(json.dumps({
    "username": os.environ["USERNAME"],
    "password": os.environ["PASSWORD"],
}, ensure_ascii=False))
PY
)"

  local login_resp="$TMP_DIR/login_resp.json"
  local login_ok=0
  local attempt
  for attempt in $(seq 1 "$LOGIN_RETRY_MAX"); do
    local status
    status="$(curl -sS -X POST "$API_BASE/api/v1/auth/login" \
      -H 'Content-Type: application/json' \
      -d "$login_body" \
      -o "$login_resp" \
      -w '%{http_code}' || true)"
    if [[ "$status" == "200" ]]; then
      login_ok=1
      break
    fi
    if [[ "$status" != "429" ]]; then
      echo "ERROR: login failed with status=$status" >&2
      print_body_snippet "$login_resp" >&2
      exit 1
    fi
    sleep "$LOGIN_RETRY_INTERVAL_SEC"
  done
  if [[ "$login_ok" != "1" ]]; then
    echo "ERROR: login failed after retries (likely rate limited)" >&2
    print_body_snippet "$login_resp" >&2
    exit 1
  fi

  TOKEN="$(python3 - "$login_resp" <<'PY'
import json
import pathlib
import sys
obj = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
print(obj.get("token", ""))
PY
)"
  if [[ -z "$TOKEN" ]]; then
    echo "ERROR: login response missing token" >&2
    exit 1
  fi
}

ensure_problem() {
  if [[ -n "$PROBLEM_ID" ]]; then
    if ! [[ "$PROBLEM_ID" =~ ^[0-9]+$ ]]; then
      echo "ERROR: PROBLEM_ID must be numeric, got: $PROBLEM_ID" >&2
      exit 1
    fi
    return
  fi

  local problem_zip="$TMP_DIR/problem.zip"
  python3 - "$problem_zip" <<'PY'
import sys
import zipfile

zip_path = sys.argv[1]
with zipfile.ZipFile(zip_path, "w", compression=zipfile.ZIP_DEFLATED) as zf:
    zf.writestr("1.in", "1 2\n")
    zf.writestr("1.out", "3\n")
PY

  local upload_resp="$TMP_DIR/upload_resp.json"
  http_request POST "$API_BASE/api/v1/problems" "200" "$upload_resp" \
    -H "Authorization: Bearer $TOKEN" \
    -F "title=Submit Load Loop A+B" \
    -F "time_limit=1000" \
    -F "memory_limit=128" \
    -F "file=@$problem_zip" || exit 1

  PROBLEM_ID="$(python3 - "$upload_resp" <<'PY'
import json
import pathlib
import sys
obj = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
value = obj.get("id", "")
print(value)
PY
)"

  if ! [[ "$PROBLEM_ID" =~ ^[0-9]+$ ]]; then
    echo "ERROR: invalid problem id in upload response: $PROBLEM_ID" >&2
    print_body_snippet "$upload_resp" >&2
    exit 1
  fi
}

build_code() {
  local seq="$1"
  if [[ "$MIXED_MODE" != "1" ]]; then
    cat <<'CPP'
#include <iostream>
int main(){int a,b;std::cin>>a>>b;std::cout<<(a+b);return 0;}
CPP
    return
  fi

  local mod=$((seq % 10))
  if [[ "$mod" -le 6 ]]; then
    cat <<'CPP'
#include <iostream>
int main(){int a,b;std::cin>>a>>b;std::cout<<(a+b);return 0;}
CPP
  elif [[ "$mod" -le 8 ]]; then
    cat <<'CPP'
#include <iostream>
int main(){int a,b;std::cin>>a>>b;std::cout<<(a-b);return 0;}
CPP
  else
    cat <<'CPP'
#include <iostream>
int main(){ this_is_compile_error }
CPP
  fi
}

submit_loop() {
  local worker_id="$1"
  local deadline=0
  local seq=0
  if [[ "$DURATION_SEC" -gt 0 ]]; then
    deadline=$(( $(date +%s) + DURATION_SEC ))
  fi

  while true; do
    [[ -f "$STOP_FILE" ]] && break
    if [[ "$deadline" -gt 0 && "$(date +%s)" -ge "$deadline" ]]; then
      break
    fi

    seq=$((seq + 1))
    local source_code
    source_code="$(build_code "$seq")"
    source_code="${source_code}"$'\n'"//load_nonce_worker_${worker_id}_seq_${seq}"
    local payload
    payload="$(PROBLEM_ID="$PROBLEM_ID" SOURCE_CODE="$source_code" TIME_LIMIT_MS="$TIME_LIMIT_MS" MEMORY_LIMIT_KB="$MEMORY_LIMIT_KB" python3 - <<'PY'
import json
import os

payload = {
    "problem_id": int(os.environ["PROBLEM_ID"]),
    "language": 1,
    "code": os.environ["SOURCE_CODE"],
    "time_limit": int(os.environ["TIME_LIMIT_MS"]),
    "memory_limit": int(os.environ["MEMORY_LIMIT_KB"]),
}
print(json.dumps(payload, ensure_ascii=False))
PY
)"

    local body_file="$TMP_DIR/submit_${worker_id}_${seq}.json"
    local status
    status="$(curl -sS -X POST "$API_BASE/api/v1/submit" \
      -H "Authorization: Bearer $TOKEN" \
      -H 'Content-Type: application/json' \
      -d "$payload" \
      -o "$body_file" \
      -w '%{http_code}' || true)"

    if [[ "$status" == "200" ]]; then
      local job_id
      job_id="$(python3 - "$body_file" <<'PY'
import json
import pathlib
import sys
obj = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
print(obj.get("job_id", ""))
PY
)"
      if [[ -n "$job_id" ]]; then
        echo "$job_id" >> "$SUCCESS_IDS_FILE"
      else
        {
          echo "worker=$worker_id seq=$seq status=200 missing_job_id"
          print_body_snippet "$body_file"
        } >> "$ERROR_FILE"
      fi
    elif [[ "$status" == "429" ]]; then
      echo "worker=$worker_id seq=$seq status=429" >> "$THROTTLE_FILE"
      local reason
      reason="$(python3 - "$body_file" <<'PY'
import json
import pathlib
import sys

try:
    obj = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
    print(obj.get("code", ""))
except Exception:
    print("")
PY
)"
      case "$reason" in
        RATE_LIMITED)
          echo "worker=$worker_id seq=$seq reason=$reason" >> "$THROTTLE_RATE_LIMIT_FILE"
          ;;
        INFLIGHT_CAP_REACHED)
          echo "worker=$worker_id seq=$seq reason=$reason" >> "$THROTTLE_INFLIGHT_CAP_FILE"
          ;;
        BACKPRESSURE|BACKPRESSURE_REJECTED)
          echo "worker=$worker_id seq=$seq reason=$reason" >> "$THROTTLE_BACKPRESSURE_FILE"
          ;;
        *)
          echo "worker=$worker_id seq=$seq reason=${reason:-unknown}" >> "$THROTTLE_OTHER_FILE"
          ;;
      esac
    else
      {
        echo "worker=$worker_id seq=$seq status=${status:-<none>}"
        print_body_snippet "$body_file"
      } >> "$ERROR_FILE"
    fi

    sleep "$SUBMIT_INTERVAL_SEC"
  done
}

workers_alive() {
  local pid
  for pid in "${PIDS[@]:-}"; do
    if kill -0 "$pid" >/dev/null 2>&1; then
      return 0
    fi
  done
  return 1
}

print_summary() {
  local success_count throttle_count throttle_rl_count throttle_inflight_count throttle_bp_count throttle_other_count error_count elapsed
  success_count="$(wc -l < "$SUCCESS_IDS_FILE" 2>/dev/null || echo 0)"
  throttle_count="$(wc -l < "$THROTTLE_FILE" 2>/dev/null || echo 0)"
  throttle_rl_count="$(wc -l < "$THROTTLE_RATE_LIMIT_FILE" 2>/dev/null || echo 0)"
  throttle_inflight_count="$(wc -l < "$THROTTLE_INFLIGHT_CAP_FILE" 2>/dev/null || echo 0)"
  throttle_bp_count="$(wc -l < "$THROTTLE_BACKPRESSURE_FILE" 2>/dev/null || echo 0)"
  throttle_other_count="$(wc -l < "$THROTTLE_OTHER_FILE" 2>/dev/null || echo 0)"
  error_count="$(wc -l < "$ERROR_FILE" 2>/dev/null || echo 0)"
  elapsed=$(( $(date +%s) - START_TS ))
  if [[ "$elapsed" -le 0 ]]; then
    elapsed=1
  fi
  local rps
  rps="$(awk -v s="$success_count" -v e="$elapsed" 'BEGIN{printf "%.2f", s/e}')"
  echo "[摘要] 运行=${elapsed}s 成功=${success_count} 429=${throttle_count}(限流=${throttle_rl_count},活跃上限=${throttle_inflight_count},背压=${throttle_bp_count},其他=${throttle_other_count}) 错误=${error_count} submit_rps=${rps}"

  if [[ "$PROM_DIAG_ENABLED" == "1" ]]; then
    local prom_429_rl prom_429_inflight prom_429_bp prom_api_bp_reject prom_api_bp_inflight prom_api_bp_backpressure prom_stream_lag prom_stream_backlog prom_outbox_pending prom_exec_inflight prom_worker_stream_inflight
    prom_429_rl="$(prom_query_scalar 'sum(submit_429_total{reason="rate_limit"})' 'NaN')"
    prom_429_inflight="$(prom_query_scalar 'sum(submit_429_total{reason="inflight_cap"})' 'NaN')"
    prom_429_bp="$(prom_query_scalar 'sum(submit_429_total{reason="backpressure"})' 'NaN')"
    prom_api_bp_reject="$(prom_query_scalar 'sum(api_backpressure_reject_total)' 'NaN')"
    prom_api_bp_inflight="$(prom_query_scalar 'sum(api_backpressure_reject_total{reason="inflight_cap"})' 'NaN')"
    prom_api_bp_backpressure="$(prom_query_scalar 'sum(api_backpressure_reject_total{reason="backpressure"})' 'NaN')"
    prom_stream_lag="$(prom_query_scalar 'sum(stream_lag)' 'NaN')"
    prom_stream_backlog="$(prom_query_scalar 'sum(stream_backlog)' 'NaN')"
    prom_outbox_pending="$(prom_query_scalar 'sum(api_outbox_pending)' 'NaN')"
    prom_exec_inflight="$(prom_query_scalar 'sum(exec_inflight)' 'NaN')"
    prom_worker_stream_inflight="$(prom_query_scalar 'sum(worker_stream_inflight)' 'NaN')"

    echo "[诊断] prom429(限流)=${prom_429_rl} prom429(活跃上限)=${prom_429_inflight} prom429(背压)=${prom_429_bp} api_backpressure_reject_total=${prom_api_bp_reject} api_backpressure_reject_total{inflight_cap}=${prom_api_bp_inflight} api_backpressure_reject_total{backpressure}=${prom_api_bp_backpressure} stream_lag=${prom_stream_lag} stream_backlog=${prom_stream_backlog} outbox_pending=${prom_outbox_pending} exec_inflight=${prom_exec_inflight} worker_stream_inflight=${prom_worker_stream_inflight}"

    local signal_bp signal_inflight signal_rl
    signal_bp="$(number_gt_zero "$prom_429_bp")"
    if [[ "$signal_bp" == "0" ]]; then
      signal_bp="$(number_gt_zero "$prom_api_bp_backpressure")"
    fi
    signal_inflight="$(number_gt_zero "$prom_429_inflight")"
    signal_rl="$(number_gt_zero "$prom_429_rl")"
    if [[ "$signal_bp" == "1" ]] || [[ "$throttle_bp_count" -gt 0 ]]; then
      echo "[判读] 429 主要由队列背压触发（请重点看 stream_lag/backlog 与 outbox_pending）。"
    elif [[ "$signal_inflight" == "1" ]] || [[ "$throttle_inflight_count" -gt 0 ]]; then
      echo "[判读] 429 主要由提交面活跃任务上限触发（inflight_cap，属于防爆保护，不是入口限流）。"
    elif [[ "$signal_rl" == "1" ]] || [[ "$throttle_rl_count" -gt 0 ]]; then
      echo "[判读] 429 主要由入口限流触发（当前更像 API 门口限流，不是队列先堆满）。"
    elif [[ "$throttle_count" -gt 0 ]]; then
      echo "[判读] 出现 429，但分类未命中（请看 errors 与 429 其他原因文件）。"
    else
      echo "[判读] 当前未观察到 429 拒绝。"
    fi
  fi
}

for cmd in curl python3 awk date; do
  require_cmd "$cmd"
done

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi

if ! [[ "$WORKERS" =~ ^[0-9]+$ ]] || [[ "$WORKERS" -lt 1 ]]; then
  echo "ERROR: WORKERS must be >= 1, got: $WORKERS" >&2
  exit 1
fi
if ! [[ "$DURATION_SEC" =~ ^[0-9]+$ ]]; then
  echo "ERROR: DURATION_SEC must be >= 0 integer, got: $DURATION_SEC" >&2
  exit 1
fi

echo "[1/4] 注册并登录（若提供 TOKEN 则跳过）"
register_and_login
echo "[2/4] 准备题目（未传 PROBLEM_ID 时自动上传）"
ensure_problem
echo "使用题目 problem_id=$PROBLEM_ID"
echo "[3/4] 启动持续提交 worker"
echo "参数 workers=$WORKERS duration_sec=$DURATION_SEC submit_interval_sec=$SUBMIT_INTERVAL_SEC mixed_mode=$MIXED_MODE prom_diag=$PROM_DIAG_ENABLED"

for i in $(seq 1 "$WORKERS"); do
  submit_loop "$i" &
  PIDS+=("$!")
done

if [[ "$DURATION_SEC" -eq 0 ]]; then
  echo "无限持续模式，按 Ctrl+C 停止"
fi

while workers_alive; do
  sleep "$SUMMARY_INTERVAL_SEC"
  print_summary
done

for pid in "${PIDS[@]:-}"; do
  wait "$pid" || true
done

touch "$STOP_FILE"
echo "[4/4] final summary"
print_summary
echo "最近成功 job_id 样例:"
tail -n 10 "$SUCCESS_IDS_FILE" 2>/dev/null || true
echo "明细文件:"
echo "  success_ids=$SUCCESS_IDS_FILE"
echo "  throttle_429_all=$THROTTLE_FILE"
echo "  throttle_429_rate_limit=$THROTTLE_RATE_LIMIT_FILE"
echo "  throttle_429_inflight_cap=$THROTTLE_INFLIGHT_CAP_FILE"
echo "  throttle_429_backpressure=$THROTTLE_BACKPRESSURE_FILE"
echo "  throttle_429_other=$THROTTLE_OTHER_FILE"
echo "  errors=$ERROR_FILE"
