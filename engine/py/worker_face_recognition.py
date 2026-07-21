"""Face Recognition Worker — InsightFace one-shot person matching.

This worker consumes frame batches (filepath-based), detects faces, matches them
against the relay person registry, and emits structured detection events with:
  - face bbox + confidence + track_id
  - person_id/person_name when matched
  - first_detection flags
  - snapshot_path + brief AI-style summary for matched/new faces

ENV vars:
  RELAY_HTTP_BASE           — relay HTTP base URL (default: http://localhost:8020)
  FACE_CONF                 — detection score threshold (default: 0.35)
  FACE_MATCH_THRESHOLD      — cosine similarity threshold (default: 0.45)
  FACE_REGISTRY_REFRESH_S   — seconds between registry refreshes (default: 60)
  SNAPSHOT_DIR              — local snapshot dir (default: /tmp/snapshots)
  SNAPSHOT_COOLDOWN_SECONDS — min seconds between saved snapshots per camera (default: 20)
  TRACKER_FRAME_RATE        — ByteTrack expected FPS (default: 10)
"""

import asyncio
import io
import os
import shutil
import threading
import time
from typing import Any, Dict, List, Optional, Set, Tuple

import numpy as np
import requests
from PIL import Image, ImageDraw

from ws_client_handler import client_handler

try:
    import face_recognizer as _shared_face_rec
    SHARED_FACE_RECOGNIZER_AVAILABLE = True
except Exception as e:
    print(f"[face_recognition] Shared face recognizer not available: {e}")
    SHARED_FACE_RECOGNIZER_AVAILABLE = False

try:
    from tracker import TrackerManager
    TRACKING_AVAILABLE = True
except Exception as e:
    print(f"[face_recognition] Tracker not available: {e}")
    TRACKING_AVAILABLE = False

try:
    from insightface.app import FaceAnalysis as _InsightFaceAnalysis
    _iface_app: Optional[_InsightFaceAnalysis] = None
    INSIGHTFACE_AVAILABLE = True
except Exception as e:
    print(f"[face_recognition] InsightFace not available: {e} — install with: pip install insightface onnxruntime")
    INSIGHTFACE_AVAILABLE = False

_registry_lock = threading.Lock()
_known_faces: List[Dict[str, Any]] = []
_registry_loaded = threading.Event()
_snap_cooldowns: Dict[str, float] = {}


def _legacy_mode_enabled() -> bool:
    return str(os.getenv("LEGACY_MODE", "")).strip().lower() in ("1", "true", "yes", "on")


def _resolve_insightface_runtime() -> Tuple[List[str], int]:
    disable_gpu = os.getenv("DISABLE_GPU", "0") == "1"
    if disable_gpu:
        print("[face_recognition] DISABLE_GPU=1, forcing CPUExecutionProvider")
        return ["CPUExecutionProvider"], -1

    try:
        import onnxruntime as ort
        providers = ort.get_available_providers()
    except Exception as e:
        print(f"[face_recognition] Could not query ONNX Runtime providers: {e}")
        return ["CPUExecutionProvider"], -1

    if "CUDAExecutionProvider" in providers:
        return ["CUDAExecutionProvider", "CPUExecutionProvider"], 0

    print(
        "[face_recognition] CUDAExecutionProvider not available; "
        f"running on CPU. Providers={providers}"
    )
    return ["CPUExecutionProvider"], -1


def _get_insightface():
    global _iface_app
    if _iface_app is not None:
        return _iface_app
    if not INSIGHTFACE_AVAILABLE:
        return None

    providers, ctx_id = _resolve_insightface_runtime()
    model_name = "buffalo_sc" if _legacy_mode_enabled() else (str(os.getenv("FACE_PRIMARY_MODEL", "buffalo_sc")).strip() or "buffalo_sc")
    app = _InsightFaceAnalysis(
        name=model_name,
        providers=providers,
    )
    app.prepare(ctx_id=ctx_id, det_size=(640, 640))
    _iface_app = app
    print(f"[face_recognition] InsightFace loaded (ctx_id={ctx_id}, providers={providers})")
    return app


def _cosine_similarity(a: np.ndarray, b: np.ndarray) -> float:
    norm_a = np.linalg.norm(a)
    norm_b = np.linalg.norm(b)
    if norm_a == 0 or norm_b == 0:
        return 0.0
    return float(np.dot(a, b) / (norm_a * norm_b))


