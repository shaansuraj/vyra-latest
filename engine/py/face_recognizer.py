"""Shared face recognition module — InsightFace singleton + person registry.

Includes a feature-flagged v2 matcher with:
- composite similarity scoring (max/top-k/prototype)
- confidence tiers (strong/weak/unknown)
- optional adaptive secondary model stage for uncertain cases
"""

from __future__ import annotations

import io
import logging
import os
import threading
import time
from concurrent.futures import ThreadPoolExecutor
from typing import Any, Callable, Dict, List, Optional, Tuple

import cv2
import numpy as np
import requests
from PIL import Image, ImageOps

logger = logging.getLogger(__name__)

# ── Feature flags / config ───────────────────────────────────────────────────


def _env_bool(name: str, default: bool = False) -> bool:
    raw = os.getenv(name)
    if raw is None:
        return default
    return str(raw).strip().lower() in ("1", "true", "yes", "on")


def _env_int(name: str, default: int) -> int:
    raw = os.getenv(name)
    if raw is None:
        return default
    try:
        return int(raw)
    except Exception:
        return default


def _env_float(name: str, default: float) -> float:
    raw = os.getenv(name)
    if raw is None:
        return default
    try:
        return float(raw)
    except Exception:
        return default


def _v2_enabled() -> bool:
    # V2 is now the default path; set env FACE_RECOGNITION_V2_ENABLED=0 to opt out.
    return _env_bool("FACE_RECOGNITION_V2_ENABLED", True)


def _min_profiles() -> int:
    return max(1, _env_int("FACE_ENROLLMENT_MIN_PROFILES", 1))


def _diagnostics_enabled() -> bool:
    return _env_bool("FACE_DIAGNOSTICS", False)


def _legacy_mode_enabled() -> bool:
    return _env_bool("LEGACY_MODE", False)


def _primary_model_name() -> str:
    if _legacy_mode_enabled():
        return "buffalo_sc"
    return os.getenv("FACE_PRIMARY_MODEL", "buffalo_sc")


def _secondary_model_name() -> str:
    if _legacy_mode_enabled():
        return "buffalo_l"
    return os.getenv("FACE_SECONDARY_MODEL", "buffalo_l")


def _strong_threshold() -> float:
    if _v2_enabled():
        return _env_float("FACE_MATCH_STRONG_THRESHOLD", 0.52)
    return _env_float("FACE_MATCH_THRESHOLD", 0.45)


def _weak_threshold() -> float:
    return _env_float("FACE_MATCH_WEAK_THRESHOLD", 0.42)


def _min_margin() -> float:
    return _env_float("FACE_MATCH_MIN_MARGIN", 0.035)


def _top_k() -> int:
    return max(1, _env_int("FACE_MATCH_TOP_K", 3))


def _match_threshold_legacy() -> float:
    return _env_float("FACE_MATCH_THRESHOLD", 0.45)


# ── InsightFace apps ─────────────────────────────────────────────────────────

try:
    from insightface.app import FaceAnalysis as _InsightFaceAnalysis
    _INSIGHTFACE_AVAILABLE = True
except Exception as e:
    logger.warning(f"[FaceRecognizer] InsightFace not available: {e} — install with: pip install insightface onnxruntime")
    _INSIGHTFACE_AVAILABLE = False

_iface_apps: Dict[str, Any] = {}
_iface_lock = threading.Lock()
_secondary_load_failed = False


def _resolve_insightface_runtime() -> tuple[list[str], int]:
    disable_gpu = os.getenv("DISABLE_GPU", "0") == "1"
    if disable_gpu:
        logger.info("[FaceRecognizer] DISABLE_GPU=1, forcing CPUExecutionProvider")
        return ["CPUExecutionProvider"], -1

    try:
        import onnxruntime as ort
        providers = ort.get_available_providers()
    except Exception as e:
        logger.warning(f"[FaceRecognizer] Could not query ONNX Runtime providers: {e}")
        return ["CPUExecutionProvider"], -1

    if "CUDAExecutionProvider" in providers:
        return ["CUDAExecutionProvider", "CPUExecutionProvider"], 0

    logger.warning(
        "[FaceRecognizer] CUDAExecutionProvider not available. "
        "Face recognition will run on CPU. Providers=%s",
        providers,
    )
    return ["CPUExecutionProvider"], -1


