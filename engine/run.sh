# #!/usr/bin/env bash

# # --- Configuration ---
# # Set the session name
# SESSION="engine"


# # Define the full path to your project's root directory
# # This makes the script runnable from anywhere
# PROJECT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# UV_ENV_FILE="$PROJECT_DIR/.env"

# # --- Script Body ---
# # Kill any existing tmux session with the same name
# tmux kill-session -t "$SESSION" 2>/dev/null

# # Create a new, detached tmux session with the first window named "distributor"
# tmux new-session -d -s "$SESSION" -n distributor -c "$PROJECT_DIR"

# # Send the command to start the distributor service
# # Using an environment variable for clarity
# DISTRIBUTOR_CMD="bun run index.ts"
# tmux send-keys -t "$SESSION:distributor" "$DISTRIBUTOR_CMD" C-m

# # --- Worker Panes ---

# # Create and run the embedding worker
# tmux new-window -t "$SESSION" -n worker_embedding 
# EMBEDDING_CMD="WORKER_TYPE=\"embedding\" MAX_LATENCY_MS=\"5000\" WORKER_SECRET=\"93471afb3ce8e3d5e07eb07bb71ff98d\" uv run --env-file \"$UV_ENV_FILE\" python -m worker_embedding"
# tmux send-keys -t "$SESSION:worker_embedding" "cd \"$PROJECT_DIR/py\" && $EMBEDDING_CMD" C-m


# # Create and run the general VLM worker
# tmux new-window -t "$SESSION" -n worker_vlm 
# VLM_CMD="WORKER_TYPE=\"vlm\" MAX_LATENCY_MS=\"5000\" MAX_BATCH_SIZE=\"32\" WORKER_SECRET=\"93471afb3ce8e3d5e07eb07bb71ff98d\" uv run --env-file \"$UV_ENV_FILE\" python -m worker_vlm"
# tmux send-keys -t "$SESSION:worker_vlm" "cd \"$PROJECT_DIR/py\" && $VLM_CMD" C-m

# # Create and run the caption worker
# tmux new-window -t "$SESSION" -n worker_caption_moondream
# CAPTION_CMD="WORKER_TYPE=\"caption\" MAX_LATENCY_MS=\"5000\" MAX_BATCH_SIZE=\"1\" WORKER_SECRET=\"93471afb3ce8e3d5e07eb07bb71ff98d\" uv run --env-file \"$UV_ENV_FILE\" python -m worker_caption_moondream"
# tmux send-keys -t "$SESSION:worker_caption_moondream" "cd \"$PROJECT_DIR/py\" && $CAPTION_CMD" C-m


# # Create and run the fast embedding worker
# tmux new-window -t "$SESSION" -n worker_fast_embedding 
# FAST_EMBEDDING_CMD="WORKER_TYPE=\"fast_embedding\" MAX_LATENCY_MS=\"200\" WORKER_SECRET=\"93471afb3ce8e3d5e07eb07bb71ff98d\" uv run --env-file \"$UV_ENV_FILE\" python -m worker_embedding"
# tmux send-keys -t "$SESSION:worker_fast_embedding" "cd \"$PROJECT_DIR/py\" && $FAST_EMBEDDING_CMD" C-m

# # # Create and run the object detection worker
# # tmux new-window -t "$SESSION" -n worker_object_detection 
# # OBJECT_DETECTION_CMD="WORKER_TYPE=\"object_detection\" MAX_LATENCY_MS=\"300\" MAX_BATCH_SIZE=\"64\" uv run --env-file \"$UV_ENV_FILE\" python -m worker_object_detection"
# # tmux send-keys -t "$SESSION:worker_object_detection" "cd \"$PROJECT_DIR/py\" && $OBJECT_DETECTION_CMD" C-m


# # Create and run the llm fast worker
# tmux new-window -t "$SESSION" -n worker_llm_fast
# LLM_FAST_CMD="WORKER_TYPE=\"llm_fast\" MAX_LATENCY_MS=\"200\" WORKER_SECRET=\"93471afb3ce8e3d5e07eb07bb71ff98d\" uv run --env-file \"$UV_ENV_FILE\" python -m worker_llm"
# tmux send-keys -t "$SESSION:worker_llm_fast" "cd \"$PROJECT_DIR/py\" && $LLM_FAST_CMD" C-m

# # 
# tmux new-window -t "$SESSION" -n worker_motion_energy
# MOTION_ENERGY_CMD="WORKER_TYPE=\"motion_energy\" MAX_LATENCY_MS=\"300\" WORKER_SECRET=\"93471afb3ce8e3d5e07eb07bb71ff98d\" uv run --env-file \"$UV_ENV_FILE\" python -m worker_motion_energy"
# tmux send-keys -t "$SESSION:worker_motion_energy" "cd \"$PROJECT_DIR/py\" && $MOTION_ENERGY_CMD" C-m

