# VYRA Airgapped 2-Machine Cluster Deployment Guide

> Deploy VYRA on two airgapped machines with GPU acceleration, Docker Swarm clustering, and fault tolerance.

---

## Table of Contents

1. [Prerequisites](#1-prerequisites)
2. [Architecture Overview](#2-architecture-overview)
3. [Phase 1: Code Changes (Before Building)](#3-phase-1-code-changes-before-building)
4. [Phase 2: Build Docker Images](#4-phase-2-build-docker-images)
5. [Phase 3: Pre-Download AI Models](#5-phase-3-pre-download-ai-models)
6. [Phase 4: Export & Transfer](#6-phase-4-export--transfer)
7. [Phase 5: Cluster Setup](#7-phase-5-cluster-setup)
8. [Phase 6: Deploy the Stack](#8-phase-6-deploy-the-stack)
9. [Phase 7: Fault Tolerance](#9-phase-7-fault-tolerance)
10. [Phase 8: Verification](#10-phase-8-verification)
11. [Operational Notes](#11-operational-notes)
12. [Troubleshooting](#12-troubleshooting)

---

## 1. Prerequisites

### 1.1 Hardware

| | Machine 1 | Machine 2 |
|---|---|---|
| Role | Swarm Manager | Swarm Worker |
| GPU | NVIDIA RTX 5090 (32GB VRAM) | NVIDIA RTX 5090 (32GB VRAM) |
| RAM | 48GB | 48GB |
| Storage | ≥100GB SSD (relay data, snapshots) | ≥50GB SSD (models, containers) |
| Network | Gigabit LAN | Gigabit LAN |

### 1.2 Software (pre-installed on both machines)

| Software | Version | Purpose |
|---|---|---|
| Docker Engine | 24.0+ | Container runtime |
| NVIDIA Driver | 550+ | GPU support |
| NVIDIA Container Toolkit | 1.14+ | GPU in containers |
| keepalived | 2.2+ | Floating VIP for HA |
| nfs-kernel-server | any | Machine 1 only — NFS export |
| nfs-common | any | Machine 2 only — NFS mount |

**NVIDIA Container Toolkit installation (on internet-connected machine, transfer .deb packages):**
```bash
# Download on internet machine:
apt download nvidia-container-toolkit nvidia-container-toolkit-base \
  libnvidia-container1 libnvidia-container-tools

# Transfer .deb files to airgapped machines, then:
sudo dpkg -i *.deb
sudo nvidia-ctk runtime configure --runtime=docker
sudo systemctl restart docker

# Verify:
docker run --rm --gpus all nvidia/cuda:12.8.0-base-ubuntu22.04 nvidia-smi
```

### 1.3 Network

| Address | Purpose |
|---|---|
| `192.168.1.10` | Machine 1 (Swarm Manager) — placeholder, replace with actual |
| `192.168.1.20` | Machine 2 (Swarm Worker) — placeholder, replace with actual |
| `192.168.1.5` | Floating VIP (keepalived) — placeholder, choose unused IP |
| `192.168.1.10:5001` | Local Docker registry |

Both machines must be on the same LAN subnet.

### 1.4 Transfer Medium

A portable SSD (≥64GB) for transferring Docker images and models from the internet-connected build machine. Total transfer size: ~30-35GB.

---

## 2. Architecture Overview

```
                        Floating VIP: 192.168.1.5
                    ┌──────────┴──────────┐
                    │                     │
    Machine 1 (Manager)          Machine 2 (Worker)
    192.168.1.10                 192.168.1.20
┌─────────────────────┐    ┌─────────────────────┐
│                     │    │                     │
│  relay (Go)         │    │  worker-vlm (GPU)   │
│    Port 8020 HTTP   │    │    SmolVLM2-2.2B    │
│    Port 9020 WS     │    │    ~6GB VRAM        │
│                     │    │                     │
│  app (Nginx)        │◄──►│  worker-face (GPU)  │
│    Port 80          │NFS │    InsightFace       │
│                     │    │    ~500MB VRAM       │
│  engine (Bun)       │    │                     │
│    Port 5000        │    │  app (Nginx replica) │
│                     │    │    Port 80           │
│  adapter-ppe        │    │                     │
│  adapter-objdet     │    │  keepalived BACKUP   │
│  adapter-vlm        │    │                     │
│  adapter-face       │    └─────────────────────┘
│                     │
│  worker-ppe (GPU)   │    VRAM Budget:
│    YOLOv8m ~2GB     │    Machine 1: ~4GB / 32GB
│                     │    Machine 2: ~7GB / 32GB
│  worker-objdet (GPU)│
│    YOLOv8m ~2GB     │
│                     │
│  coturn (STUN)      │
│    Port 3478        │
│                     │
│  Docker Registry    │
│    Port 5001        │
│                     │
│  keepalived MASTER  │
└─────────────────────┘

Data Storage:
  /data/vyra/relay/          ← NFS shared (Machine 1 exports)
    ├── database/relay.db    ← SQLite (WAL mode)
    ├── storage/snapshots/   ← Detection snapshots
    └── storage/persons/     ← Face enrollment images

  /opt/vyra/models/          ← Local on each machine (read-only)
    ├── hf/                  ← HuggingFace model cache
    └── insightface/         ← InsightFace model cache
```

### GPU Worker Placement Rationale

| Worker | Machine | VRAM | Why |
|---|---|---|---|
| worker-ppe | 1 (Manager) | ~2GB | Colocated with relay for lowest latency |
| worker-objdet | 1 (Manager) | ~2GB | Same reasoning |
| worker-vlm | 2 (Worker) | ~6GB | Largest model, isolated to avoid contention |
| worker-face | 2 (Worker) | ~500MB | Paired with VLM on the other machine |

Both machines have ~24-26GB VRAM headroom for scaling replicas.

---

## 3. Phase 1: Code Changes (Before Building)

Four changes are required before building Docker images. These are performed on the internet-connected build machine.

### 3.1 Make STUN Server Configurable

The codebase has 3 hardcoded references to `stun.l.google.com:19302` which won't resolve in an airgapped network.

#### File 1: `application/relay/webrtc.go` (line 130-133)

**Before:**
```go
pc, err := api.NewPeerConnection(webrtc.Configuration{
    ICEServers: []webrtc.ICEServer{
        {URLs: []string{"stun:stun.l.google.com:19302"}},
    },
})
```

**After:**
```go
pc, err := api.NewPeerConnection(webrtc.Configuration{
    ICEServers: func() []webrtc.ICEServer {
        if u := os.Getenv("STUN_URL"); u != "" {
            return []webrtc.ICEServer{{URLs: []string{u}}}
        }
        return nil // Host candidates only — works on LAN
    }(),
})
```

Add `"os"` to imports if not already present.

#### File 2: `application/app/src/components/VideoTile.tsx` (line 182)

**Before:**
```typescript
newPc = new RTCPeerConnection({
    iceServers: [{ urls: 'stun:stun.l.google.com:19302' }],
});
```

**After:**
```typescript
newPc = new RTCPeerConnection({
    iceServers: import.meta.env.VITE_STUN_URL
        ? [{ urls: import.meta.env.VITE_STUN_URL }]
        : [],
});
```

#### File 3: `application/app/src/components/WebRTCStream.tsx` (lines 74-78)

**Before:**
```typescript
const newPc = new RTCPeerConnection({
    iceServers: [{
        urls: 'stun:stun.l.google.com:19302'
    }]
});
```

**After:**
```typescript
const newPc = new RTCPeerConnection({
    iceServers: import.meta.env.VITE_STUN_URL
        ? [{ urls: import.meta.env.VITE_STUN_URL }]
        : [],
});
```

### 3.2 Fix Docker Compose Worker Port Bug

#### File 4: `docker-compose.yml` (line 31)

The `x-worker-common` environment has `RELAY_HTTP_BASE: http://relay:8020` but workers need port **9020** for snapshot uploads.

**Before:**
```yaml
x-worker-common: &worker-common
  environment: &worker-common-env
    RELAY_HTTP_BASE: http://relay:8020
```

**After:**
```yaml
x-worker-common: &worker-common
  environment: &worker-common-env
    RELAY_HTTP_BASE: http://relay:9020
```

---

## 4. Phase 2: Build Docker Images

All commands run on the **internet-connected build machine**.

### 4.1 Set Build Variables

```bash
# Replace with your actual VIP or Machine 1 IP
export MACHINE_VIP=192.168.1.5
export STUN_URL="stun:${MACHINE_VIP}:3478"
```

### 4.2 Build All Images

```bash
cd /path/to/VYRA

# 1. Relay (Go multi-stage, ~50MB)
docker build -t vyra/relay:v1 ./application/relay

# 2. Frontend (Bun build + Nginx, ~30MB)
#    VITE_RELAY_API_URL and VITE_STUN_URL are baked into the JS bundle
docker build \
  --build-arg VITE_RELAY_API_URL=http://${MACHINE_VIP}:8020 \
  --build-arg VITE_STUN_URL=${STUN_URL} \
  -t vyra/app:v1 ./application/app

# 3. Engine (Bun runtime, ~80MB)
docker build -t vyra/engine:v1 ./engine

# 4. CV Worker (Python 3.12 + PyTorch CUDA cu128, ~8-12GB)
#    This is the largest image — downloads PyTorch CUDA wheels during build
docker build -t vyra/cv-worker:v1 -f ./engine/py/Dockerfile.worker ./engine/py

# 5. LLM Worker (optional, ~4GB)
docker build -t vyra/llm-worker:v1 -f ./engine/py/Dockerfile.llm ./engine/py

# 6. STUN Server (coturn, ~15MB)
docker pull coturn/coturn:latest
docker tag coturn/coturn:latest vyra/coturn:v1

# 7. Docker Registry (for Swarm image distribution)
docker pull registry:2
```

### 4.3 Verify Images

```bash
docker images | grep -E "vyra/|registry"
# Expected:
# vyra/relay         v1    ~50MB
# vyra/app           v1    ~30MB
# vyra/engine        v1    ~80MB
# vyra/cv-worker     v1    ~8-12GB
# vyra/llm-worker    v1    ~4GB
# vyra/coturn        v1    ~15MB
# registry           2     ~25MB
```

**Important:** The `vyra/app:v1` image has `VITE_RELAY_API_URL` and `VITE_STUN_URL` baked into the compiled JavaScript. If you change the VIP or STUN address later, you must rebuild this image. Consider using a DNS hostname (e.g., `vyra.local`) resolved via `/etc/hosts` to avoid this.

---

## 5. Phase 3: Pre-Download AI Models

Run on the **internet-connected build machine** with Python 3.12 and the required packages.

### 5.1 Create Models Directory

```bash
export MODEL_DIR=/tmp/vyra-models
mkdir -p ${MODEL_DIR}/{hf,insightface}
```

### 5.2 Download Models

```bash
# Set HuggingFace cache to our models dir
export HF_HOME=${MODEL_DIR}/hf

# 1. SmolVLM2-2.2B-Instruct (~8.4 GB)
python3 -c "
from huggingface_hub import snapshot_download
snapshot_download('HuggingFaceTB/SmolVLM2-2.2B-Instruct')
print('SmolVLM2 downloaded')
"

# 2. PPE YOLO Model (~50 MB)
python3 -c "
from huggingface_hub import hf_hub_download
hf_hub_download('Hexmon/vyra-yolo-ppe-detection', 'best.pt')
print('PPE model downloaded')
"

# 3. YOLOv8m (~50 MB)
python3 -c "
from ultralytics import YOLO
import shutil, os
model = YOLO('yolov8m.pt')
# Copy to our models dir
src = os.path.expanduser('yolov8m.pt')
if not os.path.exists(src):
    src = os.path.expanduser('~/.cache/ultralytics/yolov8m.pt')
shutil.copy(src, '${MODEL_DIR}/hf/yolov8m.pt')
print('YOLOv8m downloaded')
"

# 4. InsightFace buffalo_sc (~16 MB) and buffalo_l (~326 MB)
INSIGHTFACE_HOME=${MODEL_DIR}/insightface python3 -c "
import insightface
from insightface.app import FaceAnalysis
import os
os.environ['INSIGHTFACE_HOME'] = '${MODEL_DIR}/insightface'
for name in ['buffalo_sc', 'buffalo_l']:
    app = FaceAnalysis(name=name, root='${MODEL_DIR}/insightface')
    app.prepare(ctx_id=-1, det_size=(640, 640))
    print(f'{name} downloaded')
"
```

### 5.3 Find Exact Model Paths

After downloading, record the snapshot hashes for environment variables:

```bash
python3 -c "
import os
hf_home = '${MODEL_DIR}/hf'
cache = os.path.join(hf_home, 'hub')
for entry in sorted(os.listdir(cache)):
    if entry.startswith('models--'):
        snap_dir = os.path.join(cache, entry, 'snapshots')
        if os.path.isdir(snap_dir):
            for snap in os.listdir(snap_dir):
                print(f'{entry}: {snap}')
"
```

Record these paths. You'll need them for the Swarm compose environment variables:
- `PPE_MODEL_ID=/models/hf/hub/models--Hexmon--vyra-yolo-ppe-detection/snapshots/<HASH>/best.pt`
- `VLM_MODEL_ID=/models/hf/hub/models--HuggingFaceTB--SmolVLM2-2.2B-Instruct/snapshots/<HASH>`
- `MODEL_ID=/models/hf/yolov8m.pt`

### 5.4 Verify Models Directory

```bash
du -sh ${MODEL_DIR}/*
# Expected:
# ~9GB    /tmp/vyra-models/hf
# ~350MB  /tmp/vyra-models/insightface

find ${MODEL_DIR} -name "*.pt" -o -name "*.safetensors" -o -name "*.onnx" | head -20
```

---

## 6. Phase 4: Export & Transfer

### 6.1 Export Docker Images

```bash
mkdir -p /tmp/vyra-export/images

# Save each image as compressed tarball
docker save vyra/relay:v1       | gzip > /tmp/vyra-export/images/vyra-relay-v1.tar.gz
docker save vyra/app:v1         | gzip > /tmp/vyra-export/images/vyra-app-v1.tar.gz
docker save vyra/engine:v1      | gzip > /tmp/vyra-export/images/vyra-engine-v1.tar.gz
docker save vyra/cv-worker:v1   | gzip > /tmp/vyra-export/images/vyra-cv-worker-v1.tar.gz
docker save vyra/llm-worker:v1  | gzip > /tmp/vyra-export/images/vyra-llm-worker-v1.tar.gz
docker save vyra/coturn:v1      | gzip > /tmp/vyra-export/images/vyra-coturn-v1.tar.gz
docker save registry:2          | gzip > /tmp/vyra-export/images/docker-registry-2.tar.gz
```

### 6.2 Package Models

```bash
tar -czf /tmp/vyra-export/vyra-models.tar.gz -C /tmp vyra-models/
```

### 6.3 Package Deployment Configs

```bash
# Copy this guide and the Swarm compose file
cp deploy/AIRGAP_DEPLOYMENT.md /tmp/vyra-export/
# The docker-compose.swarm.yml content is provided inline in Phase 6 below
```

### 6.4 Transfer to External SSD

```bash
# Mount SSD (e.g., at /mnt/transfer)
cp -r /tmp/vyra-export/* /mnt/transfer/
sync

ls -lh /mnt/transfer/images/
ls -lh /mnt/transfer/vyra-models.tar.gz
# Total: ~30-35 GB
```

### 6.5 Copy to Airgapped Machines

On both machines:

```bash
# Create working directory
sudo mkdir -p /opt/vyra/{images,models}

# Copy from SSD (mounted at /mnt/transfer)
cp /mnt/transfer/images/*.tar.gz /opt/vyra/images/

# Models — only need to extract on machines that run workers
tar -xzf /mnt/transfer/vyra-models.tar.gz -C /opt/vyra/models/
```

---

## 7. Phase 5: Cluster Setup

### 7.1 Load Docker Images (Both Machines)

```bash
for img in /opt/vyra/images/*.tar.gz; do
  echo "Loading $(basename $img)..."
  docker load < "$img"
done

# Verify
docker images | grep -E "vyra/|registry"
```

### 7.2 Start Local Docker Registry (Machine 1 Only)

```bash
# Start registry
docker run -d \
  --name vyra-registry \
  --restart always \
  -p 5001:5000 \
  -v /opt/vyra/registry-data:/var/lib/registry \
  registry:2

# Push all images to local registry
REGISTRY=192.168.1.10:5001
for img in relay app engine cv-worker llm-worker coturn; do
  docker tag vyra/${img}:v1 ${REGISTRY}/vyra/${img}:v1
  docker push ${REGISTRY}/vyra/${img}:v1
  echo "Pushed ${img}"
done
```

### 7.3 Configure Machine 2 Docker Daemon

On **Machine 2**, trust the insecure local registry:

```bash
# Create or edit /etc/docker/daemon.json
cat > /etc/docker/daemon.json << 'EOF'
{
  "insecure-registries": ["192.168.1.10:5001"],
  "default-runtime": "nvidia",
  "runtimes": {
    "nvidia": {
      "path": "nvidia-container-runtime",
      "runtimeArgs": []
    }
  }
}
EOF

sudo systemctl restart docker
```

Do the same on **Machine 1** (add the nvidia runtime config if not already present).

### 7.4 Initialize Docker Swarm

```bash
# Machine 1: Initialize as manager
docker swarm init --advertise-addr 192.168.1.10

# Copy the join command from the output, looks like:
#   docker swarm join --token SWMTKN-1-abc123... 192.168.1.10:2377

# Machine 2: Join as worker
docker swarm join --token SWMTKN-1-abc123... 192.168.1.10:2377

# Machine 1: Verify cluster
docker node ls
# Should show 2 nodes
```

### 7.5 Label Nodes

```bash
# Get node hostnames
docker node ls --format "{{.Hostname}}"

# Machine 1 (manager): label for placement constraints
docker node update --label-add role=manager --label-add gpu=true <machine1-hostname>

# Machine 2 (worker):
docker node update --label-add role=worker --label-add gpu=true <machine2-hostname>
```

### 7.6 Create Overlay Networks

```bash
# Machine 1 (manager):
docker network create --driver overlay --attachable vyra-core
docker network create --driver overlay --attachable vyra-cv
```

### 7.7 NFS Shared Storage

The relay's SQLite database, snapshots, and face enrollment data must be accessible from both machines for failover.

**Machine 1 (NFS server):**

```bash
sudo apt install nfs-kernel-server

# Create data directory
sudo mkdir -p /data/vyra/relay

# Export to LAN
echo "/data/vyra/relay 192.168.1.0/24(rw,sync,no_subtree_check,no_root_squash)" \
  | sudo tee -a /etc/exports

sudo exportfs -ra
sudo systemctl restart nfs-kernel-server
```

**Machine 2 (NFS client):**

```bash
sudo apt install nfs-common

# Create mount point
sudo mkdir -p /data/vyra/relay

# Add to fstab for persistent mount
echo "192.168.1.10:/data/vyra/relay /data/vyra/relay nfs4 defaults,_netdev 0 0" \
  | sudo tee -a /etc/fstab

sudo mount -a

# Verify
df -h /data/vyra/relay
```

### 7.8 Create Docker Volumes

**Machine 1:**

```bash
# Relay data volume (local bind to NFS export source)
docker volume create --driver local \
  --opt type=none \
  --opt device=/data/vyra/relay \
  --opt o=bind \
  vyra_relay_data

# Models volume (read-only local bind)
docker volume create --driver local \
  --opt type=none \
  --opt device=/opt/vyra/models/vyra-models \
  --opt o=bind \
  vyra_models
```

**Machine 2:**

```bash
# Relay data volume (NFS mount)
docker volume create --driver local \
  --opt type=nfs \
  --opt o=addr=192.168.1.10,rw \
  --opt device=:/data/vyra/relay \
  vyra_relay_data

# Models volume (local)
docker volume create --driver local \
  --opt type=none \
  --opt device=/opt/vyra/models/vyra-models \
  --opt o=bind \
  vyra_models
```

---

## 8. Phase 6: Deploy the Stack

### 8.1 Create Environment File

On Machine 1:

```bash
mkdir -p /opt/vyra/deploy

cat > /opt/vyra/deploy/.env << EOF
JWT_SECRET=$(openssl rand -hex 32)
WORKER_SECRET=$(openssl rand -hex 32)
EOF
```

### 8.2 Create Swarm Compose File

Create `/opt/vyra/deploy/docker-compose.swarm.yml`:

```yaml
version: "3.9"

x-worker-env: &worker-env
  HF_HOME: /models/hf
  HF_HUB_OFFLINE: "1"
  TRANSFORMERS_OFFLINE: "1"
  HF_DATASETS_OFFLINE: "1"
  INSIGHTFACE_HOME: /models/insightface
  BACKEND_WS_URL: ws://engine:5000/ws
  RELAY_HTTP_BASE: http://relay:9020
  WORKER_SECRET: "${WORKER_SECRET}"
  PYTHONUNBUFFERED: "1"

x-worker-volumes: &worker-volumes
  - vyra_models:/models:ro

services:

  # ═══════════════════════════════════════════════════════════════════
  # STUN Server (coturn) — LAN WebRTC ICE
  # ═══════════════════════════════════════════════════════════════════
  coturn:
    image: 192.168.1.10:5001/vyra/coturn:v1
    ports:
      - "3478:3478/udp"
      - "3478:3478/tcp"
    command: >
      --no-auth
      --realm=vyra.local
      --log-file=stdout
    networks:
      - vyra-core
    deploy:
      replicas: 1
      placement:
        constraints: [node.labels.role == manager]
      restart_policy:
        condition: on-failure
        delay: 5s

  # ═══════════════════════════════════════════════════════════════════
  # Core Services
  # ═══════════════════════════════════════════════════════════════════
  relay:
    image: 192.168.1.10:5001/vyra/relay:v1
    environment:
      APP_DIR: /var/lib/vyra/relay
      FRAME_INTERVAL_SECONDS: "0.1"
      CV_FRAME_BATCH_SIZE: "1"
      RELAY_PORT: "9020"
      API_PORT: "8020"
      ENGINE_PORT: "5000"
      DASHBOARD_URL: "http://192.168.1.5"
      AUTO_REQUEST_REALTIME_STREAM: "true"
      SNAPSHOT_RETENTION_DAYS: "30"
      JWT_SECRET: "${JWT_SECRET}"
      WORKER_SECRET: "${WORKER_SECRET}"
      STUN_URL: "stun:192.168.1.5:3478"
      LLM_WORKER_URL: "http://llm-worker:5003"
      # VYRA_ROOT intentionally unset — disables ProcessManager in Docker mode
    volumes:
      - vyra_relay_data:/var/lib/vyra/relay
    ports:
      - "8020:8020"
      - "9020:9020"
    networks:
      - vyra-core
    healthcheck:
      test: ["CMD", "curl", "-fsS", "http://127.0.0.1:8020/flags"]
      interval: 15s
      timeout: 5s
      retries: 5
    deploy:
      replicas: 1
      placement:
        constraints: [node.labels.role == manager]
      restart_policy:
        condition: on-failure
        delay: 10s
        max_attempts: 5

  app:
    image: 192.168.1.10:5001/vyra/app:v1
    ports:
      - "80:80"
    networks:
      - vyra-core
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://127.0.0.1/healthz"]
      interval: 15s
      timeout: 5s
      retries: 3
    deploy:
      replicas: 2
      restart_policy:
        condition: on-failure

  engine:
    image: 192.168.1.10:5001/vyra/engine:v1
    ports:
      - "5000:5000"
    networks:
      - vyra-core
      - vyra-cv
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://127.0.0.1:5000/version"]
      interval: 15s
      timeout: 5s
      retries: 5
    deploy:
      replicas: 1
      placement:
        constraints: [node.labels.role == manager]
      restart_policy:
        condition: on-failure
        delay: 5s

  # ═══════════════════════════════════════════════════════════════════
  # Adapters (Relay ↔ Engine bridges)
  # ═══════════════════════════════════════════════════════════════════
  adapter-ppe:
    image: 192.168.1.10:5001/vyra/engine:v1
    command: ["bun", "run", "adapter"]
    environment:
      RELAY_WS_URL: ws://relay:9020/worker/connect
      RELAY_HTTP_BASE: http://relay:9020
      ENGINE_WS_URL: ws://engine:5000/ws
      WORKER_ID: vyra/ppe_detection
      ENGINE_WORKER_TYPE: ppe_detection
      MIN_SUBMIT_INTERVAL_MS: "0"
      MAX_IN_FLIGHT: "8"
      MAX_QUEUE_SIZE: "64"
      IN_FLIGHT_TIMEOUT_MS: "60000"
      MAX_JOB_RETRIES: "1"
    networks: [vyra-core, vyra-cv]
    deploy:
      replicas: 1
      restart_policy:
        condition: on-failure

  adapter-objdet:
    image: 192.168.1.10:5001/vyra/engine:v1
    command: ["bun", "run", "adapter"]
    environment:
      RELAY_WS_URL: ws://relay:9020/worker/connect
      RELAY_HTTP_BASE: http://relay:9020
      ENGINE_WS_URL: ws://engine:5000/ws
      WORKER_ID: vyra/person_detection
      ENGINE_WORKER_TYPE: object_detection
      MIN_SUBMIT_INTERVAL_MS: "0"
      MAX_IN_FLIGHT: "8"
      MAX_QUEUE_SIZE: "64"
      IN_FLIGHT_TIMEOUT_MS: "60000"
      MAX_JOB_RETRIES: "1"
    networks: [vyra-core, vyra-cv]
    deploy:
      replicas: 1
      restart_policy:
        condition: on-failure

  adapter-vlm:
    image: 192.168.1.10:5001/vyra/engine:v1
    command: ["bun", "run", "adapter"]
    environment:
      RELAY_WS_URL: ws://relay:9020/worker/connect
      RELAY_HTTP_BASE: http://relay:9020
      ENGINE_WS_URL: ws://engine:5000/ws
      WORKER_ID: vyra/vlm
      ENGINE_WORKER_TYPE: vlm
      MIN_SUBMIT_INTERVAL_MS: "200"
      MAX_IN_FLIGHT: "4"
      MAX_QUEUE_SIZE: "48"
      IN_FLIGHT_TIMEOUT_MS: "60000"
      MAX_JOB_RETRIES: "1"
    networks: [vyra-core, vyra-cv]
    deploy:
      replicas: 1
      restart_policy:
        condition: on-failure

  adapter-face:
    image: 192.168.1.10:5001/vyra/engine:v1
    command: ["bun", "run", "adapter"]
    environment:
      RELAY_WS_URL: ws://relay:9020/worker/connect
      RELAY_HTTP_BASE: http://relay:9020
      ENGINE_WS_URL: ws://engine:5000/ws
      WORKER_ID: vyra/face_recognition
      ENGINE_WORKER_TYPE: face_recognition
      MIN_SUBMIT_INTERVAL_MS: "0"
      MAX_IN_FLIGHT: "4"
      MAX_QUEUE_SIZE: "32"
      IN_FLIGHT_TIMEOUT_MS: "15000"
      MAX_JOB_RETRIES: "1"
    networks: [vyra-core, vyra-cv]
    deploy:
      replicas: 1
      restart_policy:
        condition: on-failure

  # ═══════════════════════════════════════════════════════════════════
  # GPU Workers — Machine 1
  # ═══════════════════════════════════════════════════════════════════
  worker-ppe:
    image: 192.168.1.10:5001/vyra/cv-worker:v1
    command: ["uv", "run", "python", "-m", "worker_ppe"]
    environment:
      <<: *worker-env
      WORKER_TYPE: ppe_detection
      MAX_LATENCY_MS: "100"
      MAX_BATCH_SIZE: "64"
      PPE_ENABLE_VLM: "0"
      PPE_ENABLE_FACE_RECOGNITION: "0"
      # Replace <HASH> with actual snapshot hash from Phase 3.3
      PPE_MODEL_ID: /models/hf/hub/models--Hexmon--vyra-yolo-ppe-detection/snapshots/<HASH>/best.pt
      TRACKER_FRAME_RATE: "10"
      VLM_COOLDOWN_SECONDS: "15"
      SNAPSHOT_DIR: /tmp/vyra_snapshots
    volumes: *worker-volumes
    networks: [vyra-cv]
    deploy:
      replicas: 1
      placement:
        constraints: [node.labels.role == manager]
      resources:
        reservations:
          devices:
            - driver: nvidia
              count: 1
              capabilities: [gpu]
      restart_policy:
        condition: on-failure
        delay: 10s

  worker-objdet:
    image: 192.168.1.10:5001/vyra/cv-worker:v1
    command: ["uv", "run", "python", "-m", "worker_object_detection"]
    environment:
      <<: *worker-env
      WORKER_TYPE: object_detection
      MAX_LATENCY_MS: "300"
      MAX_BATCH_SIZE: "64"
      OBJECT_DET_ENABLE_VLM: "0"
      OBJECT_DET_ENABLE_FACE_RECOGNITION: "0"
      MODEL_ID: /models/hf/yolov8m.pt
      TRACKER_FRAME_RATE: "10"
      SNAPSHOT_DIR: /tmp/vyra_snapshots
    volumes: *worker-volumes
    networks: [vyra-cv]
    deploy:
      replicas: 1
      placement:
        constraints: [node.labels.role == manager]
      resources:
        reservations:
          devices:
            - driver: nvidia
              count: 1
              capabilities: [gpu]
      restart_policy:
        condition: on-failure
        delay: 10s

  # ═══════════════════════════════════════════════════════════════════
  # GPU Workers — Machine 2
  # ═══════════════════════════════════════════════════════════════════
  worker-vlm:
    image: 192.168.1.10:5001/vyra/cv-worker:v1
    command: ["uv", "run", "python", "-m", "worker_vlm"]
    environment:
      <<: *worker-env
      WORKER_TYPE: vlm
      MAX_LATENCY_MS: "5000"
      MAX_BATCH_SIZE: "32"
      # Replace <HASH> with actual snapshot hash from Phase 3.3
      VLM_MODEL_ID: /models/hf/hub/models--HuggingFaceTB--SmolVLM2-2.2B-Instruct/snapshots/<HASH>
      MAX_NEW_TOKENS: "256"
    volumes: *worker-volumes
    networks: [vyra-cv]
    deploy:
      replicas: 1
      placement:
        constraints: [node.labels.role == worker]
      resources:
        reservations:
          devices:
            - driver: nvidia
              count: 1
              capabilities: [gpu]
      restart_policy:
        condition: on-failure
        delay: 10s

  worker-face:
    image: 192.168.1.10:5001/vyra/cv-worker:v1
    command: ["uv", "run", "python", "-m", "worker_face_recognition"]
    environment:
      <<: *worker-env
      WORKER_TYPE: face_recognition
      MAX_LATENCY_MS: "300"
      MAX_BATCH_SIZE: "32"
      FACE_PRIMARY_MODEL: buffalo_sc
      FACE_SECONDARY_MODEL: buffalo_l
      FACE_MATCH_STRONG_THRESHOLD: "0.52"
      FACE_MATCH_WEAK_THRESHOLD: "0.42"
    volumes: *worker-volumes
    networks: [vyra-cv]
    deploy:
      replicas: 1
      placement:
        constraints: [node.labels.role == worker]
      resources:
        reservations:
          devices:
            - driver: nvidia
              count: 1
              capabilities: [gpu]
      restart_policy:
        condition: on-failure
        delay: 10s

  # ═══════════════════════════════════════════════════════════════════
  # LLM Worker (optional — Ask AI feature)
  # ═══════════════════════════════════════════════════════════════════
  # Uncomment to enable:
  # llm-worker:
  #   image: 192.168.1.10:5001/vyra/llm-worker:v1
  #   environment:
  #     LLM_MODEL_ID: google/gemma-3-1b-it
  #     LLM_WORKER_PORT: "5003"
  #     MAX_ANSWER_TOKENS: "220"
  #     LLM_TEMPERATURE: "0.2"
  #   ports:
  #     - "5003:5003"
  #   networks: [vyra-core]
  #   deploy:
  #     replicas: 1
  #     placement:
  #       constraints: [node.labels.role == worker]
  #     resources:
  #       reservations:
  #         devices:
  #           - driver: nvidia
  #             count: 1
  #             capabilities: [gpu]
  #     restart_policy:
  #       condition: on-failure

networks:
  vyra-core:
    external: true
  vyra-cv:
    external: true

volumes:
  vyra_relay_data:
    external: true
  vyra_models:
    external: true
```

### 8.3 Deploy

```bash
cd /opt/vyra/deploy

# Source environment variables
set -a; source .env; set +a

# Deploy the stack
docker stack deploy \
  --compose-file docker-compose.swarm.yml \
  --with-registry-auth \
  vyra

# Monitor deployment progress
watch docker stack ps vyra
```

### 8.4 Wait for Services

All services should reach `Running` state within 1-2 minutes (GPU workers may take longer on first start as they load models from the mounted volume).

```bash
# Check all services
docker service ls

# Expected output (all replicas matching):
# vyra_coturn        1/1
# vyra_relay         1/1
# vyra_app           2/2
# vyra_engine        1/1
# vyra_adapter-ppe   1/1
# vyra_adapter-objdet 1/1
# vyra_adapter-vlm   1/1
# vyra_adapter-face  1/1
# vyra_worker-ppe    1/1
# vyra_worker-objdet 1/1
# vyra_worker-vlm    1/1
# vyra_worker-face   1/1
```

---

## 9. Phase 7: Fault Tolerance

### 9.1 keepalived — Floating VIP

Install keepalived on both machines and configure for automatic VIP failover.

**Machine 1 — `/etc/keepalived/keepalived.conf`:**

```
vrrp_script chk_relay {
    script "/usr/bin/curl -fsS http://127.0.0.1:8020/flags"
    interval 5
    weight -20
    fall 3
    rise 2
}

vrrp_instance VYRA_VIP {
    state MASTER
    interface eth0          # ← Replace with actual NIC name (ip addr show)
    virtual_router_id 51
    priority 100
    advert_int 1

    authentication {
        auth_type PASS
        auth_pass vyraHA
    }

    virtual_ipaddress {
        192.168.1.5/24
    }

    track_script {
        chk_relay
    }
}
```

**Machine 2 — `/etc/keepalived/keepalived.conf`:**

```
vrrp_script chk_relay {
    script "/usr/bin/curl -fsS http://127.0.0.1:8020/flags"
    interval 5
    weight -20
    fall 3
    rise 2
}

vrrp_instance VYRA_VIP {
    state BACKUP
    interface eth0          # ← Replace with actual NIC name
    virtual_router_id 51
    priority 90
    advert_int 1

    authentication {
        auth_type PASS
        auth_pass vyraHA
    }

    virtual_ipaddress {
        192.168.1.5/24
    }

    track_script {
        chk_relay
    }
}
```

**Start keepalived on both machines:**

```bash
sudo systemctl enable keepalived
sudo systemctl start keepalived

# Verify VIP on Machine 1
ip addr show eth0 | grep 192.168.1.5
```

### 9.2 Failure Scenarios

#### Scenario A: Machine 2 (Worker) Dies

**Impact:**
- worker-vlm and worker-face go offline
- VLM enrichment and face recognition stop
- PPE detection, object detection, relay, frontend all continue on Machine 1

**Automatic behavior:**
- Adapter timeouts: `IN_FLIGHT_TIMEOUT_MS` causes jobs to time out gracefully
- Adapters log "no worker available" and enter backoff
- Relay continues processing PPE/ObjDet events normally

**When Machine 2 returns:**
- Swarm automatically reschedules worker-vlm and worker-face
- Workers reconnect to engine within seconds
- Full functionality restored

**If Machine 2 is permanently lost:**
```bash
# Move VLM + Face workers to Machine 1
docker service update --constraint-rm 'node.labels.role==worker' \
  --constraint-add 'node.labels.gpu==true' vyra_worker-vlm
docker service update --constraint-rm 'node.labels.role==worker' \
  --constraint-add 'node.labels.gpu==true' vyra_worker-face
```

#### Scenario B: Machine 1 (Manager) Dies

**Impact:**
- All core services (relay, engine, adapters) stop
- All detection processing stops
- keepalived VIP migrates to Machine 2

**Recovery procedure (on Machine 2):**

```bash
# 1. Force new single-manager Swarm
docker swarm init --force-new-cluster --advertise-addr 192.168.1.20

# 2. If Machine 1's NFS is down, use the local NFS mirror:
#    The NFS mount at /data/vyra/relay has a recent copy
docker volume rm vyra_relay_data 2>/dev/null
docker volume create --driver local \
  --opt type=none \
  --opt device=/data/vyra/relay \
  --opt o=bind \
  vyra_relay_data

# 3. Re-label this node for all roles
docker node update --label-add role=manager --label-add gpu=true $(hostname)

# 4. Recreate overlay networks
docker network create --driver overlay --attachable vyra-core
docker network create --driver overlay --attachable vyra-cv

# 5. Redeploy stack (all services now on Machine 2)
cd /opt/vyra/deploy
set -a; source .env; set +a
docker stack deploy --compose-file docker-compose.swarm.yml vyra
```

### 9.3 SQLite Database Backup

Set up nightly backup cron on Machine 1:

```bash
cat > /etc/cron.d/vyra-backup << 'EOF'
# Nightly SQLite backup at 2 AM (online-safe, no locking)
0 2 * * * root sqlite3 /data/vyra/relay/database/relay.db ".backup '/data/vyra/relay/database/relay.db.bak.$(date +\%Y\%m\%d)'"

# Keep 7 days of backups
5 2 * * * root find /data/vyra/relay/database/ -name "relay.db.bak.*" -mtime +7 -delete
EOF
```

The SQLite `.backup` command is online-safe — it creates a consistent copy without blocking active connections.

---

## 10. Phase 8: Verification

### 10.1 Service Health

```bash
# All services running
docker stack ps vyra --no-trunc | grep -v Shutdown

# Relay health
curl -s http://192.168.1.5:8020/flags | python3 -m json.tool

# Engine version
curl -s http://192.168.1.5:5000/version

# Service logs (check for errors)
docker service logs vyra_relay --tail 30
docker service logs vyra_engine --tail 30
```

### 10.2 GPU Utilization

```bash
# Machine 1 — should show worker-ppe and worker-objdet
nvidia-smi

# Machine 2 — should show worker-vlm and worker-face
ssh machine2 nvidia-smi
```

### 10.3 Model Loading

```bash
# Check PPE worker loaded model
docker service logs vyra_worker-ppe 2>&1 | grep -iE "model|yolo|loaded|error" | tail -10

# Check VLM worker loaded model
docker service logs vyra_worker-vlm 2>&1 | grep -iE "smolvlm|loaded|error|model" | tail -10

# Check face recognition worker
docker service logs vyra_worker-face 2>&1 | grep -iE "buffalo|insightface|loaded|error" | tail -10

# Check object detection worker
docker service logs vyra_worker-objdet 2>&1 | grep -iE "yolov8|loaded|error" | tail -10
```

### 10.4 WebRTC Streaming

1. Open browser at `http://192.168.1.5` (the VIP)
2. Register a user account
3. Navigate to **Control → Cameras → Add Camera**
4. Enter camera details (RTSP type, IP, port, path)
5. Create a **Managed Node** and assign the camera
6. Start the node
7. Navigate to **Command Center** (Bird's Eye)
8. Verify video tile shows live stream
9. Check browser DevTools Console — should NOT show STUN errors
10. Verify ICE connection state reaches `connected`

### 10.5 AI Pipeline

1. With a camera streaming, navigate to **Control → Agents**
2. Start the **PPE Detection** agent (this starts engine, worker-ppe, adapter-ppe)
3. Navigate to **Command Center** — verify bounding boxes appear on detected persons/PPE
4. Navigate to **Investigate → Moments** — verify events appear with severity badges
5. Click a snapshot thumbnail — verify image viewer opens with zoom

### 10.6 Failover Test

```bash
# Drain Machine 2 to simulate failure
docker node update --availability drain <machine2-hostname>

# Wait 30 seconds, verify services
docker service ls
# worker-vlm and worker-face should show 0/1

# Verify PPE/ObjDet still working
# Check relay logs for graceful degradation
docker service logs vyra_relay --tail 20

# Restore Machine 2
docker node update --availability active <machine2-hostname>

# Verify workers restart
watch docker service ps vyra_worker-vlm
# Should show Running within 30s
```

### 10.7 VIP Failover Test

```bash
# On Machine 1:
sudo systemctl stop keepalived

# On Machine 2, verify VIP migrated:
ip addr show eth0 | grep 192.168.1.5
# Should show the VIP

# Browser at http://192.168.1.5 should still work
# (if relay is on Machine 1 and only keepalived was stopped,
#  the VIP moves but relay is still reachable via Machine 1's real IP)

# Restore:
sudo systemctl start keepalived
# VIP returns to Machine 1 (higher priority)
```

---

## 11. Operational Notes

### 11.1 ProcessManager is Disabled in Docker

The relay's `ProcessManager` normally spawns engine, workers, and nodes as OS child processes. In Docker mode, `VYRA_ROOT` is intentionally unset, so `ProcessManager` returns a no-op instance. All processes are managed as Swarm services instead.

This means the **Control → Agents → Start/Stop** buttons in the UI won't work for spawning processes. Workers are managed via `docker service scale vyra_worker-ppe=N` instead.

### 11.2 Frontend Image is Environment-Specific

`VITE_RELAY_API_URL` is compiled into the static JavaScript bundle at `docker build` time. Changing the relay's IP/VIP requires rebuilding `vyra/app:v1`.

**Mitigation:** Use a DNS hostname (e.g., `vyra.local`) resolved via `/etc/hosts` on all machines. Then the app can be built once with `VITE_RELAY_API_URL=http://vyra.local:8020`.

### 11.3 Scaling Workers

To add more worker replicas (both machines have VRAM headroom):

```bash
# Scale PPE workers to 2 replicas
docker service scale vyra_worker-ppe=2

# Scale VLM workers to 2 replicas
docker service scale vyra_worker-vlm=2
```

Workers with the same `WORKER_TYPE` automatically form a pool in the engine with round-robin routing.

### 11.4 Updating Images

When deploying a new version:

1. Build new images on internet machine with tag `v2`
2. Export and transfer to airgapped machines
3. Load and push to local registry
4. Update the compose file image tags
5. `docker stack deploy --compose-file docker-compose.swarm.yml vyra` (rolling update)

### 11.5 Log Management

Swarm services log to Docker's logging driver. To prevent disk filling:

```bash
# /etc/docker/daemon.json — add log rotation
{
  "log-driver": "json-file",
  "log-opts": {
    "max-size": "50m",
    "max-file": "3"
  }
}
```

### 11.6 Monitoring

Basic monitoring without internet-dependent tools:

```bash
# Watch all service health in real-time
watch -n 5 'docker service ls && echo "---" && nvidia-smi --query-gpu=utilization.gpu,memory.used,memory.total --format=csv,noheader'

# Check relay event throughput
docker service logs vyra_relay --since 1m 2>&1 | grep -c "event"
```

---

## 12. Troubleshooting

### Worker won't start — "No GPU found"

```bash
# Verify NVIDIA runtime is configured
docker info | grep -i runtime
# Should show: Runtimes: nvidia runc

# Test GPU access directly
docker run --rm --gpus all nvidia/cuda:12.8.0-base-ubuntu22.04 nvidia-smi
```

### WebRTC streams show blank/black tile

1. Check if coturn is running: `docker service logs vyra_coturn`
2. Check browser console for ICE errors
3. On LAN, WebRTC should work even without STUN — try setting `VITE_STUN_URL=""` and rebuilding app
4. Verify camera is reachable from Machine 1: `ffprobe rtsp://camera-ip/stream`

### Workers can't load models — "Model not found"

```bash
# Verify models volume is mounted
docker exec $(docker ps -q -f name=vyra_worker-ppe) ls -la /models/hf/

# Check exact model path
docker exec $(docker ps -q -f name=vyra_worker-ppe) find /models -name "best.pt"

# Update PPE_MODEL_ID in compose to match the actual path
```

### Relay crashes with "database is locked"

SQLite WAL mode supports concurrent reads but single writer. If relay crashes during write:
1. Check for stale WAL files: `ls -la /data/vyra/relay/database/`
2. Restart relay: `docker service update --force vyra_relay`
3. If persistent: increase `PRAGMA busy_timeout` (default 5000ms)

### NFS mount failed on Machine 2

```bash
# Check NFS server status on Machine 1
sudo systemctl status nfs-kernel-server
sudo exportfs -v

# Test from Machine 2
showmount -e 192.168.1.10
sudo mount -t nfs4 192.168.1.10:/data/vyra/relay /data/vyra/relay
```

### Swarm services stuck in "Pending"

```bash
# Check why service isn't scheduled
docker service ps vyra_worker-vlm --no-trunc
# Look for "no suitable node" errors

# Common causes:
# - Node labels missing: docker node update --label-add role=worker <node>
# - GPU reservation but no nvidia runtime: check daemon.json
# - Image not available: push to local registry
```

### High memory usage

```bash
# Check per-service memory
docker stats --no-stream

# If cv-worker uses too much memory, reduce batch size:
# MAX_BATCH_SIZE: "16"  (instead of 64)
```

---

*Generated from codebase analysis, February 2026.*
