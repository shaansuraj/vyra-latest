"""Security Check Worker — YOLOv8m person detection with ByteTrack + optional background VLM.

Pipeline:
  1. YOLO inference — detects all objects, filters to persons for output
  2. ByteTrack — persistent track_ids per camera stream
  3. First-time person: save snapshot with bboxes, queue VLM analysis (non-blocking)
  4. Returning person: periodic re-analysis via cooldown-based gating
  5. Background VLM thread processes requests; result attached on next event

Architecture note:
  Person detection must never block on VLM initialization/inference. VLM
  enrichment is optional and asynchronous.
"""

import asyncio
from collections import OrderedDict
from concurrent.futures import ThreadPoolExecutor
import os
import queue as py_queue
import shutil
import threading
import time
from typing import Any, Callable, Dict, List, Optional, Set, Tuple

import numpy as np
from PIL import Image, ImageDraw

from ws_client_handler import client_handler
from objdet_face_lookup import select_face_lookup_frame_id

# Multi-object tracking (ByteTrack) for persistent IDs across frames
try:
    from tracker import TrackerManager
    TRACKING_AVAILABLE = True
except Exception as e:
    print(f"[object_detection] Tracker not available: {e}")
    TRACKING_AVAILABLE = False

try:
    import torch
    from ultralytics import YOLO
except ImportError:
    print("ERROR: ultralytics/torch not installed.")
    raise

try:
    from vlm_helper import VLMHelper
    VLM_AVAILABLE = True
except Exception as e:
    print(f"[object_detection] VLM helper not available: {e}")
    VLM_AVAILABLE = False

try:
    import face_recognizer as _face_rec
    FACE_REC_AVAILABLE = True
except Exception as e:
    print(f"[object_detection] Face recognizer not available: {e}")
    FACE_REC_AVAILABLE = False

try:
    from person_reid_embedder import extract_embedding_from_crop as _extract_reid_embedding_from_crop
    from person_reid_embedder import extract_observation_from_crop as _extract_reid_observation_from_crop
except Exception:
    _extract_reid_embedding_from_crop = None
    _extract_reid_observation_from_crop = None


def _env_bool(name: str, default: bool = False) -> bool:
    raw = os.getenv(name)
    if raw is None:
        return default
    return str(raw).strip().lower() in ("1", "true", "yes", "on")


def _env_int(name: str, default: int) -> int:
    raw = os.getenv(name)
    try:
        return int(raw) if raw else default
    except Exception:
        return default


_PERSON_IDENTITY_RESOLVE_EXECUTOR = ThreadPoolExecutor(
    max_workers=max(1, _env_int("PERSON_IDENTITY_EMBEDDING_WORKERS", 2))
)


def _env_float(name: str, default: float) -> float:
    raw = os.getenv(name)
    try:
        return float(raw) if raw else default
    except Exception:
        return default


def _env_float_any(names: List[str], default: float) -> float:
    for name in names:
        raw = os.getenv(name)
        if raw:
            try:
                return float(raw)
            except Exception:
                continue
    return default


def _env_int_any(names: List[str], default: int) -> int:
    for name in names:
        raw = os.getenv(name)
        if raw:
            try:
                return int(raw)
            except Exception:
                continue
    return default


def _person_class_ids(names: Any) -> Optional[List[int]]:
    pairs: List[Tuple[int, str]] = []
    if isinstance(names, dict):
        for key, value in names.items():
            try:
                pairs.append((int(key), str(value).strip().lower()))
            except Exception:
                continue
    elif isinstance(names, (list, tuple)):
        for idx, value in enumerate(names):
            pairs.append((idx, str(value).strip().lower()))

    ids = [idx for idx, label in pairs if label == "person"]
    return ids or None


def _legacy_mode_enabled() -> bool:
    return _env_bool("LEGACY_MODE", False)


_OBJDET_DEBUG_LOGS = _env_bool("OBJECT_DET_DEBUG_LOGS", False)


def _debug_log(msg: str) -> None:
    if _OBJDET_DEBUG_LOGS:
        print(msg)


def _coerce_bool(value: Any, default: bool = False) -> bool:
    if isinstance(value, bool):
        return value
    if isinstance(value, (int, float)):
        return value != 0
    if isinstance(value, str):
        v = value.strip().lower()
        if v == "":
            return default
        return v in ("1", "true", "yes", "on")
    return default


_CUSTOM_MODEL_CACHE_LOCK = threading.Lock()
_CUSTOM_MODEL_CACHE: "OrderedDict[str, Any]" = OrderedDict()


def _custom_model_cache_max() -> int:
    try:
        return max(1, int(os.getenv("CUSTOM_MODEL_CACHE_MAX", "2")))
    except Exception:
        return 2


def _load_custom_model_cached(model_path: str) -> Any:
    path = str(model_path or "").strip()
    if not path:
        raise ValueError("custom_model_path is required")

    with _CUSTOM_MODEL_CACHE_LOCK:
        cached = _CUSTOM_MODEL_CACHE.get(path)
        if cached is not None:
            _CUSTOM_MODEL_CACHE.move_to_end(path)
            return cached

    model = YOLO(path)
    with _CUSTOM_MODEL_CACHE_LOCK:
        _CUSTOM_MODEL_CACHE[path] = model
        _CUSTOM_MODEL_CACHE.move_to_end(path)
        while len(_CUSTOM_MODEL_CACHE) > _custom_model_cache_max():
            _CUSTOM_MODEL_CACHE.popitem(last=False)
    return model


def _patch_ultralytics_torch_safe_load() -> None:
    try:
        from importlib.metadata import version as pkg_version
        ul_ver = tuple(int(x) for x in pkg_version("ultralytics").split(".")[:2])
        # ultralytics>=8.1 handles this internally
        if ul_ver >= (8, 1):
            return

        from ultralytics.nn import tasks as ul_tasks  # type: ignore

        def _torch_safe_load_patched(file, *args, **kwargs):
            ckpt = torch.load(file, map_location="cpu", weights_only=False)
            # ultralytics<8.1 expects (checkpoint, file)
            return ckpt, file

        if hasattr(ul_tasks, "torch_safe_load"):
            ul_tasks.torch_safe_load = _torch_safe_load_patched  # type: ignore
    except Exception:
        pass


def _pick_device() -> str:
    try:
        if os.environ.get("CUDA_VISIBLE_DEVICES", "").strip().lower() == "cuda":
            os.environ["CUDA_VISIBLE_DEVICES"] = ""
        if torch.cuda.is_available() and torch.cuda.device_count() > 0:
            return "0"
        return "cpu"
    except Exception:
        return "cpu"


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
        job_id = x.get("_job_id")
        media_id = x.get("_media_id") or payload_media_id
        media_name = x.get("_media_name") or media_id
        if fid and fp:
            out.append({
                "id": str(fid),
                "filepath": str(fp),
                "_job_id": job_id,
                "_media_id": str(media_id),
                "_media_name": str(media_name),
            })
    return out


# ── Cross-camera identity memory ───────────────────────────────────────────────

def _cosine_similarity(a: np.ndarray, b: np.ndarray) -> float:
    try:
        na = float(np.linalg.norm(a))
        nb = float(np.linalg.norm(b))
        if na == 0.0 or nb == 0.0:
            return 0.0
        return float(np.dot(a, b) / (na * nb))
    except Exception:
        return 0.0