def _get_insightface(model_name: Optional[str] = None):
    if not _INSIGHTFACE_AVAILABLE:
        return None
    name = (model_name or _primary_model_name()).strip() or _primary_model_name()
    with _iface_lock:
        if name in _iface_apps:
            return _iface_apps[name]
        providers, ctx_id = _resolve_insightface_runtime()
        app = _InsightFaceAnalysis(name=name, providers=providers)
        app.prepare(ctx_id=ctx_id, det_size=(640, 640))
        _iface_apps[name] = app
        logger.info(f"[FaceRecognizer] InsightFace loaded model={name} (ctx_id={ctx_id}, providers={providers})")
        return app


def _get_secondary_app():
    global _secondary_load_failed
    if _secondary_load_failed:
        return None
    if not _v2_enabled():
        return None
    if not _env_bool("FACE_SECONDARY_ENABLED", False):
        return None
    if _secondary_model_name() == _primary_model_name():
        return None
    try:
        return _get_insightface(_secondary_model_name())
    except Exception as e:
        _secondary_load_failed = True
        logger.warning(f"[FaceRecognizer] Secondary model unavailable ({_secondary_model_name()}): {e}")
        return None


# ── Registry state ────────────────────────────────────────────────────────────

_registry_lock = threading.Lock()
_known_faces: List[Dict[str, Any]] = []  # compatibility: points to primary gallery
_known_faces_primary: List[Dict[str, Any]] = []
_known_faces_secondary: List[Dict[str, Any]] = []
_prototypes_primary: Dict[str, Dict[str, Any]] = {}
_prototypes_secondary: Dict[str, Dict[str, Any]] = {}
_last_registry_refresh = 0.0
_registry_loaded = threading.Event()

_diag_lock = threading.Lock()
_diag_metrics: Dict[str, int] = {
    "attempts": 0,
    "strong_matches": 0,
    "weak_candidates": 0,
    "unknown_results": 0,
    "secondary_stage_invocations": 0,
}


def _inc_diag(key: str) -> None:
    if key not in _diag_metrics:
        return
    with _diag_lock:
        _diag_metrics[key] += 1


def get_diagnostics_snapshot() -> Dict[str, int]:
    with _diag_lock:
        return dict(_diag_metrics)


def _normalize_vec(v: np.ndarray) -> np.ndarray:
    n = float(np.linalg.norm(v))
    if n <= 0.0:
        return v.astype(np.float32)
    return (v / n).astype(np.float32)


def _cosine_similarity(a: np.ndarray, b: np.ndarray) -> float:
    norm_a = np.linalg.norm(a)
    norm_b = np.linalg.norm(b)
    if norm_a == 0 or norm_b == 0:
        return 0.0
    return float(np.dot(a, b) / (norm_a * norm_b))


def _laplacian_variance(rgb_img: np.ndarray) -> float:
    try:
        gray = cv2.cvtColor(rgb_img, cv2.COLOR_RGB2GRAY)
        return float(cv2.Laplacian(gray, cv2.CV_64F).var())
    except Exception:
        return 0.0


def _profile_views(img: Image.Image) -> List[np.ndarray]:
    base = ImageOps.exif_transpose(img).convert("RGB")
    arr = np.array(base)
    # Enriched one-shot views for profile robustness.
    views = [
        arr,
        np.fliplr(arr),
        np.array(base.rotate(-8, resample=Image.BICUBIC, expand=False).convert("RGB")),
        np.array(base.rotate(8, resample=Image.BICUBIC, expand=False).convert("RGB")),
        np.array(base.rotate(-15, resample=Image.BICUBIC, expand=False).convert("RGB")),
        np.array(base.rotate(15, resample=Image.BICUBIC, expand=False).convert("RGB")),
        np.clip((arr.astype(np.float32) * 1.08), 0, 255).astype(np.uint8),
        np.clip((arr.astype(np.float32) * 0.92), 0, 255).astype(np.uint8),
    ]
    return views


