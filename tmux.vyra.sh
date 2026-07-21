#!/usr/bin/env bash
set -euo pipefail

# Legacy tmux launcher.
# Prefer scripts/vyractl.sh for explicit relay lifecycle control.
# This wrapper keeps a fixed tmux session and only detaches when --detach is passed.

SESSION_NAME="vyra"
APP_PORT="${APP_PORT:-3000}"
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DETACH_MODE="false"

if [[ "${1:-}" == "--detach" ]]; then
  DETACH_MODE="true"
fi

if ! command -v tmux >/dev/null 2>&1; then
  echo "tmux is required" >&2
  exit 1
fi

if tmux has-session -t "${SESSION_NAME}" 2>/dev/null; then
  echo "Session '${SESSION_NAME}' already exists."
  if [[ "${DETACH_MODE}" == "true" ]]; then
    echo "Leaving it detached."
    exit 0
  fi
  tmux attach -t "${SESSION_NAME}"
  exit 0
fi

tmux new-session -d -s "${SESSION_NAME}" -n "services"

tmux send-keys -t "${SESSION_NAME}:services" \
  "cd ${ROOT_DIR} && scripts/vyractl.sh start --launcher-tag tmux" C-m

tmux split-window -h -t "${SESSION_NAME}:services"
tmux send-keys -t "${SESSION_NAME}:services.1" \
  "cd ${ROOT_DIR}/application/app && bun run dev --port ${APP_PORT}" C-m

tmux select-layout -t "${SESSION_NAME}:services" even-horizontal
tmux select-pane -t "${SESSION_NAME}:services.0"

echo "tmux session '${SESSION_NAME}' started (Relay + App)."
echo "Relay lifecycle is managed by scripts/vyractl.sh."
echo "Detach tmux with Ctrl+B then D."

if [[ "${DETACH_MODE}" == "true" ]]; then
  echo "Leaving session detached."
  exit 0
fi

tmux attach -t "${SESSION_NAME}"
