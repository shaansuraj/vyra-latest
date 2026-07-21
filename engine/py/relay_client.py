"""Relay REST client helpers for detection workers.

When VLM and face recognition are handled by shared worker pools instead of
being embedded, detection workers use these helpers to:
  - POST /cv/face-lookup  (fire-and-forget async face identification)
  - GET  /cv/face-identities  (bulk-fetch cached identities for a camera)

All calls are non-blocking: face lookups run in a background thread pool,
identity fetches use a short local TTL cache to avoid per-frame HTTP overhead.
"""

import os
import threading
import time
from concurrent.futures import ThreadPoolExecutor
from typing import Any, Dict, List, Optional

import requests

_RELAY_HTTP_BASE = os.getenv("RELAY_HTTP_BASE", "http://localhost:8020")

# ── Face identity cache ─────────────────────────────────────────────────────

_identity_cache_lock = threading.Lock()
_identity_cache: Dict[str, Dict[str, Any]] = {}  # media_id → {track_id_str: identity}
_identity_cache_ts: Dict[str, float] = {}  # media_id → last_fetch_ts
_IDENTITY_CACHE_TTL = 0.5  # seconds

_person_identity_cache_lock = threading.Lock()
_person_identity_cache: Dict[str, Dict[str, Any]] = {}  # media_id → {track_id_str: identity}
_person_identity_cache_ts: Dict[str, float] = {}  # media_id → last_fetch_ts
_PERSON_IDENTITY_CACHE_TTL = 0.5  # seconds
_PERSON_IDENTITY_SYNC_TIMEOUT_S = max(
    0.05,
    float(os.getenv("PERSON_IDENTITY_RESOLVE_WAIT_MS", "350")) / 1000.0,
)

# Background thread pool for fire-and-forget requests
_executor = ThreadPoolExecutor(max_workers=2, thread_name_prefix="relay-client")


def set_relay_base(base_url: str) -> None:
    """Override relay base URL (call early, before any requests)."""
    global _RELAY_HTTP_BASE
    _RELAY_HTTP_BASE = base_url.rstrip("/")


def _build_person_identity_payload(
    media_id: str,
    track_id: int,
    frame_id: str = "",
    person_id: str = "",
    person_name: str = "",
    match_score: Optional[float] = None,
    embedding: Optional[List[float]] = None,
    camera_name: str = "",
    camera_timestamp: str = "",
    embedding_kind: str = "",
    embedding_model: str = "",
    quality_score: Optional[float] = None,
    observation_ts: str = "",
) -> Dict[str, Any]:
    payload: Dict[str, Any] = {
        "media_id": media_id,
        "track_id": int(track_id),
    }
    if frame_id:
        payload["frame_id"] = frame_id
    if person_id:
        payload["person_id"] = person_id
    if person_name:
        payload["person_name"] = person_name
    if match_score is not None:
        payload["match_score"] = float(match_score)
    if embedding:
        payload["embedding"] = embedding
    if camera_name:
        payload["camera_name"] = camera_name
    if camera_timestamp:
        payload["camera_timestamp"] = camera_timestamp
    if embedding_kind:
        payload["embedding_kind"] = embedding_kind
    if embedding_model:
        payload["embedding_model"] = embedding_model
    if quality_score is not None:
        payload["quality_score"] = float(quality_score)
    if observation_ts:
        payload["observation_ts"] = observation_ts
    return payload


def _update_person_identity_cache(media_id: str, track_id: int, data: Dict[str, Any]) -> None:
    with _person_identity_cache_lock:
        media_cache = _person_identity_cache.get(media_id, {})
        media_cache[str(track_id)] = data
        _person_identity_cache[media_id] = media_cache
        _person_identity_cache_ts[media_id] = time.time()


def request_face_lookup(
    media_id: str,
    track_id: int,
    frame_id: str,
    person_bbox: Optional[Dict[str, float]] = None,
) -> None:
    """Fire-and-forget: submit a face lookup request to the relay.

    The relay forwards it to the shared face worker pool.  Results are cached
    server-side and returned by fetch_face_identities() on subsequent frames.
    """
    def _do():
        try:
            requests.post(
                f"{_RELAY_HTTP_BASE}/cv/face-lookup",
                json={
                    "media_id": media_id,
                    "track_id": track_id,
                    "frame_id": frame_id,
                    "person_bbox": person_bbox or {},
                },
                timeout=2.0,
            )
        except Exception:
            pass  # Non-critical: face identity will show as Unknown

    _executor.submit(_do)