def _embedding_weight(face: Any, source_rgb: np.ndarray) -> float:
    det_score = float(getattr(face, "det_score", 0.0) or 0.0)
    bbox = getattr(face, "bbox", None)
    min_side = 0.0
    if bbox is not None and len(bbox) >= 4:
        min_side = float(max(0.0, min(float(bbox[2] - bbox[0]), float(bbox[3] - bbox[1]))))
    blur = _laplacian_variance(source_rgb)

    # Conservative weighting to keep poor quality profiles from dominating.
    score_q = min(1.0, max(0.0, det_score))
    size_q = min(1.6, max(0.3, min_side / 120.0))
    blur_q = min(1.5, max(0.35, blur / 120.0))
    return float(max(0.2, min(2.5, score_q * size_q * blur_q)))


def _build_prototypes(gallery: List[Dict[str, Any]]) -> Dict[str, Dict[str, Any]]:
    by_person: Dict[str, Dict[str, Any]] = {}
    for it in gallery:
        pid = str(it.get("person_id") or "").strip()
        if not pid:
            continue
        w = float(it.get("weight", 1.0))
        emb = it.get("embedding")
        if not isinstance(emb, np.ndarray):
            continue
        rec = by_person.setdefault(
            pid,
            {
                "person_id": pid,
                "person_name": str(it.get("name") or pid),
                "sum": np.zeros_like(emb, dtype=np.float32),
                "w_sum": 0.0,
            },
        )
        rec["sum"] = rec["sum"] + (emb.astype(np.float32) * w)
        rec["w_sum"] += w

    out: Dict[str, Dict[str, Any]] = {}
    for pid, rec in by_person.items():
        if rec["w_sum"] <= 0.0:
            continue
        proto = _normalize_vec(rec["sum"] / rec["w_sum"])
        out[pid] = {
            "person_id": pid,
            "person_name": rec["person_name"],
            "embedding": proto,
            "w_sum": rec["w_sum"],
        }
    return out


