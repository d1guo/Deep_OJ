#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

if ! command -v rg >/dev/null 2>&1; then
  echo "[audit] 错误: 未找到 rg (ripgrep)，无法执行配置覆盖审计。"
  exit 2
fi

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

READ_KEYS="$TMP_DIR/read_keys.txt"
EXPORTED_KEYS="$TMP_DIR/exported_keys.txt"
ALLOW_MISSING="$TMP_DIR/allow_missing.txt"
ALLOW_UNUSED="$TMP_DIR/allow_unused.txt"
READ_PLUS_ALLOW="$TMP_DIR/read_plus_allow.txt"
EXPORTED_PLUS_ALLOW="$TMP_DIR/exported_plus_allow.txt"
MISSING_KEYS="$TMP_DIR/missing_keys.txt"
UNUSED_KEYS="$TMP_DIR/unused_keys.txt"

# 1) 采集生产代码读取的环境变量键（排除 _test.go）
rg --no-heading --glob '!**/*_test.go' -g '*.go' -n \
  '(?:os\.(?:Getenv|LookupEnv)|getEnv(?:Int|Int64|Bool|String)?)\("([A-Z][A-Z0-9_]+)"' \
  src/go/cmd src/go/internal \
  | rg -v '_test\.go:' \
  | sed -E 's/.*\("([A-Z][A-Z0-9_]+)".*/\1/' \
  | sort -u > "$READ_KEYS"

# 额外采集通过 env 常量名间接读取的键（例如 executor 中 envXxx 常量）
rg --no-heading --glob '!**/*_test.go' -g '*.go' -n \
  'env[A-Za-z0-9_]*\s*=\s*"([A-Z][A-Z0-9_]+)"' \
  src/go/cmd src/go/internal \
  | rg -v '_test\.go:' \
  | sed -E 's/.*"([A-Z][A-Z0-9_]+)".*/\1/' \
  | sort -u >> "$READ_KEYS"
sort -u -o "$READ_KEYS" "$READ_KEYS"

# 2) 采集 appconfig 统一导出的键
rg --no-heading -n 'setEnv(?:Int|Int64|Bool)?\("([A-Z][A-Z0-9_]+)"' src/go/internal/appconfig/config.go \
  | sed -E 's/.*\("([A-Z][A-Z0-9_]+)".*/\1/' \
  | sort -u > "$EXPORTED_KEYS"

# 3) 白名单（可读但不要求由 appconfig 导出）
cat > "$ALLOW_MISSING" <<'WL'
APP_CONFIG
WL

# 4) 白名单（允许导出但当前无需在 Go 侧读取）
: > "$ALLOW_UNUSED"

cat "$EXPORTED_KEYS" "$ALLOW_MISSING" | sort -u > "$EXPORTED_PLUS_ALLOW"
cat "$READ_KEYS" "$ALLOW_UNUSED" | sort -u > "$READ_PLUS_ALLOW"

comm -23 "$READ_KEYS" "$EXPORTED_PLUS_ALLOW" > "$MISSING_KEYS"
comm -23 "$EXPORTED_KEYS" "$READ_PLUS_ALLOW" > "$UNUSED_KEYS"

# 5) 配置文件结构快速检查
SECTION_ERRORS=0
for section in server path compile runner security observability api scheduler worker redis postgres; do
  if ! rg -n "^${section}:" config.yaml >/dev/null 2>&1; then
    echo "[audit] 缺少顶层 section: ${section} (config.yaml)"
    SECTION_ERRORS=1
  fi
  if ! rg -n "^${section}:" config.docker.yaml >/dev/null 2>&1; then
    echo "[audit] 缺少顶层 section: ${section} (config.docker.yaml)"
    SECTION_ERRORS=1
  fi
done

# runner.pool_size 已判定为无效配置语义，不应再出现在配置文件中
if awk '
  /^runner:/{in_runner=1; next}
  /^[^[:space:]]/{in_runner=0}
  in_runner && /^[[:space:]]+pool_size:/ {found=1}
  END {exit found ? 0 : 1}
' config.yaml; then
  echo "[audit] 发现死配置: config.yaml 中仍存在 runner.pool_size"
  SECTION_ERRORS=1
fi

if awk '
  /^runner:/{in_runner=1; next}
  /^[^[:space:]]/{in_runner=0}
  in_runner && /^[[:space:]]+pool_size:/ {found=1}
  END {exit found ? 0 : 1}
' config.docker.yaml; then
  echo "[audit] 发现死配置: config.docker.yaml 中仍存在 runner.pool_size"
  SECTION_ERRORS=1
fi

echo "[audit] --- 读取键数量 ---"
echo "[audit] read_keys=$(wc -l < "$READ_KEYS")"
echo "[audit] exported_keys=$(wc -l < "$EXPORTED_KEYS")"

echo "[audit] --- 未映射读取键 (read - exported - allow_missing) ---"
if [[ -s "$MISSING_KEYS" ]]; then
  cat "$MISSING_KEYS"
else
  echo "[audit] 无"
fi

echo "[audit] --- 疑似死导出键 (exported - read - allow_unused) ---"
if [[ -s "$UNUSED_KEYS" ]]; then
  cat "$UNUSED_KEYS"
else
  echo "[audit] 无"
fi

if [[ -s "$MISSING_KEYS" || -s "$UNUSED_KEYS" || "$SECTION_ERRORS" -ne 0 ]]; then
  echo "[audit] 结果: 失败（存在未映射键/死导出键/结构问题）"
  exit 1
fi

echo "[audit] 结果: 通过（无未映射键、无死配置键、结构检查通过）"
