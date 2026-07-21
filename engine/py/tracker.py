"""Multi-object tracking (ByteTrack) with per-stream state memory.

Provides:
- StreamTracker: ByteTrack instance + ID-based state for VLM gating
- TrackerManager: manages one tracker per camera stream (media_id)

Usage in workers:
    from tracker import TrackerManager

    media_id = data.get("media_id", "default")
    tracker = TrackerManager.get(media_id)
    item["detections"] = tracker.update(item["detections"])

    # Gate VLM calls:
    if tracker.should_alert(track_id, "no_helmet", cooldown_seconds=15):
        # ... call VLM
"""

import os
import time
from typing import Any, Dict, List, Optional

import numpy as np
import supervision as sv


class StreamTracker:
    """Per-stream ByteTrack tracker with state memory for VLM gating."""

    def __init__(self, frame_rate: int = 5):
        lost_buffer = int(os.getenv("TRACKER_LOST_BUFFER", "30"))
        self.tracker = sv.ByteTrack(
            track_activation_threshold=0.25,
            lost_track_buffer=lost_buffer,
            minimum_matching_threshold=0.8,
            frame_rate=frame_rate,
        )
        # key -> {status, last_alert_time}
        # Key is track_id (int>=0) for tracked detections, or
        # "_untracked_{status}" string for untracked ones (track_id < 0)
        self.state: Dict[Any, Dict[str, Any]] = {}
        self.last_update = time.time()

        # Track IDs that have already fired a "first seen" event.
        # Before a track_id is in this set, is_first_seen() returns True.
        self._seen_track_ids: set = set()

        # Per-track face identity cache.
        # None  = lookup not yet attempted
        # {}    = no match found (empty dict)
        # {"person_id": ..., "person_name": ..., "match_score": ...} = matched
        self._identity_cache: Dict[int, Optional[Dict]] = {}

    def update(self, detections: List[Dict[str, Any]]) -> List[Dict[str, Any]]:
        """Run ByteTrack on detections, return detections with track_id added."""
        if not detections:
            return detections

        xyxy = np.array(
            [
                [
                    d["box_px"]["x_min"],
                    d["box_px"]["y_min"],
                    d["box_px"]["x_max"],
                    d["box_px"]["y_max"],
                ]
                for d in detections
            ],
            dtype=np.float32,
        )
        confidence = np.array([d["confidence"] for d in detections], dtype=np.float32)
        class_id = np.array([d.get("class_id", 0) for d in detections], dtype=int)

        sv_dets = sv.Detections(
            xyxy=xyxy,
            confidence=confidence,
            class_id=class_id,
        )

        tracked = self.tracker.update_with_detections(sv_dets)

        # Map tracked results back to original detections by IoU. ByteTrack may
        # return boxes shifted by its motion model, so tiny coordinate-diff
        # thresholds drop or swap tracks when multiple people are present.
        result: List[Dict[str, Any]] = []
        tracked_to_original = _match_tracked_detections(tracked.xyxy, xyxy)
        matched_originals: set = set()

        for i in range(len(tracked.xyxy)):
            track_id = (
                int(tracked.tracker_id[i])
                if tracked.tracker_id is not None
                else -1
            )
            best_idx = tracked_to_original.get(i)
            if best_idx is not None:
                det = dict(detections[best_idx])
                det["track_id"] = track_id
                result.append(det)
                matched_originals.add(best_idx)

        # Include detections that ByteTrack didn't match (new/low-conf)
        for i, det in enumerate(detections):
            if i not in matched_originals:
                d = dict(det)
                d["track_id"] = -1
                result.append(d)

        self.last_update = time.time()
        return result

    def should_alert(
        self, track_id: int, status: str, cooldown_seconds: float = 10.0
    ) -> bool:
        """Determine if a VLM alert should fire for this track.

        For untracked detections (track_id < 0) a stable string key derived
        from the status is used so the cooldown still applies - preventing a
        static object (e.g. fallen chair) from triggering on every frame.
        """
        # Stable key: integer for tracked, string for untracked
        state_key: Any = track_id if track_id >= 0 else f"_untracked_{status}"

        now = time.time()
        state = self.state.get(state_key)

        if state is None:
            # First time seeing this track / status
            self.state[state_key] = {"status": status, "last_alert_time": now}
            return True

        # State change trigger (e.g. helmet -> no_helmet)
        if state["status"] != status:
            self.state[state_key] = {"status": status, "last_alert_time": now}
            return True

        # Time-based cooldown
        if now - state["last_alert_time"] >= cooldown_seconds:
            self.state[state_key]["last_alert_time"] = now
            return True

        return False

    # ------------------------------------------------------------------
    # First-seen tracking
    # ------------------------------------------------------------------

    def is_first_seen(self, track_id: int) -> bool:
        """Return True the first time this track_id is encountered.

        Call mark_seen() after processing the first-detection event so
        subsequent frames don't re-trigger first-detection logic.
        """
        return track_id >= 0 and track_id not in self._seen_track_ids

    def mark_seen(self, track_id: int) -> None:
        """Record that first-detection has already been handled for this track."""
        if track_id >= 0:
            self._seen_track_ids.add(track_id)

    # ------------------------------------------------------------------
    # Identity (face recognition) cache
    # ------------------------------------------------------------------

    def has_identity(self, track_id: int) -> bool:
        """Return True if a face recognition lookup has been attempted for this track."""
        return track_id in self._identity_cache

    def get_identity(self, track_id: int) -> Optional[Dict]:
        """Return cached identity dict (may be empty if no match was found)."""
        return self._identity_cache.get(track_id)

    def set_identity(self, track_id: int, identity: Optional[Dict]) -> None:
        """Cache the face recognition result for a track.

        Pass an empty dict {} to record 'lookup done, no match'.
        Pass None to clear/invalidate a cache entry.
        """
        if identity is None:
            self._identity_cache.pop(track_id, None)
        else:
            self._identity_cache[track_id] = identity

    # ------------------------------------------------------------------
    # Stale cleanup
    # ------------------------------------------------------------------

    def cleanup_stale(self, max_age_seconds: float = 120.0):
        """Remove state entries for tracks not seen recently."""
        now = time.time()
        stale = [
            tid
            for tid, s in self.state.items()
            if now - s.get("last_alert_time", 0) > max_age_seconds
        ]
        for tid in stale:
            del self.state[tid]
            # Also clean up identity cache and seen-set for integer track IDs
            if isinstance(tid, int):
                self._identity_cache.pop(tid, None)
                self._seen_track_ids.discard(tid)


