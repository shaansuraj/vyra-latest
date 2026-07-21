import os
import tempfile
import unittest
from unittest.mock import MagicMock, patch

from PIL import Image

from worker_ppe import (
    VIOLATION_CLASS_IDS,
    _apply_relay_face_identity,
    _apply_relay_person_identity,
    _make_vlm_key,
    _resolve_person_identity_for_track,
    _save_snapshot_with_bboxes,
    _should_request_relay_face_lookup,
    _should_request_person_identity_resolve,
)


class TestPPERegression(unittest.TestCase):
    def test_make_vlm_key_is_label_order_invariant(self) -> None:
        a = _make_vlm_key("cam-1", 9, ["no_hardhat", "no_vest"])
        b = _make_vlm_key("cam-1", 9, ["no_vest", "no_hardhat"])
        self.assertEqual(a, b)

    def test_snapshot_draws_only_violation_boxes(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            src = os.path.join(td, "in.jpg")
            out = os.path.join(td, "out.jpg")
            Image.new("RGB", (120, 120), color=(255, 255, 255)).save(src, "JPEG", quality=95)

            viol_cls = next(iter(VIOLATION_CLASS_IDS))
            detections = [
                {
                    "label": "person",
                    "confidence": 0.95,
                    "class_id": 1,  # non-violation class in PPE model
                    "box_px": {"x_min": 10, "y_min": 10, "x_max": 40, "y_max": 40},
                },
                {
                    "label": "no_hardhat",
                    "confidence": 0.93,
                    "class_id": viol_cls,
                    "box_px": {"x_min": 60, "y_min": 60, "x_max": 100, "y_max": 100},
                },
            ]
            _save_snapshot_with_bboxes(src, out, detections, camera_name="")
            self.assertTrue(os.path.exists(out))

            img = Image.open(out).convert("RGB")
            # Non-violation border should remain white-ish.
            px_non = img.getpixel((10, 10))
            self.assertGreaterEqual(px_non[0], 220)
            self.assertGreaterEqual(px_non[1], 220)
            self.assertGreaterEqual(px_non[2], 220)

            # Violation border should be red-tinted.
            px_viol = img.getpixel((60, 60))
            self.assertGreater(px_viol[0], px_viol[1])
            self.assertGreater(px_viol[0], px_viol[2])

    def test_should_request_person_identity_resolve_retries_unknown_after_interval(self) -> None:
        should = _should_request_person_identity_resolve(
            first_seen=False,
            person={"global_person_id": "unknown:9", "identity_source": "reid_match"},
            now_ts=20.0,
            last_resolve_ts=10.0,
            min_interval_seconds=3.0,
        )
        self.assertTrue(should)

    def test_should_request_person_identity_resolve_skips_known_person(self) -> None:
        should = _should_request_person_identity_resolve(
            first_seen=False,
            person={"global_person_id": "person:55", "person_id": "55", "identity_source": "face_recognition"},
            now_ts=20.0,
            last_resolve_ts=10.0,
            min_interval_seconds=3.0,
        )
        self.assertFalse(should)

    def test_apply_relay_person_identity_preserves_known_face_over_unknown_track(self) -> None:
        person = {
            "track_id": 31,
            "person_id": "425ca57f-8254-4709-8a5a-70d4ac8a7172",
            "person_name": "SubhGlass",
            "match_score": 0.582,
        }
        applied = _apply_relay_person_identity(
            person,
            {
                "global_person_id": "unknown:68",
                "person_name": "Unknown #31",
                "identity_source": "unknown_track",
                "identity_confidence": 0.999,
            },
        )
        self.assertTrue(applied)
        self.assertEqual(
            person.get("global_person_id"),
            "person:425ca57f-8254-4709-8a5a-70d4ac8a7172",
        )
        self.assertEqual(person.get("person_name"), "SubhGlass")
        self.assertEqual(person.get("identity_source"), "face_recognition")
        self.assertEqual(person.get("identity_confidence"), 0.582)

    def test_apply_relay_person_identity_promotes_known_payload_with_unknown_global_id(self) -> None:
        person = {"track_id": 27}
        applied = _apply_relay_person_identity(
            person,
            {
                "global_person_id": "unknown:68",
                "person_id": "person-1",
                "person_name": "Known Person",
                "identity_source": "unknown_track",
            },
        )
        self.assertTrue(applied)
        self.assertEqual(person.get("global_person_id"), "person:person-1")
        self.assertEqual(person.get("person_id"), "person-1")
        self.assertEqual(person.get("person_name"), "Known Person")
        self.assertEqual(person.get("identity_source"), "face_recognition")

    def test_apply_relay_face_identity_promotes_weak_candidate(self) -> None:
        person = {
            "track_id": 8,
            "global_person_id": "unknown:8",
            "identity_source": "unknown_track",
        }
        with patch.dict(os.environ, {"FACE_RELAY_PROMOTE_WEAK_CANDIDATES": "1", "FACE_RELAY_PROMOTE_MIN_SCORE": "0.42"}):
            applied = _apply_relay_face_identity(
                person,
                {
                    "candidate_person_id": "person-1",
                    "candidate_person_name": "Known Person",
                    "candidate_score": 0.496,
                    "confidence_tier": "weak",
                },
                8,
            )

        self.assertTrue(applied)
        self.assertEqual(person.get("person_id"), "person-1")
        self.assertEqual(person.get("person_name"), "Known Person")
        self.assertEqual(person.get("match_score"), 0.496)
        self.assertEqual(person.get("global_person_id"), "person:person-1")
        self.assertEqual(person.get("identity_source"), "face_recognition")

    def test_should_request_relay_face_lookup_retries_unresolved_track_after_interval(self) -> None:
        should = _should_request_relay_face_lookup(
            first_seen=False,
            person={"person_name": "Unknown #8"},
            frame_id="frame-a",
            now_ts=20.0,
            last_lookup_ts=16.0,
            attempts=2,
            retry_seconds=3.0,
            max_attempts=12,
        )
        self.assertTrue(should)

    def test_should_request_relay_face_lookup_skips_known_person(self) -> None:
        should = _should_request_relay_face_lookup(
            first_seen=True,
            person={"global_person_id": "person:55", "person_name": "Known"},
            frame_id="frame-a",
            now_ts=20.0,
            last_lookup_ts=0.0,
            attempts=0,
            retry_seconds=3.0,
            max_attempts=12,
        )
        self.assertFalse(should)

    @patch("worker_ppe._appearance_embedding_from_crop", return_value=[0.2, 0.3])
    def test_resolve_person_identity_for_track_first_seen_attempts_sync(self, _mock_emb: MagicMock) -> None:
        person = {"track_id": 11, "box_px": {"x_min": 1, "y_min": 1, "x_max": 20, "y_max": 30}}
        sync_calls = []
        async_calls = []
        last_ts = {}

        def sync_fn(**kwargs):
            sync_calls.append(kwargs)
            return {"global_person_id": "unknown:51", "identity_source": "reid_match"}

        def async_fn(**kwargs):
            async_calls.append(kwargs)

        _resolve_person_identity_for_track(
            first_seen=True,
            person=person,
            media_id="cam-1",
            track_id=11,
            frame_id="frame-a",
            frame_path="/tmp/fake.jpg",
            camera_name="Camera 1",
            camera_timestamp="2026-02-24T00:00:00Z",
            now_ts=12.0,
            identity_resolve_last_ts=last_ts,
            min_interval_seconds=10.0,
            resolve_sync=sync_fn,
            resolve_async=async_fn,
        )

        self.assertEqual(len(sync_calls), 1)
        self.assertEqual(person.get("global_person_id"), "unknown:51")
        self.assertNotIn("identity_pending", person)
        self.assertEqual(len(async_calls), 0)

    @patch("worker_ppe._appearance_embedding_from_crop", return_value=[0.2, 0.3])
    def test_resolve_person_identity_for_track_timeout_marks_pending_without_unknown_id(self, _mock_emb: MagicMock) -> None:
        person = {
            "track_id": 15,
            "global_person_id": "unknown:15",
            "person_name": "Unknown #15",
            "identity_source": "unknown_track",
            "box_px": {"x_min": 1, "y_min": 1, "x_max": 20, "y_max": 30},
        }
        async_calls = []
        last_ts = {}

        def sync_fn(**_kwargs):
            raise TimeoutError("timeout")

        def async_fn(**kwargs):
            async_calls.append(kwargs)

        _resolve_person_identity_for_track(
            first_seen=True,
            person=person,
            media_id="cam-2",
            track_id=15,
            frame_id="frame-b",
            frame_path="/tmp/fake.jpg",
            camera_name="Camera 2",
            camera_timestamp="2026-02-24T00:00:00Z",
            now_ts=20.0,
            identity_resolve_last_ts=last_ts,
            min_interval_seconds=5.0,
            resolve_sync=sync_fn,
            resolve_async=async_fn,
        )

        self.assertTrue(person.get("identity_pending"))
        self.assertNotIn("global_person_id", person)
        self.assertEqual(len(async_calls), 1)

    @patch("worker_ppe._appearance_embedding_from_crop", return_value=[0.2, 0.3])
    def test_retry_path_then_relay_identity_stops_additional_resolve(self, _mock_emb: MagicMock) -> None:
        person = {
            "track_id": 21,
            "global_person_id": "unknown:21",
            "identity_source": "reid_match",
            "box_px": {"x_min": 1, "y_min": 1, "x_max": 20, "y_max": 30},
        }
        async_calls = []
        last_ts = {}

        def async_fn(**kwargs):
            async_calls.append(kwargs)

        _resolve_person_identity_for_track(
            first_seen=False,
            person=person,
            media_id="cam-3",
            track_id=21,
            frame_id="frame-c",
            frame_path="/tmp/fake.jpg",
            camera_name="Camera 3",
            camera_timestamp="2026-02-24T00:00:00Z",
            now_ts=100.0,
            identity_resolve_last_ts=last_ts,
            min_interval_seconds=0.0,
            resolve_sync=None,
            resolve_async=async_fn,
        )
        self.assertEqual(len(async_calls), 1)

        _apply_relay_person_identity(
            person,
            {
                "global_person_id": "person:77",
                "person_id": "77",
                "person_name": "Known",
                "identity_source": "face_recognition",
            },
        )
        _resolve_person_identity_for_track(
            first_seen=False,
            person=person,
            media_id="cam-3",
            track_id=21,
            frame_id="frame-d",
            frame_path="/tmp/fake.jpg",
            camera_name="Camera 3",
            camera_timestamp="2026-02-24T00:00:00Z",
            now_ts=110.0,
            identity_resolve_last_ts=last_ts,
            min_interval_seconds=0.0,
            resolve_sync=None,
            resolve_async=async_fn,
        )

        self.assertEqual(len(async_calls), 1)
        self.assertEqual(person.get("global_person_id"), "person:77")
        self.assertFalse(person.get("identity_pending", False))


if __name__ == "__main__":
    unittest.main()