def _profile_views(img: Image.Image) -> List[np.ndarray]:
    """One-shot profile expansion from a single reference photo.

    We generate small geometric variants so matching remains robust for mild
    pose/yaw differences without requiring multiple uploaded photos.
    """
    base = img.convert("RGB")
    variants = [
        np.array(base),
        np.fliplr(np.array(base)),
        np.array(base.rotate(-8, resample=Image.BICUBIC, expand=False).convert("RGB")),
        np.array(base.rotate(8, resample=Image.BICUBIC, expand=False).convert("RGB")),
    ]
    return variants


def _refresh_registry():
    if not _legacy_mode_enabled() and SHARED_FACE_RECOGNIZER_AVAILABLE and hasattr(_shared_face_rec, "start_registry_refresh"):
        try:
            _shared_face_rec.start_registry_refresh()
            shared_ready = getattr(_shared_face_rec, "_registry_loaded", None)
            if shared_ready is not None and hasattr(shared_ready, "wait"):
                shared_ready.wait(timeout=5.0)
            _registry_loaded.set()
            return
        except Exception as e:
            print(f"[face_recognition] Shared registry refresh unavailable: {e}")

    relay_base = os.getenv("RELAY_HTTP_BASE", "http://localhost:8020")
    try:
        resp = requests.get(f"{relay_base}/persons/registry", timeout=5)
        resp.raise_for_status()
        entries = resp.json()
    except Exception as e:
        print(f"[face_recognition] Failed to fetch persons registry: {e}")
        _registry_loaded.set()
        return

    app = _get_insightface()
    if app is None:
        print("[face_recognition] InsightFace not available — cannot extract embeddings")
        _registry_loaded.set()
        return

    new_known: List[Dict[str, Any]] = []
    for entry in entries:
        pid = entry.get("person_id", "")
        name = entry.get("name", "unknown")
        if not pid:
            continue
        profile_images: List[Tuple[str, Image.Image]] = []

        # New multi-profile endpoint.
        try:
            prof_resp = requests.get(f"{relay_base}/persons/{pid}/faces", timeout=5)
            prof_resp.raise_for_status()
            profiles = prof_resp.json()
            if isinstance(profiles, list):
                for prof in profiles:
                    prof_id = str((prof or {}).get("id", "")).strip()
                    if not prof_id:
                        continue
                    try:
                        img_resp = requests.get(
                            f"{relay_base}/persons/{pid}/faces/{prof_id}",
                            timeout=5,
                        )
                        img_resp.raise_for_status()
                        img = Image.open(io.BytesIO(img_resp.content)).convert("RGB")
                        profile_images.append((prof_id, img))
                    except Exception as e:
                        print(f"[face_recognition] Error loading profile {prof_id} for {name} ({pid}): {e}")
        except Exception:
            pass

        # Legacy fallback for compatibility.
        if not profile_images:
            try:
                img_resp = requests.get(f"{relay_base}/persons/{pid}/face", timeout=5)
                img_resp.raise_for_status()
                img = Image.open(io.BytesIO(img_resp.content)).convert("RGB")
                profile_images.append(("legacy", img))
            except Exception as e:
                print(f"[face_recognition] Error loading legacy face for {name} ({pid}): {e}")
                continue

        person_embs = 0
        for profile_id, img in profile_images:
            embs: List[np.ndarray] = []
            for candidate in _profile_views(img):
                faces = app.get(candidate)
                if not faces:
                    continue
                face = max(faces, key=lambda f: (f.bbox[2] - f.bbox[0]) * (f.bbox[3] - f.bbox[1]))
                embs.append(face.normed_embedding)

            if not embs:
                print(f"[face_recognition] No face detected in profile {profile_id} for {name} ({pid})")
                continue

            for emb in embs:
                new_known.append({"person_id": pid, "name": name, "embedding": emb})
            person_embs += len(embs)

        print(f"[face_recognition] Loaded {person_embs} profile embedding(s) for {name} ({pid})")

    with _registry_lock:
        _known_faces.clear()
        _known_faces.extend(new_known)

    _registry_loaded.set()
    print(f"[face_recognition] Registry loaded: {len(new_known)} profile embedding(s)")


def _registry_refresh_loop():
    refresh_interval = float(os.getenv("FACE_REGISTRY_REFRESH_S", "60"))
    _refresh_registry()
    while True:
        time.sleep(refresh_interval)
        _refresh_registry()