class _GlobalPersonIdentityManager:
    """In-process global identity memory (recognized + unknown) across cameras."""

    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._track_bindings: Dict[Tuple[str, int], Dict[str, Any]] = {}
        self._known_person_ids: Dict[str, str] = {}
        self._unknown_gallery: Dict[str, List[np.ndarray]] = {}
        self._last_visit_seen: Dict[Tuple[str, str], float] = {}
        self._global_last_seen: Dict[str, float] = {}
        self._next_unknown_idx = 1

        self.unknown_match_threshold = float(os.getenv("UNKNOWN_FACE_MATCH_THRESHOLD", "0.58"))
        self.visit_absence_seconds = float(os.getenv("PERSON_VISIT_ABSENCE_SECONDS", "5.0"))
        self.max_unknown_embeddings = int(os.getenv("UNKNOWN_FACE_MAX_PROFILES", "12"))
        self.track_stale_seconds = float(os.getenv("PERSON_TRACK_STALE_SECONDS", "180.0"))
        self.identity_stale_seconds = float(os.getenv("PERSON_IDENTITY_STALE_SECONDS", "1800.0"))

    @staticmethod
    def _unknown_name(global_id: str) -> str:
        if global_id.startswith("unknown:"):
            suffix = global_id.split(":", 1)[1]
            return f"Unknown #{suffix}"
        return "Unknown"

    def _new_unknown_id_locked(self) -> str:
        gid = f"unknown:{self._next_unknown_idx}"
        self._next_unknown_idx += 1
        return gid

    def _match_unknown_locked(self, emb: np.ndarray) -> Tuple[Optional[str], float]:
        best_gid: Optional[str] = None
        best_sim = 0.0
        for gid, refs in self._unknown_gallery.items():
            for ref in refs:
                sim = _cosine_similarity(emb, ref)
                if sim > best_sim:
                    best_sim = sim
                    best_gid = gid
        if best_gid and best_sim >= self.unknown_match_threshold:
            return best_gid, best_sim
        return None, best_sim

    def _append_unknown_embedding_locked(self, gid: str, emb: np.ndarray) -> None:
        bucket = self._unknown_gallery.setdefault(gid, [])
        bucket.append(emb.astype(np.float32))
        if len(bucket) > self.max_unknown_embeddings:
            del bucket[0 : len(bucket) - self.max_unknown_embeddings]

    def get_track_binding(self, media_id: str, track_id: int) -> Optional[Dict[str, Any]]:
        key = (media_id, track_id)
        now = time.time()
        with self._lock:
            binding = self._track_bindings.get(key)
            if not binding:
                return None
            binding["last_seen"] = now
            self._global_last_seen[binding["global_person_id"]] = now
            return dict(binding)

    def ensure_track_binding(self, media_id: str, track_id: int) -> Dict[str, Any]:
        """Ensure track has a stable global identity (unknown fallback)."""
        key = (media_id, track_id)
        now = time.time()
        with self._lock:
            existing = self._track_bindings.get(key)
            if existing:
                existing["last_seen"] = now
                self._global_last_seen[existing["global_person_id"]] = now
                return dict(existing)

            gid = self._new_unknown_id_locked()
            binding = {
                "global_person_id": gid,
                "person_id": None,
                "person_name": self._unknown_name(gid),
                "match_score": None,
                "last_seen": now,
            }
            self._track_bindings[key] = binding
            self._global_last_seen[gid] = now
            return dict(binding)

    def bind_track_from_face(self, media_id: str, track_id: int, face_result: Dict[str, Any]) -> Dict[str, Any]:
        """Update one track identity from async face recognition output."""
        key = (media_id, track_id)
        now = time.time()

        person_id = str(face_result.get("person_id") or "").strip()
        person_name = str(face_result.get("person_name") or "").strip()
        match_score_raw = face_result.get("match_score")
        match_score = float(match_score_raw) if isinstance(match_score_raw, (int, float)) else None

        face_embedding = face_result.get("face_embedding")
        emb: Optional[np.ndarray] = None
        if isinstance(face_embedding, np.ndarray):
            emb = face_embedding.astype(np.float32)

        with self._lock:
            if person_id:
                gid = self._known_person_ids.get(person_id) or f"person:{person_id}"
                self._known_person_ids[person_id] = gid
                binding = {
                    "global_person_id": gid,
                    "person_id": person_id,
                    "person_name": person_name or person_id,
                    "match_score": match_score,
                    "last_seen": now,
                }
            else:
                gid: Optional[str] = None
                best_sim: Optional[float] = None
                if emb is not None:
                    gid, sim = self._match_unknown_locked(emb)
                    best_sim = sim
                    if gid is None:
                        gid = self._new_unknown_id_locked()
                    self._append_unknown_embedding_locked(gid, emb)
                else:
                    prev = self._track_bindings.get(key)
                    if prev and isinstance(prev.get("global_person_id"), str):
                        gid = prev["global_person_id"]
                    if not gid:
                        gid = self._new_unknown_id_locked()

                binding = {
                    "global_person_id": gid,
                    "person_id": None,
                    "person_name": self._unknown_name(gid),
                    "match_score": round(best_sim, 3) if best_sim is not None else None,
                    "last_seen": now,
                }

            self._track_bindings[key] = binding
            self._global_last_seen[binding["global_person_id"]] = now
            return dict(binding)

    def mark_camera_visit(self, global_person_id: str, media_id: str, now_ts: float) -> bool:
        """Return True when this is a new visit in this camera."""
        visit_key = (global_person_id, media_id)
        with self._lock:
            last_seen = self._last_visit_seen.get(visit_key)
            is_new_visit = last_seen is None or (now_ts - last_seen) >= self.visit_absence_seconds
            self._last_visit_seen[visit_key] = now_ts
            self._global_last_seen[global_person_id] = now_ts
            return is_new_visit

    def cleanup_stale(self) -> None:
        now = time.time()
        with self._lock:
            # Drop stale track-to-global bindings.
            stale_tracks = [
                key for key, v in self._track_bindings.items()
                if now - float(v.get("last_seen", 0.0)) > self.track_stale_seconds
            ]
            for key in stale_tracks:
                self._track_bindings.pop(key, None)

            # Drop very old visit records.
            stale_visits = [
                key for key, last in self._last_visit_seen.items()
                if now - last > self.identity_stale_seconds
            ]
            for key in stale_visits:
                self._last_visit_seen.pop(key, None)

            # Drop global identities not seen recently.
            stale_global_ids = [
                gid for gid, last in self._global_last_seen.items()
                if now - last > self.identity_stale_seconds
            ]
            for gid in stale_global_ids:
                self._global_last_seen.pop(gid, None)
                self._unknown_gallery.pop(gid, None)