def fetch_face_identities(media_id: str) -> Dict[str, Any]:
    """Bulk-fetch cached face identities for a camera from the relay.

    Returns {track_id_str: {person_id, person_name, match_score, pending}}.
    Uses a local TTL cache to avoid per-frame HTTP calls.
    """
    now = time.time()

    with _identity_cache_lock:
        last_ts = _identity_cache_ts.get(media_id, 0.0)
        if (now - last_ts) < _IDENTITY_CACHE_TTL:
            return _identity_cache.get(media_id, {})

    # Cache miss or stale — fetch from relay
    try:
        resp = requests.get(
            f"{_RELAY_HTTP_BASE}/cv/face-identities",
            params={"media_id": media_id},
            timeout=1.0,
        )
        if resp.status_code == 200:
            data = resp.json()
        else:
            data = {}
    except Exception:
        data = {}

    with _identity_cache_lock:
        _identity_cache[media_id] = data
        _identity_cache_ts[media_id] = time.time()

    return data


def request_person_identity_resolve(
    media_id: str,
    track_id: int,
    frame_id: str = "",
    person_id: str = "",
    person_name: str = "",
    match_score: Optional[float] = None,
    embedding: Optional[list] = None,
    camera_name: str = "",
    camera_timestamp: str = "",
    embedding_kind: str = "",
    embedding_model: str = "",
    quality_score: Optional[float] = None,
    observation_ts: str = "",
) -> None:
    """Fire-and-forget resolve request for global person identity."""

    def _do():
        payload = _build_person_identity_payload(
            media_id=media_id,
            track_id=track_id,
            frame_id=frame_id,
            person_id=person_id,
            person_name=person_name,
            match_score=match_score,
            embedding=embedding,
            camera_name=camera_name,
            camera_timestamp=camera_timestamp,
            embedding_kind=embedding_kind,
            embedding_model=embedding_model,
            quality_score=quality_score,
            observation_ts=observation_ts,
        )

        try:
            resp = requests.post(
                f"{_RELAY_HTTP_BASE}/cv/person-identity/resolve",
                json=payload,
                timeout=2.0,
            )
            if resp.status_code == 200:
                data = resp.json()
                if isinstance(data, dict):
                    _update_person_identity_cache(media_id, track_id, data)
        except Exception:
            pass

    _executor.submit(_do)


def resolve_person_identity_sync(
    media_id: str,
    track_id: int,
    frame_id: str = "",
    person_id: str = "",
    person_name: str = "",
    match_score: Optional[float] = None,
    embedding: Optional[List[float]] = None,
    camera_name: str = "",
    camera_timestamp: str = "",
    embedding_kind: str = "",
    embedding_model: str = "",
    quality_score: Optional[float] = None,
    observation_ts: str = "",
    timeout_seconds: Optional[float] = None,
) -> Dict[str, Any]:
    """Resolve person identity synchronously (short blocking call).

    Intended for first-seen frames where deterministic identity assignment is
    preferred over pure fire-and-forget behavior.
    """
    payload = _build_person_identity_payload(
        media_id=media_id,
        track_id=track_id,
        frame_id=frame_id,
        person_id=person_id,
        person_name=person_name,
        match_score=match_score,
        embedding=embedding,
        camera_name=camera_name,
        camera_timestamp=camera_timestamp,
        embedding_kind=embedding_kind,
        embedding_model=embedding_model,
        quality_score=quality_score,
        observation_ts=observation_ts,
    )
    timeout = timeout_seconds if timeout_seconds is not None else _PERSON_IDENTITY_SYNC_TIMEOUT_S
    timeout = max(0.05, float(timeout))

    try:
        resp = requests.post(
            f"{_RELAY_HTTP_BASE}/cv/person-identity/resolve",
            json=payload,
            timeout=timeout,
        )
        if resp.status_code != 200:
            return {}
        data = resp.json()
        if isinstance(data, dict):
            _update_person_identity_cache(media_id, track_id, data)
            return data
    except Exception:
        return {}
    return {}


def fetch_person_identities(media_id: str) -> Dict[str, Any]:
    """Fetch cached global person identities for one camera."""
    now = time.time()
    with _person_identity_cache_lock:
        last_ts = _person_identity_cache_ts.get(media_id, 0.0)
        if (now - last_ts) < _PERSON_IDENTITY_CACHE_TTL:
            return _person_identity_cache.get(media_id, {})

    try:
        resp = requests.get(
            f"{_RELAY_HTTP_BASE}/cv/person-identities",
            params={"media_id": media_id},
            timeout=1.0,
        )
        if resp.status_code == 200:
            data = resp.json()
        else:
            data = {}
    except Exception:
        data = {}

    with _person_identity_cache_lock:
        _person_identity_cache[media_id] = data
        _person_identity_cache_ts[media_id] = time.time()
    return data