def _refresh_registry():
    global _last_registry_refresh
    primary_app = _get_insightface(_primary_model_name())
    if primary_app is None:
        _registry_loaded.set()
        return

    secondary_app = _get_secondary_app()
    relay_base = os.getenv("RELAY_HTTP_BASE", "http://localhost:8020")
    try:
        resp = requests.get(f"{relay_base}/persons/registry", timeout=5)
        resp.raise_for_status()
        entries = resp.json()
    except Exception as e:
        logger.warning(f"[FaceRecognizer] Failed to fetch registry: {e}")
        _registry_loaded.set()
        return

    primary_gallery: List[Dict[str, Any]] = []
    secondary_gallery: List[Dict[str, Any]] = []
    profile_count = 0

    for entry in entries:
        pid = str(entry.get("person_id") or "").strip()
        name = str(entry.get("name") or "unknown").strip() or "unknown"
        if not pid:
            continue

        if _v2_enabled():
            has_ready_flag = "enrollment_ready" in entry
            profile_count = int(entry.get("profile_count") or 0)
            enrollment_ready = bool(entry.get("enrollment_ready")) if has_ready_flag else profile_count >= _min_profiles()
            if not enrollment_ready:
                reason = str(entry.get("enrollment_status_reason") or "")
                logger.info(
                    "[FaceRecognizer] Skipping person %s (%s): enrollment not ready (profiles=%s min=%s reason=%s)",
                    name,
                    pid,
                    profile_count,
                    _min_profiles(),
                    reason,
                )
                continue

        profile_images: List[tuple[str, Image.Image]] = []

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
                        logger.warning(
                            f"[FaceRecognizer] Error loading profile {prof_id} for {name} ({pid}): {e}"
                        )
        except Exception:
            pass

        if not profile_images:
            try:
                img_resp = requests.get(f"{relay_base}/persons/{pid}/face", timeout=5)
                img_resp.raise_for_status()
                img = Image.open(io.BytesIO(img_resp.content)).convert("RGB")
                profile_images.append(("legacy", img))
            except Exception as e:
                logger.warning(f"[FaceRecognizer] Error loading legacy face for {name} ({pid}): {e}")
                continue

        person_primary = 0
        person_secondary = 0
        for profile_id, img in profile_images:
            profile_count += 1
            for view in _profile_views(img):
                try:
                    faces = primary_app.get(view)
                except Exception as e:
                    logger.debug(f"[FaceRecognizer] Primary inference failed for {name}/{profile_id}: {e}")
                    faces = []
                if not faces:
                    continue
                face = max(faces, key=lambda f: (f.bbox[2] - f.bbox[0]) * (f.bbox[3] - f.bbox[1]))
                emb = getattr(face, "normed_embedding", None)
                if emb is None:
                    continue
                w = _embedding_weight(face, view)
                primary_gallery.append(
                    {
                        "person_id": pid,
                        "name": name,
                        "embedding": _normalize_vec(emb),
                        "weight": w,
                        "profile_id": profile_id,
                    }
                )
                person_primary += 1

                if secondary_app is not None:
                    try:
                        sf = secondary_app.get(view)
                    except Exception:
                        sf = []
                    if sf:
                        s_face = max(sf, key=lambda f: (f.bbox[2] - f.bbox[0]) * (f.bbox[3] - f.bbox[1]))
                        s_emb = getattr(s_face, "normed_embedding", None)
                        if s_emb is not None:
                            secondary_gallery.append(
                                {
                                    "person_id": pid,
                                    "name": name,
                                    "embedding": _normalize_vec(s_emb),
                                    "weight": w,
                                    "profile_id": profile_id,
                                }
                            )
                            person_secondary += 1

        logger.info(
            f"[FaceRecognizer] Loaded {person_primary} primary and {person_secondary} secondary embeddings for {name} ({pid})"
        )

    proto_primary = _build_prototypes(primary_gallery)
    proto_secondary = _build_prototypes(secondary_gallery)
    with _registry_lock:
        _known_faces_primary.clear()
        _known_faces_primary.extend(primary_gallery)
        _known_faces_secondary.clear()
        _known_faces_secondary.extend(secondary_gallery)
        _prototypes_primary.clear()
        _prototypes_primary.update(proto_primary)
        _prototypes_secondary.clear()
        _prototypes_secondary.update(proto_secondary)
        # Compatibility alias.
        _known_faces.clear()
        _known_faces.extend(primary_gallery)

    _last_registry_refresh = time.time()
    _registry_loaded.set()
    logger.info(
        "[FaceRecognizer] Registry ready: profiles=%d primary_embeddings=%d secondary_embeddings=%d prototypes=%d",
        profile_count,
        len(primary_gallery),
        len(secondary_gallery),
        len(proto_primary),
    )


def _registry_refresh_loop():
    refresh_interval = _env_float("FACE_REGISTRY_REFRESH_S", 60.0)
    _refresh_registry()
    while True:
        time.sleep(refresh_interval)
        _refresh_registry()


def start_registry_refresh():
    """Start the background registry refresh thread. Call once at worker startup."""
    t = threading.Thread(target=_registry_refresh_loop, daemon=True, name="FaceRegistryRefresh")
    t.start()


def _match_embedding_legacy(emb: np.ndarray) -> Dict[str, Any]:
    threshold = _match_threshold_legacy()
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
            "match_margin": round(best_sim, 3),
        }
    return {
        "confidence_tier": "unknown",
        "candidate_person_id": best_id,
        "candidate_person_name": best_name,
        "candidate_score": round(best_sim, 3),
        "match_margin": 0.0,
    }