def _match_embedding_result(emb: np.ndarray) -> Dict[str, Any]:
    if not _legacy_mode_enabled() and SHARED_FACE_RECOGNIZER_AVAILABLE and hasattr(_shared_face_rec, "_match_embedding"):
        try:
            result = _shared_face_rec._match_embedding(emb)
            if isinstance(result, dict):
                return result
        except Exception as e:
            print(f"[face_recognition] Shared matcher failed, falling back to local matcher: {e}")

    threshold = float(os.getenv("FACE_MATCH_THRESHOLD", "0.45"))
    weak_threshold = float(os.getenv("FACE_MATCH_WEAK_THRESHOLD", "0.42"))
    best_id, best_name, best_sim = None, None, 0.0
    with _registry_lock:
        for known in _known_faces:
            sim = _cosine_similarity(emb, known["embedding"])
            if sim > best_sim:
                best_sim = sim
                best_id = known["person_id"]
                best_name = known["name"]

    if best_sim >= threshold:
        return {
            "person_id": best_id,
            "person_name": best_name,
            "match_score": round(best_sim, 3),
            "confidence_tier": "strong",
            "candidate_person_id": best_id,
            "candidate_person_name": best_name,
            "candidate_score": round(best_sim, 3),
            "match_margin": round(best_sim, 3),
        }
    return {
        "confidence_tier": "weak" if best_sim >= weak_threshold else "unknown",
        "candidate_person_id": best_id,
        "candidate_person_name": best_name,
        "candidate_score": round(best_sim, 3),
        "match_margin": 0.0,
    }


def _strong_identity_from_match(result: Dict[str, Any]) -> Tuple[Optional[str], Optional[str], float]:
    if not isinstance(result, dict):
        return None, None, 0.0
    person_id = str(result.get("person_id") or "").strip()
    if not person_id:
        return None, None, float(result.get("candidate_score", 0.0) or 0.0)
    person_name = str(result.get("person_name") or "").strip() or person_id
    score = float(result.get("match_score", result.get("candidate_score", 0.0)) or 0.0)
    return person_id, person_name, score


def _match_embedding(emb: np.ndarray) -> Tuple[Optional[str], Optional[str], float]:
    return _strong_identity_from_match(_match_embedding_result(emb))


def _apply_match_metadata(det: Dict[str, Any], result: Dict[str, Any]) -> None:
    if not isinstance(result, dict):
        return
    for key in (
        "confidence_tier",
        "candidate_person_id",
        "candidate_person_name",
        "candidate_score",
        "match_margin",
        "max_sim",
        "top_k_mean",
        "proto_sim",
        "match_stage",
        "secondary_used",
        "secondary_available",
        "secondary_candidate_score",
        "secondary_match_margin",
    ):
        value = result.get(key)
        if value not in (None, ""):
            det[key] = value


def _coerce_track_id(value: Any) -> Optional[int]:
    if value is None or isinstance(value, bool):
        return None
    try:
        return int(value)
    except Exception:
        return None


def _coerce_float(value: Any) -> Optional[float]:
    if value is None or isinstance(value, bool):
        return None
    try:
        return float(value)
    except Exception:
        return None


def _normalize_lookup_box(raw: Any) -> Optional[Dict[str, float]]:
    if not isinstance(raw, dict):
        return None
    x1 = _coerce_float(raw.get("x_min"))
    y1 = _coerce_float(raw.get("y_min"))
    x2 = _coerce_float(raw.get("x_max"))
    y2 = _coerce_float(raw.get("y_max"))
    if None in (x1, y1, x2, y2):
        return None
    if x2 <= x1 or y2 <= y1:
        return None
    return {
        "x_min": float(x1),
        "y_min": float(y1),
        "x_max": float(x2),
        "y_max": float(y2),
    }


def _normalize_box(box_px: Dict[str, float], image_width: int, image_height: int) -> Dict[str, float]:
    w = max(float(image_width), 1.0)
    h = max(float(image_height), 1.0)
    return {
        "x_min": round(float(box_px.get("x_min", 0.0)) / w, 6),
        "y_min": round(float(box_px.get("y_min", 0.0)) / h, 6),
        "x_max": round(float(box_px.get("x_max", 0.0)) / w, 6),
        "y_max": round(float(box_px.get("y_max", 0.0)) / h, 6),
    }