class _FaceTrackEvidenceManager:
    """Per-track face matching retries + evidence accumulation."""

    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._states: Dict[Tuple[str, int], Dict[str, Any]] = {}
        self.v2_enabled = _env_bool("FACE_RECOGNITION_V2_ENABLED", True)
        self.retry_interval_s = float(os.getenv("FACE_TRACK_RETRY_INTERVAL_S", "0.6")) if self.v2_enabled else 9999.0
        self.max_retries = max(1, int(os.getenv("FACE_TRACK_MAX_RETRIES", "8"))) if self.v2_enabled else 1
        self.bootstrap_attempts = max(1, int(os.getenv("FACE_TRACK_BOOTSTRAP_ATTEMPTS", "3"))) if self.v2_enabled else 1
        self.bootstrap_interval_s = max(
            0.0,
            float(os.getenv("FACE_TRACK_BOOTSTRAP_INTERVAL_S", "0.25")),
        ) if self.v2_enabled else 9999.0
        self.evidence_window = max(2, int(os.getenv("FACE_TRACK_EVIDENCE_WINDOW", "5")))
        default_hits = max(2, (self.evidence_window // 2) + 1)
        self.weak_promote_hits = max(2, int(os.getenv("FACE_TRACK_WEAK_PROMOTE_HITS", str(default_hits))))
        self.stale_seconds = float(os.getenv("PERSON_TRACK_STALE_SECONDS", "180.0"))
        self.diag_enabled = _env_bool("FACE_DIAGNOSTICS", False)
        self.metrics: Dict[str, int] = {
            "lookup_attempts": 0,
            "strong_matches": 0,
            "weak_candidates": 0,
            "weak_promotions": 0,
            "unknown_finalizations": 0,
            "stale_callbacks": 0,
        }

    def _new_state(self, now_ts: float) -> Dict[str, Any]:
        return {
            "attempts": 0,
            "attempt_seq": 0,
            "last_seen": now_ts,
            "last_attempt_ts": 0.0,
            "lookup_inflight": False,
            "finalized": False,
            "final_tier": "",
            "unknown_finalized": False,
            "last_tier": "unknown",
            "evidence_history": [],
            "candidate_counts": {},
            "candidate_names": {},
            "candidate_scores": {},
            "best_candidate_id": "",
            "best_hits": 0,
        }

    def touch(self, media_id: str, track_id: int, now_ts: float) -> None:
        key = (media_id, track_id)
        with self._lock:
            st = self._states.get(key)
            if st is None:
                self._states[key] = self._new_state(now_ts)
            else:
                st["last_seen"] = now_ts

    def begin_lookup(self, media_id: str, track_id: int, now_ts: float) -> Optional[int]:
        key = (media_id, track_id)
        with self._lock:
            st = self._states.get(key)
            if st is None:
                st = self._new_state(now_ts)
                self._states[key] = st

            st["last_seen"] = now_ts
            if st["lookup_inflight"]:
                return None
            if st["finalized"]:
                return None

            if st["attempts"] >= self.max_retries:
                if not st["unknown_finalized"]:
                    st["unknown_finalized"] = True
                    st["finalized"] = True
                    st["final_tier"] = "unknown"
                    self.metrics["unknown_finalizations"] += 1
                return None

            min_interval = self.retry_interval_s
            if int(st["attempts"]) < self.bootstrap_attempts:
                min_interval = min(self.retry_interval_s, self.bootstrap_interval_s)
            if st["attempts"] > 0 and (now_ts - float(st["last_attempt_ts"])) < min_interval:
                return None

            st["lookup_inflight"] = True
            st["attempts"] += 1
            st["last_attempt_ts"] = now_ts
            st["attempt_seq"] += 1
            self.metrics["lookup_attempts"] += 1
            return int(st["attempt_seq"])

    def resolve(self, media_id: str, track_id: int, attempt_seq: int, face_result: Dict[str, Any]) -> Dict[str, Any]:
        now_ts = time.time()
        key = (media_id, track_id)
        with self._lock:
            st = self._states.get(key)
            if st is None:
                self.metrics["stale_callbacks"] += 1
                return {"state": "stale"}
            if int(st.get("attempt_seq", 0)) != int(attempt_seq):
                self.metrics["stale_callbacks"] += 1
                return {"state": "stale"}

            st["lookup_inflight"] = False
            st["last_seen"] = now_ts

            tier = str(face_result.get("confidence_tier") or "unknown")
            strong_person_id = str(face_result.get("person_id") or "").strip()
            strong_person_name = str(face_result.get("person_name") or "").strip()
            candidate_id = str(face_result.get("candidate_person_id") or "").strip()
            candidate_name = str(face_result.get("candidate_person_name") or "").strip()
            candidate_score = float(face_result.get("candidate_score", 0.0) or 0.0)
            st["last_tier"] = tier

            if strong_person_id:
                st["finalized"] = True
                st["final_tier"] = "strong"
                self.metrics["strong_matches"] += 1
                return {
                    "state": "strong",
                    "bind": {
                        "person_id": strong_person_id,
                        "person_name": strong_person_name or strong_person_id,
                        "match_score": float(face_result.get("match_score", candidate_score) or candidate_score),
                        "face_embedding": face_result.get("face_embedding"),
                    },
                    "attempts": st["attempts"],
                }

            if tier == "weak" and candidate_id:
                self.metrics["weak_candidates"] += 1
                counts = st["candidate_counts"]
                names = st["candidate_names"]
                scores = st["candidate_scores"]
                history = st["evidence_history"]

                history.append(candidate_id)
                counts[candidate_id] = int(counts.get(candidate_id, 0)) + 1
                names[candidate_id] = candidate_name or candidate_id
                scores[candidate_id] = max(float(scores.get(candidate_id, 0.0) or 0.0), candidate_score)

                if len(history) > self.evidence_window:
                    old = history.pop(0)
                    old_count = int(counts.get(old, 0)) - 1
                    if old_count <= 0:
                        counts.pop(old, None)
                    else:
                        counts[old] = old_count

                best_candidate = ""
                best_hits = 0
                for pid, hits in counts.items():
                    if int(hits) > best_hits:
                        best_hits = int(hits)
                        best_candidate = pid

                st["best_candidate_id"] = best_candidate
                st["best_hits"] = best_hits

                if best_candidate and best_hits >= self.weak_promote_hits:
                    st["finalized"] = True
                    st["final_tier"] = "weak_promoted"
                    self.metrics["weak_promotions"] += 1
                    return {
                        "state": "weak_promoted",
                        "bind": {
                            "person_id": best_candidate,
                            "person_name": str(names.get(best_candidate) or best_candidate),
                            "match_score": float(scores.get(best_candidate, candidate_score) or candidate_score),
                            "face_embedding": face_result.get("face_embedding"),
                        },
                        "attempts": st["attempts"],
                        "evidence_hits": best_hits,
                        "candidate_person_id": best_candidate,
                    }

                return {
                    "state": "weak",
                    "bind": face_result,
                    "attempts": st["attempts"],
                    "evidence_hits": best_hits,
                    "candidate_person_id": candidate_id,
                }

            if st["attempts"] >= self.max_retries:
                if not st["unknown_finalized"]:
                    st["unknown_finalized"] = True
                    st["finalized"] = True
                    st["final_tier"] = "unknown"
                    self.metrics["unknown_finalizations"] += 1

            return {
                "state": "unknown",
                "bind": face_result,
                "attempts": st["attempts"],
                "unknown_finalized": bool(st["unknown_finalized"]),
            }

    def cancel_inflight(self, media_id: str, track_id: int, attempt_seq: int) -> None:
        key = (media_id, track_id)
        with self._lock:
            st = self._states.get(key)
            if not st:
                return
            if int(st.get("attempt_seq", 0)) != int(attempt_seq):
                return
            st["lookup_inflight"] = False

    def diag(self, media_id: str, track_id: int) -> Dict[str, Any]:
        key = (media_id, track_id)
        with self._lock:
            st = self._states.get(key)
            if not st:
                return {}
            out: Dict[str, Any] = {
                "face_lookup_attempts": int(st.get("attempts", 0) or 0),
                "face_lookup_inflight": bool(st.get("lookup_inflight", False)),
                "face_confidence_tier": str(st.get("final_tier") or st.get("last_tier") or "unknown"),
            }
            best_candidate_id = str(st.get("best_candidate_id") or "")
            if best_candidate_id:
                out["face_candidate_person_id"] = best_candidate_id
                out["face_evidence_hits"] = int(st.get("best_hits", 0) or 0)
            if bool(st.get("unknown_finalized", False)):
                out["face_unknown_finalized"] = True
            return out

    def cleanup_stale(self) -> None:
        now_ts = time.time()
        with self._lock:
            stale_keys = [
                k
                for k, st in self._states.items()
                if (now_ts - float(st.get("last_seen", 0.0) or 0.0)) > self.stale_seconds
            ]
            for key in stale_keys:
                self._states.pop(key, None)

    def metrics_snapshot(self) -> Dict[str, int]:
        with self._lock:
            return dict(self.metrics)


def _apply_identity_to_person(person: Dict[str, Any], binding: Optional[Dict[str, Any]]) -> None:
    if not isinstance(binding, dict):
        return
    gid = binding.get("global_person_id")
    if isinstance(gid, str) and gid:
        person["global_person_id"] = gid

    pid = binding.get("person_id")
    if isinstance(pid, str) and pid:
        person["person_id"] = pid

    pname = binding.get("person_name")
    if isinstance(pname, str) and pname:
        person["person_name"] = pname

    ms = binding.get("match_score")
    if isinstance(ms, (int, float)):
        person["match_score"] = float(ms)

    _promote_known_person_identity(person)


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
    if not isinstance(identity, dict) or _coerce_bool(identity.get("pending"), False):
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


def _submit_person_identity_resolve_async(
    *,
    person: Dict[str, Any],
    media_id: str,
    track_id: int,
    frame_id: str,
    frame_path: str,
    camera_name: str,
    camera_timestamp: str,
    embedding_enabled: bool,
    resolve_async: Callable[..., None],
) -> None:
    box_px = dict(person.get("box_px", {})) if isinstance(person.get("box_px"), dict) else {}
    req_person_id = str(person.get("person_id") or "")
    req_person_name = str(person.get("person_name") or "")
    req_match_score = person.get("match_score")

    def _run() -> None:
        req_observation: Dict[str, Any] = {}
        if embedding_enabled and frame_path:
            req_observation = _appearance_observation_from_crop(frame_path, box_px)
        try:
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
        except Exception as exc:
            print(f"[object_detection] Person identity async resolve error: {exc}")

    _PERSON_IDENTITY_RESOLVE_EXECUTOR.submit(_run)


def _mark_identity_pending(person: Dict[str, Any]) -> None:
    """Mark unresolved identity while avoiding camera-local unknown IDs in output."""
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


def _has_global_person_id(person: Dict[str, Any]) -> bool:
    gid = person.get("global_person_id")
    return isinstance(gid, str) and bool(gid.strip())


def _ensure_person_identity_for_visit(
    identity_mgr: _GlobalPersonIdentityManager,
    media_id: str,
    person: Dict[str, Any],
) -> None:
    """Preserve relay-resolved identity without minting camera-local unknown IDs."""
    if _coerce_bool(person.get("identity_pending"), False):
        return
    if _has_global_person_id(person):
        return
    return


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


def _person_box_event_geometry_ok(
    person: Dict[str, Any],
    image_width: int,
    image_height: int,
    *,
    min_height_ratio: float,
    min_area_ratio: float,
) -> bool:
    box = _box_tuple_px(person.get("box_px", {}))
    if box is None or image_width <= 0 or image_height <= 0:
        return False
    x1, y1, x2, y2 = box
    bw = max(0.0, x2 - x1)
    bh = max(0.0, y2 - y1)
    frame_area = float(image_width * image_height)
    if frame_area <= 0:
        return False
    if (bh / float(image_height)) < min_height_ratio:
        return False
    if ((bw * bh) / frame_area) < min_area_ratio:
        return False
    return True


def _is_event_credible_person(
    person: Dict[str, Any],
    image_width: int,
    image_height: int,
    *,
    min_confidence: float,
    min_track_hits: int,
    min_height_ratio: float,
    min_area_ratio: float,
) -> bool:
    if _has_known_person_identity(person):
        return True
    try:
        tid = int(person.get("track_id", -1))
    except Exception:
        tid = -1
    if tid < 0:
        return False
    try:
        confidence = float(person.get("confidence", 0.0) or 0.0)
    except Exception:
        confidence = 0.0
    if confidence < min_confidence:
        return False
    try:
        track_hits = int(person.get("track_hits", 0) or 0)
    except Exception:
        track_hits = 0
    if track_hits < min_track_hits:
        return False
    return _person_box_event_geometry_ok(
        person,
        image_width,
        image_height,
        min_height_ratio=min_height_ratio,
        min_area_ratio=min_area_ratio,
    )


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
    sync_enabled: bool = False,
    embedding_enabled: bool = True,
    resolve_sync: Optional[Callable[..., Dict[str, Any]]] = None,
    resolve_async: Optional[Callable[..., None]] = None,
) -> None:
    """Run first-seen sync resolve, then async retry scheduling."""
    resolve_key = (media_id, int(track_id))
    sync_resolved = False
    unresolved = _identity_is_unresolved(person)
    if track_embedding_history is None:
        track_embedding_history = {}
    if first_seen:
        track_embedding_history.pop(resolve_key, None)

    req_observation: Dict[str, Any] = {}
    need_sync = bool(sync_enabled and first_seen and unresolved and resolve_sync is not None)

    last_resolve_ts = float(identity_resolve_last_ts.get(resolve_key, 0.0))
    should_resolve_identity = False
    if resolve_async is not None:
        should_resolve_identity = _should_request_person_identity_resolve(
            first_seen=(first_seen and not need_sync),
            person=person,
            now_ts=now_ts,
            last_resolve_ts=last_resolve_ts,
            min_interval_seconds=min_interval_seconds,
        )

    if embedding_enabled and need_sync:
        req_observation = _appearance_observation_from_crop(frame_path, person.get("box_px", {})) if frame_path else {}
        req_observation = _fuse_track_observation(
            track_embedding_history,
            resolve_key,
            req_observation,
            track_fusion_window,
        )

    if need_sync:
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
                should_resolve_identity = True
        except Exception:
            _mark_identity_pending(person)
            should_resolve_identity = True

    if resolve_async is None:
        return

    if sync_resolved:
        return

    if not should_resolve_identity:
        return

    if embedding_enabled and not req_observation:
        _submit_person_identity_resolve_async(
            person=person,
            media_id=media_id,
            track_id=int(track_id),
            frame_id=frame_id,
            frame_path=frame_path,
            camera_name=camera_name,
            camera_timestamp=camera_timestamp,
            embedding_enabled=True,
            resolve_async=resolve_async,
        )
        identity_resolve_last_ts[resolve_key] = now_ts
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
        print(f"[object_detection] Person identity resolve error: {_e}")


def _box_tuple_px(box: Dict[str, Any]) -> Optional[Tuple[float, float, float, float]]:
    try:
        x1 = float(box.get("x_min", 0.0) or 0.0)
        y1 = float(box.get("y_min", 0.0) or 0.0)
        x2 = float(box.get("x_max", 0.0) or 0.0)
        y2 = float(box.get("y_max", 0.0) or 0.0)
    except Exception:
        return None
    if x2 <= x1 or y2 <= y1:
        return None
    return (x1, y1, x2, y2)


def _appearance_embedding_from_crop(frame_path: str, box_px: Dict[str, Any]) -> List[float]:
    """Appearance embedding for relay-side unknown re-ID.

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


def _intersection_area(a: Tuple[float, float, float, float], b: Tuple[float, float, float, float]) -> float:
    x1 = max(a[0], b[0])
    y1 = max(a[1], b[1])
    x2 = min(a[2], b[2])
    y2 = min(a[3], b[3])
    if x2 <= x1 or y2 <= y1:
        return 0.0
    return float((x2 - x1) * (y2 - y1))


def _associate_faces_to_person_tracks(
    persons: List[Dict[str, Any]],
    full_frame_faces: List[Dict[str, Any]],
) -> Dict[int, Dict[str, float]]:
    """Associate full-frame detected faces to tracked person detections.

    We match by face-in-person coverage + light geometric priors and greedily
    assign a single face per track.
    """
    candidates: List[Tuple[float, int, int]] = []
    person_rows: List[Tuple[int, Tuple[float, float, float, float]]] = []
    face_rows: List[Tuple[Dict[str, float], Tuple[float, float, float, float], float]] = []

    for idx, p in enumerate(persons):
        tid = int(p.get("track_id", -1) or -1)
        if tid < 0:
            continue
        pb = _box_tuple_px(p.get("box_px", {}))
        if pb is None:
            continue
        person_rows.append((idx, pb))

    for f in full_frame_faces:
        fb_raw = f.get("box_px", {})
        fb = _box_tuple_px(fb_raw if isinstance(fb_raw, dict) else {})
        if fb is None:
            continue
        det_conf = float(f.get("face_det_conf", 0.0) or 0.0)
        face_rows.append(({
            "x_min": float(fb[0]),
            "y_min": float(fb[1]),
            "x_max": float(fb[2]),
            "y_max": float(fb[3]),
        }, fb, det_conf))

    if not person_rows or not face_rows:
        return {}

    for pi, (person_idx, pb) in enumerate(person_rows):
        px1, py1, px2, py2 = pb
        pw = max(1.0, px2 - px1)
        ph = max(1.0, py2 - py1)
        expected_y = py1 + (0.22 * ph)

        for fi, (_, fb, face_conf) in enumerate(face_rows):
            fx1, fy1, fx2, fy2 = fb
            fw = max(1.0, fx2 - fx1)
            fh = max(1.0, fy2 - fy1)
            face_area = fw * fh
            inter = _intersection_area(pb, fb)
            cover = inter / face_area if face_area > 0.0 else 0.0

            fcx = 0.5 * (fx1 + fx2)
            fcy = 0.5 * (fy1 + fy2)
            center_inside = (px1 <= fcx <= px2) and (py1 <= fcy <= py2)

            if not center_inside and cover < 0.35:
                continue

            center_dx = abs((fcx - px1) / pw - 0.5)
            center_score = max(0.0, 1.0 - (center_dx * 1.4))
            vertical_score = max(0.0, 1.0 - (abs(fcy - expected_y) / ph) * 2.8)

            score = (0.52 * cover) + (0.24 * face_conf) + (0.14 * vertical_score) + (0.10 * center_score)
            if score >= 0.28:
                candidates.append((score, pi, fi))

    if not candidates:
        return {}

    candidates.sort(key=lambda x: x[0], reverse=True)
    used_persons: Set[int] = set()
    used_faces: Set[int] = set()
    out: Dict[int, Dict[str, float]] = {}

    for _score, pi, fi in candidates:
        if pi in used_persons or fi in used_faces:
            continue
        person_idx, _ = person_rows[pi]
        face_box, _, _ = face_rows[fi]
        tid = int(persons[person_idx].get("track_id", -1) or -1)
        if tid < 0:
            continue
        out[tid] = face_box
        used_persons.add(pi)
        used_faces.add(fi)

    return out


# ── Background VLM processor ─────────────────────────────────────────────────

class _BgVLM:
    """
    Background daemon thread for VLM analysis.
    Keeps the fast YOLO pipeline non-blocking.
    """

    def __init__(self, vlm_instance: Any) -> None:
        self._vlm = vlm_instance
        self._q: "py_queue.Queue[Optional[tuple]]" = py_queue.Queue(maxsize=8)
        self._cache: Dict[str, str] = {}
        self._lock = threading.Lock()
        t = threading.Thread(target=self._run, daemon=True, name="sec-bg-vlm")
        t.start()
        _debug_log("[object_detection] Background VLM thread started")

    def request(self, key: str, image_path: str, prompt: str, max_tokens: int = 200) -> None:
        try:
            self._q.put_nowait((key, image_path, prompt, max_tokens))
        except py_queue.Full:
            pass

    def get_result(self, key: str) -> Optional[str]:
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
                    _debug_log(f"[object_detection] BgVLM done: key={key[:40]}")
                except Exception as exc:
                    print(f"[object_detection] BgVLM error: {exc}")
            except py_queue.Empty:
                continue


def _draw_bboxes_on_snapshot(frame_path: str, output_path: str, persons: List[Dict[str, Any]]) -> None:
    """Save frame with person bounding boxes drawn (blue with identity label)."""
    try:
        img = Image.open(frame_path).convert("RGB")
        draw = ImageDraw.Draw(img)
        for p in persons:
            box = p.get("box_px", {})
            x1 = int(box.get("x_min", 0))
            y1 = int(box.get("y_min", 0))
            x2 = int(box.get("x_max", 0))
            y2 = int(box.get("y_max", 0))
            tid = p.get("track_id")
            person_name = p.get("person_name")
            global_pid = p.get("global_person_id")
            conf = p.get("confidence", 0.0)
            color = (60, 120, 255)  # blue for persons
            draw.rectangle([x1, y1, x2, y2], outline=color, width=2)
            tid_str = f"#{tid} " if (tid is not None and tid >= 0) else ""
            label = str(person_name).strip() if isinstance(person_name, str) and person_name else "person"
            if isinstance(global_pid, str) and global_pid.startswith("unknown:"):
                label = label if label.lower().startswith("unknown") else f"{label} ({global_pid})"
            text = f"{tid_str}{label} {int(conf * 100)}%"
            text_box = draw.textbbox((0, 0), text)
            text_h = max(12, int(text_box[3] - text_box[1]))
            text_y = min(max(0, y2 - text_h - 2), max(0, img.height - text_h - 2))
            draw.text((x1, text_y), text, fill=color)
        img.save(output_path, "JPEG", quality=90)
    except Exception as exc:
        shutil.copy2(frame_path, output_path)
        print(f"[object_detection] bbox draw failed ({exc}), used raw copy")


def load_ai_model():
    model_id = os.getenv("MODEL_ID", "yolov8m.pt")
    conf_threshold = _env_float_any(["OBJECT_DET_CONF", "CONF_THRESHOLD"], 0.18)
    iou_threshold = _env_float_any(["OBJECT_DET_IOU", "IOU_THRESHOLD"], 0.55)
    max_det = _env_int_any(["OBJECT_DET_MAX_DET", "MAX_DETECTIONS"], 300)
    imgsz = _env_int_any(["OBJECT_DET_IMG_SIZE", "IMG_SIZE"], 960)
    person_only = _env_bool("OBJECT_DET_PERSON_ONLY", True)
    half_precision = _env_bool("OBJECT_DET_HALF", True)
    augment_inference = _env_bool("OBJECT_DET_AUGMENT", False)
    event_conf_threshold = _env_float_any(["OBJECT_DET_EVENT_CONF", "PERSON_EVENT_CONF"], 0.35)
    event_min_track_hits = _env_int_any(["OBJECT_DET_EVENT_MIN_HITS", "PERSON_EVENT_MIN_HITS"], 3)
    event_min_height_ratio = _env_float_any(["OBJECT_DET_EVENT_MIN_HEIGHT_RATIO"], 0.08)
    event_min_area_ratio = _env_float_any(["OBJECT_DET_EVENT_MIN_AREA_RATIO"], 0.002)

    _patch_ultralytics_torch_safe_load()
    device = _pick_device()

    print(f"[object_detection] Loading {model_id} on device={device}")
    model = YOLO(model_id)
    names = getattr(model, "names", {})
    person_classes = _person_class_ids(names) if person_only else None
    if person_only and not person_classes:
        print("[object_detection] OBJECT_DET_PERSON_ONLY=1 but no 'person' class was found; using all classes")
    print(
        "[object_detection] YOLO runtime: "
        f"conf={conf_threshold} iou={iou_threshold} imgsz={imgsz} "
        f"max_det={max_det} classes={person_classes or 'all'} "
        f"half={half_precision and device != 'cpu'} augment={augment_inference}"
    )
    print(
        "[object_detection] Event gate: "
        f"conf>={event_conf_threshold} hits>={event_min_track_hits} "
        f"height_ratio>={event_min_height_ratio} area_ratio>={event_min_area_ratio}"
    )
    legacy_mode = _legacy_mode_enabled()

    # Face recognition can be disabled when a shared face worker pool handles it.
    global FACE_REC_AVAILABLE
    face_rec_enabled = _env_bool("OBJECT_DET_ENABLE_FACE_RECOGNITION", True)
    if not face_rec_enabled:
        FACE_REC_AVAILABLE = False
        print("[object_detection] Face recognition disabled (OBJECT_DET_ENABLE_FACE_RECOGNITION=0)")
    elif FACE_REC_AVAILABLE:
        _face_rec.start_registry_refresh()
        print("[object_detection] Face recognizer started (async registry refresh)")
    identity_mgr = _GlobalPersonIdentityManager()
    identity_resolve_last_ts: Dict[Tuple[str, int], float] = {}
    relay_face_lookup_last_ts: Dict[Tuple[str, int], float] = {}
    relay_face_lookup_attempts: Dict[Tuple[str, int], int] = {}
    track_embedding_history: Dict[Tuple[str, int], List[Tuple[List[float], float]]] = {}
    person_track_stats: Dict[Tuple[str, int], Dict[str, float]] = {}
    track_fusion_window = max(1, _env_int("PERSON_REID_TRACK_FUSION_WINDOW", 4))
    identity_resolve_retry_seconds = max(
        0.0,
        float(os.getenv("PERSON_IDENTITY_RESOLVE_RETRY_SECONDS", "3.0")),
    )
    identity_resolve_sync_enabled = _env_bool("PERSON_IDENTITY_SYNC_RESOLVE", False)
    identity_embedding_enabled = _env_bool("PERSON_IDENTITY_EMBEDDING_ENABLED", True)
    relay_face_lookup_retry_seconds = max(
        0.0,
        _env_float("FACE_RELAY_LOOKUP_RETRY_SECONDS", _env_float("FACE_LOOKUP_RETRY_SECONDS", 1.0)),
    )
    relay_face_lookup_max_attempts = max(
        0,
        _env_int("FACE_RELAY_LOOKUP_MAX_ATTEMPTS", _env_int("FACE_LOOKUP_MAX_ATTEMPTS", 12)),
    )
    face_track_mgr = _FaceTrackEvidenceManager()
    face_diag_enabled = _env_bool("FACE_DIAGNOSTICS", False)
    face_diag_last_log = 0.0

    # When embedded face rec is disabled, use relay REST for shared face worker pool.
    use_relay_face = (not legacy_mode) and (not face_rec_enabled)
    use_relay_person_identity = not legacy_mode
    relay_client_available = False
    relay_request_face_lookup = None
    relay_fetch_face_identities = None
    relay_request_person_identity_resolve = None
    relay_resolve_person_identity_sync = None
    relay_fetch_person_identities = None
    try:
        from relay_client import request_face_lookup as _request_face_lookup
        from relay_client import fetch_face_identities as _fetch_face_identities
        from relay_client import request_person_identity_resolve as _request_person_identity_resolve
        from relay_client import resolve_person_identity_sync as _resolve_person_identity_sync
        from relay_client import fetch_person_identities as _fetch_person_identities
        from relay_client import set_relay_base as _set_relay_base

        _relay_base = os.getenv("RELAY_HTTP_BASE", "http://localhost:8020")
        _set_relay_base(_relay_base)
        relay_client_available = True
        relay_request_face_lookup = _request_face_lookup
        relay_fetch_face_identities = _fetch_face_identities
        relay_request_person_identity_resolve = _request_person_identity_resolve
        relay_resolve_person_identity_sync = _resolve_person_identity_sync
        relay_fetch_person_identities = _fetch_person_identities
        if use_relay_face:
            print(f"[object_detection] Relay face identity client enabled (base={_relay_base})")
    except ImportError:
        if use_relay_face:
            use_relay_face = False
            print("[object_detection] relay_client not available — face identity disabled")

    use_relay_person_identity = use_relay_person_identity and relay_client_available

    print(
        "[object_detection] Face track matcher: "
        f"v2={face_track_mgr.v2_enabled} "
        f"retry_interval_s={face_track_mgr.retry_interval_s} "
        f"bootstrap_attempts={face_track_mgr.bootstrap_attempts} "
        f"bootstrap_interval_s={face_track_mgr.bootstrap_interval_s} "
        f"max_retries={face_track_mgr.max_retries} "
        f"evidence_window={face_track_mgr.evidence_window}"
    )
    print(
        "[object_detection] Person identity realtime mode: "
        f"sync_resolve={identity_resolve_sync_enabled} "
        f"body_reid_embeddings={identity_embedding_enabled}"
    )

    def _run_detection(data: Dict[str, Any]) -> Tuple[List[Dict[str, Any]], List[Dict[str, Any]]]:
        """Run YOLO + ByteTrack. Returns (inputs_list, outputs)."""
        nonlocal face_diag_last_log
        inputs_list = _flatten_inputs(data)
        if not inputs_list:
            return [], []

        paths = [it["filepath"] for it in inputs_list]
        ids   = [it["id"] for it in inputs_list]
        input_by_id = {it["id"]: it for it in inputs_list}

        sizes: Dict[str, Tuple[int, int]] = {}
        for fid, fp in zip(ids, paths):
            try:
                img = Image.open(fp).convert("RGB")
                sizes[fid] = img.size
            except Exception:
                sizes[fid] = (0, 0)

        outputs: List[Dict[str, Any]] = []
        try:
            results = model.predict(
                paths,
                conf=conf_threshold,
                iou=iou_threshold,
                imgsz=imgsz,
                max_det=max_det,
                classes=person_classes,
                device=device,
                half=half_precision and device != "cpu",
                augment=augment_inference,
                verbose=False,
            )
        except Exception as e:
            for fid in ids:
                w, h = sizes.get(fid, (0, 0))
                inp = input_by_id.get(fid, {})
                outputs.append({
                    "id": fid,
                    "image_width": w,
                    "image_height": h,
                    "detections": [],
                    "error": str(e),
                    "_job_id": inp.get("_job_id"),
                    "_filepath": inp.get("filepath"),
                    "_media_id": inp.get("_media_id"),
                    "_media_name": inp.get("_media_name"),
                })
            return inputs_list, outputs

        for fid, res in zip(ids, results):
            w, h = sizes.get(fid, (0, 0))
            detections: List[Dict[str, Any]] = []
            try:
                if hasattr(res, "boxes") and res.boxes is not None and len(res.boxes) > 0:
                    boxes = res.boxes
                    xyxy = boxes.xyxy
                    confs = boxes.conf
                    clss = boxes.cls
                    for j in range(xyxy.shape[0]):
                        x_min = float(xyxy[j, 0].item())
                        y_min = float(xyxy[j, 1].item())
                        x_max = float(xyxy[j, 2].item())
                        y_max = float(xyxy[j, 3].item())
                        conf = float(confs[j].item())
                        cls_id = int(clss[j].item())
                        label = names.get(cls_id, str(cls_id)) if isinstance(names, dict) else str(cls_id)
                        detections.append({
                            "label": label,
                            "confidence": round(conf, 3),
                            "class_id": cls_id,
                            "box_px": {
                                "x_min": round(x_min, 2), "y_min": round(y_min, 2),
                                "x_max": round(x_max, 2), "y_max": round(y_max, 2),
                            },
                            "box": {
                                "x_min": round(x_min / w, 6) if w else 0.0,
                                "y_min": round(y_min / h, 6) if h else 0.0,
                                "x_max": round(x_max / w, 6) if w else 0.0,
                                "y_max": round(y_max / h, 6) if h else 0.0,
                            },
                        })
            except Exception as e:
                inp = input_by_id.get(fid, {})
                outputs.append({
                    "id": fid,
                    "image_width": w,
                    "image_height": h,
                    "detections": [],
                    "error": str(e),
                    "_job_id": inp.get("_job_id"),
                    "_filepath": inp.get("filepath"),
                    "_media_id": inp.get("_media_id"),
                    "_media_name": inp.get("_media_name"),
                })
                continue
            inp = input_by_id.get(fid, {})
            outputs.append({
                "id": fid,
                "image_width": w,
                "image_height": h,
                "detections": detections,
                "_job_id": inp.get("_job_id"),
                "_filepath": inp.get("filepath"),
                "_media_id": inp.get("_media_id"),
                "_media_name": inp.get("_media_name"),
            })

        # ByteTrack per stream + cross-camera identity enrichment
        if TRACKING_AVAILABLE:
            frame_rate = int(os.getenv("TRACKER_FRAME_RATE", "10"))
            tracker_by_media: Dict[str, Any] = {}
            touched_media_ids: Set[str] = set()

            def _on_face_result(mid: str, tid: int, seq: int, result: Dict[str, Any]) -> None:
                result_dict = result if isinstance(result, dict) else {}
                resolution = face_track_mgr.resolve(mid, tid, seq, result_dict)
                state = str(resolution.get("state") or "")
                if state == "stale":
                    return

                bind_payload = resolution.get("bind")
                if not isinstance(bind_payload, dict):
                    bind_payload = {}
                bound = identity_mgr.bind_track_from_face(mid, tid, bind_payload)

                if face_diag_enabled:
                    attempts = int(resolution.get("attempts", 0) or 0)
                    evidence_hits = int(resolution.get("evidence_hits", 0) or 0)
                    if state == "strong":
                        _debug_log(
                            f"[object_detection] Face match strong: media={mid} track={tid} "
                            f"person={bound.get('person_name')} attempts={attempts}"
                        )
                    elif state == "weak_promoted":
                        _debug_log(
                            f"[object_detection] Face match promoted: media={mid} track={tid} "
                            f"person={bound.get('person_name')} hits={evidence_hits} attempts={attempts}"
                        )
                    elif state == "unknown" and bool(resolution.get("unknown_finalized", False)):
                        _debug_log(
                            f"[object_detection] Face unknown finalized: media={mid} track={tid} attempts={attempts}"
                        )

            for item in outputs:
                item_media_id = str(
                    item.get("_media_id")
                    or data.get("media_id")
                    or data.get("media_idx")
                    or data.get("service_id")
                    or "default"
                )
                touched_media_ids.add(item_media_id)
                tracker = tracker_by_media.get(item_media_id)
                if tracker is None:
                    tracker = TrackerManager.get(item_media_id, frame_rate=frame_rate)
                    tracker_by_media[item_media_id] = tracker

                if item.get("detections"):
                    item["detections"] = tracker.update(item["detections"])

                persons = [d for d in item.get("detections", []) if d.get("label", "").lower() == "person"]
                if not persons:
                    continue

                frame_path = str(item.get("_filepath", ""))
                item_frame_id = select_face_lookup_frame_id(item)

                # Relay-based face identity: bulk-fetch cached identities for this camera.
                relay_identities: Dict[str, Any] = {}
                if use_relay_face and item_media_id:
                    try:
                        if relay_fetch_face_identities is not None:
                            relay_identities = relay_fetch_face_identities(item_media_id)
                    except Exception:
                        pass
                relay_person_identities: Dict[str, Any] = {}
                if use_relay_person_identity and relay_fetch_person_identities is not None and item_media_id:
                    try:
                        relay_person_identities = relay_fetch_person_identities(item_media_id)
                    except Exception:
                        pass

                img_np: Optional[np.ndarray] = None
                assoc_face_box_by_tid: Dict[int, Dict[str, float]] = {}
                if not use_relay_face and FACE_REC_AVAILABLE and frame_path and os.path.exists(frame_path):
                    try:
                        img_np = np.array(Image.open(frame_path).convert("RGB"))
                        if hasattr(_face_rec, "detect_faces_full_frame"):
                            frame_faces = _face_rec.detect_faces_full_frame(img_np)
                            assoc_face_box_by_tid = _associate_faces_to_person_tracks(persons, frame_faces)
                    except Exception:
                        img_np = None

                for person in persons:
                    tid = int(person.get("track_id", -1))
                    now_ts = time.time()
                    if tid < 0:
                        # Untracked detection: surface as unknown without persistent ID.
                        person.setdefault("person_name", "Unknown")
                        continue

                    track_key = (item_media_id, tid)
                    stats = person_track_stats.get(track_key)
                    if not stats or (now_ts - float(stats.get("last_seen", 0.0))) > 2.0:
                        stats = {"hits": 0.0, "first_seen": now_ts}
                    stats["hits"] = float(stats.get("hits", 0.0)) + 1.0
                    stats["last_seen"] = now_ts
                    person_track_stats[track_key] = stats
                    person["track_hits"] = int(stats["hits"])
                    person["track_age_s"] = round(max(0.0, now_ts - float(stats.get("first_seen", now_ts))), 3)

                    face_track_mgr.touch(item_media_id, tid, now_ts)

                    # Keep tracker state warm for existing first-seen behavior.
                    first_seen = tracker.is_first_seen(tid)
                    if first_seen:
                        tracker.mark_seen(tid)

                    latest = identity_mgr.get_track_binding(item_media_id, tid)
                    if latest and str(latest.get("person_id") or "").strip():
                        _apply_identity_to_person(person, latest)
                    elif not use_relay_person_identity:
                        fallback = identity_mgr.ensure_track_binding(item_media_id, tid)
                        _apply_identity_to_person(person, fallback)

                    if use_relay_face:
                        # Apply relay-side face identity cache
                        rid = relay_identities.get(str(tid))
                        _apply_relay_face_identity(person, rid, tid)

                        lookup_key = (item_media_id, tid)
                        if _should_request_relay_face_lookup(
                            first_seen=first_seen,
                            person=person,
                            frame_id=item_frame_id,
                            now_ts=now_ts,
                            last_lookup_ts=relay_face_lookup_last_ts.get(lookup_key, 0.0),
                            attempts=relay_face_lookup_attempts.get(lookup_key, 0),
                            retry_seconds=relay_face_lookup_retry_seconds,
                            max_attempts=relay_face_lookup_max_attempts,
                        ):
                            try:
                                pb = person.get("box_px", {})
                                if relay_request_face_lookup is not None:
                                    relay_request_face_lookup(
                                        item_media_id, tid, item_frame_id,
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
                                print(f"[object_detection] Relay face lookup error: {_e}")

                    if use_relay_person_identity:
                        pid_identity = relay_person_identities.get(str(tid))
                        _apply_relay_person_identity(person, pid_identity)

                    if use_relay_person_identity:
                        _resolve_person_identity_for_track(
                            first_seen=first_seen,
                            person=person,
                            media_id=item_media_id,
                            track_id=int(tid),
                            frame_id=item_frame_id or str(item.get("id") or ""),
                            frame_path=frame_path,
                            camera_name=str(item.get("_media_name") or item_media_id),
                            camera_timestamp=time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                            now_ts=now_ts,
                            identity_resolve_last_ts=identity_resolve_last_ts,
                            track_embedding_history=track_embedding_history,
                            track_fusion_window=track_fusion_window,
                            min_interval_seconds=identity_resolve_retry_seconds,
                            sync_enabled=identity_resolve_sync_enabled,
                            embedding_enabled=identity_embedding_enabled,
                            resolve_sync=relay_resolve_person_identity_sync,
                            resolve_async=relay_request_person_identity_resolve,
                        )

                    if not use_relay_face:
                        # Embedded face rec path
                        diag = face_track_mgr.diag(item_media_id, tid)
                        if diag:
                            person.update(diag)

                        if not FACE_REC_AVAILABLE or img_np is None:
                            continue

                        attempt_seq = face_track_mgr.begin_lookup(item_media_id, tid, now_ts)
                        if attempt_seq is None:
                            continue

                        pb = person.get("box_px", {})
                        assoc_box = assoc_face_box_by_tid.get(tid)
                        if isinstance(assoc_box, dict):
                            person["associated_face_box"] = assoc_box
                        try:
                            if hasattr(_face_rec, "submit_async_person_dual"):
                                _face_rec.submit_async_person_dual(
                                    img_np,
                                    pb.get("x_min", 0), pb.get("y_min", 0),
                                    pb.get("x_max", 0), pb.get("y_max", 0),
                                    track_id=tid,
                                    include_embedding=True,
                                    associated_face_box=assoc_box,
                                    callback=lambda t, r, _mid=item_media_id, _seq=attempt_seq: _on_face_result(
                                        _mid,
                                        t,
                                        _seq,
                                        r if isinstance(r, dict) else {},
                                    ),
                                )
                            else:
                                _face_rec.submit_async(
                                    img_np,
                                    pb.get("x_min", 0), pb.get("y_min", 0),
                                    pb.get("x_max", 0), pb.get("y_max", 0),
                                    track_id=tid,
                                    include_embedding=True,
                                    callback=lambda t, r, _mid=item_media_id, _seq=attempt_seq: _on_face_result(
                                        _mid,
                                        t,
                                        _seq,
                                        r if isinstance(r, dict) else {},
                                    ),
                                )
                        except Exception as _e:
                            face_track_mgr.cancel_inflight(item_media_id, tid, attempt_seq)
                            print(f"[object_detection] Face rec submit error: {_e}")

            for item_media_id in touched_media_ids:
                tracker = tracker_by_media.get(item_media_id)
                if tracker is not None:
                    tracker.cleanup_stale()
            stats_cutoff = time.time() - 120.0
            for key, stats in list(person_track_stats.items()):
                if float(stats.get("last_seen", 0.0)) < stats_cutoff:
                    person_track_stats.pop(key, None)
            face_track_mgr.cleanup_stale()
        else:
            # No tracker available: best-effort labels only.
            for item in outputs:
                for det in item.get("detections", []):
                    if det.get("label", "").lower() == "person":
                        det.setdefault("person_name", "Unknown")

        identity_mgr.cleanup_stale()
        if face_diag_enabled:
            now_ts = time.time()
            if (now_ts - face_diag_last_log) >= 10.0:
                face_diag_last_log = now_ts
                det_metrics = face_track_mgr.metrics_snapshot()
                if FACE_REC_AVAILABLE and hasattr(_face_rec, "get_diagnostics_snapshot"):
                    _debug_log(
                        f"[object_detection] Face diagnostics: track={det_metrics} "
                        f"matcher={_face_rec.get_diagnostics_snapshot()}"
                    )
                else:
                    _debug_log(f"[object_detection] Face diagnostics: track={det_metrics}")

        return inputs_list, outputs

    custom_snapshot_last: Dict[str, float] = {}
    custom_snapshot_cooldown_s = float(os.getenv("CUSTOM_VLM_SNAPSHOT_COOLDOWN_S", "3.0"))

    def _draw_custom_bboxes(frame_path: str, output_path: str, detections: List[Dict[str, Any]]) -> None:
        """Save frame with generic detection bboxes drawn."""
        try:
            img = Image.open(frame_path).convert("RGB")
            draw = ImageDraw.Draw(img)
            for det in detections:
                box = det.get("box_px", {})
                x1 = int(box.get("x_min", 0))
                y1 = int(box.get("y_min", 0))
                x2 = int(box.get("x_max", 0))
                y2 = int(box.get("y_max", 0))
                label = str(det.get("label", "object"))
                conf = float(det.get("confidence", 0.0) or 0.0)
                tid = det.get("track_id")
                prefix = f"#{int(tid)} " if isinstance(tid, (int, float)) and int(tid) >= 0 else ""
                draw.rectangle([x1, y1, x2, y2], outline=(110, 180, 255), width=2)
                text = f"{prefix}{label} {int(conf * 100)}%"
                text_box = draw.textbbox((0, 0), text)
                text_h = max(12, int(text_box[3] - text_box[1]))
                text_y = min(max(0, y2 - text_h - 2), max(0, img.height - text_h - 2))
                draw.text((x1, text_y), text, fill=(110, 180, 255))
            img.save(output_path, "JPEG", quality=90)
        except Exception:
            shutil.copy2(frame_path, output_path)

    def _run_custom_detection(data: Dict[str, Any]) -> Tuple[List[Dict[str, Any]], List[Dict[str, Any]], Dict[str, Any]]:
        """Run custom YOLO model from job input, without person-only filtering."""
        inputs_list = _flatten_inputs(data)
        if not inputs_list:
            return [], [], {
                "custom_enable_vlm": _coerce_bool(data.get("custom_enable_vlm"), False),
                "custom_enable_face": _coerce_bool(data.get("custom_enable_face"), False),
                "custom_model_path": str(data.get("custom_model_path") or "").strip(),
            }

        custom_model_path = str(
            data.get("custom_model_path")
            or os.getenv("CUSTOM_YOLO_MODEL_PATH", "")
        ).strip()
        custom_enable_vlm = _coerce_bool(
            data.get("custom_enable_vlm"),
            _env_bool("OBJECT_DET_ENABLE_VLM", False),
        )
        custom_enable_face = _coerce_bool(
            data.get("custom_enable_face"),
            _env_bool("OBJECT_DET_ENABLE_FACE_RECOGNITION", False),
        )

        opts = {
            "custom_enable_vlm": custom_enable_vlm,
            "custom_enable_face": custom_enable_face,
            "custom_model_path": custom_model_path,
        }

        paths = [it["filepath"] for it in inputs_list]
        ids = [it["id"] for it in inputs_list]
        input_by_id = {it["id"]: it for it in inputs_list}

        sizes: Dict[str, Tuple[int, int]] = {}
        for fid, fp in zip(ids, paths):
            try:
                img = Image.open(fp).convert("RGB")
                sizes[fid] = img.size
            except Exception:
                sizes[fid] = (0, 0)

        outputs: List[Dict[str, Any]] = []
        if not custom_model_path:
            for fid in ids:
                w, h = sizes.get(fid, (0, 0))
                inp = input_by_id.get(fid, {})
                outputs.append({
                    "id": fid,
                    "image_width": w,
                    "image_height": h,
                    "detections": [],
                    "error": "custom_model_path is required",
                    "_job_id": inp.get("_job_id"),
                    "_filepath": inp.get("filepath"),
                    "_media_id": inp.get("_media_id"),
                    "_media_name": inp.get("_media_name"),
                })
            return inputs_list, outputs, opts

        try:
            custom_model = _load_custom_model_cached(custom_model_path)
            custom_names = getattr(custom_model, "names", {})
        except Exception as e:
            err_msg = f"custom model load failed ({custom_model_path}): {e}"
            print(f"[object_detection] {err_msg}")
            for fid in ids:
                w, h = sizes.get(fid, (0, 0))
                inp = input_by_id.get(fid, {})
                outputs.append({
                    "id": fid,
                    "image_width": w,
                    "image_height": h,
                    "detections": [],
                    "error": err_msg,
                    "_job_id": inp.get("_job_id"),
                    "_filepath": inp.get("filepath"),
                    "_media_id": inp.get("_media_id"),
                    "_media_name": inp.get("_media_name"),
                })
            return inputs_list, outputs, opts

        try:
            results = custom_model.predict(
                paths,
                conf=conf_threshold,
                iou=iou_threshold,
                imgsz=imgsz,
                max_det=max_det,
                device=device,
                half=half_precision and device != "cpu",
                augment=augment_inference,
                verbose=False,
            )
        except Exception as e:
            for fid in ids:
                w, h = sizes.get(fid, (0, 0))
                inp = input_by_id.get(fid, {})
                outputs.append({
                    "id": fid,
                    "image_width": w,
                    "image_height": h,
                    "detections": [],
                    "error": str(e),
                    "_job_id": inp.get("_job_id"),
                    "_filepath": inp.get("filepath"),
                    "_media_id": inp.get("_media_id"),
                    "_media_name": inp.get("_media_name"),
                })
            return inputs_list, outputs, opts

        for fid, res in zip(ids, results):
            w, h = sizes.get(fid, (0, 0))
            detections: List[Dict[str, Any]] = []
            try:
                if hasattr(res, "boxes") and res.boxes is not None and len(res.boxes) > 0:
                    boxes = res.boxes
                    xyxy = boxes.xyxy
                    confs = boxes.conf
                    clss = boxes.cls
                    for j in range(xyxy.shape[0]):
                        x_min = float(xyxy[j, 0].item())
                        y_min = float(xyxy[j, 1].item())
                        x_max = float(xyxy[j, 2].item())
                        y_max = float(xyxy[j, 3].item())
                        conf = float(confs[j].item())
                        cls_id = int(clss[j].item())
                        label = custom_names.get(cls_id, str(cls_id)) if isinstance(custom_names, dict) else str(cls_id)
                        detections.append({
                            "label": label,
                            "confidence": round(conf, 3),
                            "class_id": cls_id,
                            "box_px": {
                                "x_min": round(x_min, 2),
                                "y_min": round(y_min, 2),
                                "x_max": round(x_max, 2),
                                "y_max": round(y_max, 2),
                            },
                            "box": {
                                "x_min": round(x_min / w, 6) if w else 0.0,
                                "y_min": round(y_min / h, 6) if h else 0.0,
                                "x_max": round(x_max / w, 6) if w else 0.0,
                                "y_max": round(y_max / h, 6) if h else 0.0,
                            },
                        })
            except Exception as e:
                inp = input_by_id.get(fid, {})
                outputs.append({
                    "id": fid,
                    "image_width": w,
                    "image_height": h,
                    "detections": [],
                    "error": str(e),
                    "_job_id": inp.get("_job_id"),
                    "_filepath": inp.get("filepath"),
                    "_media_id": inp.get("_media_id"),
                    "_media_name": inp.get("_media_name"),
                })
                continue

            inp = input_by_id.get(fid, {})
            outputs.append({
                "id": fid,
                "image_width": w,
                "image_height": h,
                "detections": detections,
                "_job_id": inp.get("_job_id"),
                "_filepath": inp.get("filepath"),
                "_media_id": inp.get("_media_id"),
                "_media_name": inp.get("_media_name"),
            })

        tracker_by_media: Dict[str, Any] = {}
        if TRACKING_AVAILABLE:
            frame_rate = int(os.getenv("TRACKER_FRAME_RATE", "10"))
            for item in outputs:
                item_media_id = str(item.get("_media_id") or "default")
                tracker_key = f"custom:{item_media_id}:{custom_model_path}"
                tracker = tracker_by_media.get(tracker_key)
                if tracker is None:
                    tracker = TrackerManager.get(tracker_key, frame_rate=frame_rate)
                    tracker_by_media[tracker_key] = tracker
                if item.get("detections"):
                    item["detections"] = tracker.update(item["detections"])

        if (
            not _legacy_mode_enabled()
            and custom_enable_face
            and relay_client_available
            and relay_request_face_lookup is not None
            and relay_fetch_face_identities is not None
        ):
            for item in outputs:
                item_media_id = str(item.get("_media_id") or "default")
                item_frame_id = select_face_lookup_frame_id(item)
                relay_identities: Dict[str, Any] = {}
                try:
                    relay_identities = relay_fetch_face_identities(item_media_id)
                except Exception:
                    relay_identities = {}

                for det in item.get("detections", []):
                    if str(det.get("label", "")).strip().lower() != "person":
                        continue
                    tid = int(det.get("track_id", -1))
                    if tid < 0:
                        det.setdefault("person_name", "Unknown")
                        continue

                    cached = relay_identities.get(str(tid))
                    _apply_relay_face_identity(det, cached, tid)

                    tracker_key = f"custom:{item_media_id}:{custom_model_path}"
                    tracker = tracker_by_media.get(tracker_key)
                    first_seen = bool(tracker and tracker.is_first_seen(tid))
                    now_ts = time.time()
                    lookup_key = (item_media_id, tid)
                    if _should_request_relay_face_lookup(
                        first_seen=first_seen,
                        person=det,
                        frame_id=item_frame_id,
                        now_ts=now_ts,
                        last_lookup_ts=relay_face_lookup_last_ts.get(lookup_key, 0.0),
                        attempts=relay_face_lookup_attempts.get(lookup_key, 0),
                        retry_seconds=relay_face_lookup_retry_seconds,
                        max_attempts=relay_face_lookup_max_attempts,
                    ):
                        try:
                            pb = det.get("box_px", {})
                            relay_request_face_lookup(
                                item_media_id,
                                tid,
                                item_frame_id,
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
                            print(f"[object_detection] Custom relay face lookup error: {_e}")
                    if first_seen and tracker:
                        tracker.mark_seen(tid)

        if TRACKING_AVAILABLE:
            for tracker in tracker_by_media.values():
                try:
                    tracker.cleanup_stale()
                except Exception:
                    pass

        return inputs_list, outputs, opts

    def _annotate_custom_events(
        inputs_list: List[Dict[str, Any]],
        outputs: List[Dict[str, Any]],
        opts: Dict[str, Any],
    ) -> None:
        snapshot_dir = os.getenv("SNAPSHOT_DIR", "/tmp/snapshots")
        os.makedirs(snapshot_dir, exist_ok=True)
        input_by_id = {inp["id"]: inp for inp in inputs_list}
        custom_enable_vlm = bool(opts.get("custom_enable_vlm", False))
        custom_enable_face = bool(opts.get("custom_enable_face", False))

        for item in outputs:
            inp = input_by_id.get(item.get("id"), {})
            item_media_id = str(item.get("_media_id") or inp.get("_media_id") or "default")
            detections = item.get("detections", [])
            if not isinstance(detections, list):
                detections = []
                item["detections"] = detections

            item["total_detections"] = len(detections)
            item["custom_enable_vlm"] = custom_enable_vlm
            item["custom_enable_face"] = custom_enable_face

            if not detections:
                continue

            by_label: Dict[str, int] = {}
            for det in detections:
                lbl = str(det.get("label", "object")).strip() or "object"
                by_label[lbl] = by_label.get(lbl, 0) + 1

            labels_sorted = sorted(by_label.keys())
            item["violation_labels"] = labels_sorted
            summary_parts = [f"{k}:{by_label[k]}" for k in sorted(by_label.keys(), key=lambda x: by_label[x], reverse=True)[:6]]
            item["event_brief"] = "Detections - " + ", ".join(summary_parts)

            person_names = list(
                dict.fromkeys(
                    str(det.get("person_name"))
                    for det in detections
                    if str(det.get("label", "")).strip().lower() == "person"
                    and isinstance(det.get("person_name"), str)
                    and str(det.get("person_name")).strip() != ""
                )
            )
            if person_names:
                item["person_names"] = person_names
            person_count = sum(1 for det in detections if str(det.get("label", "")).strip().lower() == "person")
            item["person_count"] = person_count

            if custom_enable_vlm:
                frame_path = item.get("_filepath") or inp.get("filepath")
                now_ts = time.time()
                last_ts = custom_snapshot_last.get(item_media_id, 0.0)
                if frame_path and os.path.exists(frame_path) and (now_ts - last_ts) >= custom_snapshot_cooldown_s:
                    snap_name = f"{item['id']}_custom_detect.jpg"
                    snap_path = os.path.join(snapshot_dir, snap_name)
                    _draw_custom_bboxes(frame_path, snap_path, detections)
                    item["snapshot_path"] = snap_path
                    custom_snapshot_last[item_media_id] = now_ts

    def _group_output(inputs_list: List[Dict[str, Any]], outputs: List[Dict[str, Any]]) -> Dict[str, Any]:
        job_id_map: Dict[str, List[Dict[str, Any]]] = {}
        for idx, out_item in enumerate(outputs):
            inp = inputs_list[idx] if idx < len(inputs_list) else {}
            job_id = inp.get("_job_id")
            if job_id:
                clean = {k: v for k, v in out_item.items() if not str(k).startswith("_")}
                job_id_map.setdefault(job_id, []).append(clean)
        if job_id_map:
            return {"output": [{"id": jid, "output": items} for jid, items in job_id_map.items()]}
        clean_outputs = [{k: v for k, v in item.items() if not str(k).startswith("_")} for item in outputs]
        return {"output": clean_outputs}

    def _annotate_person_events(
        inputs_list: List[Dict[str, Any]],
        outputs: List[Dict[str, Any]],
    ) -> None:
        snapshot_dir = os.getenv("SNAPSHOT_DIR", "/tmp/snapshots")
        os.makedirs(snapshot_dir, exist_ok=True)
        input_by_id = {inp["id"]: inp for inp in inputs_list}

        for item in outputs:
            inp = input_by_id.get(item.get("id"), {})
            item_media_id = str(
                item.get("_media_id")
                or inp.get("_media_id")
                or "default"
            )
            item_media_name = str(
                item.get("_media_name")
                or inp.get("_media_name")
                or item_media_id
            )

            persons = [d for d in item.get("detections", []) if d.get("label", "").lower() == "person"]
            item["detections"] = persons
            item["person_count"] = len(persons)
            if not persons:
                item["first_detection"] = False
                continue

            image_width = int(item.get("image_width") or 0)
            image_height = int(item.get("image_height") or 0)
            event_persons = [
                p for p in persons
                if _is_event_credible_person(
                    p,
                    image_width,
                    image_height,
                    min_confidence=event_conf_threshold,
                    min_track_hits=event_min_track_hits,
                    min_height_ratio=event_min_height_ratio,
                    min_area_ratio=event_min_area_ratio,
                )
            ]
            item["event_person_count"] = len(event_persons)
            if not event_persons:
                item["first_detection"] = False
                continue

            now_ts = time.time()
            visit_persons: List[Dict[str, Any]] = []
            visit_ids: Set[str] = set()
            for p in event_persons:
                _ensure_person_identity_for_visit(identity_mgr, item_media_id, p)
                gid = p.get("global_person_id")
                if isinstance(gid, str) and gid:
                    if identity_mgr.mark_camera_visit(gid, item_media_id, now_ts):
                        p["first_detection"] = True
                        visit_persons.append(p)
                        visit_ids.add(gid)

            names = list(
                dict.fromkeys(
                    p.get("person_name")
                    for p in event_persons
                    if isinstance(p.get("person_name"), str) and p.get("person_name")
                )
            )
            if names:
                item["person_names"] = names

            n_visits = len(visit_persons)
            item["first_detection"] = n_visits > 0
            item["severity"] = "medium" if n_visits > 0 else "low"
            item["camera_name"] = item_media_name or item_media_id
            item["camera_timestamp"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())

            if visit_ids:
                item["visit_person_ids"] = sorted(visit_ids)
                item["global_person_ids"] = sorted(visit_ids)
                item["global_person_id"] = sorted(visit_ids)[0]

            if n_visits > 0:
                visit_names = list(
                    dict.fromkeys(
                        p.get("person_name")
                        for p in visit_persons
                        if isinstance(p.get("person_name"), str) and p.get("person_name")
                    )
                )
                known_visit_names = [
                    name for name in visit_names
                    if isinstance(name, str) and _is_known_person_name(name)
                ]
                known_frame_names = [
                    name for name in names
                    if isinstance(name, str) and _is_known_person_name(name)
                ]
                brief_names = known_visit_names or known_frame_names or visit_names
                camera_label = item_media_name or item_media_id
                if brief_names:
                    item["event_brief"] = f"{', '.join(brief_names[:3])} detected on {camera_label}; person visit observed."
                else:
                    item["event_brief"] = f"Person detected on {camera_label}; visit observed ({n_visits})."
                item["vlm_summary"] = item["event_brief"]
                item["event_kind"] = "person_visit"
            elif names:
                camera_label = item_media_name or item_media_id
                item["event_brief"] = f"{', '.join(names[:2])} detected on {camera_label}; appears to be working."
                item["event_kind"] = "person_activity"
            else:
                item["event_brief"] = f"Person activity observed ({len(event_persons)} person(s))"
                item["event_kind"] = "person_activity"

            # Save one snapshot per unique camera-visit event.
            if n_visits > 0:
                frame_path = item.get("_filepath") or inp.get("filepath")
                if frame_path and os.path.exists(frame_path):
                    snap_name = f"{item['id']}_person_first.jpg"
                    snap_path = os.path.join(snapshot_dir, snap_name)
                    _draw_bboxes_on_snapshot(frame_path, snap_path, event_persons)
                    item["snapshot_path"] = snap_path
                    _debug_log(f"[object_detection] Snapshot: {snap_name} persons={len(event_persons)}")

    # ── Security worker with background VLM ──────────────────────────────────

    def worker_function_person_only(data: Dict[str, Any]) -> Dict[str, Any]:
        inputs_list, outputs = _run_detection(data)
        _annotate_person_events(inputs_list, outputs)
        return _group_output(inputs_list, outputs)

    def worker_function_custom(data: Dict[str, Any]) -> Dict[str, Any]:
        inputs_list, outputs, opts = _run_custom_detection(data)
        _annotate_custom_events(inputs_list, outputs, opts)
        return _group_output(inputs_list, outputs)

    def _is_custom_job(data: Dict[str, Any]) -> bool:
        if _coerce_bool(data.get("custom_yolo_enabled"), False):
            return True
        model_path = str(data.get("custom_model_path") or "").strip()
        return model_path != ""

    enable_vlm = os.getenv("OBJECT_DET_ENABLE_VLM", "0").strip().lower() in ("1", "true", "yes", "on")
    if not enable_vlm:
        print("[object_detection] OBJECT_DET_ENABLE_VLM=0 — person detection only mode")
        def _dispatch_without_vlm(data: Dict[str, Any]) -> Dict[str, Any]:
            if _is_custom_job(data):
                return worker_function_custom(data)
            return worker_function_person_only(data)
        return _dispatch_without_vlm

    if not VLM_AVAILABLE:
        print("[object_detection] VLM helper unavailable — person detection only mode")
        def _dispatch_without_vlm_helper(data: Dict[str, Any]) -> Dict[str, Any]:
            if _is_custom_job(data):
                return worker_function_custom(data)
            return worker_function_person_only(data)
        return _dispatch_without_vlm_helper

    # Lazy async VLM init so detection starts immediately.
    bg_vlm_lock = threading.Lock()
    bg_vlm_holder: Dict[str, Optional[_BgVLM]] = {"inst": None}
    vlm_init_started = False
    vlm_init_failed = False

    def _start_vlm_init_once() -> None:
        nonlocal vlm_init_started, vlm_init_failed
        if vlm_init_started or vlm_init_failed:
            return
        vlm_init_started = True

        def _init() -> None:
            nonlocal vlm_init_failed
            try:
                _debug_log("[object_detection] Initializing background VLM worker...")
                vlm = VLMHelper.get_instance()
                inst = _BgVLM(vlm)
                with bg_vlm_lock:
                    bg_vlm_holder["inst"] = inst
                _debug_log("[object_detection] Background VLM integration enabled for Security Check")
            except Exception as e:
                vlm_init_failed = True
                print(f"[object_detection] VLM init failed: {e} — continuing in person-only mode")

        t = threading.Thread(target=_init, daemon=True, name="objdet-vlm-init")
        t.start()

    _start_vlm_init_once()

    def _get_bg_vlm() -> Optional[_BgVLM]:
        with bg_vlm_lock:
            return bg_vlm_holder["inst"]

    def worker_function_security(data: Dict[str, Any]) -> Dict[str, Any]:
        t0 = time.time()
        inputs_list, outputs = _run_detection(data)

        vlm_cooldown = float(os.getenv("VLM_COOLDOWN_SECONDS", "15"))
        frame_rate = int(os.getenv("TRACKER_FRAME_RATE", "10"))

        # Populate person-only fields first (detections/person_count/snapshot_path/...).
        _annotate_person_events(inputs_list, outputs)

        for item in outputs:
            persons = item.get("detections", [])
            if not persons:
                continue

            item_media_id = str(
                item.get("_media_id")
                or data.get("media_id")
                or data.get("media_idx")
                or data.get("service_id")
                or "default"
            )
            vlm_tracker = (
                TrackerManager.get(item_media_id, frame_rate=frame_rate)
                if TRACKING_AVAILABLE else None
            )

            # ── Check VLM cache for any ready results ────────────────────────
            for p in persons:
                tid = p.get("track_id", -1)
                vlm_key = f"{item_media_id}:person:{tid}"
                bg_vlm = _get_bg_vlm()
                if bg_vlm:
                    result = bg_vlm.get_result(vlm_key)
                    existing = str(item.get("vlm_summary") or "").strip()
                    fallback = str(item.get("event_brief") or "").strip()
                    if result and (not existing or existing == fallback):
                        item["vlm_summary"] = result
                        _debug_log(f"[object_detection] VLM result attached: track={tid}")

            # ── Determine which persons need VLM analysis ────────────────────
            new_persons: List[Dict[str, Any]] = []
            cooldown_persons: List[Dict[str, Any]] = []

            for p in persons:
                tid = p.get("track_id", -1)
                if p.get("first_detection"):
                    new_persons.append(p)
                elif tid >= 0 and vlm_tracker and vlm_tracker.should_alert(
                    tid, f"person_{tid}", cooldown_seconds=vlm_cooldown
                ):
                    cooldown_persons.append(p)

            alerts_needed = new_persons + cooldown_persons
            if not alerts_needed:
                continue

            frame_path = item.get("_filepath")
            if not frame_path or not os.path.exists(frame_path):
                continue

            n_new   = len(new_persons)
            n_total = len(persons)
            names = item.get("person_names", [])
            if not isinstance(names, list):
                names = []

            # ── Queue VLM analysis in background (non-blocking) ──────────────
            identified_text = f"Identified person(s): {', '.join(names[:3])}. " if names else ""
            prompt = (
                f"Security monitoring. {n_total} person(s) detected "
                f"({n_new} new arrival(s)). "
                f"{identified_text}"
                "Briefly describe: (1) the location and environment — "
                "what kind of area is this, what are people doing; "
                "(2) any security concerns such as unauthorized access, "
                "suspicious behaviour, fight, theft, or safety hazard. "
                "3-4 sentences max."
            )
            for p in alerts_needed:
                tid = p.get("track_id", -1)
                vlm_key = f"{item_media_id}:person:{tid}"
                bg_vlm = _get_bg_vlm()
                if bg_vlm:
                    bg_vlm.request(vlm_key, frame_path, prompt)

        dt_ms = (time.time() - t0) * 1000
        _debug_log(f"[object_detection] Security done. batch={len(inputs_list)} time_ms={dt_ms:.1f}")

        return _group_output(inputs_list, outputs)

    def worker_function_dispatch(data: Dict[str, Any]) -> Dict[str, Any]:
        if _is_custom_job(data):
            return worker_function_custom(data)
        return worker_function_security(data)

    return worker_function_dispatch


if __name__ == "__main__":
    worker_function = load_ai_model()
    asyncio.run(client_handler(worker_function))
