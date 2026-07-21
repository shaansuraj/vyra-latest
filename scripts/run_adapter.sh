#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT/unblink-engine-main"

echo "[hexvision] Starting relay↔engine adapter (worker_id=unblink/base-vl)..."

RELAY_WS_URL="${RELAY_WS_URL:-ws://localhost:9020/worker/connect}" \
RELAY_HTTP_BASE="${RELAY_HTTP_BASE:-http://localhost:9020}" \
ENGINE_WS_URL="${ENGINE_WS_URL:-ws://localhost:5000/ws}" \
WORKER_ID="${WORKER_ID:-unblink/base-vl}" \
MAX_FRAMES_PER_JOB="${MAX_FRAMES_PER_JOB:-4}" \
MAX_IN_FLIGHT="${MAX_IN_FLIGHT:-4}" \
  bun run adapter
