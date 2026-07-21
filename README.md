# VYRA

VYRA is a self-hosted AI camera platform with a Go relay API, Solid/Vite dashboard, Bun-based engine distributor, and optional Python CV/LLM workers.

## Quick Start

Prerequisites:

- Git
- Docker Engine with Docker Compose v2
- NVIDIA GPU + CUDA runtime for real-time CV/LLM workers. The core profile can start without GPU workers.

For local development without Docker, install Go, Bun, Python 3.12, and `uv`.

Run the core platform:

```bash
git clone <your-repo-url> VYRA
cd VYRA
cp .env.example .env
docker compose --profile core up -d --build
```

Open:

- Dashboard: http://localhost:5173
- Relay API: http://localhost:8020
- Engine: http://localhost:5000/version

Start optional workers:

```bash
# Core platform + CV workers
docker compose --profile core --profile cv up -d --build

# Core platform + CV workers + Ask-AI worker
docker compose --profile core --profile cv --profile llm up -d --build
```

Stop everything:

```bash
docker compose down
```

## Configuration

Docker Compose reads `.env` from the repository root. Start from `.env.example` and replace at least `JWT_SECRET` and `WORKER_SECRET` for anything beyond a local throwaway run.

Useful profiles:

- `core`: relay API, dashboard, and engine distributor
- `cv`: Python CV workers and relay adapters
- `llm`: local Ask-AI worker
- `gpu`: tag for GPU-heavy workers

Persistent relay state is stored in the Docker volume `relay_data`, not in the Git checkout.

## Local Development

Relay:

```bash
cd application/relay
go test ./...
go run ./cmd/relay
```

Dashboard:

```bash
cd application/app
bun install
bun run dev
```

Engine:

```bash
cd engine
bun install
bun run start
```

Python workers:

```bash
cd engine/py
uv sync
uv run python -m worker_object_detection
```

The Python project uses the local `engine/py/transformers` source tree as an editable dependency for SAM3 support, so keep that directory in the repository. Virtual environments, downloaded model weights, logs, databases, and local `.env` files are ignored.

The first CV/LLM run may download several GB of model dependencies.

## More Documentation

- [Docker Compose guide](DOCKER_COMPOSE_GUIDE.md)
- [Architecture](VYRA_ARCHITECTURE.md)
- [Relay README](application/relay/README.md)
- [Engine README](engine/README.md)
