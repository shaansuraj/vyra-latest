# Docker Compose Deployment

## Profiles
- `core`: relay API, dashboard app, engine distributor
- `cv`: relay-engine adapters + CV workers (PPE, person detection, VLM, face)
- `llm`: Ask-AI local LLM worker (`google/gemma-3-1b-it`)
- `gpu`: tag for GPU-heavy services (same services can also run under `cv`/`llm`)

## Start
```bash
# Core platform only
docker compose --profile core up -d --build

# Core + CV processing
docker compose --profile core --profile cv up -d --build

# Core + CV + Ask-AI worker
docker compose --profile core --profile cv --profile llm up -d --build
```

## Scale worker pools
```bash
# Example: scale dedicated CV workers/adapters
docker compose --profile core --profile cv up -d --scale worker-ppe=2 --scale worker-objdet=2 --scale adapter-ppe=2 --scale adapter-objdet=2

# Scale shared VLM/Face pools
docker compose --profile core --profile cv up -d --scale worker-vlm=1 --scale worker-face=1 --scale adapter-vlm=1 --scale adapter-face=1
```

## Endpoints
- Dashboard: `http://localhost:5173`
- Relay API: `http://localhost:8020`
- Relay worker WS: `ws://localhost:9020/worker/connect`
- Engine WS: `ws://localhost:5000/ws`
- LLM worker health (when `llm` profile is up): `http://localhost:5003/health`

## Persistence
`relay_data` volume stores:
- `database/relay.db`
- snapshots
- custom YOLO model uploads
- relay storage artifacts

## Notes
- In Compose mode, services run as separate containers and are typically scaled with `docker compose --scale`.
- Relay reads `LLM_WORKER_URL` (`http://llm-worker:5003` by default in compose) so `/query` works against the external Ask-AI worker.
- If `llm` profile is not running, `/query` returns `409 llm_worker_not_running` and the UI prompt remains actionable.

## Automated Smoke / Load
Use the committed script for repeatable API smoke + lightweight load checks:

```bash
# health-only (no auth token)
scripts/e2e_smoke.sh

# full authenticated flow
RELAY_TOKEN="<jwt>" RELAY_BASE="http://127.0.0.1:8020" scripts/e2e_smoke.sh
```

Optional:
- `SMOKE_LOAD_SECONDS=60` to increase the load-loop duration.
- `SMOKE_IDENTITY_TRACKS=16` to increase synthetic cross-camera identity traffic.
- `SMOKE_MEDIA_IDS=cam-a,cam-b,cam-c` to override media IDs for identity simulation.

For a heavier authenticated multi-camera load pass:

```bash
RELAY_TOKEN="<jwt>" RELAY_BASE="http://127.0.0.1:8020" scripts/e2e_multicamera_load.sh
```
