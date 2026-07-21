#!/usr/bin/env bash
set -euo pipefail

RELAY_BASE="${RELAY_BASE:-http://127.0.0.1:8020}"
TOKEN="${RELAY_TOKEN:-}"
DURATION="${SOAK_DURATION_SECONDS:-60}"
POLL_SECONDS="${SOAK_POLL_SECONDS:-2}"
MEDIA_IDS_CSV="${SOAK_MEDIA_IDS:-soak-cam-a,soak-cam-b,soak-cam-c}"
TRACKS_PER_MEDIA="${SOAK_TRACKS_PER_MEDIA:-12}"
SUMMARY_FILE="${SOAK_SUMMARY_FILE:-/tmp/vyra_soak_summary.json}"

if [[ -z "${TOKEN}" ]]; then
  echo "[soak] RELAY_TOKEN is required"
  exit 1
fi

if ! command -v jq >/dev/null 2>&1; then
  echo "[soak] jq is required"
  exit 1
fi

if ! [[ "${DURATION}" =~ ^[0-9]+$ ]] || [[ "${DURATION}" -le 0 ]]; then
  echo "[soak] SOAK_DURATION_SECONDS must be a positive integer"
  exit 1
fi

if ! [[ "${POLL_SECONDS}" =~ ^[0-9]+$ ]] || [[ "${POLL_SECONDS}" -le 0 ]]; then
  echo "[soak] SOAK_POLL_SECONDS must be a positive integer"
  exit 1
fi

if ! [[ "${TRACKS_PER_MEDIA}" =~ ^[0-9]+$ ]] || [[ "${TRACKS_PER_MEDIA}" -le 0 ]]; then
  echo "[soak] SOAK_TRACKS_PER_MEDIA must be a positive integer"
  exit 1
fi

api_get() {
  local path="$1"
  curl -sS "${RELAY_BASE}${path}" -H "Authorization: Bearer ${TOKEN}"
}

api_post_json_code() {
  local path="$1"
  local payload="$2"
  curl -sS -o /tmp/vyra_soak_post_resp.json -w '%{http_code}' \
    -X POST "${RELAY_BASE}${path}" \
    -H "Authorization: Bearer ${TOKEN}" \
    -H "Content-Type: application/json" \
    -d "${payload}"
}

samples_file="$(mktemp /tmp/vyra-soak-workers.XXXXXX.jsonl)"
trap 'rm -f "${samples_file}" /tmp/vyra_soak_post_resp.json' EXIT

IFS=',' read -r -a MEDIA_IDS <<<"${MEDIA_IDS_CSV}"
for i in "${!MEDIA_IDS[@]}"; do
  MEDIA_IDS[$i]="$(echo "${MEDIA_IDS[$i]}" | sed 's/^ *//; s/ *$//')"
done

echo "[soak] relay base: ${RELAY_BASE}"
echo "[soak] media IDs: ${MEDIA_IDS[*]}"
echo "[soak] duration=${DURATION}s poll=${POLL_SECONDS}s tracks_per_media=${TRACKS_PER_MEDIA}"

baseline="$(api_get "/system/workers")"
echo "${baseline}" | jq -e '.workers != null' >/dev/null

resolve_attempts=0
resolve_failures=0
sample_count=0
end_ts=$(( $(date +%s) + DURATION ))

while [[ "$(date +%s)" -lt "${end_ts}" ]]; do
  now_iso="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  for media_id in "${MEDIA_IDS[@]}"; do
    [[ -n "${media_id}" ]] || continue
    for track_id in $(seq 1 "${TRACKS_PER_MEDIA}"); do
      case $((track_id % 3)) in
        0) emb='[0.98,0.02,0.00]' ;;
        1) emb='[0.03,0.95,0.02]' ;;
        *) emb='[0.01,0.05,0.94]' ;;
      esac
      payload="$(jq -cn \
        --arg media "${media_id}" \
        --argjson track "${track_id}" \
        --argjson emb "${emb}" \
        --arg ts "${now_iso}" \
        '{media_id:$media, track_id:$track, embedding:$emb, camera_name:$media, camera_timestamp:$ts}')"
      code="$(api_post_json_code "/cv/person-identity/resolve" "${payload}")"
      resolve_attempts=$((resolve_attempts + 1))
      if [[ "${code}" != "200" ]]; then
        resolve_failures=$((resolve_failures + 1))
      fi
    done
    curl -sS "${RELAY_BASE}/cv/person-identities?media_id=${media_id}" >/dev/null || true
  done

  sample="$(api_get "/system/workers")"
  echo "${sample}" >> "${samples_file}"
  sample_count=$((sample_count + 1))
  sleep "${POLL_SECONDS}"
