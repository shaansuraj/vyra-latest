"""Unified PPE Detection Worker — Hexmon/vyra-yolo-ppe-detection

Model: hf://Hexmon/vyra-yolo-ppe-detection/best.pt (YOLOv8m, 14 classes)

Classes:
  0: Fall-Detected    1: Gloves         2: Goggles        3: Hardhat
  4: Ladder           5: Mask           6: NO-Gloves      7: NO-Goggles
  8: NO-Hardhat       9: NO-Mask       10: NO-Safety Vest 11: Person
 12: Safety Cone     13: Safety Vest

Pipeline per frame:
  1. YOLO inference → all 14 classes detected in one pass
  2. Separate Person (11) vs PPE/violation detections
  3. ByteTrack on Person boxes only → persistent track_ids per camera
  4. Associate PPE items to nearest person (center-in-expanded-box)
  5. Build per-person violation summary (which PPE is missing)
  6. Save snapshot with bboxes IMMEDIATELY on gated violation
  7. Queue VLM analysis in background thread (non-blocking)
  8. Attach background VLM result on next available frame

ENV overrides:
  PPE_MODEL_ID         — HF model path (default: hf://Hexmon/vyra-yolo-ppe-detection/best.pt)
  PPE_CONF             — detection confidence threshold (default: 0.35)
  PPE_IOU              — NMS IoU threshold (default: 0.45)
  PPE_MAX_DET          — max detections per image (default: 300)
  TRACKER_FRAME_RATE   — ByteTrack expected FPS (default: 10)
  TRACKER_LOST_BUFFER  — frames to keep lost tracks (default: 30)
  VLM_COOLDOWN_SECONDS — min seconds between VLM calls per track (default: 15)
  SNAPSHOT_DIR         — local directory for violation snapshots (default: /tmp/snapshots)
  PPE_REQUIRE_PERSON_ASSOCIATION — if true, NO-* violations must be linked to a person (default: true)
"""

import asyncio
import os
import queue as py_queue
import shutil
import threading
import time
from typing import Any, Callable, Dict, List, Optional, Set, Tuple

import numpy as np
import torch
from PIL import Image, ImageDraw

from ws_client_handler import client_handler

# ── Optional dependencies ────────────────────────────────────────────────────

try:
    from tracker import TrackerManager
    TRACKING_AVAILABLE = True
except Exception as e:
    print(f"[ppe_detection] Tracker not available: {e}")
    TRACKING_AVAILABLE = False

try:
    from vlm_helper import VLMHelper
    VLM_AVAILABLE = True
except Exception as e:
    print(f"[ppe_detection] VLM helper not available: {e}")
    VLM_AVAILABLE = False

try:
    import face_recognizer as _face_rec
    FACE_REC_AVAILABLE = True
except Exception as e:
    print(f"[ppe_detection] Face recognizer not available: {e}")
    FACE_REC_AVAILABLE = False

try:
    from person_reid_embedder import extract_embedding_from_crop as _extract_reid_embedding_from_crop
    from person_reid_embedder import extract_observation_from_crop as _extract_reid_observation_from_crop
except Exception:
    _extract_reid_embedding_from_crop = None
    _extract_reid_observation_from_crop = None

# ── Class definitions ────────────────────────────────────────────────────────

PERSON_CLASS_ID = 11
FALL_CLASS_ID = 0
NO_PPE_CLASS_IDS: Set[int] = {6, 7, 8, 9, 10}

# Classes that indicate a PPE safety violation (trigger VLM + snapshot)
VIOLATION_CLASS_IDS: Set[int] = {FALL_CLASS_ID, *NO_PPE_CLASS_IDS}
# Fall-Detected(0), NO-Gloves(6), NO-Goggles(7), NO-Hardhat(8), NO-Mask(9), NO-Safety Vest(10)

# Informational context objects — don't count as violations
CONTEXT_CLASS_IDS: Set[int] = {4, 12}  # Ladder, Safety Cone

# Canonical class names (matches model card)
CLASS_NAMES: Dict[int, str] = {
    0: "Fall-Detected", 1: "Gloves",    2: "Goggles",       3: "Hardhat",
    4: "Ladder",        5: "Mask",      6: "NO-Gloves",      7: "NO-Goggles",
    8: "NO-Hardhat",    9: "NO-Mask",  10: "NO-Safety Vest", 11: "Person",
    12: "Safety Cone", 13: "Safety Vest",
}

# ── Helpers ──────────────────────────────────────────────────────────────────

def _env_float(name: str, default: float) -> float:
    v = os.getenv(name)
    try:
        return float(v) if v else default
    except Exception:
        return default


def _env_int(name: str, default: int) -> int:
    v = os.getenv(name)
    try:
        return int(v) if v else default
    except Exception:
        return default


def _env_bool(name: str, default: bool = False) -> bool:
    raw = os.getenv(name)
    if raw is None:
        return default
    return str(raw).strip().lower() in ("1", "true", "yes", "on")


def _legacy_mode_enabled() -> bool:
    return _env_bool("LEGACY_MODE", False)


_PPE_DEBUG_LOGS = _env_bool("PPE_DEBUG_LOGS", False)


def _debug_log(msg: str) -> None:
    if _PPE_DEBUG_LOGS:
        print(msg)


def _sanitize_cuda_env() -> None:
    cuda_vis = os.environ.get("CUDA_VISIBLE_DEVICES")
    if cuda_vis and cuda_vis.strip().lower() == "cuda":
        print("[ppe_detection] WARNING: CUDA_VISIBLE_DEVICES='cuda' is invalid. Unsetting.")
        os.environ.pop("CUDA_VISIBLE_DEVICES", None)


def _pick_device() -> str:
    _sanitize_cuda_env()
    try:
        if torch.cuda.is_available() and torch.cuda.device_count() > 0:
            return "0"
        return "cpu"
    except Exception:
        return "cpu"


def _patch_ultralytics_torch_safe_load() -> None:
    """Patch torch_safe_load for ultralytics < 8.1 (PyTorch 2.6+ weights_only compat).
    ultralytics >= 8.1 handles weights_only=False natively via its own torch_load wrapper."""
    try:
        from importlib.metadata import version as pkg_version
        ul_ver = tuple(int(x) for x in pkg_version("ultralytics").split(".")[:2])
        if ul_ver >= (8, 1):
            print("[ppe_detection] Patched torch_safe_load (weights_only=False)")
            return  # natively handled by ultralytics >= 8.1

        import ultralytics.nn.tasks as ul_tasks

        def _patched(file, *args, **kwargs):
            ckpt = torch.load(file, map_location="cpu", weights_only=False)
            return ckpt, file

        if hasattr(ul_tasks, "torch_safe_load"):
            ul_tasks.torch_safe_load = _patched
            print("[ppe_detection] Patched torch_safe_load (weights_only=False)")
        else:
            print("[ppe_detection] WARNING: torch_safe_load not found in ultralytics.nn.tasks")
    except Exception as e:
        print(f"[ppe_detection] Could not patch torch_safe_load: {e}")


def _safe_open_image(path: str) -> Optional[Tuple[Image.Image, int, int]]:
    try:
        img = Image.open(path).convert("RGB")
        w, h = img.size
        return img, w, h
    except Exception:
        return None


def _appearance_embedding_from_crop(frame_path: str, box_px: Dict[str, Any]) -> List[float]:
    """Appearance embedding for cross-camera unknown ID matching.

    Primary: model-based embedding (SigLIP2 helper).
    Fallback: lightweight RGB histogram.
    """
    if _extract_reid_embedding_from_crop is not None:
        emb = _extract_reid_embedding_from_crop(frame_path, box_px)
        if emb:
            return emb

    try:
        x1 = int(float(box_px.get("x_min", 0) or 0))
        y1 = int(float(box_px.get("y_min", 0) or 0))
        x2 = int(float(box_px.get("x_max", 0) or 0))
        y2 = int(float(box_px.get("y_max", 0) or 0))
        if x2 <= x1 or y2 <= y1:
            return []
        img = Image.open(frame_path).convert("RGB")
        crop = img.crop((x1, y1, x2, y2)).resize((64, 128))
        arr = np.asarray(crop, dtype=np.float32) / 255.0
        if arr.size == 0:
            return []
        emb: List[float] = []
        for ch in range(3):
            hist, _ = np.histogram(arr[:, :, ch], bins=16, range=(0.0, 1.0), density=True)
            emb.extend(float(v) for v in hist.tolist())
        return emb
    except Exception:
        return []


def _appearance_observation_from_crop(frame_path: str, box_px: Dict[str, Any]) -> Dict[str, Any]:
    if _extract_reid_observation_from_crop is not None:
        try:
            observation = _extract_reid_observation_from_crop(frame_path, box_px)
            if isinstance(observation, dict):
                embedding = observation.get("embedding")
                return {
                    "embedding": embedding if isinstance(embedding, list) else [],
                    "embedding_kind": "body_reid",
                    "embedding_model": str(observation.get("embedding_model") or ""),
                    "quality_score": float(observation.get("quality_score") or 0.0),
                    "embedding_backend": str(observation.get("embedding_backend") or ""),
                }
        except Exception:
            pass
    embedding = _appearance_embedding_from_crop(frame_path, box_px)
    return {
        "embedding": embedding,
        "embedding_kind": "body_reid",
        "embedding_model": "",
        "quality_score": 1.0 if embedding else 0.0,
        "embedding_backend": "",
    }


