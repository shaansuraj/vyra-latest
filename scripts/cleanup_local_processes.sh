#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

mapfile -t CANDIDATES < <(ps -eo pid=,pgid=,args= | awk '
  /bun run adapter/ ||
  /bun run src\/relay_worker_adapter\.ts/ ||
  /worker_query_llm\.py/ ||
  /python .*worker_(ppe|object_detection|face_recognition|vlm|embedding|rag_reranker)\.py/ ||
  /python .* -m worker_(ppe|object_detection|face_recognition|vlm|embedding|rag_reranker)/ {
    print $1 " " $2 " " substr($0, index($0,$3))
  }
')

if [[ ${#CANDIDATES[@]} -eq 0 ]]; then
  echo "[vyra] No local worker/adapter processes found."
  exit 0
fi

declare -A KILL_PGIDS=()

for row in "${CANDIDATES[@]}"; do
  pid="$(awk '{print $1}' <<<"$row")"
  pgid="$(awk '{print $2}' <<<"$row")"
  cwd="$(readlink -f "/proc/$pid/cwd" 2>/dev/null || true)"
  if [[ -z "$cwd" ]]; then
    continue
  fi
  if [[ "$cwd" == "$ROOT/engine"* || "$cwd" == "$ROOT/application/relay"* ]]; then
    KILL_PGIDS["$pgid"]=1
    echo "[vyra] Marking stale process group $pgid (pid $pid, cwd $cwd)"
  fi
done

if [[ ${#KILL_PGIDS[@]} -eq 0 ]]; then
  echo "[vyra] No matching local stale process groups found."
  exit 0
fi

for pgid in "${!KILL_PGIDS[@]}"; do
  kill -TERM -- "-$pgid" 2>/dev/null || true
done
sleep 1
for pgid in "${!KILL_PGIDS[@]}"; do
  kill -KILL -- "-$pgid" 2>/dev/null || true
done

echo "[vyra] Cleanup complete."