done

final="$(api_get "/system/workers")"
echo "${final}" | jq -e '.workers != null' >/dev/null

worker_rows_file="$(mktemp /tmp/vyra-soak-worker-rows.XXXXXX.jsonl)"
trap 'rm -f "${samples_file}" /tmp/vyra_soak_post_resp.json "${worker_rows_file}"' EXIT

for pool in ppe objdet vlm face; do
  desired="$(echo "${final}" | jq --arg p "${pool}" '.workers[$p].desired // 0')"
  connected="$(echo "${final}" | jq --arg p "${pool}" '.workers[$p].connected_workers // 0')"
  queue_max="$(jq -s --arg p "${pool}" 'map(.workers[$p].queue_fill_pct // 0) | max // 0' "${samples_file}")"
  dropped_start="$(echo "${baseline}" | jq --arg p "${pool}" '.workers[$p].total_dropped // 0')"
  dropped_final="$(echo "${final}" | jq --arg p "${pool}" '.workers[$p].total_dropped // 0')"
  dropped_delta=$((dropped_final - dropped_start))
  reason="$(echo "${final}" | jq -r --arg p "${pool}" '.workers[$p].reason // ""')"

  jq -cn \
    --arg pool "${pool}" \
    --argjson desired "${desired}" \
    --argjson connected "${connected}" \
    --argjson queue_max "${queue_max}" \
    --argjson dropped_start "${dropped_start}" \
    --argjson dropped_final "${dropped_final}" \
    --argjson dropped_delta "${dropped_delta}" \
    --arg reason "${reason}" \
    '{
      pool: $pool,
      desired: $desired,
      connected_workers_final: $connected,
      queue_fill_max_pct: $queue_max,
      total_dropped_start: $dropped_start,
      total_dropped_final: $dropped_final,
      dropped_delta: $dropped_delta,
      reason: $reason
    }' >> "${worker_rows_file}"
done

workers_json="$(jq -s '.' "${worker_rows_file}")"

jq -n \
  --argjson workers "${workers_json}" \
  --argjson base "${baseline}" \
  --argjson final "${final}" \
  --argjson duration "${DURATION}" \
  --argjson poll_seconds "${POLL_SECONDS}" \
  --argjson resolve_attempts "${resolve_attempts}" \
  --argjson resolve_failures "${resolve_failures}" \
  --argjson sample_count "${sample_count}" \
  '{
    soak: {
      duration_seconds: $duration,
      poll_seconds: $poll_seconds,
      sample_count: $sample_count,
      resolve_attempts: $resolve_attempts,
      resolve_failures: $resolve_failures
    },
    workers: $workers,
    identity_resolve: {
      resolve_count_start: ($base.metrics.identity_resolve.resolve_count // 0),
      resolve_count_final: ($final.metrics.identity_resolve.resolve_count // 0),
      resolve_count_delta: (($final.metrics.identity_resolve.resolve_count // 0) - ($base.metrics.identity_resolve.resolve_count // 0)),
      avg_latency_ms_final: ($final.metrics.identity_resolve.avg_latency_ms // 0)
    },
    event_persist: {
      success_delta: (($final.metrics.event_persist.success // 0) - ($base.metrics.event_persist.success // 0)),
      failure_delta: (($final.metrics.event_persist.failure // 0) - ($base.metrics.event_persist.failure // 0)),
      dropped_delta: (($final.metrics.event_persist.dropped // 0) - ($base.metrics.event_persist.dropped // 0))
    },
    snapshot_upload: {
      success_delta: (($final.metrics.snapshot_upload.success // 0) - ($base.metrics.snapshot_upload.success // 0)),
      failure_delta: (($final.metrics.snapshot_upload.failure // 0) - ($base.metrics.snapshot_upload.failure // 0))
    }
  }' > "${SUMMARY_FILE}"

echo "[soak] summary written: ${SUMMARY_FILE}"
cat "${SUMMARY_FILE}" | jq .