def _should_request_person_identity_resolve(
    *,
    first_seen: bool,
    person: Dict[str, Any],
    now_ts: float,
    last_resolve_ts: float,
    min_interval_seconds: float,
) -> bool:
    """Decide whether to refresh relay-side person identity for this track."""
    if first_seen:
        return True
    if min_interval_seconds > 0 and (now_ts - last_resolve_ts) < min_interval_seconds:
        return False

    gid = str(person.get("global_person_id") or "").strip()
    source = str(person.get("identity_source") or "").strip().lower()
    person_id = str(person.get("person_id") or "").strip()

    if person_id:
        return False
    if gid == "" or gid.startswith("unknown:"):
        return True
    if source in ("unknown_track", "reid_new", "reid_match", ""):
        return True
    return False


def _has_known_person_identity(person: Dict[str, Any]) -> bool:
    if str(person.get("person_id") or "").strip():
        return True
    gid = str(person.get("global_person_id") or "").strip()
    if gid.startswith("person:"):
        return True
    name = str(person.get("person_name") or "").strip()
    return _is_known_person_name(name)


_UNKNOWN_IDENTITY_SOURCES = {"", "unknown_track", "reid_new", "reid_match"}


def _is_unknown_global_person_id(gid: str) -> bool:
    return gid.startswith("unknown:")


def _is_known_person_name(name: str) -> bool:
    name = name.strip()
    return bool(name and not name.lower().startswith("unknown"))


def _promote_known_person_identity(person: Dict[str, Any]) -> bool:
    """Make enrolled face identity the canonical global identity for this detection."""
    person_id = str(person.get("person_id") or "").strip()
    if not person_id:
        return False

    changed = False
    canonical_gid = f"person:{person_id}"
    gid = str(person.get("global_person_id") or "").strip()
    if not gid or _is_unknown_global_person_id(gid):
        person["global_person_id"] = canonical_gid
        changed = True

    source = str(person.get("identity_source") or "").strip().lower()
    if source in _UNKNOWN_IDENTITY_SOURCES:
        person["identity_source"] = "face_recognition"
        changed = True

    match_score = person.get("match_score")
    if isinstance(match_score, (float, int)) and not isinstance(person.get("identity_confidence"), (float, int)):
        person["identity_confidence"] = float(match_score)
        changed = True

    person.pop("identity_pending", None)
    return changed


def _apply_relay_face_identity(person: Dict[str, Any], identity: Optional[Dict[str, Any]], track_id: int) -> bool:
    """Apply cached relay face identity, including weak enrolled-person candidates."""
    if not isinstance(identity, dict):
        return False
    pending_raw = identity.get("pending")
    if str(pending_raw).strip().lower() in {"1", "true", "yes", "on"}:
        return False

    applied = False
    confidence_tier = str(identity.get("confidence_tier") or "").strip().lower()
    candidate_id = str(identity.get("candidate_person_id") or "").strip()
    candidate_name = str(identity.get("candidate_person_name") or "").strip()
    candidate_score = identity.get("candidate_score")

    if candidate_id:
        person["candidate_person_id"] = candidate_id
        applied = True
    if candidate_name:
        person["candidate_person_name"] = candidate_name
        applied = True
    if isinstance(candidate_score, (int, float)):
        person["candidate_score"] = float(candidate_score)
        applied = True
    if confidence_tier:
        person["confidence_tier"] = confidence_tier
        applied = True

    pname = str(identity.get("person_name") or "").strip()
    pid = str(identity.get("person_id") or "").strip()
    match_score = identity.get("match_score")
    if pname:
        person["person_name"] = pname
        if pid:
            person["person_id"] = pid
        if isinstance(match_score, (int, float)):
            person["match_score"] = float(match_score)
        _promote_known_person_identity(person)
        return True

    promote_weak = _env_bool("FACE_RELAY_PROMOTE_WEAK_CANDIDATES", True)
    min_score = _env_float("FACE_RELAY_PROMOTE_MIN_SCORE", _env_float("FACE_MATCH_WEAK_THRESHOLD", 0.42))
    score_ok = isinstance(candidate_score, (int, float)) and float(candidate_score) >= min_score
    tier_ok = confidence_tier in {"weak", "weak_promoted", "strong"}
    if promote_weak and tier_ok and score_ok and candidate_id and candidate_name:
        person["person_id"] = candidate_id
        person["person_name"] = candidate_name
        person["match_score"] = float(candidate_score)
        _promote_known_person_identity(person)
        return True

    if not _has_known_person_identity(person):
        person["person_name"] = f"Unknown #{track_id}"
        applied = True
    return applied


def _should_request_relay_face_lookup(
    *,
    first_seen: bool,
    person: Dict[str, Any],
    frame_id: str,
    now_ts: float,
    last_lookup_ts: float,
    attempts: int,
    retry_seconds: float,
    max_attempts: int,
) -> bool:
    if not frame_id:
        return False
    if _has_known_person_identity(person):
        return False
    if max_attempts > 0 and attempts >= max_attempts:
        return False
    if first_seen:
        return True
    if retry_seconds <= 0:
        return True
    if last_lookup_ts <= 0:
        return True
    return (now_ts - last_lookup_ts) >= retry_seconds


def _apply_relay_person_identity(person: Dict[str, Any], identity: Optional[Dict[str, Any]]) -> bool:
    if not isinstance(identity, dict):
        return False

    applied = False
    gid = str(identity.get("global_person_id") or "").strip()
    pid = str(identity.get("person_id") or "").strip()
    pname = str(identity.get("person_name") or "").strip()
    incoming_known = bool(
        pid
        or gid.startswith("person:")
        or _is_known_person_name(pname)
    )
    current_known = _has_known_person_identity(person)

    if current_known and not incoming_known:
        return _promote_known_person_identity(person)

    if gid and not (_is_unknown_global_person_id(gid) and (current_known or incoming_known)):
        person["global_person_id"] = gid
        applied = True

    if pid:
        person["person_id"] = pid
        applied = True

    if pname and not (current_known and not _is_known_person_name(pname)):
        person["person_name"] = pname
        applied = True

    source = str(identity.get("identity_source") or "").strip()
    source_is_unknown = source.lower() in _UNKNOWN_IDENTITY_SOURCES
    if source and not (source_is_unknown and _has_known_person_identity(person)):
        person["identity_source"] = source
        applied = True

    conf = identity.get("identity_confidence")
    if isinstance(conf, (float, int)) and not (source_is_unknown and _has_known_person_identity(person)):
        person["identity_confidence"] = float(conf)
        applied = True

    if _promote_known_person_identity(person):
        applied = True

    if applied:
        person.pop("identity_pending", None)
    return applied


def _identity_is_unresolved(person: Dict[str, Any]) -> bool:
    person_id = str(person.get("person_id") or "").strip()
    if person_id:
        return False
    gid = str(person.get("global_person_id") or "").strip()
    return gid == "" or gid.startswith("unknown:")


def _normalize_embedding(values: List[float]) -> List[float]:
    if not values:
        return []
    arr = np.asarray(values, dtype=np.float32)
    norm = float(np.linalg.norm(arr))
    if norm <= 0.0:
        return []
    arr = arr / norm
    return [float(v) for v in arr.tolist()]


def _fuse_track_observation(
    history_by_track: Dict[Tuple[str, int], List[Tuple[List[float], float]]],
    track_key: Tuple[str, int],
    observation: Dict[str, Any],
    fusion_window: int,
) -> Dict[str, Any]:
    embedding = observation.get("embedding")
    if fusion_window <= 1 or not isinstance(embedding, list) or not embedding:
        return observation

    dim = len(embedding)
    if dim == 0:
        return observation

    quality = max(0.05, float(observation.get("quality_score") or 0.0))
    history = history_by_track.get(track_key, [])
    history = [(emb, weight) for emb, weight in history if isinstance(emb, list) and len(emb) == dim]
    history.append(([float(v) for v in embedding], quality))
    history = history[-fusion_window:]
    history_by_track[track_key] = history

    total_weight = sum(weight for _, weight in history)
    if total_weight <= 0:
        return observation

    fused = np.zeros(dim, dtype=np.float32)
    for emb, weight in history:
        fused += np.asarray(emb, dtype=np.float32) * float(weight)
    fused /= float(total_weight)

    merged = dict(observation)
    merged["embedding"] = _normalize_embedding([float(v) for v in fused.tolist()])
    merged["fusion_count"] = len(history)
    return merged


def _mark_identity_pending(person: Dict[str, Any]) -> None:
    """Mark unresolved identity without leaking synthetic unknown IDs."""
    person["identity_pending"] = True
    gid = str(person.get("global_person_id") or "").strip()
    if gid.startswith("unknown:"):
        person.pop("global_person_id", None)
        pname = str(person.get("person_name") or "").strip().lower()
        if pname.startswith("unknown"):
            person.pop("person_name", None)
        source = str(person.get("identity_source") or "").strip().lower()
        if source in ("unknown_track", "reid_new", "reid_match", ""):
            person.pop("identity_source", None)


