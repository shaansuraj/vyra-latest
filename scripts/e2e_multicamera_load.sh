#!/usr/bin/env bash
set -euo pipefail

if [[ -z "${RELAY_TOKEN:-}" ]]; then
  echo "[multicam-load] RELAY_TOKEN is required"
  exit 1
fi

export SMOKE_LOAD_SECONDS="${SMOKE_LOAD_SECONDS:-120}"
export SMOKE_IDENTITY_TRACKS="${SMOKE_IDENTITY_TRACKS:-24}"
export SMOKE_QUERY_TEXT="${SMOKE_QUERY_TEXT:-Summarize multi-camera activity today}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec "${SCRIPT_DIR}/e2e_smoke.sh"