def _bbox_area(box: Dict[str, float]) -> float:
    return max(0.0, float(box["x_max"]) - float(box["x_min"])) * max(0.0, float(box["y_max"]) - float(box["y_min"]))


def _bbox_intersection(a: Dict[str, float], b: Dict[str, float]) -> float:
    x1 = max(float(a["x_min"]), float(b["x_min"]))
    y1 = max(float(a["y_min"]), float(b["y_min"]))
    x2 = min(float(a["x_max"]), float(b["x_max"]))
    y2 = min(float(a["y_max"]), float(b["y_max"]))
    if x2 <= x1 or y2 <= y1:
        return 0.0
    return (x2 - x1) * (y2 - y1)


def _face_box_to_full(face: Any, offset_x: float = 0.0, offset_y: float = 0.0) -> Optional[Dict[str, float]]:
    bbox = getattr(face, "bbox", None)
    if bbox is None or len(bbox) < 4:
        return None
    return {
        "x_min": round(float(bbox[0]) + offset_x, 2),
        "y_min": round(float(bbox[1]) + offset_y, 2),
        "x_max": round(float(bbox[2]) + offset_x, 2),
        "y_max": round(float(bbox[3]) + offset_y, 2),
    }


def _select_best_face(faces: List[Any], target_box: Dict[str, float], offset_x: float = 0.0, offset_y: float = 0.0) -> Tuple[Optional[Any], Optional[Dict[str, float]], float]:
    best_face: Optional[Any] = None
    best_box: Optional[Dict[str, float]] = None
    best_score = -1.0
    for face in faces:
        full_box = _face_box_to_full(face, offset_x=offset_x, offset_y=offset_y)
        if full_box is None:
            continue
        area = _bbox_area(full_box)
        if area <= 0.0:
            continue
        overlap = _bbox_intersection(full_box, target_box)
        coverage = overlap / area if area > 0 else 0.0
        cx = (float(full_box["x_min"]) + float(full_box["x_max"])) / 2.0
        cy = (float(full_box["y_min"]) + float(full_box["y_max"])) / 2.0
        center_inside = (
            float(target_box["x_min"]) <= cx <= float(target_box["x_max"])
            and float(target_box["y_min"]) <= cy <= float(target_box["y_max"])
        )
        det_score = float(getattr(face, "det_score", 0.0) or 0.0)
        score = (coverage * 3.0) + (0.6 if center_inside else 0.0) + det_score
        if score > best_score:
            best_face = face
            best_box = full_box
            best_score = score
    return best_face, best_box, best_score


def _lookup_face_in_bbox(app: Any, img_np: np.ndarray, person_bbox: Optional[Dict[str, float]]) -> Tuple[Optional[np.ndarray], Optional[Dict[str, float]], float]:
    h, w = img_np.shape[:2]
    faces: List[Any] = []
    offset_x = 0.0
    offset_y = 0.0

    if person_bbox:
        pad_x = max(8.0, (float(person_bbox["x_max"]) - float(person_bbox["x_min"])) * 0.18)
        pad_y = max(8.0, (float(person_bbox["y_max"]) - float(person_bbox["y_min"])) * 0.25)
        cx1 = max(0, int(float(person_bbox["x_min"]) - pad_x))
        cy1 = max(0, int(float(person_bbox["y_min"]) - pad_y))
        cx2 = min(w, int(float(person_bbox["x_max"]) + pad_x))
        cy2 = min(h, int(float(person_bbox["y_max"]) + pad_y))
        if cx2 > cx1 and cy2 > cy1:
            crop = img_np[cy1:cy2, cx1:cx2]
            if crop.size > 0:
                try:
                    faces = app.get(crop)
                    offset_x = float(cx1)
                    offset_y = float(cy1)
                except Exception as e:
                    print(f"[face_recognition] Targeted crop inference error: {e}")

    target_box = person_bbox or {
        "x_min": 0.0,
        "y_min": 0.0,
        "x_max": float(w),
        "y_max": float(h),
    }
    face, face_box, _ = _select_best_face(faces, target_box, offset_x=offset_x, offset_y=offset_y)
    if face is None:
        try:
            full_faces = app.get(img_np)
        except Exception as e:
            print(f"[face_recognition] Full-frame fallback inference error: {e}")
            full_faces = []
        face, face_box, _ = _select_best_face(full_faces, target_box)

    if face is None:
        return None, None, 0.0

    emb = getattr(face, "normed_embedding", None)
    if emb is None:
        return None, face_box, float(getattr(face, "det_score", 0.0) or 0.0)
    return emb, face_box, float(getattr(face, "det_score", 0.0) or 0.0)