# # # Create and run the video segmentation worker (SAM3)
# # tmux new-window -t "$SESSION" -n worker_segmentation
# # SEGMENTATION_CMD="WORKER_TYPE=\"segmentation\" MAX_LATENCY_MS=\"500\" uv run --env-file \"$UV_ENV_FILE\" python -m worker_sam3"
# # tmux send-keys -t "$SESSION:worker_segmentation" "cd \"$PROJECT_DIR/py\" && $SEGMENTATION_CMD" C-m

# # Create and run the image segmentation worker (SAM3 - simplified)
# tmux new-window -t "$SESSION" -n worker_segmentation
# SEGMENTATION_CMD="WORKER_TYPE=\"segmentation\" MAX_LATENCY_MS=\"500\" WORKER_SECRET=\"93471afb3ce8e3d5e07eb07bb71ff98d\"  uv run --env-file \"$UV_ENV_FILE\" python -m worker_segmentation_image"
# tmux send-keys -t "$SESSION:worker_segmentation" "cd \"$PROJECT_DIR/py\" && $SEGMENTATION_CMD" C-m

# # # 
# # tmux new-window -t "$SESSION" -n worker_llm
# # LLM_CMD="WORKER_TYPE=\"llm\" MAX_LATENCY_MS=\"5000\" MAX_BATCH_SIZE=\"4\" MODEL_ID=\"microsoft/Phi-4-mini-instruct\" uv run --env-file \"$UV_ENV_FILE\" python -m worker_llm"
# # tmux send-keys -t "$SESSION:worker_llm" "cd \"$PROJECT_DIR/py\" && $LLM_CMD" C-m

# # --- Finalization ---
# # Select the 'distributor' window by default
# tmux select-window -t "$SESSION:distributor"

# # Attach to the newly created tmux session
# echo "Attaching to tmux session '$SESSION'. To detach, press Ctrl+B then D."
# tmux attach -t "$SESSION"

#!/usr/bin/env bash
set -euo pipefail

SESSION="engine"
SECRET="93471afb3ce8e3d5e07eb07bb71ff98d"
PROJECT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
UV_ENV_FILE="$PROJECT_DIR/.env"
LOG_DIR="$PROJECT_DIR/logs"
mkdir -p "$LOG_DIR"

tmux kill-session -t "$SESSION" 2>/dev/null || true

# distributor
tmux new-session -d -s "$SESSION" -n distributor -c "$PROJECT_DIR" \
  "bash -lc 'bun run index.ts |& tee \"$LOG_DIR/distributor.log\"'"

start_py_worker () {
  local WIN="$1"
  local CMD="$2"

  tmux new-window -d -t "$SESSION" -n "$WIN" -c "$PROJECT_DIR/py" \
    "bash -lc 'set -a; source \"$UV_ENV_FILE\"; set +a; $CMD |& tee \"$LOG_DIR/$WIN.log\"'"
}

SECRET="93471afb3ce8e3d5e07eb07bb71ff98d"

start_py_worker "worker_embedding" \
  "WORKER_TYPE=embedding MAX_LATENCY_MS=5000 WORKER_SECRET=$SECRET uv run python -m worker_embedding"

start_py_worker "worker_vlm" \
  "WORKER_TYPE=vlm MAX_LATENCY_MS=5000 MAX_BATCH_SIZE=32 WORKER_SECRET=$SECRET uv run python -m worker_vlm"

start_py_worker "worker_caption_moondream" \
  "WORKER_TYPE=caption MAX_LATENCY_MS=5000 MAX_BATCH_SIZE=1 WORKER_SECRET=$SECRET uv run python -m worker_caption_moondream"

start_py_worker "worker_fast_embedding" \
  "WORKER_TYPE=fast_embedding MAX_LATENCY_MS=200 WORKER_SECRET=$SECRET uv run python -m worker_embedding"

start_py_worker "worker_object_detection" \
  "WORKER_TYPE=object_detection MAX_LATENCY_MS=300 MAX_BATCH_SIZE=64 WORKER_SECRET=$SECRET uv run python -m worker_object_detection"

start_py_worker "worker_llm_fast" \
  "WORKER_TYPE=llm_fast MAX_LATENCY_MS=200 WORKER_SECRET=$SECRET uv run python -m worker_llm"

start_py_worker "worker_motion_energy" \
  "WORKER_TYPE=motion_energy MAX_LATENCY_MS=300 WORKER_SECRET=$SECRET uv run python -m worker_motion_energy"

start_py_worker "worker_segmentation" \
  "WORKER_TYPE=segmentation MAX_LATENCY_MS=500 WORKER_SECRET=$SECRET uv run python -m worker_segmentation_image"

tmux select-window -t "$SESSION:distributor"
echo "Attaching to tmux session '$SESSION'. To detach: Ctrl+B then D."
tmux attach -t "$SESSION"
