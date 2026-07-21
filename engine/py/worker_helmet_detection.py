import asyncio
import os
import shutil
import time
from typing import Any, Dict, List, Optional, Tuple

import torch
from PIL import Image

from ws_client_handler import client_handler

# Multi-object tracking (ByteTrack) for persistent IDs across frames
try:
    from tracker import TrackerManager
    TRACKING_AVAILABLE = True
except Exception as e:
    print(f"[helmet_detection] Tracker not available: {e}")
    TRACKING_AVAILABLE = False

# Try to import VLM helper for violation analysis
try:
    from vlm_helper import VLMHelper
    VLM_AVAILABLE = True
except Exception as e:
    print(f"[helmet_detection] VLM helper not available: {e}")
    VLM_AVAILABLE = False


"""Helmet / PPE Detection Worker (YOLOv8)

Model: keremberke/yolov8m-protective-equipment-detection

Supported labels (model classes):
['glove', 'goggles', 'helmet', 'mask', 'no_glove', 'no_goggles', 'no_helmet', 'no_mask', 'no_shoes', 'shoes']

Input (from engine):
{
  "inputs": [
    {"id": "frame_1", "filepath": "/abs/path/to/frame1.jpg"},
    {"id": "frame_2", "filepath": "/abs/path/to/frame2.jpg"}
  ]
}

Output:
{
  "output": [
    {
      "id": "frame_1",
      "image_width": 1920,
      "image_height": 1080,
      "detections": [
        {
          "label": "no_helmet",
          "confidence": 0.91,
          "box": {"x_min": 0.12, "y_min": 0.08, "x_max": 0.33, "y_max": 0.74},
          "box_px": {"x_min": 230.1, "y_min": 84.6, "x_max": 633.2, "y_max": 800.4},
          "class_id": 6
        }
      ]
    }
  ]
}
"""


def _env_float(name: str, default: float) -> float:
    v = os.getenv(name)
    if v is None or v == "":
        return default
    try:
        return float(v)
    except Exception:
        return default


def _env_int(name: str, default: int) -> int:
    v = os.getenv(name)
    if v is None or v == "":
        return default
    try:
        return int(v)
    except Exception:
        return default


def _safe_open_image(path: str) -> Optional[Tuple[Image.Image, int, int]]:
    try:
        img = Image.open(path).convert("RGB")
        w, h = img.size
        return img, w, h
    except Exception:
        return None


def _patch_ultralytics_torch_safe_load() -> None:
    """
    Fix for PyTorch 2.6+ where torch.load defaults to weights_only=True,
    which breaks loading Ultralytics .pt checkpoints from HuggingFace.
    We force weights_only=False for trusted model checkpoints.
    """
    try:
        # ultralytics path (primary)
        import ultralytics.nn.tasks as ul_tasks  # type: ignore

        def _torch_safe_load_patched(file, *args, **kwargs):
            return torch.load(file, map_location="cpu", weights_only=False)

        if hasattr(ul_tasks, "torch_safe_load"):
            ul_tasks.torch_safe_load = _torch_safe_load_patched  # type: ignore
            print("[helmet_detection] Patched ultralytics.nn.tasks.torch_safe_load(weights_only=False)")
        else:
            print("[helmet_detection] WARNING: ultralytics.nn.tasks.torch_safe_load not found")
    except Exception as e:
        print(f"[helmet_detection] WARNING: could not patch torch_safe_load: {e}")


def _sanitize_cuda_env() -> None:
    """
    Fix cases where CUDA_VISIBLE_DEVICES is incorrectly set to 'cuda' (string),
    causing torch to report cuda available but 0 devices.
    """
    cuda_vis = os.environ.get("CUDA_VISIBLE_DEVICES")
    if cuda_vis and cuda_vis.strip().lower() == "cuda":
        print("[helmet_detection] WARNING: CUDA_VISIBLE_DEVICES='cuda' is invalid. Unsetting it.")
        os.environ.pop("CUDA_VISIBLE_DEVICES", None)


def _pick_ultralytics_device() -> str:
    """
    Ultralytics expects:
      - '0', '1', '0,1' for CUDA devices
      - 'cpu' for CPU
    NOT 'cuda'
    """
    _sanitize_cuda_env()
    try:
        if torch.cuda.is_available() and torch.cuda.device_count() > 0:
            return "0"
        return "cpu"
    except Exception:
        return "cpu"