def _make_lookup_detection(
    *,
    track_id: Optional[int],
    box_px: Optional[Dict[str, float]],
    image_width: int,
    image_height: int,
    confidence: float,
    person_id: Optional[str] = None,
    person_name: Optional[str] = None,
    match_score: float = 0.0,
) -> Dict[str, Any]:
    fallback_box = box_px or {
        "x_min": 0.0,
        "y_min": 0.0,
        "x_max": float(max(image_width, 1)),
        "y_max": float(max(image_height, 1)),
    }
    det: Dict[str, Any] = {
        "label": person_name if person_name else "unknown_face",
        "confidence": float(confidence),
        "class_id": 0,
        "box_px": {
            "x_min": round(float(fallback_box["x_min"]), 2),
            "y_min": round(float(fallback_box["y_min"]), 2),
            "x_max": round(float(fallback_box["x_max"]), 2),
            "y_max": round(float(fallback_box["y_max"]), 2),
        },
        "box": _normalize_box(fallback_box, image_width, image_height),
    }
    if track_id is not None and track_id >= 0:
        det["track_id"] = track_id
    if person_id:
        det["person_id"] = person_id
    if person_name:
        det["person_name"] = person_name
        det["match_score"] = round(float(match_score), 3)
    return det


def _flatten_inputs(payload: Dict[str, Any]) -> List[Dict[str, Any]]:
    items = payload.get("inputs", [])
    if not isinstance(items, list):
        return []

    payload_media_id = (
        payload.get("media_id")
        or payload.get("media_idx")
        or payload.get("service_id")
        or "default"
    )
    payload_media_name = (
        payload.get("media_name")
        or payload.get("service_name")
        or payload_media_id
    )

    flat: List[Dict[str, Any]] = []
    for it in items:
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
                    flat.append(frame)
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
            flat.append(frame)

    out: List[Dict[str, Any]] = []
    for x in flat:
        fid = x.get("id")
        fp = x.get("filepath") or x.get("path")
        media_id = x.get("_media_id") or payload_media_id
        media_name = x.get("_media_name") or media_id
        if fid and fp:
            out.append({
                "id": str(fid),
                "filepath": str(fp),
                "_job_id": x.get("_job_id"),
                "_media_id": str(media_id),
                "_media_name": str(media_name),
                "_face_lookup": bool(x.get("_face_lookup")),
                "_track_id": _coerce_track_id(x.get("track_id")),
                "_person_bbox": _normalize_lookup_box(x.get("person_bbox")),
            })
    return out


def _draw_face_snapshot(frame_path: str, output_path: str, detections: List[Dict[str, Any]], camera_name: str = "") -> None:
    try:
        img = Image.open(frame_path).convert("RGB")
        draw = ImageDraw.Draw(img)

        for d in detections:
            b = d.get("box_px", {})
            x1, y1 = int(b.get("x_min", 0)), int(b.get("y_min", 0))
            x2, y2 = int(b.get("x_max", 0)), int(b.get("y_max", 0))
            name = d.get("person_name")
            tid = d.get("track_id", -1)
            conf = int(float(d.get("confidence", 0.0)) * 100)

            matched = bool(name)
            color = (50, 210, 120) if matched else (245, 190, 60)
            label = f"#{tid} {name}" if (matched and tid is not None and tid >= 0) else (
                f"#{tid} unknown" if tid is not None and tid >= 0 else "unknown"
            )
            draw.rectangle([x1, y1, x2, y2], outline=color, width=2)
            text = f"{label} {conf}%"
            text_box = draw.textbbox((0, 0), text)
            text_h = max(12, int(text_box[3] - text_box[1]))
            text_y = min(max(0, y2 - text_h - 2), max(0, img.height - text_h - 2))
            draw.text((x1, text_y), text, fill=color)

        if camera_name:
            w, h = img.size
            draw.rectangle([0, h - 20, w, h], fill=(0, 0, 0, 180))
            draw.text((6, h - 16), f"{camera_name}", fill=(255, 255, 255))

        img.save(output_path, "JPEG", quality=90)
    except Exception as exc:
        shutil.copy2(frame_path, output_path)
        print(f"[face_recognition] snapshot draw failed ({exc}), used raw copy")


