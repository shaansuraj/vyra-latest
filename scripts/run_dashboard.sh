#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT/application/app"

echo "[vyra] Starting dashboard dev server on http://localhost:3000..."

exec npm run dev -- --host 0.0.0.0 --port 3000