def _score_gallery(
    emb: np.ndarray,
    gallery: List[Dict[str, Any]],
    prototypes: Dict[str, Dict[str, Any]],
) -> List[Dict[str, Any]]:
    per_person: Dict[str, Dict[str, Any]] = {}
    for rec in gallery:
        pid = str(rec.get("person_id") or "").strip()
        if not pid:
            continue
        sim = _cosine_similarity(emb, rec["embedding"])
        item = per_person.setdefault(
            pid,
            {
                "person_id": pid,
                "person_name": str(rec.get("name") or pid),
                "sims": [],
                "max_sim": 0.0,
                "proto_sim": 0.0,
            },
        )
        item["sims"].append(sim)
        if sim > item["max_sim"]:
            item["max_sim"] = sim

    for pid, item in per_person.items():
        proto = prototypes.get(pid)
        if proto and isinstance(proto.get("embedding"), np.ndarray):
            item["proto_sim"] = _cosine_similarity(emb, proto["embedding"])
        sims = sorted(item["sims"], reverse=True)
        k = min(_top_k(), len(sims))
        top_k_mean = float(sum(sims[:k]) / k) if k > 0 else 0.0
        # Composite score for robust one-shot matching.
        item["top_k_mean"] = top_k_mean
        item["score"] = (0.65 * item["max_sim"]) + (0.25 * top_k_mean) + (0.10 * item["proto_sim"])

    return sorted(per_person.values(), key=lambda x: x["score"], reverse=True)


def _tier_from_scores(top1_score: float, margin: float) -> str:
    if top1_score >= _strong_threshold() and margin >= _min_margin():
        return "strong"
    if top1_score >= _weak_threshold():
        return "weak"
    return "unknown"


def _build_match_result(
    top: Optional[Dict[str, Any]],
    second: Optional[Dict[str, Any]],
    stage: str,
) -> Dict[str, Any]:
    if not top:
        return {
            "confidence_tier": "unknown",
            "match_stage": stage,
            "candidate_score": 0.0,
            "match_margin": 0.0,
        }
    margin = float(top["score"] - float(second["score"])) if second else float(top["score"])
    tier = _tier_from_scores(float(top["score"]), margin)
    out: Dict[str, Any] = {
        "confidence_tier": tier,
        "match_stage": stage,
        "candidate_person_id": top["person_id"],
        "candidate_person_name": top["person_name"],
        "candidate_score": round(float(top["score"]), 3),
        "max_sim": round(float(top["max_sim"]), 3),
        "top_k_mean": round(float(top["top_k_mean"]), 3),
        "proto_sim": round(float(top["proto_sim"]), 3),
        "match_margin": round(margin, 3),
    }
    if tier == "strong":
        out["person_id"] = top["person_id"]
        out["person_name"] = top["person_name"]
        out["match_score"] = round(float(top["score"]), 3)
    return out


def _match_embedding_v2(emb: np.ndarray, stage: str = "primary") -> Dict[str, Any]:
    if stage == "secondary":
        with _registry_lock:
            ranked = _score_gallery(emb, _known_faces_secondary, _prototypes_secondary)
    else:
        with _registry_lock:
            ranked = _score_gallery(emb, _known_faces_primary, _prototypes_primary)
    top = ranked[0] if ranked else None
    second = ranked[1] if len(ranked) > 1 else None
    return _build_match_result(top, second, stage)


def _match_embedding(emb: np.ndarray) -> Dict[str, Any]:
    if _v2_enabled():
        return _match_embedding_v2(emb, stage="primary")
    return _match_embedding_legacy(emb)


# ── Core identify ─────────────────────────────────────────────────────────────

