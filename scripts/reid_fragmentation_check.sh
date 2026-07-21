#!/usr/bin/env bash
set -euo pipefail

RELAY_BASE="${RELAY_BASE:-http://127.0.0.1:8020}"
TOKEN="${RELAY_TOKEN:-}"
RELAY_DB_PATH="${RELAY_DB_PATH:-}"
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

if [[ -z "${TOKEN}" ]]; then
  echo "[check] RELAY_TOKEN is required"
  exit 1
fi

if ! command -v jq >/dev/null 2>&1; then
  echo "[check] jq is required"
  exit 1
fi

if [[ -z "${RELAY_DB_PATH}" ]]; then
  app_dir="$(awk -F= '/^APP_DIR=/{print $2}' "${ROOT_DIR}/application/relay/.env" | tail -n 1 | tr -d '\r')"
  if [[ -n "${app_dir}" ]]; then
    RELAY_DB_PATH="${app_dir%/}/database/relay.db"
  fi
fi

if [[ -z "${RELAY_DB_PATH}" ]]; then
  echo "[check] RELAY_DB_PATH is empty; set RELAY_DB_PATH explicitly"
  exit 1
fi

if [[ ! -f "${RELAY_DB_PATH}" ]]; then
  echo "[check] DB not found: ${RELAY_DB_PATH}"
  exit 1
fi

echo "[check] relay base: ${RELAY_BASE}"
echo "[check] db path: ${RELAY_DB_PATH}"

HAS_SQLITE3=0
if command -v sqlite3 >/dev/null 2>&1; then
  HAS_SQLITE3=1
fi
echo "[check] sqlite backend: $([[ "${HAS_SQLITE3}" -eq 1 ]] && echo "sqlite3-cli" || echo "python-stdlib")"

workers_json="$(curl -sS "${RELAY_BASE}/system/workers" -H "Authorization: Bearer ${TOKEN}")"
echo "${workers_json}" | jq -e '.workers != null' >/dev/null

echo ""
echo "=== /system/workers snapshot ==="
echo "${workers_json}" | jq '{
  autoscaling,
  autoscaleMax,
  identity_resolve: .metrics.identity_resolve,
  pools: (
    .workers
    | with_entries(
        .value = {
          desired: (.value.desired // 0),
          connected_workers: (.value.connected_workers // 0),
          queue_fill_pct: (.value.queue_fill_pct // 0),
          total_dropped: (.value.total_dropped // 0),
          reason: (.value.reason // "")
        }
      )
  )
}'

sql_one() {
  local q="$1"
  if [[ "${HAS_SQLITE3}" -eq 1 ]]; then
    sqlite3 "${RELAY_DB_PATH}" "${q}"
    return
  fi
  python3 - "$RELAY_DB_PATH" "${q}" <<'PY'
import sqlite3
import sys

db_path = sys.argv[1]
query = sys.argv[2]
con = sqlite3.connect(db_path)
try:
    cur = con.cursor()
    cur.execute(query)
    row = cur.fetchone()
    if row is None:
        print("")
    else:
        print("" if row[0] is None else row[0])
finally:
    con.close()
PY
}

sql_table() {
  local q="$1"
  if [[ "${HAS_SQLITE3}" -eq 1 ]]; then
    sqlite3 -header -column "${RELAY_DB_PATH}" "${q}"
    return
  fi
  python3 - "$RELAY_DB_PATH" "${q}" <<'PY'
import sqlite3
import sys

db_path = sys.argv[1]
query = sys.argv[2]
con = sqlite3.connect(db_path)
try:
    cur = con.cursor()
    cur.execute(query)
    cols = [d[0] for d in cur.description or []]
    if cols:
        print("\t".join(cols))
    for row in cur.fetchall():
        print("\t".join("" if v is None else str(v) for v in row))
finally:
    con.close()
PY
}

sightings_total="$(sql_one "SELECT COUNT(*) FROM person_sightings;")"
sightings_noncanonical_unknown="$(sql_one "SELECT COUNT(*) FROM person_sightings WHERE global_person_id GLOB 'unknown:*:*';")"
sightings_canonical_unknown="$(sql_one "SELECT COUNT(*) FROM person_sightings WHERE global_person_id GLOB 'unknown:[0-9]*' AND global_person_id NOT GLOB 'unknown:*:*';")"
sightings_known="$(sql_one "SELECT COUNT(*) FROM person_sightings WHERE global_person_id GLOB 'person:*';")"

globals_total="$(sql_one "SELECT COUNT(*) FROM person_global_identities;")"
globals_noncanonical_unknown="$(sql_one "SELECT COUNT(*) FROM person_global_identities WHERE global_person_id GLOB 'unknown:*:*';")"
globals_canonical_unknown="$(sql_one "SELECT COUNT(*) FROM person_global_identities WHERE global_person_id GLOB 'unknown:[0-9]*' AND global_person_id NOT GLOB 'unknown:*:*';")"
globals_known="$(sql_one "SELECT COUNT(*) FROM person_global_identities WHERE global_person_id GLOB 'person:*';")"

echo ""
echo "=== DB canonical-ID checks ==="
echo "person_sightings total:                 ${sightings_total}"
echo "person_sightings canonical unknown:*:   ${sightings_canonical_unknown}"
echo "person_sightings noncanonical unknown:*:*: ${sightings_noncanonical_unknown}"
echo "person_sightings known person:*:        ${sightings_known}"
echo ""
echo "person_global_identities total:         ${globals_total}"
echo "person_global_identities canonical unknown:*: ${globals_canonical_unknown}"
echo "person_global_identities noncanonical unknown:*:*: ${globals_noncanonical_unknown}"
echo "person_global_identities known person:*: ${globals_known}"

echo ""
echo "=== Top noncanonical IDs in person_sightings (if any) ==="
sql_table "
SELECT global_person_id, COUNT(*) AS cnt, MAX(created_at) AS last_seen
FROM person_sightings
WHERE global_person_id GLOB 'unknown:*:*'
GROUP BY global_person_id
ORDER BY cnt DESC, last_seen DESC
LIMIT 15;
"

echo ""
echo "=== Recent unknown sightings (canonical) ==="
sql_table "
SELECT global_person_id, person_id, service_id, created_at
FROM person_sightings
WHERE global_person_id GLOB 'unknown:[0-9]*'
  AND global_person_id NOT GLOB 'unknown:*:*'
ORDER BY created_at DESC
LIMIT 15;
"

if [[ "${sightings_noncanonical_unknown}" -gt 0 || "${globals_noncanonical_unknown}" -gt 0 ]]; then
  echo ""
  echo "[check] STATUS: WARN (noncanonical unknown IDs present)"
  exit 2
fi

echo ""
echo "[check] STATUS: OK (no noncanonical unknown IDs found)"
