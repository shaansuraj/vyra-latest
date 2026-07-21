#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT/unblink-engine-main"

echo "[hexvision] Starting engine distributor on ws://localhost:5000/ws..."

bun install
bun start