def _extract_face_embedding_from_bbox_with_meta(
    img_np: np.ndarray,
    x1: float, y1: float, x2: float, y2: float,
    model_name: Optional[str] = None,
) -> Tuple[Optional[np.ndarray], Dict[str, Any]]:
    app = _get_insightface(model_name)
    if app is None:
        return None, {}

    h, w = img_np.shape[:2]
    pad_x = (x2 - x1) * 0.2
    pad_y = (y2 - y1) * 0.3
    cx1 = max(0, int(x1 - pad_x))
    cy1 = max(0, int(y1 - pad_y))
    cx2 = min(w, int(x2 + pad_x))
    cy2 = min(h, int(y2 + pad_y))
    if cx2 <= cx1 or cy2 <= cy1:
        return None, {}

    crop = img_np[cy1:cy2, cx1:cx2]
    if crop.size == 0:
        return None, {}

    try:
        faces = app.get(crop)
        if not faces:
            return None, {"face_count": 0}
        face = max(faces, key=lambda f: float(getattr(f, "det_score", 0.0) or 0.0))
        emb = getattr(face, "normed_embedding", None)
        if emb is None:
            return None, {"face_count": len(faces)}
        bbox = getattr(face, "bbox", None)
        min_side = 0.0
        if bbox is not None and len(bbox) >= 4:
            min_side = float(max(0.0, min(float(bbox[2] - bbox[0]), float(bbox[3] - bbox[1]))))
        meta = {
            "face_count": len(faces),
            "face_det_conf": round(float(getattr(face, "det_score", 0.0) or 0.0), 6),
            "face_min_px": round(min_side, 3),
            "face_blur_var": round(_laplacian_variance(crop), 3),
        }
        return _normalize_vec(emb), meta
    except Exception as e:
        logger.warning(f"[FaceRecognizer] Inference error: {e}")
        return None, {}


def _extract_face_embedding_from_bbox(
    img_np: np.ndarray,
    x1: float, y1: float, x2: float, y2: float,
) -> Optional[np.ndarray]:
    emb, _ = _extract_face_embedding_from_bbox_with_meta(img_np, x1, y1, x2, y2, None)
    return emb


def _run_adaptive_stage(
    img_np: np.ndarray,
    x1: float, y1: float, x2: float, y2: float,
    primary_result: Dict[str, Any],
) -> Dict[str, Any]:
    secondary_app = _get_secondary_app()
    if secondary_app is None:
        return primary_result

    tier = str(primary_result.get("confidence_tier") or "unknown")
    if tier == "strong":
        return primary_result

    _inc_diag("secondary_stage_invocations")
    emb2, meta2 = _extract_face_embedding_from_bbox_with_meta(
        img_np, x1, y1, x2, y2, _secondary_model_name()
    )
    if emb2 is None:
        out = dict(primary_result)
        out["secondary_used"] = True
        out["secondary_available"] = True
        return out

    secondary_result = _match_embedding_v2(emb2, stage="secondary")
    out = dict(primary_result)
    out["secondary_used"] = True
    out["secondary_available"] = True
    out["secondary_candidate_score"] = secondary_result.get("candidate_score")
    out["secondary_match_margin"] = secondary_result.get("match_margin")
    out["secondary_face_det_conf"] = meta2.get("face_det_conf")

    sec_tier = str(secondary_result.get("confidence_tier") or "unknown")
    if sec_tier == "strong":
        return secondary_result | {
            "secondary_used": True,
            "secondary_available": True,
        }

    p_pid = str(primary_result.get("candidate_person_id") or "")
    s_pid = str(secondary_result.get("candidate_person_id") or "")
    if p_pid and p_pid == s_pid:
        p_score = float(primary_result.get("candidate_score", 0.0) or 0.0)
        s_score = float(secondary_result.get("candidate_score", 0.0) or 0.0)
        avg = (p_score + s_score) / 2.0
        if avg >= (_weak_threshold() + 0.015):
            out["candidate_person_id"] = p_pid
            out["candidate_person_name"] = primary_result.get("candidate_person_name")
            out["candidate_score"] = round(avg, 3)
            out["confidence_tier"] = "weak"
            out["match_margin"] = round(
                max(
                    float(primary_result.get("match_margin", 0.0) or 0.0),
                    float(secondary_result.get("match_margin", 0.0) or 0.0),
                ),
                3,
            )
    return out


def _record_result_metrics(result: Dict[str, Any]) -> None:
    tier = str(result.get("confidence_tier") or "unknown")
    if tier == "strong" and result.get("person_id"):
        _inc_diag("strong_matches")
    elif tier == "weak":
        _inc_diag("weak_candidates")
    else:
        _inc_diag("unknown_results")