def load_ai_model():
    """Load PPE YOLOv8 model (HF) and return the worker function."""

    # Prefer ultralyticsplus (recommended by model author). If missing, keep worker online and emit per-job errors.
    try:
        from ultralyticsplus import YOLO  # type: ignore
    except Exception as e:
        install_hint = "pip install ultralyticsplus==0.0.24 ultralytics==8.0.23"
        msg = f"Missing dependency: ultralyticsplus. Install inside engine/py env. Example: {install_hint}. Root cause: {e}"
        print(f"[helmet_detection] {msg}")

        def worker_function(data: Dict[str, Any]):
            inputs_list = data.get("inputs", []) or []
            out = []
            for inp in inputs_list:
                input_id = str(inp.get("id", "unknown"))
                out.append({"id": input_id, "image_width": 0, "image_height": 0, "detections": [], "error": msg})
            return {"output": out}

        return worker_function

    # Must patch BEFORE YOLO(model_id) to fix PyTorch 2.6+ weights_only issue
    _patch_ultralytics_torch_safe_load()

    device = _pick_ultralytics_device()

    # Startup GPU sanity prints (so you always know what this process sees)
    print(
        f"[helmet_detection] torch={torch.__version__} "
        f"cuda_avail={torch.cuda.is_available()} device_count={torch.cuda.device_count()} "
        f"CUDA_VISIBLE_DEVICES={os.environ.get('CUDA_VISIBLE_DEVICES')}"
    )
    if torch.cuda.is_available() and torch.cuda.device_count() > 0:
        try:
            print(f"[helmet_detection] GPU0={torch.cuda.get_device_name(0)}")
        except Exception:
            pass

    model_id = os.getenv(
        "HELMET_MODEL_ID",
        "keremberke/yolov8m-protective-equipment-detection",
    )

    # Defaults match the model card example.
    conf = _env_float("HELMET_CONF", 0.25)
    iou = _env_float("HELMET_IOU", 0.45)
    max_det = _env_int("HELMET_MAX_DET", 1000)
    agnostic_nms = os.getenv("HELMET_AGNOSTIC_NMS", "false").lower() in ("1", "true", "yes")

    print(f"[helmet_detection] Loading model: {model_id} on device={device}...")
    model = YOLO(model_id)

    # Force device for all predict() calls
    model.overrides["conf"] = conf
    model.overrides["iou"] = iou
    model.overrides["agnostic_nms"] = agnostic_nms
    model.overrides["max_det"] = max_det
    model.overrides["device"] = device

    # Extra safety: try moving weights to device at load time
    try:
        model.to(device)
    except Exception:
        pass

    def worker_function(data: Dict[str, Any]):
        t0 = time.time()
        inputs_raw = data.get("inputs", []) or []

        # Flatten nested structure and extract job_ids
        inputs_list: List[Dict[str, Any]] = []
        for it in inputs_raw:
            if isinstance(it, dict) and isinstance(it.get("inputs"), list):
                # Nested: { id: job_id, inputs: [{ id: frame_id, filepath: ... }] }
                job_id = it.get("id")
                for inner in it.get("inputs", []):
                    if isinstance(inner, dict):
                        frame = dict(inner)
                        frame["_job_id"] = job_id
                        inputs_list.append(frame)
            elif isinstance(it, dict):
                # Flat: { id: frame_id, filepath: ... }
                inputs_list.append(it)

        print(f"[helmet_detection] Received batch: {len(inputs_list)}")

        ids: List[str] = []
        paths: List[str] = []
        sizes: List[Tuple[int, int]] = []
        job_ids: List[Optional[str]] = []

        for inp in inputs_list:
            input_id = str(inp.get("id", "unknown"))
            filepath = str(inp.get("filepath", ""))
            job_id = inp.get("_job_id")

            job_ids.append(job_id)

            if not filepath:
                print(f"[helmet_detection] Missing filepath for {input_id}; returning empty")
                ids.append(input_id)
                paths.append("")
                sizes.append((0, 0))
                continue

            opened = _safe_open_image(filepath)
            if not opened:
                print(f"[helmet_detection] Failed to open image for {input_id}: {filepath}; returning empty")
                ids.append(input_id)
                paths.append("")
                sizes.append((0, 0))
                continue

            _, w, h = opened
            ids.append(input_id)
            paths.append(filepath)
            sizes.append((w, h))

        valid_idx = [i for i, p in enumerate(paths) if p]
        valid_paths = [paths[i] for i in valid_idx]

        results = []
        if valid_paths:
            try:
                # Keep device authority in overrides; don't pass device twice.
                results = model.predict(valid_paths, verbose=False)
            except Exception as e:
                print(f"[helmet_detection] Inference failed: {e}")
                out_err = []
                for input_id, (w, h) in zip(ids, sizes):
                    out_err.append(
                        {"id": input_id, "image_width": w, "image_height": h, "detections": [], "error": str(e)}
                    )
                return {"output": out_err}

        res_map = {valid_idx[i]: results[i] for i in range(len(valid_idx))} if results else {}

        out: List[Dict[str, Any]] = []
        for i, input_id in enumerate(ids):
            w, h = sizes[i]
            dets: List[Dict[str, Any]] = []

            r = res_map.get(i)
            if r is not None and getattr(r, "boxes", None) is not None and len(r.boxes) > 0:
                names = (
                    getattr(r, "names", None)
                    or getattr(getattr(model, "model", None), "names", None)
                    or getattr(model, "names", {})
                    or {}
                )

                # r.boxes is iterable of Box objects
                for b in r.boxes:
                    try:
                        xyxy = b.xyxy[0].tolist()
                        x_min, y_min, x_max, y_max = [float(v) for v in xyxy]
                        conf_v = float(b.conf[0].item()) if hasattr(b, "conf") else float(getattr(b, "conf", 0.0))
                        cls_id = int(b.cls[0].item()) if hasattr(b, "cls") else int(getattr(b, "cls", -1))
                        label = str(names.get(cls_id, cls_id))

                        nx_min = x_min / w if w else 0.0
                        ny_min = y_min / h if h else 0.0
                        nx_max = x_max / w if w else 0.0
                        ny_max = y_max / h if h else 0.0

                        dets.append(
                            {
                                "label": label,
                                "confidence": round(conf_v, 3),
                                "class_id": cls_id,
                                "box_px": {
                                    "x_min": round(x_min, 2),
                                    "y_min": round(y_min, 2),
                                    "x_max": round(x_max, 2),
                                    "y_max": round(y_max, 2),
                                },
                                "box": {
                                    "x_min": round(nx_min, 6),
                                    "y_min": round(ny_min, 6),
                                    "x_max": round(nx_max, 6),
                                    "y_max": round(ny_max, 6),
                                },
                            }
                        )
                    except Exception:
                        continue

            out.append({"id": input_id, "image_width": w, "image_height": h, "detections": dets})

        # Run ByteTrack per-stream for persistent person IDs
        if TRACKING_AVAILABLE:
            media_id = data.get("media_id", "default")
            frame_rate = int(os.getenv("TRACKER_FRAME_RATE", "5"))
            tracker = TrackerManager.get(media_id, frame_rate=frame_rate)
            for item in out:
                if item.get("detections"):
                    item["detections"] = tracker.update(item["detections"])
            tracker.cleanup_stale()

        # Save snapshots for violations (works even without VLM)
        snapshot_dir = os.getenv("SNAPSHOT_DIR", "/tmp/snapshots")
        os.makedirs(snapshot_dir, exist_ok=True)

        for idx, item in enumerate(out):
            detections = item.get("detections", [])
            violations = [d for d in detections if str(d.get("label", "")).startswith("no_")]
            compliant = [d for d in detections if not str(d.get("label", "")).startswith("no_")]

            # Add detection counts to every frame
            item["violation_count"] = len(violations)
            item["violation_labels"] = [v.get("label") for v in violations if v.get("label")]
            item["helmet_count"] = len([d for d in compliant if d.get("label") == "helmet"])
            item["total_detections"] = len(detections)

            # Save snapshot only when violations are found
            if violations:
                frame_id = item.get("id")
                frame_path = paths[idx] if idx < len(paths) else ""

                if frame_path and os.path.exists(frame_path):
                    snapshot_filename = f"{frame_id}_violation.jpg"
                    snapshot_path = os.path.join(snapshot_dir, snapshot_filename)
                    shutil.copy2(frame_path, snapshot_path)
                    item["snapshot_path"] = snapshot_path

                    print(f"[helmet_detection] Violation snapshot saved: {snapshot_filename} "
                          f"(violations={len(violations)}, labels={item['violation_labels']})")

        dt = (time.time() - t0) * 1000
        print(f"[helmet_detection] Done. batch={len(inputs_list)} time_ms={dt:.1f}")

        # Group outputs by job_id if present
        job_id_map: Dict[str, List[Dict[str, Any]]] = {}
        for idx, out_item in enumerate(out):
            jid = job_ids[idx] if idx < len(job_ids) else None
            if jid:
                job_id_map.setdefault(jid, []).append(out_item)

        # If we have job IDs, return grouped outputs
        if job_id_map:
            grouped_outputs = [{"id": jid, "output": items} for jid, items in job_id_map.items()]
            return {"output": grouped_outputs}

        # Otherwise return flat list (backwards compatibility)
        return {"output": out}

    # Wrap with VLM integration if available
    if VLM_AVAILABLE:
        try:
            vlm = VLMHelper.get_instance()
            print("[helmet_detection] VLM integration enabled")

            def worker_function_with_vlm(data: Dict[str, Any]):
                t0 = time.time()

                # Flatten inputs to find frame paths
                inputs_raw = data.get("inputs", []) or []
                inputs_flat: List[Dict[str, Any]] = []
                for it in inputs_raw:
                    if isinstance(it, dict) and isinstance(it.get("inputs"), list):
                        inputs_flat.extend(it.get("inputs", []))
                    elif isinstance(it, dict):
                        inputs_flat.append(it)

                # First pass: run helmet detection (includes snapshot saving for violations)
                base_output = worker_function(data)

                # Second pass: add VLM analysis for violations
                output_items = base_output.get("output", [])
                all_frame_results: List[Dict[str, Any]] = []
                for out_group in output_items:
                    if isinstance(out_group, dict) and "output" in out_group:
                        all_frame_results.extend(out_group.get("output", []))
                    else:
                        if isinstance(out_group, dict):
                            all_frame_results.append(out_group)

                # Get tracker for VLM gating (reuses same tracker from worker_function)
                media_id = data.get("media_id", "default")
                vlm_cooldown = float(os.getenv("VLM_COOLDOWN_SECONDS", "15"))
                vlm_tracker = TrackerManager.get(media_id) if TRACKING_AVAILABLE else None

                for item in all_frame_results:
                    detections = item.get("detections", [])
                    violations = [d for d in detections if str(d.get("label", "")).startswith("no_")]

                    if not violations or not item.get("snapshot_path"):
                        continue

                    # Gate VLM by track state: only call for new/changed tracks
                    if vlm_tracker:
                        alerts_needed = [
                            v for v in violations
                            if vlm_tracker.should_alert(
                                v.get("track_id", -1),
                                v.get("label", "unknown"),
                                cooldown_seconds=vlm_cooldown,
                            )
                        ]
                        if not alerts_needed:
                            # Same tracks, same status, within cooldown — skip VLM
                            item.pop("snapshot_path", None)
                            continue
                    else:
                        alerts_needed = violations

                    frame_id = item.get("id")
                    frame_path = next((inp.get("filepath") for inp in inputs_flat if inp.get("id") == frame_id), None)

                    if frame_path and os.path.exists(frame_path):
                        violation_labels = [v.get("label") for v in alerts_needed if v.get("label")]
                        track_ids = [v.get("track_id", -1) for v in alerts_needed]
                        prompt = (
                            "Describe the PPE violations in this image. "
                            f"Detected: {', '.join(map(str, violation_labels))}. "
                            "Explain the safety risks and who is affected."
                        )
                        summary = vlm.analyze_image(frame_path, prompt, max_tokens=150)
                        item["vlm_summary"] = summary

                        print(f"[helmet_detection] VLM analysis for tracks={track_ids} violations={violation_labels}")

                dt = (time.time() - t0) * 1000
                print(f"[helmet_detection] Done with VLM. batch={len(inputs_flat)} time_ms={dt:.1f}")
                return base_output

            return worker_function_with_vlm
        except Exception as e:
            print(f"[helmet_detection] VLM initialization failed: {e}")
            print("[helmet_detection] Falling back to basic detection without VLM")
            return worker_function

    print("[helmet_detection] VLM not available, using basic detection only")
    return worker_function


if __name__ == "__main__":
    worker_function = load_ai_model()
    asyncio.run(client_handler(worker_function))
