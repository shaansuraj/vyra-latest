#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
APP_DIR="$ROOT/application/app"
RELAY_DIR="$ROOT/application/relay"

echo "[vyra] Cleaning stale local workers/adapters..."
"$ROOT/scripts/cleanup_local_processes.sh"

echo "[vyra] Building frontend for local production..."
(cd "$APP_DIR" && npm run build)

echo "[vyra] Starting relay with built frontend on http://127.0.0.1:8020 ..."
cd "$RELAY_DIR"
exec go run ./cmd/relay