def _identify_from_box_with_meta(
    img_np: np.ndarray,
    x1: float, y1: float, x2: float, y2: float,
) -> Tuple[Dict[str, Any], Dict[str, Any], Optional[np.ndarray]]:
    emb, meta = _extract_face_embedding_from_bbox_with_meta(img_np, x1, y1, x2, y2, None)
    if emb is None:
        return {}, meta, None
    result = _match_embedding(emb)
    if _v2_enabled():
        result = _run_adaptive_stage(img_np, x1, y1, x2, y2, result)
    return result, meta, emb


def _quality_score(meta: Dict[str, Any]) -> float:
    det_conf = float(meta.get("face_det_conf", 0.0) or 0.0)
    min_px = float(meta.get("face_min_px", 0.0) or 0.0)
    blur = float(meta.get("face_blur_var", 0.0) or 0.0)
    size_q = min(1.0, max(0.0, min_px / 140.0))
    blur_q = min(1.0, max(0.0, blur / 140.0))
    return (0.55 * det_conf) + (0.25 * size_q) + (0.20 * blur_q)


def _result_rank(
    source: str,
    result: Dict[str, Any],
    meta: Dict[str, Any],
) -> Tuple[float, float, float, float]:
    has_person = bool(result.get("person_id"))
    tier = str(result.get("confidence_tier") or "unknown")
    tier_rank = 3.0 if has_person else (2.0 if tier == "weak" else 1.0)
    score = float(result.get("match_score", result.get("candidate_score", 0.0)) or 0.0)
    margin = float(result.get("match_margin", 0.0) or 0.0)
    source_bonus = 0.12 if source == "associated_face" else 0.0
    quality = _quality_score(meta)
    composite = score + (0.08 * quality) + source_bonus
    return (tier_rank, composite, margin, quality)


def detect_faces_full_frame(img_np: np.ndarray) -> List[Dict[str, Any]]:
    """Detect faces on the full frame for person-face association."""
    app = _get_insightface(_primary_model_name())
    if app is None:
        return []

    try:
        faces = app.get(img_np)
    except Exception as e:
        logger.warning(f"[FaceRecognizer] full-frame face detection error: {e}")
        return []

    out: List[Dict[str, Any]] = []
    for face in faces:
        bbox = getattr(face, "bbox", None)
        if bbox is None or len(bbox) < 4:
            continue
        x1, y1, x2, y2 = [float(v) for v in bbox[:4]]
        min_side = float(max(0.0, min(x2 - x1, y2 - y1)))
        out.append(
            {
                "box_px": {
                    "x_min": round(x1, 2),
                    "y_min": round(y1, 2),
                    "x_max": round(x2, 2),
                    "y_max": round(y2, 2),
                },
                "face_det_conf": round(float(getattr(face, "det_score", 0.0) or 0.0), 6),
                "face_min_px": round(min_side, 3),
            }
        )

    out.sort(
        key=lambda x: (
            float(x.get("face_det_conf", 0.0) or 0.0),
            float(x.get("face_min_px", 0.0) or 0.0),
        ),
        reverse=True,
    )
    return out


