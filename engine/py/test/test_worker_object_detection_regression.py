import os
import unittest
from unittest.mock import MagicMock, patch

import worker_object_detection as objdet


class TestWorkerObjectDetectionRegression(unittest.TestCase):
    def setUp(self) -> None:
        self._orig_max = os.getenv("CUSTOM_MODEL_CACHE_MAX")
        with objdet._CUSTOM_MODEL_CACHE_LOCK:
            objdet._CUSTOM_MODEL_CACHE.clear()

    def tearDown(self) -> None:
        if self._orig_max is None:
            os.environ.pop("CUSTOM_MODEL_CACHE_MAX", None)
        else:
            os.environ["CUSTOM_MODEL_CACHE_MAX"] = self._orig_max
        with objdet._CUSTOM_MODEL_CACHE_LOCK:
            objdet._CUSTOM_MODEL_CACHE.clear()

    def test_load_custom_model_cached_requires_path(self) -> None:
        with self.assertRaises(ValueError):
            objdet._load_custom_model_cached("")

    def test_load_custom_model_cached_reuses_model_and_enforces_lru_bound(self) -> None:
        os.environ["CUSTOM_MODEL_CACHE_MAX"] = "2"
        calls = []

        def fake_yolo(path):
            calls.append(path)
            return {"path": path, "idx": len(calls)}

        with patch.object(objdet, "YOLO", side_effect=fake_yolo):
            a1 = objdet._load_custom_model_cached("/tmp/a.pt")
            b1 = objdet._load_custom_model_cached("/tmp/b.pt")
            a2 = objdet._load_custom_model_cached("/tmp/a.pt")  # cache hit
            c1 = objdet._load_custom_model_cached("/tmp/c.pt")  # evicts /tmp/b.pt
            b2 = objdet._load_custom_model_cached("/tmp/b.pt")  # miss after eviction

        self.assertIs(a1, a2)
        self.assertIsNot(b1, b2)
        self.assertNotEqual(c1["idx"], b2["idx"])
        self.assertEqual(
            calls,
            ["/tmp/a.pt", "/tmp/b.pt", "/tmp/c.pt", "/tmp/b.pt"],
        )
        with objdet._CUSTOM_MODEL_CACHE_LOCK:
            self.assertEqual(len(objdet._CUSTOM_MODEL_CACHE), 2)
            self.assertIn("/tmp/b.pt", objdet._CUSTOM_MODEL_CACHE)
            self.assertIn("/tmp/c.pt", objdet._CUSTOM_MODEL_CACHE)
            self.assertNotIn("/tmp/a.pt", objdet._CUSTOM_MODEL_CACHE)

    def test_flatten_inputs_supports_nested_batches(self) -> None:
        payload = {
            "media_id": "cam-default",
            "inputs": [
                {
                    "id": "job-1",
                    "media_id": "cam-a",
                    "inputs": [
                        {"id": "f1", "filepath": "/tmp/f1.jpg"},
                        {"id": "f2", "path": "/tmp/f2.jpg", "media_id": "cam-b"},
                    ],
                },
                {"id": "f3", "filepath": "/tmp/f3.jpg"},
            ],
        }
        flat = objdet._flatten_inputs(payload)
        self.assertEqual(len(flat), 3)
        self.assertEqual(flat[0]["_job_id"], "job-1")
        self.assertEqual(flat[0]["_media_id"], "cam-a")
        self.assertEqual(flat[1]["_media_id"], "cam-b")
        self.assertEqual(flat[2]["_media_id"], "cam-default")

    def test_ensure_person_identity_for_visit_preserves_existing_global_id(self) -> None:
        mgr = objdet._GlobalPersonIdentityManager()
        person = {
            "track_id": 7,
            "global_person_id": "person:relay-77",
            "person_name": "Relay Name",
        }
        objdet._ensure_person_identity_for_visit(mgr, "camera-a", person)
        self.assertEqual(person.get("global_person_id"), "person:relay-77")
        self.assertEqual(person.get("person_name"), "Relay Name")

    def test_ensure_person_identity_for_visit_does_not_assign_local_unknown_when_missing(self) -> None:
        mgr = objdet._GlobalPersonIdentityManager()
        person = {"track_id": 9}
        objdet._ensure_person_identity_for_visit(mgr, "camera-a", person)
        self.assertNotIn("global_person_id", person)
        self.assertNotIn("person_name", person)

    def test_should_request_person_identity_resolve_retries_unknown_after_interval(self) -> None:
        person = {"global_person_id": "unknown:12", "identity_source": "reid_match"}
        should = objdet._should_request_person_identity_resolve(
            first_seen=False,
            person=person,
            now_ts=20.0,
            last_resolve_ts=15.0,
            min_interval_seconds=3.0,
        )
        self.assertTrue(should)

    def test_should_request_person_identity_resolve_skips_known_person(self) -> None:
        person = {"global_person_id": "person:77", "person_id": "77", "identity_source": "face_recognition"}
        should = objdet._should_request_person_identity_resolve(
            first_seen=False,
            person=person,
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
        applied = objdet._apply_relay_person_identity(
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
        applied = objdet._apply_relay_person_identity(
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
            "track_id": 4,
            "global_person_id": "unknown:17",
            "identity_source": "unknown_track",
        }
        with patch.dict(os.environ, {"FACE_RELAY_PROMOTE_WEAK_CANDIDATES": "1", "FACE_RELAY_PROMOTE_MIN_SCORE": "0.42"}):
            applied = objdet._apply_relay_face_identity(
                person,
                {
                    "candidate_person_id": "person-1",
                    "candidate_person_name": "Known Person",
                    "candidate_score": 0.447,
                    "confidence_tier": "weak",
                },
                4,
            )

        self.assertTrue(applied)
        self.assertEqual(person.get("person_id"), "person-1")
        self.assertEqual(person.get("person_name"), "Known Person")
        self.assertEqual(person.get("match_score"), 0.447)
        self.assertEqual(person.get("global_person_id"), "person:person-1")
        self.assertEqual(person.get("identity_source"), "face_recognition")

    def test_apply_relay_face_identity_keeps_low_score_candidate_diagnostic(self) -> None:
        person = {"track_id": 4}
        applied = objdet._apply_relay_face_identity(
            person,
            {
                "candidate_person_id": "person-2",
                "candidate_person_name": "Maybe Person",
                "candidate_score": 0.269,
                "confidence_tier": "unknown",
            },
            4,
        )

        self.assertTrue(applied)
        self.assertEqual(person.get("person_name"), "Unknown #4")
        self.assertNotIn("person_id", person)
        self.assertEqual(person.get("candidate_person_name"), "Maybe Person")
        self.assertEqual(person.get("confidence_tier"), "unknown")

    def test_should_request_relay_face_lookup_retries_unresolved_track_after_interval(self) -> None:
        person = {"person_name": "Unknown #12"}
        should = objdet._should_request_relay_face_lookup(
            first_seen=False,
            person=person,
            frame_id="frame-1",
            now_ts=20.0,
            last_lookup_ts=16.0,
            attempts=2,
            retry_seconds=3.0,
            max_attempts=12,
        )
        self.assertTrue(should)

    def test_should_request_relay_face_lookup_skips_known_person(self) -> None:
        person = {"global_person_id": "person:77", "person_name": "Abhishek"}
        should = objdet._should_request_relay_face_lookup(
            first_seen=True,
            person=person,
            frame_id="frame-1",
            now_ts=20.0,
            last_lookup_ts=0.0,
            attempts=0,
            retry_seconds=3.0,
            max_attempts=12,
        )
        self.assertFalse(should)

    @patch.object(objdet, "_appearance_embedding_from_crop", return_value=[0.25, 0.5])
    def test_resolve_person_identity_for_track_first_seen_attempts_sync(self, _mock_emb: MagicMock) -> None:
        person = {"track_id": 5, "box_px": {"x_min": 1, "y_min": 1, "x_max": 10, "y_max": 20}}
        sync_calls = []
        async_calls = []
        last_ts = {}

        def sync_fn(**kwargs):
            sync_calls.append(kwargs)
            return {"global_person_id": "unknown:44", "identity_source": "reid_match"}

        def async_fn(**kwargs):
            async_calls.append(kwargs)

        objdet._resolve_person_identity_for_track(
            first_seen=True,
            person=person,
            media_id="cam-a",
            track_id=5,
            frame_id="f-1",
            frame_path="/tmp/fake.jpg",
            camera_name="Cam A",
            camera_timestamp="2026-02-24T00:00:00Z",
            now_ts=10.0,
            identity_resolve_last_ts=last_ts,
            min_interval_seconds=10.0,
            sync_enabled=True,
            resolve_sync=sync_fn,
            resolve_async=async_fn,
        )

        self.assertEqual(len(sync_calls), 1)
        self.assertEqual(sync_calls[0]["track_id"], 5)
        self.assertEqual(person.get("global_person_id"), "unknown:44")
        self.assertNotIn("identity_pending", person)
        self.assertEqual(len(async_calls), 0)

    @patch.object(objdet, "_appearance_embedding_from_crop", return_value=[0.1, 0.2])
    def test_resolve_person_identity_for_track_timeout_marks_pending_without_unknown_id(self, _mock_emb: MagicMock) -> None:
        person = {
            "track_id": 7,
            "global_person_id": "unknown:7",
            "person_name": "Unknown #7",
            "identity_source": "unknown_track",
            "box_px": {"x_min": 1, "y_min": 1, "x_max": 10, "y_max": 20},
        }
        async_calls = []
        last_ts = {}

        def sync_fn(**_kwargs):
            raise TimeoutError("relay timeout")

        def async_fn(**kwargs):
            async_calls.append(kwargs)

        objdet._resolve_person_identity_for_track(
            first_seen=True,
            person=person,
            media_id="cam-b",
            track_id=7,
            frame_id="f-2",
            frame_path="/tmp/fake.jpg",
            camera_name="Cam B",
            camera_timestamp="2026-02-24T00:00:00Z",
            now_ts=20.0,
            identity_resolve_last_ts=last_ts,
            min_interval_seconds=5.0,
            sync_enabled=True,
            resolve_sync=sync_fn,
            resolve_async=async_fn,
        )

        self.assertTrue(person.get("identity_pending"))
        self.assertNotIn("global_person_id", person)
        self.assertEqual(len(async_calls), 1)

    @patch.object(objdet, "_appearance_embedding_from_crop", return_value=[0.1, 0.2])
    def test_retry_path_then_relay_identity_stops_additional_resolve(self, _mock_emb: MagicMock) -> None:
        person = {
            "track_id": 9,
            "global_person_id": "unknown:9",
            "identity_source": "reid_match",
            "box_px": {"x_min": 1, "y_min": 1, "x_max": 10, "y_max": 20},
        }
        async_calls = []
        last_ts = {}

        def async_fn(**kwargs):
            async_calls.append(kwargs)

        objdet._resolve_person_identity_for_track(
            first_seen=False,
            person=person,
            media_id="cam-c",
            track_id=9,
            frame_id="f-3",
            frame_path="/tmp/fake.jpg",
            camera_name="Cam C",
            camera_timestamp="2026-02-24T00:00:00Z",
            now_ts=100.0,
            identity_resolve_last_ts=last_ts,
            min_interval_seconds=0.0,
            embedding_enabled=False,
            resolve_sync=None,
            resolve_async=async_fn,
        )
        self.assertEqual(len(async_calls), 1)

        objdet._apply_relay_person_identity(
            person,
            {
                "global_person_id": "person:99",
                "person_id": "99",
                "person_name": "Known",
                "identity_source": "face_recognition",
            },
        )
        objdet._resolve_person_identity_for_track(
            first_seen=False,
            person=person,
            media_id="cam-c",
            track_id=9,
            frame_id="f-4",
            frame_path="/tmp/fake.jpg",
            camera_name="Cam C",
            camera_timestamp="2026-02-24T00:00:00Z",
            now_ts=110.0,
            identity_resolve_last_ts=last_ts,
            min_interval_seconds=0.0,
            embedding_enabled=False,
            resolve_sync=None,
            resolve_async=async_fn,
        )
        self.assertEqual(len(async_calls), 1)
        self.assertEqual(person.get("global_person_id"), "person:99")
        self.assertFalse(person.get("identity_pending", False))


if __name__ == "__main__":
    unittest.main()
