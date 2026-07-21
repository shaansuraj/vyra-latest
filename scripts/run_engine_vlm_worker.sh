#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT/unblink-engine-main/py"

echo "[hexvision] Starting VLM worker (WORKER_TYPE=vlm)..."

command -v uv >/dev/null 2>&1 || {
  echo "uv is not installed. Install uv: https://github.com/astral-sh/uv";
  exit 1;
}

uv sync --frozen

WORKER_TYPE="vlm" MAX_LATENCY_MS="5000" MAX_BATCH_SIZE="32" \
  uv run --env-file "$ROOT/unblink-engine-main/.env" python -m worker_vlm