def _group_output(inputs_list: List[Dict[str, Any]], outputs: List[Dict[str, Any]]) -> Dict[str, Any]:
    job_id_map: Dict[str, List[Dict[str, Any]]] = {}
    for idx, out_item in enumerate(outputs):
        inp = inputs_list[idx] if idx < len(inputs_list) else {}
        jid = inp.get("_job_id")
        if jid:
            job_id_map.setdefault(jid, []).append(out_item)

    if job_id_map:
        return {"output": [{"id": jid, "output": items} for jid, items in job_id_map.items()]}
    return {"output": outputs}


def heavy_ai_workload(data: Dict[str, Any]) -> Dict[str, Any]:
    conf_thr = float(os.getenv("FACE_CONF", "0.35"))
    frame_rate = int(os.getenv("TRACKER_FRAME_RATE", "10"))
    snapshot_dir = os.getenv("SNAPSHOT_DIR", "/tmp/snapshots")
    snap_cooldown = float(os.getenv("SNAPSHOT_COOLDOWN_SECONDS", "20"))

    os.makedirs(snapshot_dir, exist_ok=True)

    app = _get_insightface()
    if app is None:
        return {"output": []}

    _registry_loaded.wait(timeout=5.0)

    inputs_list = _flatten_inputs(data)
    if not inputs_list:
        return {"output": []}

    tracker_by_media: Dict[str, Any] = {}
    touched_media_ids: Set[str] = set()

    outputs: List[Dict[str, Any]] = []

    for inp in inputs_list:
        frame_id = inp["id"]
        frame_path = inp["filepath"]
        frame_media_id = str(inp.get("_media_id") or "default")
        frame_media_name = str(inp.get("_media_name") or frame_media_id)
        lookup_mode = bool(inp.get("_face_lookup"))
        lookup_track_id = _coerce_track_id(inp.get("_track_id"))
        lookup_bbox = _normalize_lookup_box(inp.get("_person_bbox"))
        tracker = None
        if TRACKING_AVAILABLE and not lookup_mode:
            tracker = tracker_by_media.get(frame_media_id)
            if tracker is None:
                tracker = TrackerManager.get(frame_media_id, frame_rate=frame_rate)
                tracker_by_media[frame_media_id] = tracker
            touched_media_ids.add(frame_media_id)

        if not frame_path or not os.path.exists(frame_path):
            outputs.append({"id": frame_id, "image_width": 0, "image_height": 0, "detections": []})
            continue

        try:
            img = Image.open(frame_path).convert("RGB")
            img_np = np.array(img)
        except Exception as e:
            print(f"[face_recognition] Failed to decode frame {frame_id}: {e}")
            outputs.append({"id": frame_id, "image_width": 0, "image_height": 0, "detections": []})
            continue

        h, w = img_np.shape[:2]

        detections: List[Dict[str, Any]] = []
        if lookup_mode:
            emb, detected_box, detected_conf = _lookup_face_in_bbox(app, img_np, lookup_bbox)
            match_result: Dict[str, Any] = {}
            person_id, person_name, sim = None, None, 0.0
            if emb is not None:
                match_result = _match_embedding_result(emb)
                person_id, person_name, sim = _strong_identity_from_match(match_result)
            det = _make_lookup_detection(
                track_id=lookup_track_id,
                box_px=detected_box or lookup_bbox,
                image_width=w,
                image_height=h,
                confidence=detected_conf,
                person_id=person_id,
                person_name=person_name,
                match_score=sim,
            )
            _apply_match_metadata(det, match_result)
            detections.append(det)
        else:
            try:
                faces = app.get(img_np)
            except Exception as e:
                print(f"[face_recognition] InsightFace inference error: {e}")
                outputs.append({"id": frame_id, "image_width": w, "image_height": h, "detections": []})
                continue

            for face in faces:
                if float(face.det_score) < conf_thr:
                    continue

                x1, y1, x2, y2 = face.bbox.tolist()
                emb = getattr(face, "normed_embedding", None)
                match_result: Dict[str, Any] = {}
                person_id, person_name, sim = None, None, 0.0
                if emb is not None:
                    match_result = _match_embedding_result(emb)
                    person_id, person_name, sim = _strong_identity_from_match(match_result)

                det: Dict[str, Any] = {
                    "label": person_name if person_name else "unknown_face",
                    "confidence": float(face.det_score),
                    "class_id": 0,
                    "box": {
                        "x_min": round(x1 / w, 6),
                        "y_min": round(y1 / h, 6),
                        "x_max": round(x2 / w, 6),
                        "y_max": round(y2 / h, 6),
                    },
                    "box_px": {
                        "x_min": round(x1, 2),
                        "y_min": round(y1, 2),
                        "x_max": round(x2, 2),
                        "y_max": round(y2, 2),
                    },
                }
                if person_id:
                    det["person_id"] = person_id
                    det["person_name"] = person_name
                    det["match_score"] = round(sim, 3)
                _apply_match_metadata(det, match_result)
                detections.append(det)

        if tracker and detections:
            detections = tracker.update(detections)
            for det in detections:
                tid = int(det.get("track_id", -1))
                if tid < 0:
                    continue

                # Keep identity sticky across temporary low-quality face crops.
                if det.get("person_name"):
                    tracker.set_identity(tid, {
                        "person_id": det.get("person_id"),
                        "person_name": det.get("person_name"),
                        "match_score": det.get("match_score"),
                    })
                elif tracker.has_identity(tid):
                    cached = tracker.get_identity(tid) or {}
                    if cached.get("person_name"):
                        det["person_name"] = cached.get("person_name")
                        det["person_id"] = cached.get("person_id")
                        det["match_score"] = cached.get("match_score")

                if tracker.is_first_seen(tid):
                    det["first_detection"] = True
                    tracker.mark_seen(tid)
            tracker.cleanup_stale()

        matched_names = list(dict.fromkeys(
            d.get("person_name")
            for d in detections
            if isinstance(d.get("person_name"), str) and d.get("person_name")
        ))
        first_detection = False if lookup_mode else any(bool(d.get("first_detection")) for d in detections)

        event_brief = ""
        if matched_names:
            event_brief = f"Recognized: {', '.join(matched_names[:3])}"
        elif first_detection:
            event_brief = "New face detected"
        elif detections:
            event_brief = f"Face activity observed ({len(detections)} face(s))"

        ai_summary = ""
        if matched_names:
            ai_summary = (
                f"Known person(s) identified on camera {frame_media_name}: "
                f"{', '.join(matched_names[:3])}. Continue monitoring for behaviour changes."
            )
        elif first_detection:
            ai_summary = f"A new unrecognized face appeared on camera {frame_media_name}."

        out_item: Dict[str, Any] = {
            "id": frame_id,
            "image_width": w,
            "image_height": h,
            "detections": detections,
            "person_count": len(detections),
            "matched_count": len(matched_names),
            "first_detection": first_detection,
            "severity": "medium" if (matched_names or first_detection) else "low",
            "event_brief": event_brief,
        }
        if lookup_mode:
            out_item["internal_only"] = True
        if matched_names:
            out_item["person_names"] = matched_names
        if ai_summary:
            out_item["vlm_summary"] = ai_summary

        # Snapshot only for meaningful face events to avoid flooding storage.
        should_snapshot = matched_names if lookup_mode else (matched_names or first_detection)
        if detections and should_snapshot:
            snap_key = f"{frame_media_id}:face"
            now_t = time.time()
            if now_t - _snap_cooldowns.get(snap_key, 0.0) >= snap_cooldown:
                _snap_cooldowns[snap_key] = now_t
                snap_name = f"{frame_id}_face.jpg"
                snap_path = os.path.join(snapshot_dir, snap_name)
                _draw_face_snapshot(frame_path, snap_path, detections, camera_name=frame_media_name)
                out_item["snapshot_path"] = snap_path

        outputs.append(out_item)

    for mid in touched_media_ids:
        tracker = tracker_by_media.get(mid)
        if tracker is not None:
            tracker.cleanup_stale()

    return _group_output(inputs_list, outputs)


if __name__ == "__main__":
    if not _legacy_mode_enabled() and SHARED_FACE_RECOGNIZER_AVAILABLE and hasattr(_shared_face_rec, "start_registry_refresh"):
        _shared_face_rec.start_registry_refresh()
        _registry_loaded.set()
    else:
        t = threading.Thread(target=_registry_refresh_loop, daemon=True, name="FaceRegistryRefresh")
        t.start()
    asyncio.run(client_handler(heavy_ai_workload))