class TrackerManager:
    """Manages per-stream ByteTrack instances."""

    _trackers: Dict[str, StreamTracker] = {}
    MAX_TRACKERS = 50

    @classmethod
    def get(cls, stream_id: str, frame_rate: int = 5) -> StreamTracker:
        if stream_id not in cls._trackers:
            if len(cls._trackers) >= cls.MAX_TRACKERS:
                cls._evict_oldest()
            cls._trackers[stream_id] = StreamTracker(frame_rate=frame_rate)
        return cls._trackers[stream_id]

    @classmethod
    def _evict_oldest(cls):
        if not cls._trackers:
            return
        oldest_key = min(cls._trackers, key=lambda k: cls._trackers[k].last_update)
        del cls._trackers[oldest_key]


def _box_iou(a: np.ndarray, b: np.ndarray) -> float:
    """IoU for two xyxy boxes."""
    x1 = max(float(a[0]), float(b[0]))
    y1 = max(float(a[1]), float(b[1]))
    x2 = min(float(a[2]), float(b[2]))
    y2 = min(float(a[3]), float(b[3]))
    inter_w = max(0.0, x2 - x1)
    inter_h = max(0.0, y2 - y1)
    inter = inter_w * inter_h
    if inter <= 0.0:
        return 0.0
    area_a = max(0.0, float(a[2] - a[0])) * max(0.0, float(a[3] - a[1]))
    area_b = max(0.0, float(b[2] - b[0])) * max(0.0, float(b[3] - b[1]))
    union = area_a + area_b - inter
    if union <= 0.0:
        return 0.0
    return float(inter / union)


def _match_tracked_detections(
    tracked_boxes: np.ndarray,
    original_boxes: np.ndarray,
    min_iou: float = 0.10,
) -> Dict[int, int]:
    """Greedily match tracked boxes to original detections one-to-one."""
    if len(tracked_boxes) == 0 or len(original_boxes) == 0:
        return {}

    candidates: List[tuple] = []
    for tracked_idx, tracked_box in enumerate(tracked_boxes):
        for original_idx, original_box in enumerate(original_boxes):
            iou = _box_iou(tracked_box, original_box)
            if iou >= min_iou:
                candidates.append((iou, tracked_idx, original_idx))

    candidates.sort(key=lambda item: item[0], reverse=True)
    used_tracked: set = set()
    used_original: set = set()
    matches: Dict[int, int] = {}

    for _iou, tracked_idx, original_idx in candidates:
        if tracked_idx in used_tracked or original_idx in used_original:
            continue
        matches[tracked_idx] = original_idx
        used_tracked.add(tracked_idx)
        used_original.add(original_idx)

    return matches


def _match_detection(
    tracked_box: np.ndarray,
    all_boxes: np.ndarray,
) -> Optional[int]:
    """Find the original detection index with the best IoU."""
    matches = _match_tracked_detections(np.asarray([tracked_box]), all_boxes)
    return matches.get(0)
