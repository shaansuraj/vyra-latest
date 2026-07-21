FROM golang:1.24-bookworm AS builder
WORKDIR /src

COPY go.mod go.sum ./
COPY --from=node_module . /node
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/relay ./cmd/relay

FROM debian:bookworm-slim AS runtime
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*

ENV APP_DIR=/var/lib/vyra/relay \
    FRAME_INTERVAL_SECONDS=0.2 \
    CV_FRAME_BATCH_SIZE=10 \
    RELAY_PORT=9020 \
    API_PORT=8020 \
    ENGINE_PORT=5000 \
    DASHBOARD_URL=http://localhost:5173 \
    AUTO_REQUEST_REALTIME_STREAM=true \
    SNAPSHOT_RETENTION_DAYS=7 \
    JWT_SECRET=change-me-in-production \
    SIGNUP_ENABLED=false \
    WORKER_SECRET=change-me

WORKDIR /app
COPY --from=builder /out/relay /usr/local/bin/relay

VOLUME ["/var/lib/vyra/relay"]
EXPOSE 8020 9020
HEALTHCHECK --interval=15s --timeout=5s --retries=5 CMD curl -fsS http://127.0.0.1:8020/flags > /dev/null || exit 1

ENTRYPOINT ["relay"]
