#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RELAY_DIR="$ROOT/application/relay"
RELAY_ENV_FILE="$RELAY_DIR/.env"

read_env_value() {
  local key="$1"
  local file="$2"
  if [[ ! -f "$file" ]]; then
    return 0
  fi
  awk -F= -v k="$key" '
    $0 ~ "^[[:space:]]*"k"=" {
      val = substr($0, index($0, "=") + 1)
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", val)
      gsub(/^"|"$/, "", val)
      print val
      exit
    }
  ' "$file"
}

resolve_app_dir() {
  local raw="${APP_DIR:-}"
  if [[ -z "$raw" ]]; then
    raw="$(read_env_value "APP_DIR" "$RELAY_ENV_FILE")"
  fi
  if [[ -z "$raw" ]]; then
    raw="$RELAY_DIR/.hexvision_data"
  fi
  if [[ "$raw" == /* ]]; then
    echo "$raw"
  else
    echo "$(cd "$RELAY_DIR" && realpath -m "$raw")"
  fi
}

APP_DIR_RESOLVED="$(resolve_app_dir)"
LOG_DIR="$APP_DIR_RESOLVED/logs"
mkdir -p "$LOG_DIR"

STAMP="$(date +%Y%m%d-%H%M%S)"
LOG_FILE="$LOG_DIR/relay-$STAMP.log"
LATEST_LINK="$LOG_DIR/relay.latest.log"

cd "$RELAY_DIR"

echo "[vyra] Starting relay (WS:9020, API:8020)..."
echo "[vyra] Capturing logs to $LOG_FILE"
echo "[vyra] Using GOTRACEBACK=${GOTRACEBACK:-single}"
echo "[vyra] LEGACY_MODE=${LEGACY_MODE:-$(read_env_value "LEGACY_MODE" "$RELAY_ENV_FILE")}"

ln -sfn "$(basename "$LOG_FILE")" "$LATEST_LINK"

GOTRACEBACK="${GOTRACEBACK:-single}" stdbuf -oL -eL go run ./cmd/relay 2>&1 | tee "$LOG_FILE"