def _resolve_person_identity_for_track(
    *,
    first_seen: bool,
    person: Dict[str, Any],
    media_id: str,
    track_id: int,
    frame_id: str,
    frame_path: str,
    camera_name: str,
    camera_timestamp: str,
    now_ts: float,
    identity_resolve_last_ts: Dict[Tuple[str, int], float],
    track_embedding_history: Optional[Dict[Tuple[str, int], List[Tuple[List[float], float]]]] = None,
    track_fusion_window: int = 1,
    min_interval_seconds: float,
    resolve_sync: Optional[Callable[..., Dict[str, Any]]],
    resolve_async: Optional[Callable[..., None]],
) -> None:
    """Run first-seen sync resolve followed by async retry scheduling."""
    resolve_key = (media_id, int(track_id))
    sync_resolved = False
    unresolved = _identity_is_unresolved(person)
    if track_embedding_history is None:
        track_embedding_history = {}
    if first_seen:
        track_embedding_history.pop(resolve_key, None)
    req_observation = _appearance_observation_from_crop(frame_path, person.get("box_px", {})) if frame_path else {}
    req_observation = _fuse_track_observation(
        track_embedding_history,
        resolve_key,
        req_observation,
        track_fusion_window,
    )

    if first_seen and unresolved and resolve_sync is not None:
        try:
            req_person_id = str(person.get("person_id") or "")
            req_person_name = str(person.get("person_name") or "")
            req_match_score = person.get("match_score")
            sync_identity = resolve_sync(
                media_id=media_id,
                track_id=int(track_id),
                frame_id=frame_id,
                person_id=req_person_id,
                person_name=req_person_name,
                match_score=float(req_match_score) if isinstance(req_match_score, (float, int)) else None,
                embedding=req_observation.get("embedding"),
                camera_name=camera_name,
                camera_timestamp=camera_timestamp,
                embedding_kind=str(req_observation.get("embedding_kind") or "body_reid"),
                embedding_model=str(req_observation.get("embedding_model") or ""),
                quality_score=float(req_observation.get("quality_score") or 0.0),
                observation_ts=camera_timestamp,
            )
            sync_resolved = _apply_relay_person_identity(person, sync_identity)
            if sync_resolved:
                identity_resolve_last_ts[resolve_key] = now_ts
            else:
                _mark_identity_pending(person)
        except Exception:
            _mark_identity_pending(person)

    if resolve_async is None:
        return

    last_resolve_ts = float(identity_resolve_last_ts.get(resolve_key, 0.0))
    should_resolve_identity = _should_request_person_identity_resolve(
        first_seen=(first_seen and not sync_resolved),
        person=person,
        now_ts=now_ts,
        last_resolve_ts=last_resolve_ts,
        min_interval_seconds=min_interval_seconds,
    )
    if not should_resolve_identity:
        return

    try:
        req_person_id = str(person.get("person_id") or "")
        req_person_name = str(person.get("person_name") or "")
        req_match_score = person.get("match_score")
        resolve_async(
            media_id=media_id,
            track_id=int(track_id),
            frame_id=frame_id,
            person_id=req_person_id,
            person_name=req_person_name,
            match_score=float(req_match_score) if isinstance(req_match_score, (float, int)) else None,
            embedding=req_observation.get("embedding"),
            camera_name=camera_name,
            camera_timestamp=camera_timestamp,
            embedding_kind=str(req_observation.get("embedding_kind") or "body_reid"),
            embedding_model=str(req_observation.get("embedding_model") or ""),
            quality_score=float(req_observation.get("quality_score") or 0.0),
            observation_ts=camera_timestamp,
        )
        identity_resolve_last_ts[resolve_key] = now_ts
    except Exception as _e:
        print(f"[ppe_detection] Person identity resolve error: {_e}")


def _associate_ppe_to_persons(
    persons: List[Dict[str, Any]],
    ppe_items: List[Dict[str, Any]],
) -> Dict[int, int]:
    """
    Associate each PPE item to its best-matching person using center-in-expanded-box.

    Strategy: PPE item center must fall within the person's bounding box expanded
    25% in each direction. When multiple persons overlap, choose the nearest center.

    Returns: {ppe_item_index -> person_index}
    """
    if not persons or not ppe_items:
        return {}

    associations: Dict[int, int] = {}

    for ppe_idx, ppe in enumerate(ppe_items):
        bp = ppe.get("box_px", {})
        ppe_cx = (bp.get("x_min", 0.0) + bp.get("x_max", 0.0)) / 2.0
        ppe_cy = (bp.get("y_min", 0.0) + bp.get("y_max", 0.0)) / 2.0

        best_person_idx: Optional[int] = None
        best_dist = float("inf")

        for p_idx, person in enumerate(persons):
            pb = person.get("box_px", {})
            px1, py1 = pb.get("x_min", 0.0), pb.get("y_min", 0.0)
            px2, py2 = pb.get("x_max", 0.0), pb.get("y_max", 0.0)

            pw, ph = px2 - px1, py2 - py1

            # Expand 25% on each side to capture PPE items near person edges
            ex1 = px1 - pw * 0.25
            ey1 = py1 - ph * 0.25
            ex2 = px2 + pw * 0.25
            ey2 = py2 + ph * 0.25

            if ex1 <= ppe_cx <= ex2 and ey1 <= ppe_cy <= ey2:
                # Prefer the person whose center is closest to the PPE center
                pcx = (px1 + px2) / 2.0
                pcy = (py1 + py2) / 2.0
                dist = abs(ppe_cx - pcx) + abs(ppe_cy - pcy)
                if dist < best_dist:
                    best_dist = dist
                    best_person_idx = p_idx

        if best_person_idx is not None:
            associations[ppe_idx] = best_person_idx

    return associations


# ── Snapshot cooldown: per (camera + violation_key) dedup ────────────────────
_last_snapshot_time: Dict[str, float] = {}


def _save_snapshot_with_bboxes(
    frame_path: str,
    output_path: str,
    detections: List[Dict[str, Any]],
    camera_name: str = "",
) -> None:
    """Save frame JPEG with red violation boxes only."""
    try:
        img  = Image.open(frame_path).convert("RGB")
        draw = ImageDraw.Draw(img)

        # Prefer explicit violation lists when provided by the worker output.
        explicit_person_boxes = [
            d for d in detections if bool(d.get("_violation_person_box"))
        ]
        explicit_object_boxes = [
            d for d in detections if bool(d.get("_violation_object_box"))
        ]
        draw_list = explicit_person_boxes + explicit_object_boxes
        if not draw_list:
            draw_list = [
                d for d in detections
                if d.get("class_id", -1) in VIOLATION_CLASS_IDS or bool(d.get("is_violation"))
            ]

        for det in draw_list:
            cls_id = det.get("class_id", -1)
            label  = det.get("label", "")
            conf   = det.get("confidence", 0.0)
            box    = det.get("box_px", {})
            x1     = int(box.get("x_min", 0))
            y1     = int(box.get("y_min", 0))
            x2     = int(box.get("x_max", 0))
            y2     = int(box.get("y_max", 0))

            if cls_id not in VIOLATION_CLASS_IDS and not bool(det.get("is_violation")):
                continue
            color = (255, 60, 60)  # red

            # Bounding box
            draw.rectangle([x1, y1, x2, y2], outline=color, width=2)

            # Label inside the lower edge so it remains visible when a person
            # fills the frame.
            track_id = det.get("track_id")
            tid_str  = f"#{track_id} " if (track_id is not None and track_id >= 0) else ""
            text     = f"{tid_str}{label} {int(conf * 100)}%"
            text_box = draw.textbbox((0, 0), text)
            text_h = max(12, int(text_box[3] - text_box[1]))
            text_y = min(max(0, y2 - text_h - 2), max(0, img.height - text_h - 2))
            draw.text((x1, text_y), text, fill=color)

        # Camera name tag — bottom-left corner with semi-transparent background
        if camera_name:
            w, h = img.size
            tag = f"  {camera_name}  "
            tag_y = h - 18
            # Dark background strip
            draw.rectangle([0, tag_y - 2, w, h], fill=(0, 0, 0, 180))
            draw.text((4, tag_y), tag, fill=(255, 255, 255))

        img.save(output_path, "JPEG", quality=90)
    except Exception as exc:
        shutil.copy2(frame_path, output_path)
        print(f"[ppe_detection] bbox draw failed ({exc}), used raw copy")


# ── Background VLM processor ─────────────────────────────────────────────────

class _BgVLM:
    """
    Runs VLM analysis in a background daemon thread.

    Detection workers call request() (non-blocking) then poll get_result()
    on subsequent frames. This completely decouples slow VLM inference
    from the fast YOLO detection pipeline.

    Queue size is bounded to prevent buildup when VLM is slower than input rate.
    """

    def __init__(self, vlm_instance: Any) -> None:
        self._vlm = vlm_instance
        self._q: "py_queue.Queue[Optional[tuple]]" = py_queue.Queue(maxsize=8)
        self._cache: Dict[str, str] = {}
        self._lock = threading.Lock()
        t = threading.Thread(target=self._run, daemon=True, name="ppe-bg-vlm")
        t.start()
        _debug_log("[ppe_detection] Background VLM thread started")

    def request(self, key: str, image_path: str, prompt: str, max_tokens: int = 180) -> None:
        """Queue a VLM analysis request. Non-blocking; silently drops if queue full."""
        try:
            self._q.put_nowait((key, image_path, prompt, max_tokens))
        except py_queue.Full:
            pass  # VLM is busy; this frame's analysis is skipped

    def get_result(self, key: str) -> Optional[str]:
        """Return and remove the cached VLM result for key. Returns None if not ready."""
        with self._lock:
            return self._cache.pop(key, None)

    def _run(self) -> None:
        while True:
            try:
                item = self._q.get(timeout=2.0)
                if item is None:
                    break
                key, path, prompt, max_tokens = item
                try:
                    result = self._vlm.analyze_image(path, prompt, max_tokens)
                    with self._lock:
                        self._cache[key] = result
                    _debug_log(f"[ppe_detection] BgVLM done: key={key[:40]}")
                except Exception as exc:
                    print(f"[ppe_detection] BgVLM error: {exc}")
            except py_queue.Empty:
                continue