def identify_person_dual(
    img_np: np.ndarray,
    person_x1: float, person_y1: float, person_x2: float, person_y2: float,
    associated_face_box: Optional[Dict[str, float]] = None,
    include_embedding: bool = False,
) -> Dict[str, Any]:
    """Identify a person using two sources and choose the stronger result.

    Sources:
    1) associated face box from full-frame face detector (preferred)
    2) fallback face extraction from the full person box crop
    """
    _inc_diag("attempts")
    candidates: List[Tuple[str, Dict[str, Any], Dict[str, Any], Optional[np.ndarray]]] = []

    if isinstance(associated_face_box, dict):
        fx1 = float(associated_face_box.get("x_min", 0.0) or 0.0)
        fy1 = float(associated_face_box.get("y_min", 0.0) or 0.0)
        fx2 = float(associated_face_box.get("x_max", 0.0) or 0.0)
        fy2 = float(associated_face_box.get("y_max", 0.0) or 0.0)
        if fx2 > fx1 and fy2 > fy1:
            result, meta, emb = _identify_from_box_with_meta(img_np, fx1, fy1, fx2, fy2)
            candidates.append(("associated_face", result, meta, emb))

    result_person, meta_person, emb_person = _identify_from_box_with_meta(
        img_np, person_x1, person_y1, person_x2, person_y2
    )
    candidates.append(("person_crop", result_person, meta_person, emb_person))

    valid = [c for c in candidates if c[3] is not None]
    if not valid:
        _inc_diag("unknown_results")
        return {}

    source, result, meta, emb = max(
        valid,
        key=lambda c: _result_rank(c[0], c[1], c[2]),
    )
    out = dict(result) if result else {}
    out["face_source"] = source
    out["face_det_conf"] = round(float(meta.get("face_det_conf", 0.0) or 0.0), 6)
    out["face_min_px"] = round(float(meta.get("face_min_px", 0.0) or 0.0), 3)
    out["face_blur_var"] = round(float(meta.get("face_blur_var", 0.0) or 0.0), 3)

    _record_result_metrics(out)
    if _diagnostics_enabled():
        out = dict(out)
        out["diag"] = get_diagnostics_snapshot()

    if include_embedding and emb is not None:
        out["face_embedding"] = emb

    # Backward compatibility:
    # - return only strong match in legacy mode
    # - in v2 return weak/unknown metadata for evidence accumulation
    if out.get("person_id"):
        return out
    return out if _v2_enabled() else {}


def identify(
    img_np: np.ndarray,
    x1: float, y1: float, x2: float, y2: float,
    include_embedding: bool = False,
) -> Dict[str, Any]:
    _inc_diag("attempts")
    result, meta, emb = _identify_from_box_with_meta(img_np, x1, y1, x2, y2)
    if emb is None:
        _inc_diag("unknown_results")
        return {}

    _record_result_metrics(result)

    if _diagnostics_enabled():
        result = dict(result)
        result.update({k: v for k, v in meta.items() if k not in result})
        result["diag"] = get_diagnostics_snapshot()

    if include_embedding:
        out = dict(result) if result else {}
        out["face_embedding"] = emb
        return out

    # Keep backward compatibility: only return strong direct match by default.
    if result.get("person_id"):
        return result
    return result if _v2_enabled() else {}


# ── Async pool ────────────────────────────────────────────────────────────────

_executor: Optional[ThreadPoolExecutor] = None
_executor_lock = threading.Lock()


def _get_executor() -> ThreadPoolExecutor:
    global _executor
    with _executor_lock:
        if _executor is None:
            _executor = ThreadPoolExecutor(max_workers=2, thread_name_prefix="FaceRec")
    return _executor


def submit_async(
    img_np: np.ndarray,
    x1: float, y1: float, x2: float, y2: float,
    track_id: int,
    callback: Callable[[int, Dict[str, Any]], None],
    include_embedding: bool = False,
) -> None:
    img_copy = img_np.copy()

    def _run():
        try:
            result = identify(img_copy, x1, y1, x2, y2, include_embedding=include_embedding)
            callback(track_id, result)
        except Exception as e:
            logger.warning(f"[FaceRecognizer] Async error for track {track_id}: {e}")
            callback(track_id, {})

    _get_executor().submit(_run)


def submit_async_person_dual(
    img_np: np.ndarray,
    person_x1: float, person_y1: float, person_x2: float, person_y2: float,
    track_id: int,
    callback: Callable[[int, Dict[str, Any]], None],
    include_embedding: bool = False,
    associated_face_box: Optional[Dict[str, float]] = None,
) -> None:
    img_copy = img_np.copy()
    assoc_copy = dict(associated_face_box) if isinstance(associated_face_box, dict) else None

    def _run():
        try:
            result = identify_person_dual(
                img_copy,
                person_x1, person_y1, person_x2, person_y2,
                associated_face_box=assoc_copy,
                include_embedding=include_embedding,
            )
            callback(track_id, result)
        except Exception as e:
            logger.warning(f"[FaceRecognizer] Async dual-person error for track {track_id}: {e}")
            callback(track_id, {})

    _get_executor().submit(_run)
