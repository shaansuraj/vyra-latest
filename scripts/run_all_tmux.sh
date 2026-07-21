#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SESSION="hexvision"

command -v tmux >/dev/null 2>&1 || { echo "tmux not installed. Run individual scripts in separate terminals"; exit 1; }

# Kill existing

tmux kill-session -t "$SESSION" 2>/dev/null || true

# Relay

tmux new-session -d -s "$SESSION" -n relay -c "$ROOT/unblink-main/relay"
tmux send-keys -t "$SESSION:relay" "go run ./cmd/relay" C-m

# Dashboard

tmux new-window -t "$SESSION" -n dashboard -c "$ROOT/unblink-main/app"
tmux send-keys -t "$SESSION:dashboard" "bun install && bun dev" C-m

# Engine

tmux new-window -t "$SESSION" -n engine -c "$ROOT/unblink-engine-main"
tmux send-keys -t "$SESSION:engine" "bun install && bun start" C-m

# VLM Worker

tmux new-window -t "$SESSION" -n worker_vlm -c "$ROOT/unblink-engine-main/py"
tmux send-keys -t "$SESSION:worker_vlm" "uv sync --frozen && WORKER_TYPE=vlm MAX_LATENCY_MS=5000 MAX_BATCH_SIZE=32 uv run --env-file $ROOT/unblink-engine-main/.env python -m worker_vlm" C-m

# Adapter

tmux new-window -t "$SESSION" -n adapter -c "$ROOT/unblink-engine-main"
tmux send-keys -t "$SESSION:adapter" "bun run adapter" C-m

# Attach

echo "[hexvision] Started tmux session '$SESSION'."
echo "Open: http://localhost:3000"


tmux attach -t "$SESSION"