def _make_vlm_key(media_id: str, track_id: int, violations: List[str]) -> str:
    return f"{media_id}:{track_id}:{','.join(sorted(violations))}"


# ── Model loader ─────────────────────────────────────────────────────────────

def load_ai_model():
    """Load PPE YOLO model and return the appropriate worker function."""

    _patch_ultralytics_torch_safe_load()
    device = _pick_device()

    print(
        f"[ppe_detection] torch={torch.__version__} "
        f"cuda_avail={torch.cuda.is_available()} "
        f"device_count={torch.cuda.device_count()} "
        f"CUDA_VISIBLE_DEVICES={os.environ.get('CUDA_VISIBLE_DEVICES')}"
    )
    if torch.cuda.is_available() and torch.cuda.device_count() > 0:
        try:
            print(f"[ppe_detection] GPU: {torch.cuda.get_device_name(0)}")
        except Exception:
            pass

    _model_id = os.getenv("PPE_MODEL_ID", "hf://Hexmon/vyra-yolo-ppe-detection/best.pt")
    conf    = _env_float("PPE_CONF", 0.35)
    iou     = _env_float("PPE_IOU", 0.45)
    max_det = _env_int("PPE_MAX_DET", 300)

    # Resolve hf:// URIs — ultralytics.YOLO doesn't understand them natively
    if _model_id.startswith("hf://"):
        from huggingface_hub import hf_hub_download
        parts    = _model_id[len("hf://"):].split("/", 2)  # ["Owner", "repo", "file.pt"]
        repo_id  = f"{parts[0]}/{parts[1]}"
        filename = parts[2] if len(parts) > 2 else "best.pt"
        hf_token = os.getenv("HF_TOKEN")
        print(f"[ppe_detection] Downloading {repo_id}/{filename} from HuggingFace Hub…")
        model_id = hf_hub_download(repo_id=repo_id, filename=filename, token=hf_token)
        print(f"[ppe_detection] Cached at: {model_id}")
    else:
        model_id = _model_id

    print(f"[ppe_detection] Loading: {model_id} | device={device} | conf={conf} iou={iou}")

    try:
        from ultralytics import YOLO
        model = YOLO(model_id)
    except Exception as e:
        msg = f"Failed to load PPE model '{model_id}': {e}"
        print(f"[ppe_detection] FATAL: {msg}")

        def _error_worker(data: Dict[str, Any]) -> Dict[str, Any]:
            inputs = data.get("inputs", []) or []
            return {
                "output": [
                    {"id": str(inp.get("id", "")), "detections": [], "error": msg}
                    for inp in inputs
                ]
            }

        return _error_worker

    model.overrides["conf"]   = conf
    model.overrides["iou"]    = iou
    model.overrides["max_det"] = max_det
    model.overrides["device"] = device

    try:
        model.to(device)
    except Exception:
        pass

    model_names: Dict[int, str] = (
        getattr(model, "names", None)
        or getattr(getattr(model, "model", None), "names", None)
        or CLASS_NAMES
    )
    print(f"[ppe_detection] Ready. Classes: {model_names}")

    legacy_mode = _legacy_mode_enabled()
    ppe_face_rec_enabled = _env_bool("PPE_ENABLE_FACE_RECOGNITION", False)
    if FACE_REC_AVAILABLE and ppe_face_rec_enabled:
        _face_rec.start_registry_refresh()
        print("[ppe_detection] Face recognizer enabled for PPE worker (embedded)")
    elif FACE_REC_AVAILABLE:
        print("[ppe_detection] Face recognizer disabled for PPE worker (PPE_ENABLE_FACE_RECOGNITION=0)")

    # When embedded face rec is disabled, use relay REST for shared face + global identity.
    use_relay_face = (not legacy_mode) and (not ppe_face_rec_enabled)
    use_relay_person_identity = not legacy_mode
    request_person_identity_resolve = None
    resolve_person_identity_sync = None
    fetch_person_identities = None
    if use_relay_face:
        try:
            from relay_client import (
                request_face_lookup,
                fetch_face_identities,
                request_person_identity_resolve as _request_person_identity_resolve,
                resolve_person_identity_sync as _resolve_person_identity_sync,
                fetch_person_identities as _fetch_person_identities,
                set_relay_base,
            )
            relay_base = os.getenv("RELAY_HTTP_BASE", "http://localhost:8020")
            set_relay_base(relay_base)
            request_person_identity_resolve = _request_person_identity_resolve
            resolve_person_identity_sync = _resolve_person_identity_sync
            fetch_person_identities = _fetch_person_identities
            print(f"[ppe_detection] Relay face identity client enabled (base={relay_base})")
        except ImportError:
            use_relay_face = False
            use_relay_person_identity = False
            print("[ppe_detection] relay_client not available — face identity disabled")

    use_relay_person_identity = use_relay_person_identity and fetch_person_identities is not None

    # ── Stage 1: Raw YOLO inference ──────────────────────────────────────────

    def _run_yolo(data: Dict[str, Any]) -> List[Dict[str, Any]]:
        """Batch YOLO inference. Returns raw per-frame result dicts."""
        t0 = time.time()
        inputs_raw = data.get("inputs", []) or []
        payload_media_id = (
            data.get("media_id")
            or data.get("media_idx")
            or data.get("service_id")
            or "default"
        )
        payload_media_name = (
            data.get("media_name")
            or data.get("service_name")
            or payload_media_id
        )

        # Flatten nested job structure: {id: job_id, inputs: [{id: frame_id, ...}]}
        inputs_list: List[Dict[str, Any]] = []
        for it in inputs_raw:
            if isinstance(it, dict) and isinstance(it.get("inputs"), list):
                job_id = it.get("id")
                outer_media_id = (
                    it.get("media_id")
                    or it.get("media_idx")
                    or it.get("service_id")
                    or payload_media_id
                )
                outer_media_name = (
                    it.get("media_name")
                    or it.get("service_name")
                    or outer_media_id
                )
                for inner in it.get("inputs", []):
                    if isinstance(inner, dict):
                        frame = dict(inner)
                        frame["_job_id"] = job_id
                        frame["_media_id"] = (
                            frame.get("media_id")
                            or frame.get("media_idx")
                            or frame.get("service_id")
                            or outer_media_id
                        )
                        frame["_media_name"] = (
                            frame.get("media_name")
                            or frame.get("service_name")
                            or outer_media_name
                        )
                        inputs_list.append(frame)
            elif isinstance(it, dict):
                frame = dict(it)
                frame["_media_id"] = (
                    frame.get("media_id")
                    or frame.get("media_idx")
                    or frame.get("service_id")
                    or payload_media_id
                )
                frame["_media_name"] = (
                    frame.get("media_name")
                    or frame.get("service_name")
                    or payload_media_name
                )
                inputs_list.append(frame)

        _debug_log(f"[ppe_detection] Batch: {len(inputs_list)} frames")

        ids: List[str]                   = []
        paths: List[str]                 = []
        sizes: List[Tuple[int, int]]     = []
        job_ids: List[Optional[str]]     = []
        media_ids: List[str]             = []
        media_names: List[str]           = []

        for inp in inputs_list:
            input_id = str(inp.get("id", "unknown"))
            filepath  = str(inp.get("filepath", ""))
            ids.append(input_id)
            job_ids.append(inp.get("_job_id"))
            media_ids.append(str(inp.get("_media_id") or payload_media_id))
            media_names.append(str(inp.get("_media_name") or payload_media_name))

            opened = _safe_open_image(filepath) if filepath else None
            if opened:
                _, w, h = opened
                paths.append(filepath)
                sizes.append((w, h))
            else:
                if filepath:
                    print(f"[ppe_detection] Cannot open image: {filepath}")
                paths.append("")
                sizes.append((0, 0))

        valid_idx   = [i for i, p in enumerate(paths) if p]
        valid_paths = [paths[i] for i in valid_idx]

        yolo_map: Dict[int, Any] = {}
        if valid_paths:
            try:
                raw = model.predict(valid_paths, verbose=False)
                for i, res in zip(valid_idx, raw):
                    yolo_map[i] = res
            except Exception as e:
                print(f"[ppe_detection] Inference failed: {e}")

        out: List[Dict[str, Any]] = []
        for i, input_id in enumerate(ids):
            w, h = sizes[i]
            all_dets: List[Dict[str, Any]] = []

            r = yolo_map.get(i)
            if r is not None and getattr(r, "boxes", None) is not None and len(r.boxes) > 0:
                names = (
                    getattr(r, "names", None)
                    or model_names
                    or CLASS_NAMES
                )
                for b in r.boxes:
                    try:
                        x1, y1, x2, y2 = [float(v) for v in b.xyxy[0].tolist()]
                        conf_v = float(b.conf[0].item()) if hasattr(b, "conf") else 0.0
                        cls_id = int(b.cls[0].item())  if hasattr(b, "cls")  else -1
                        label  = str(names.get(cls_id, CLASS_NAMES.get(cls_id, str(cls_id))))

                        all_dets.append({
                            "label":      label,
                            "confidence": round(conf_v, 3),
                            "class_id":   cls_id,
                            "box_px": {
                                "x_min": round(x1, 2), "y_min": round(y1, 2),
                                "x_max": round(x2, 2), "y_max": round(y2, 2),
                            },
                            "box": {
                                "x_min": round(x1 / w, 6) if w else 0.0,
                                "y_min": round(y1 / h, 6) if h else 0.0,
                                "x_max": round(x2 / w, 6) if w else 0.0,
                                "y_max": round(y2 / h, 6) if h else 0.0,
                            },
                        })
                    except Exception:
                        continue

            out.append({
                "id":           input_id,
                "_path":        paths[i],
                "_job_id":      job_ids[i],
                "_media_id":    media_ids[i],
                "_media_name":  media_names[i],
                "image_width":  w,
                "image_height": h,
                "all_dets":     all_dets,
            })

        _debug_log(
            f"[ppe_detection] YOLO done. batch={len(valid_paths)} "
            f"time_ms={(time.time() - t0) * 1000:.1f}"
        )
        return out

    # ── Stage 2: Tracking + association + stats ──────────────────────────────

    def _build_output(
        raw_frames: List[Dict[str, Any]],
        snapshot_dir: str,
    ) -> List[Dict[str, Any]]:
        """
        For each frame:
        - Separate Person vs PPE detections
        - ByteTrack on Person boxes → persistent track_ids
        - Associate PPE items to their person via expanded-box check
        - Tag each detection with track_id (PPE inherits person's track_id)
        - Compute per-frame stats and deterministic severity
        """
        frame_rate = _env_int("TRACKER_FRAME_RATE", 10)
        require_person_assoc = _env_bool("PPE_REQUIRE_PERSON_ASSOCIATION", True)
        tracker_by_media: Dict[str, Any] = {}
        touched_media_ids: Set[str] = set()
        identity_resolve_last_ts: Dict[Tuple[str, int], float] = {}
        relay_face_lookup_last_ts: Dict[Tuple[str, int], float] = {}
        relay_face_lookup_attempts: Dict[Tuple[str, int], int] = {}
        track_embedding_history: Dict[Tuple[str, int], List[Tuple[List[float], float]]] = {}
        track_fusion_window = max(1, _env_int("PERSON_REID_TRACK_FUSION_WINDOW", 4))
        identity_resolve_retry_seconds = max(
            0.0,
            _env_float("PERSON_IDENTITY_RESOLVE_RETRY_SECONDS", 3.0),
        )
        relay_face_lookup_retry_seconds = max(
            0.0,
            _env_float("FACE_RELAY_LOOKUP_RETRY_SECONDS", _env_float("FACE_LOOKUP_RETRY_SECONDS", 1.0)),
        )
        relay_face_lookup_max_attempts = max(
            0,
            _env_int("FACE_RELAY_LOOKUP_MAX_ATTEMPTS", _env_int("FACE_LOOKUP_MAX_ATTEMPTS", 12)),
        )
        os.makedirs(snapshot_dir, exist_ok=True)

        results: List[Dict[str, Any]] = []

        for frame in raw_frames:
            input_id = frame["id"]
            w, h     = frame["image_width"], frame["image_height"]
            all_dets = frame["all_dets"]
            frame_media_id = str(frame.get("_media_id") or "default")
            tracker = None
            if TRACKING_AVAILABLE:
                tracker = tracker_by_media.get(frame_media_id)
                if tracker is None:
                    tracker = TrackerManager.get(frame_media_id, frame_rate=frame_rate)
                    tracker_by_media[frame_media_id] = tracker
                touched_media_ids.add(frame_media_id)

            # ── Split by class ───────────────────────────────────────────────
            person_dets = [d for d in all_dets if d.get("class_id") == PERSON_CLASS_ID]
            other_dets  = [d for d in all_dets if d.get("class_id") != PERSON_CLASS_ID]

            # ── ByteTrack persons ────────────────────────────────────────────
            if tracker and person_dets:
                person_dets = tracker.update(person_dets)
            else:
                for d in person_dets:
                    d.setdefault("track_id", -1)

            # ── Face recognition identity + first-seen ───────────────────────
            frame_path = frame.get("_path", "")

            # Relay-based face identity: bulk-fetch cached identities for this camera.
            relay_identities: Dict[str, Any] = {}
            if use_relay_face and frame_media_id:
                try:
                    relay_identities = fetch_face_identities(frame_media_id)
                except Exception:
                    pass
            relay_person_identities: Dict[str, Any] = {}
            if use_relay_person_identity and frame_media_id and fetch_person_identities is not None:
                try:
                    relay_person_identities = fetch_person_identities(frame_media_id)
                except Exception:
                    pass

            for person in person_dets:
                tid = person.get("track_id", -1)
                if tid < 0 or not tracker:
                    continue

                # Apply cached identity (person_name, person_id, match_score).
                if use_relay_face:
                    # Use relay-side face identity cache
                    rid = relay_identities.get(str(tid))
                    _apply_relay_face_identity(person, rid, int(tid))
                if use_relay_person_identity:
                    pid_identity = relay_person_identities.get(str(tid))
                    _apply_relay_person_identity(person, pid_identity)
                elif tracker.has_identity(tid):
                    # Embedded face rec path
                    identity = tracker.get_identity(tid)
                    pname = identity.get("person_name") if identity else None
                    if pname:
                        person["person_name"] = pname
                        person["person_id"]   = identity.get("person_id")
                        person["match_score"] = identity.get("match_score")
                    else:
                        person["person_name"] = f"Unknown #{tid}"

                now_ts = time.time()
                first_seen = bool(tracker.is_first_seen(tid))

                # First-seen: submit face recognition, mark seen
                if first_seen:
                    tracker.mark_seen(tid)
                    person["first_detection"] = True

                    if use_relay_person_identity and request_person_identity_resolve is None:
                        pass
                    elif ppe_face_rec_enabled:
                        tracker.set_identity(tid, {})  # {} = lookup in-progress
                        if FACE_REC_AVAILABLE and frame_path:
                            try:
                                img_np = np.array(Image.open(frame_path).convert("RGB"))
                                pb = person["box_px"]
                                _face_rec.submit_async(
                                    img_np,
                                    pb["x_min"], pb["y_min"], pb["x_max"], pb["y_max"],
                                    track_id=tid,
                                    callback=lambda t, r, _tr=tracker: _tr.set_identity(t, r),
                                )
                            except Exception as _e:
                                print(f"[ppe_detection] Face rec submit error: {_e}")

                if use_relay_face and frame_path:
                    lookup_key = (frame_media_id, int(tid))
                    if _should_request_relay_face_lookup(
                        first_seen=first_seen,
                        person=person,
                        frame_id=input_id,
                        now_ts=now_ts,
                        last_lookup_ts=relay_face_lookup_last_ts.get(lookup_key, 0.0),
                        attempts=relay_face_lookup_attempts.get(lookup_key, 0),
                        retry_seconds=relay_face_lookup_retry_seconds,
                        max_attempts=relay_face_lookup_max_attempts,
                    ):
                        try:
                            pb = person.get("box_px", {})
                            request_face_lookup(
                                frame_media_id, tid, input_id,
                                person_bbox={
                                    "x_min": pb.get("x_min", 0),
                                    "y_min": pb.get("y_min", 0),
                                    "x_max": pb.get("x_max", 0),
                                    "y_max": pb.get("y_max", 0),
                                },
                            )
                            relay_face_lookup_last_ts[lookup_key] = now_ts
                            relay_face_lookup_attempts[lookup_key] = (
                                relay_face_lookup_attempts.get(lookup_key, 0) + 1
                            )
                        except Exception as _e:
                            print(f"[ppe_detection] Relay face lookup error: {_e}")

                if use_relay_person_identity:
                    _resolve_person_identity_for_track(
                        first_seen=first_seen,
                        person=person,
                        media_id=frame_media_id,
                        track_id=int(tid),
                        frame_id=input_id,
                        frame_path=frame_path,
                        camera_name=str(frame.get("_media_name") or frame_media_id),
                        camera_timestamp=time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                        now_ts=now_ts,
                        identity_resolve_last_ts=identity_resolve_last_ts,
                        track_embedding_history=track_embedding_history,
                        track_fusion_window=track_fusion_window,
                        min_interval_seconds=identity_resolve_retry_seconds,
                        resolve_sync=resolve_person_identity_sync,
                        resolve_async=request_person_identity_resolve,
                    )

            # ── Associate PPE to persons ─────────────────────────────────────
            associations = _associate_ppe_to_persons(person_dets, other_dets)

            person_by_idx = {i: p for i, p in enumerate(person_dets)}

            for ppe_idx, ppe in enumerate(other_dets):
                p_idx = associations.get(ppe_idx)
                if p_idx is not None:
                    person = person_by_idx[p_idx]
                    ppe["track_id"]        = person.get("track_id", -1)
                    ppe["person_track_id"] = person.get("track_id", -1)
                else:
                    ppe["track_id"]        = -1
                    ppe["person_track_id"] = None

            # ── Per-person PPE summary ────────────────────────────────────────
            person_violations: Dict[int, List[str]] = {}
            person_ppe_ok:     Dict[int, List[str]] = {}

            for ppe in other_dets:
                p_track = ppe.get("person_track_id")
                if p_track is None:
                    continue
                cls_id = ppe.get("class_id", -1)
                label  = ppe["label"]
                if cls_id in NO_PPE_CLASS_IDS:
                    person_violations.setdefault(p_track, []).append(label)
                elif cls_id not in CONTEXT_CLASS_IDS:
                    person_ppe_ok.setdefault(p_track, []).append(label)

            # Annotate each person with their PPE summary
            for person in person_dets:
                tid = person.get("track_id", -1)
                person["ppe_violations"] = person_violations.get(tid, [])
                person["ppe_compliant"]  = person_ppe_ok.get(tid, [])

            # ── Frame-level stats ────────────────────────────────────────────
            associated_no_ppe_labels: List[str] = []
            for v_list in person_violations.values():
                associated_no_ppe_labels.extend(v_list)
            unassociated_no_ppe: List[Dict[str, Any]] = []
            for ppe in other_dets:
                if ppe.get("person_track_id") is None and ppe.get("class_id") in NO_PPE_CLASS_IDS:
                    unassociated_no_ppe.append(ppe)

            ppe_violation_labels: List[str] = list(associated_no_ppe_labels)
            if not require_person_assoc:
                ppe_violation_labels.extend([str(p.get("label") or "") for p in unassociated_no_ppe if str(p.get("label") or "")])
            elif not ppe_violation_labels and unassociated_no_ppe:
                # Strict mode fallback:
                # if association fails, allow only strong NO-* detections.
                # This prevents complete event/snapshot loss when Person class is intermittently missed.
                # Keep it bounded to avoid noisy false positives.
                conf_threshold = 0.60 if person_dets else 0.55
                high_conf_labels = [
                    str(p.get("label") or "")
                    for p in unassociated_no_ppe
                    if float(p.get("confidence") or 0.0) >= conf_threshold and str(p.get("label") or "")
                ]
                ppe_violation_labels.extend(high_conf_labels[:2])

            fall_labels = [
                d.get("label")
                for d in other_dets
                if d.get("class_id") == FALL_CLASS_ID
            ]
            fall_labels = [v for v in fall_labels if isinstance(v, str) and v]

            all_violation_labels: List[str] = ppe_violation_labels + fall_labels

            violation_count  = len(all_violation_labels)
            violation_labels = list(dict.fromkeys(all_violation_labels))  # ordered unique
            person_count     = len(person_dets)
            total_detections = len(all_dets)
            fall_detected    = bool(fall_labels)

            # ── Deterministic severity (from detections, not VLM text) ────────
            if fall_detected:
                severity = "high"
            elif ppe_violation_labels:
                max_per_person = max(
                    (len(v) for v in person_violations.values()), default=0
                )
                severity = "high" if max_per_person >= 3 else "medium"
            else:
                severity = "low"

            has_violations = bool(ppe_violation_labels) or fall_detected

            # ── Named event brief ─────────────────────────────────────────────
            first_dets = [p for p in person_dets if p.get("first_detection")]
            # Names of persons with active violations (for violation brief)
            viol_person_names: List[str] = [
                p["person_name"]
                for p in person_dets
                if p.get("ppe_violations") and p.get("person_name")
            ]
            # All identified persons in frame (for person_names metadata)
            all_person_names: List[str] = [
                p["person_name"] for p in person_dets if p.get("person_name")
            ]

            frame_camera_name = str(frame.get("_media_name") or frame_media_id)
            frame_camera_ts = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
            event_brief = ""
            if first_dets:
                names = [p["person_name"] for p in first_dets if p.get("person_name")]
                n_unknown = sum(1 for p in first_dets if not p.get("person_name"))
                subj_parts = names + ([f"{n_unknown} unknown person(s)"] if n_unknown else [])
                subject = ", ".join(subj_parts) or "Person"
                if ppe_violation_labels:
                    ppe_labels_unique = list(dict.fromkeys(ppe_violation_labels))
                    event_brief = f"{subject} seen on {frame_camera_name} at {frame_camera_ts} without {', '.join(ppe_labels_unique[:2])}"
                else:
                    event_brief = f"{subject} detected on {frame_camera_name} at {frame_camera_ts}"
            elif fall_detected:
                subject = viol_person_names[0] if viol_person_names else "Worker"
                event_brief = f"{subject} fall detected on {frame_camera_name} at {frame_camera_ts}"
            elif ppe_violation_labels:
                ppe_labels_unique = list(dict.fromkeys(ppe_violation_labels))
                if viol_person_names:
                    subject = ", ".join(viol_person_names[:2])
                    event_brief = f"{subject} seen on {frame_camera_name} at {frame_camera_ts} without {', '.join(ppe_labels_unique[:2])}"
                else:
                    event_brief = f"PPE violation on {frame_camera_name} at {frame_camera_ts}: {', '.join(ppe_labels_unique[:2])}"

            # ── Violation-only detection payload ──────────────────────────────
            fall_by_track: Dict[int, bool] = {}
            for d in other_dets:
                if d.get("class_id") == FALL_CLASS_ID:
                    tid = d.get("person_track_id")
                    if isinstance(tid, int):
                        fall_by_track[tid] = True

            violation_person_boxes: List[Dict[str, Any]] = []
            for person in person_dets:
                tid = int(person.get("track_id", -1) or -1)
                violations_for_person = list(person.get("ppe_violations") or [])
                if tid in fall_by_track and "Fall-Detected" not in violations_for_person:
                    violations_for_person.append("Fall-Detected")
                if not violations_for_person:
                    continue
                _promote_known_person_identity(person)
                if not str(person.get("global_person_id") or "").strip():
                    person["identity_pending"] = True
                clean = {k: v for k, v in person.items() if not k.startswith("_")}
                clean["missing_ppe"] = list(dict.fromkeys(violations_for_person))
                clean["label"] = f"without-{','.join(clean['missing_ppe'][:2])}"
                clean["is_violation"] = True
                clean["_violation_person_box"] = True
                violation_person_boxes.append(clean)

            violation_object_boxes: List[Dict[str, Any]] = []
            for det in other_dets:
                cls_id = det.get("class_id", -1)
                if cls_id not in VIOLATION_CLASS_IDS:
                    continue
                clean = {k: v for k, v in det.items() if not k.startswith("_")}
                clean["is_violation"] = True
                clean["_violation_object_box"] = True
                violation_object_boxes.append(clean)

            final_dets = violation_person_boxes + violation_object_boxes

            result: Dict[str, Any] = {
                "id":               input_id,
                "image_width":      w,
                "image_height":     h,
                "detections":       final_dets,
                "violation_person_boxes": violation_person_boxes,
                "violation_object_boxes": violation_object_boxes,
                "violation_count":  violation_count,
                "violation_labels": violation_labels,
                "person_count":     person_count,
                "total_detections": total_detections,
                "fall_detected":    fall_detected,
                "has_violations":   has_violations,
                "severity":         severity,
                "first_detection":  bool(first_dets),
                "event_brief":      event_brief,
                "person_names":     all_person_names,
                "camera_name":      frame_camera_name,
                "camera_timestamp": frame_camera_ts,
                "event_kind":       "ppe_violation" if has_violations else "ppe_observation",
            }
            if has_violations and event_brief:
                result["vlm_summary"] = event_brief
            results.append(result)

        for mid in touched_media_ids:
            tracker = tracker_by_media.get(mid)
            if tracker is not None:
                tracker.cleanup_stale()

        return results

    # ── Helper: group output by job_id if present ────────────────────────────

    def _group_by_job(
        output_frames: List[Dict[str, Any]],
        raw_frames: List[Dict[str, Any]],
    ) -> Dict[str, Any]:
        job_id_map: Dict[str, List[Dict[str, Any]]] = {}
        raw_path_map = {rf["id"]: rf for rf in raw_frames}

        for frame in output_frames:
            rf  = raw_path_map.get(frame["id"], {})
            jid = rf.get("_job_id")
            if jid:
                job_id_map.setdefault(jid, []).append(frame)

        if job_id_map:
            return {
                "output": [
                    {"id": jid, "output": items}
                    for jid, items in job_id_map.items()
                ]
            }
        return {"output": output_frames}

    # ── Worker: detection + snapshots + background VLM ───────────────────────

    def _make_worker_with_bg_vlm(bg_vlm: _BgVLM):
        """
        Returns a worker function that:
        - Runs YOLO + ByteTrack at full speed (no VLM blocking)
        - Saves snapshots IMMEDIATELY on gated violations
        - Queues VLM analysis in background; attaches result on next frame
        """

        def worker_function_fast(data: Dict[str, Any]) -> Dict[str, Any]:
            t0            = time.time()
            snapshot_dir  = os.getenv("SNAPSHOT_DIR", "/tmp/snapshots")
            vlm_cooldown  = _env_float("VLM_COOLDOWN_SECONDS", 15.0)
            snap_cooldown = _env_float("SNAPSHOT_COOLDOWN_SECONDS", 30.0)
            always_snapshot_on_violation = _env_bool("PPE_SNAPSHOT_ALWAYS_ON_VIOLATION", True)

            raw_frames    = _run_yolo(data)
            output_frames = _build_output(raw_frames, snapshot_dir)
            raw_by_id = {rf["id"]: rf for rf in raw_frames}
            path_by_id = {rf["id"]: rf.get("_path", "") for rf in raw_frames}

            for frame in output_frames:
                raw_frame = raw_by_id.get(frame.get("id"), {})
                frame_media_id = str(
                    raw_frame.get("_media_id")
                    or data.get("media_id")
                    or data.get("media_idx")
                    or data.get("service_id")
                    or "default"
                )
                frame_media_name = str(
                    raw_frame.get("_media_name")
                    or data.get("media_name")
                    or data.get("service_name")
                    or frame_media_id
                )
                vlm_tracker = TrackerManager.get(frame_media_id) if TRACKING_AVAILABLE else None

                detections  = frame.get("detections", [])
                person_dets = [d for d in detections if d.get("class_id") == PERSON_CLASS_ID]

                # ── Always check VLM cache first (results from background thread) ──
                # Attach any ready VLM result even on non-violation frames
                for person in person_dets:
                    tid        = person.get("track_id", -1)
                    violations = person.get("ppe_violations", [])
                    if not violations:
                        continue
                    vlm_key = _make_vlm_key(frame_media_id, tid, violations)
                    result  = bg_vlm.get_result(vlm_key)
                    existing = str(frame.get("vlm_summary") or "").strip()
                    fallback = str(frame.get("event_brief") or "").strip()
                    if result and (not existing or existing == fallback):
                        frame["vlm_summary"]   = result
                        frame["vlm_track_ids"] = [tid]
                        _debug_log(f"[ppe_detection] VLM result attached: track={tid}")
                        # Let VLM upgrade severity if it assessed higher risk
                        rl = result.lower()
                        if "severity:high" in rl or "severity: high" in rl:
                            vlm_sev = "high"
                        elif "severity:medium" in rl or "severity: medium" in rl:
                            vlm_sev = "medium"
                        else:
                            vlm_sev = None
                        _SEV_ORDER = {"low": 0, "medium": 1, "high": 2}
                        cur = frame.get("severity", "low")
                        if vlm_sev and _SEV_ORDER.get(vlm_sev, 0) > _SEV_ORDER.get(cur, 0):
                            frame["severity"] = vlm_sev

                # ── First-detection snapshot + VLM ───────────────────────────
                if frame.get("first_detection") and frame.get("has_violations"):
                    frame_path = path_by_id.get(frame["id"], "")
                    if frame_path and os.path.exists(frame_path):
                        fd_persons = [d for d in detections
                                      if d.get("class_id") == PERSON_CLASS_ID
                                      and d.get("first_detection")]
                        if fd_persons:
                            fd_snap = f"{frame['id']}_first_detection.jpg"
                            fd_snap_path = os.path.join(snapshot_dir, fd_snap)
                            _save_snapshot_with_bboxes(frame_path, fd_snap_path, detections, camera_name=frame_media_name)
                            frame["snapshot_path"] = fd_snap_path
                            names = [p.get("person_name") for p in fd_persons if p.get("person_name")]
                            viols = frame.get("violation_labels", [])
                            name_str = names[0] if len(names) == 1 else (", ".join(names) if names else "unknown person")
                            fd_prompt = (
                                f"A new person ({name_str}) has appeared on workplace camera {frame_media_name}. "
                                + (f"PPE violations detected: {', '.join(viols[:2])}. " if viols else "")
                                + "Briefly describe: (1) the person's appearance and location; "
                                "(2) any visible safety concerns. 2-3 sentences."
                            )
                            fd_key = f"first:{frame['id']}"
                            bg_vlm.request(fd_key, frame_path, fd_prompt, max_tokens=120)
                            _debug_log(f"[ppe_detection] First-detection snapshot + VLM queued for {name_str}")

                if not frame.get("has_violations"):
                    continue

                # ── VLM gating: who needs new VLM analysis? ──────────────────
                persons_to_alert: List[Dict[str, Any]] = []

                for person in person_dets:
                    tid        = person.get("track_id", -1)
                    violations = person.get("ppe_violations", [])
                    if not violations:
                        continue
                    violation_key = ",".join(sorted(violations))
                    should = (
                        vlm_tracker.should_alert(tid, violation_key, cooldown_seconds=vlm_cooldown)
                        if vlm_tracker else True
                    )
                    if should:
                        persons_to_alert.append({"track_id": tid, "violations": violations})

                # Fall-Detected without tracked person
                for fd in detections:
                    if fd.get("class_id") == FALL_CLASS_ID and fd.get("person_track_id") is None:
                        should = (
                            vlm_tracker.should_alert(-1, "Fall-Detected", cooldown_seconds=vlm_cooldown)
                            if vlm_tracker else True
                        )
                        if should:
                            persons_to_alert.append({"track_id": -1, "violations": ["Fall-Detected"]})

                # Optional legacy behavior: unassociated NO-* violations.
                # Default hardened mode requires NO-* to be person-associated.
                if not ppe_require_person_assoc:
                    unassociated = [
                        d.get("label")
                        for d in detections
                        if d.get("class_id") in NO_PPE_CLASS_IDS and d.get("person_track_id") is None
                    ]
                    unassociated = [v for v in unassociated if isinstance(v, str) and v]
                    if unassociated:
                        uniq_unassoc = list(dict.fromkeys(unassociated))
                        violation_key = ",".join(sorted(uniq_unassoc))
                        should = (
                            vlm_tracker.should_alert(-2, violation_key, cooldown_seconds=vlm_cooldown)
                            if vlm_tracker else True
                        )
                        if should:
                            persons_to_alert.append({"track_id": -2, "violations": uniq_unassoc})

                if not persons_to_alert:
                    frame_labels = frame.get("violation_labels", [])
                    if isinstance(frame_labels, list):
                        generic_labels = [v for v in frame_labels if isinstance(v, str) and v]
                    else:
                        generic_labels = []
                    if frame.get("fall_detected") and "Fall-Detected" not in generic_labels:
                        generic_labels.insert(0, "Fall-Detected")
                    if generic_labels:
                        persons_to_alert.append({"track_id": -3, "violations": generic_labels})
                    else:
                        continue

                frame_path = path_by_id.get(frame["id"], "")
                if not frame_path or not os.path.exists(frame_path):
                    continue

                all_viol_labels = list(dict.fromkeys(
                    v for p in persons_to_alert for v in p["violations"]
                ))
                n_persons = len(persons_to_alert)
                track_to_name = {
                    int(p.get("track_id", -999)): p.get("person_name")
                    for p in person_dets
                    if p.get("person_name")
                }
                named_persons = list(dict.fromkeys(
                    track_to_name.get(int(p.get("track_id", -999)))
                    for p in persons_to_alert
                    if track_to_name.get(int(p.get("track_id", -999)))
                ))

                # ── Save snapshot IMMEDIATELY (no VLM wait) ──────────────────
                snap_key = f"{frame_media_id}:{','.join(sorted(all_viol_labels))}"
                now_snap = time.time()
                critical_violation = "Fall-Detected" in all_viol_labels
                should_snapshot = (
                    always_snapshot_on_violation
                    or critical_violation
                    or (now_snap - _last_snapshot_time.get(snap_key, 0.0) >= snap_cooldown)
                )
                if should_snapshot:
                    _last_snapshot_time[snap_key] = now_snap
                    snap_name     = f"{frame['id']}_ppe_violation.jpg"
                    snapshot_path = os.path.join(snapshot_dir, snap_name)
                    _save_snapshot_with_bboxes(frame_path, snapshot_path, detections, camera_name=frame_media_name)
                    frame["snapshot_path"] = snapshot_path
                    _debug_log(f"[ppe_detection] Snapshot saved: {snap_name} violations={all_viol_labels}")

                # ── Queue VLM analysis in background (non-blocking) ──────────
                identified_text = f"Identified person(s): {', '.join(named_persons)}. " if named_persons else ""
                prompt = (
                    f"Workplace safety inspection. "
                    f"PPE violations detected: {', '.join(all_viol_labels)} "
                    f"on {n_persons} worker(s). "
                    f"{identified_text}"
                    "Briefly describe: (1) the work scene and environment, "
                    "what are the workers doing and what equipment is present; "
                    "(2) which PPE is missing and the immediate safety risk. "
                    "3-4 sentences max."
                )
                for p in persons_to_alert:
                    vlm_key = _make_vlm_key(frame_media_id, p["track_id"], p["violations"])
                    bg_vlm.request(vlm_key, frame_path, prompt)

            dt_ms = (time.time() - t0) * 1000
            _debug_log(f"[ppe_detection] Fast worker done. time_ms={dt_ms:.1f}")
            return _group_by_job(output_frames, raw_frames)

        return worker_function_fast

    # ── Decide which worker to return ────────────────────────────────────────

    # Allow disabling the embedded VLM via env var (relay-triggered VLM is preferred
    # when the system is fully set up — set PPE_ENABLE_VLM=0 to use snapshot-only mode).
    ppe_require_person_assoc = _env_bool("PPE_REQUIRE_PERSON_ASSOCIATION", True)
    print(f"[ppe_detection] PPE_REQUIRE_PERSON_ASSOCIATION={ppe_require_person_assoc}")
    ppe_enable_vlm = os.getenv("PPE_ENABLE_VLM", "1").strip().lower() not in ("0", "false", "no", "off")
    if not ppe_enable_vlm:
        print("[ppe_detection] PPE_ENABLE_VLM=0 — snapshot-only mode (relay will trigger VLM separately)")
        VLM_AVAILABLE = False

        if not VLM_AVAILABLE:
            print("[ppe_detection] VLM not available — snapshot-only mode (no VLM analysis)")
            # Even without VLM, we still save snapshots via a stripped-down function
        def worker_function_snapshots(data: Dict[str, Any]) -> Dict[str, Any]:
            t0           = time.time()
            snapshot_dir = os.getenv("SNAPSHOT_DIR", "/tmp/snapshots")
            vlm_cooldown = _env_float("VLM_COOLDOWN_SECONDS", 15.0)
            snap_cooldown = _env_float("SNAPSHOT_COOLDOWN_SECONDS", 30.0)
            always_snapshot_on_violation = _env_bool("PPE_SNAPSHOT_ALWAYS_ON_VIOLATION", True)

            raw_frames    = _run_yolo(data)
            output_frames = _build_output(raw_frames, snapshot_dir)
            raw_by_id = {rf["id"]: rf for rf in raw_frames}
            path_by_id = {rf["id"]: rf.get("_path", "") for rf in raw_frames}

            for frame in output_frames:
                raw_frame = raw_by_id.get(frame.get("id"), {})
                frame_media_id = str(
                    raw_frame.get("_media_id")
                    or data.get("media_id")
                    or data.get("media_idx")
                    or data.get("service_id")
                    or "default"
                )
                frame_media_name = str(
                    raw_frame.get("_media_name")
                    or data.get("media_name")
                    or data.get("service_name")
                    or frame_media_id
                )
                vlm_tracker = TrackerManager.get(frame_media_id) if TRACKING_AVAILABLE else None

                if not frame.get("has_violations"):
                    continue

                detections  = frame.get("detections", [])
                person_dets = [d for d in detections if d.get("class_id") == PERSON_CLASS_ID]

                persons_to_alert: List[Dict[str, Any]] = []
                for person in person_dets:
                    tid        = person.get("track_id", -1)
                    violations = person.get("ppe_violations", [])
                    if not violations:
                        continue
                    violation_key = ",".join(sorted(violations))
                    should = (
                        vlm_tracker.should_alert(tid, violation_key, cooldown_seconds=vlm_cooldown)
                        if vlm_tracker else True
                    )
                    if should:
                        persons_to_alert.append({"track_id": tid, "violations": violations})

                # Optional legacy behavior: unassociated NO-* violations.
                # Default hardened mode requires NO-* to be person-associated.
                if not ppe_require_person_assoc:
                    unassociated = [
                        d.get("label")
                        for d in detections
                        if d.get("class_id") in NO_PPE_CLASS_IDS and d.get("person_track_id") is None
                    ]
                    unassociated = [v for v in unassociated if isinstance(v, str) and v]
                    if unassociated:
                        uniq_unassoc = list(dict.fromkeys(unassociated))
                        violation_key = ",".join(sorted(uniq_unassoc))
                        should = (
                            vlm_tracker.should_alert(-2, violation_key, cooldown_seconds=vlm_cooldown)
                            if vlm_tracker else True
                        )
                        if should:
                            persons_to_alert.append({"track_id": -2, "violations": uniq_unassoc})

                if not persons_to_alert:
                    frame_labels = frame.get("violation_labels", [])
                    if isinstance(frame_labels, list):
                        generic_labels = [v for v in frame_labels if isinstance(v, str) and v]
                    else:
                        generic_labels = []
                    if frame.get("fall_detected") and "Fall-Detected" not in generic_labels:
                        generic_labels.insert(0, "Fall-Detected")
                    if generic_labels:
                        persons_to_alert.append({"track_id": -3, "violations": generic_labels})
                    else:
                        continue

                frame_path = path_by_id.get(frame["id"], "")
                if not frame_path or not os.path.exists(frame_path):
                    continue

                all_viol_labels = list(dict.fromkeys(
                    v for p in persons_to_alert for v in p["violations"]
                ))
                snap_key = f"{frame_media_id}:{','.join(sorted(all_viol_labels))}"
                now_snap = time.time()
                critical_violation = "Fall-Detected" in all_viol_labels
                should_snapshot = (
                    always_snapshot_on_violation
                    or critical_violation
                    or (now_snap - _last_snapshot_time.get(snap_key, 0.0) >= snap_cooldown)
                )
                if should_snapshot:
                    _last_snapshot_time[snap_key] = now_snap
                    snap_name     = f"{frame['id']}_ppe_violation.jpg"
                    snapshot_path = os.path.join(snapshot_dir, snap_name)
                    _save_snapshot_with_bboxes(frame_path, snapshot_path, detections, camera_name=frame_media_name)
                    frame["snapshot_path"] = snapshot_path
                    _debug_log(f"[ppe_detection] Snapshot: {snap_name} violations={all_viol_labels}")

            dt_ms = (time.time() - t0) * 1000
            _debug_log(f"[ppe_detection] Snapshots-only worker done. time_ms={dt_ms:.1f}")
            return _group_by_job(output_frames, raw_frames)

        return worker_function_snapshots

    try:
        vlm = VLMHelper.get_instance()
        bg_vlm = _BgVLM(vlm)
        print("[ppe_detection] Background VLM integration enabled (non-blocking)")
        return _make_worker_with_bg_vlm(bg_vlm)
    except Exception as e:
        print(f"[ppe_detection] VLM init failed: {e} — snapshot-only mode")
        # Fall through to snapshot-only worker
        def worker_function_snapshots_fallback(data: Dict[str, Any]) -> Dict[str, Any]:
            snapshot_dir = os.getenv("SNAPSHOT_DIR", "/tmp/snapshots")
            vlm_cooldown = _env_float("VLM_COOLDOWN_SECONDS", 15.0)
            snap_cooldown = _env_float("SNAPSHOT_COOLDOWN_SECONDS", 30.0)
            always_snapshot_on_violation = _env_bool("PPE_SNAPSHOT_ALWAYS_ON_VIOLATION", True)

            raw_frames    = _run_yolo(data)
            output_frames = _build_output(raw_frames, snapshot_dir)
            raw_by_id = {rf["id"]: rf for rf in raw_frames}
            path_by_id = {rf["id"]: rf.get("_path", "") for rf in raw_frames}

            for frame in output_frames:
                raw_frame = raw_by_id.get(frame.get("id"), {})
                frame_media_id = str(
                    raw_frame.get("_media_id")
                    or data.get("media_id")
                    or data.get("media_idx")
                    or data.get("service_id")
                    or "default"
                )
                frame_media_name = str(
                    raw_frame.get("_media_name")
                    or data.get("media_name")
                    or data.get("service_name")
                    or frame_media_id
                )
                vlm_tracker = TrackerManager.get(frame_media_id) if TRACKING_AVAILABLE else None

                if not frame.get("has_violations"):
                    continue
                detections  = frame.get("detections", [])
                person_dets = [d for d in detections if d.get("class_id") == PERSON_CLASS_ID]
                persons_to_alert = []
                for person in person_dets:
                    tid        = person.get("track_id", -1)
                    violations = person.get("ppe_violations", [])
                    if not violations:
                        continue
                    violation_key = ",".join(sorted(violations))
                    should = (
                        vlm_tracker.should_alert(tid, violation_key, cooldown_seconds=vlm_cooldown)
                        if vlm_tracker else True
                    )
                    if should:
                        persons_to_alert.append({"track_id": tid, "violations": violations})

                # Optional legacy behavior: unassociated NO-* violations.
                # Default hardened mode requires NO-* to be person-associated.
                if not ppe_require_person_assoc:
                    unassociated = [
                        d.get("label")
                        for d in detections
                        if d.get("class_id") in NO_PPE_CLASS_IDS and d.get("person_track_id") is None
                    ]
                    unassociated = [v for v in unassociated if isinstance(v, str) and v]
                    if unassociated:
                        uniq_unassoc = list(dict.fromkeys(unassociated))
                        violation_key = ",".join(sorted(uniq_unassoc))
                        should = (
                            vlm_tracker.should_alert(-2, violation_key, cooldown_seconds=vlm_cooldown)
                            if vlm_tracker else True
                        )
                        if should:
                            persons_to_alert.append({"track_id": -2, "violations": uniq_unassoc})

                if not persons_to_alert:
                    frame_labels = frame.get("violation_labels", [])
                    if isinstance(frame_labels, list):
                        generic_labels = [v for v in frame_labels if isinstance(v, str) and v]
                    else:
                        generic_labels = []
                    if frame.get("fall_detected") and "Fall-Detected" not in generic_labels:
                        generic_labels.insert(0, "Fall-Detected")
                    if generic_labels:
                        persons_to_alert.append({"track_id": -3, "violations": generic_labels})
                    else:
                        continue
                frame_path = path_by_id.get(frame["id"], "")
                if not frame_path or not os.path.exists(frame_path):
                    continue
                all_viol_labels = list(dict.fromkeys(
                    v for p in persons_to_alert for v in p["violations"]
                ))
                snap_key = f"{frame_media_id}:{','.join(sorted(all_viol_labels))}"
                now_snap = time.time()
                critical_violation = "Fall-Detected" in all_viol_labels
                should_snapshot = (
                    always_snapshot_on_violation
                    or critical_violation
                    or (now_snap - _last_snapshot_time.get(snap_key, 0.0) >= snap_cooldown)
                )
                if should_snapshot:
                    _last_snapshot_time[snap_key] = now_snap
                    snap_name     = f"{frame['id']}_ppe_violation.jpg"
                    snapshot_path = os.path.join(snapshot_dir, snap_name)
                    _save_snapshot_with_bboxes(frame_path, snapshot_path, detections, camera_name=frame_media_name)
                    frame["snapshot_path"] = snapshot_path
            return _group_by_job(output_frames, raw_frames)

        return worker_function_snapshots_fallback


if __name__ == "__main__":
    worker_function = load_ai_model()
    asyncio.run(client_handler(worker_function))
