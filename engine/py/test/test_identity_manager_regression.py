import unittest

import numpy as np

from worker_object_detection import _GlobalPersonIdentityManager


class TestGlobalIdentityManagerRegression(unittest.TestCase):
    def test_recognized_person_keeps_same_global_id_across_cameras(self) -> None:
        mgr = _GlobalPersonIdentityManager()
        a = mgr.bind_track_from_face(
            "camera-a",
            7,
            {"person_id": "42", "person_name": "Abhishek", "match_score": 0.98},
        )
        b = mgr.bind_track_from_face(
            "camera-b",
            3,
            {"person_id": "42", "person_name": "Abhishek", "match_score": 0.97},
        )
        self.assertEqual(a.get("global_person_id"), "person:42")
        self.assertEqual(b.get("global_person_id"), "person:42")
        self.assertEqual(b.get("person_name"), "Abhishek")

    def test_unknown_embedding_matches_existing_unknown_identity(self) -> None:
        mgr = _GlobalPersonIdentityManager()
        # Make matching slightly easier for deterministic test behavior.
        mgr.unknown_match_threshold = 0.5

        emb_a = np.array([1.0, 0.0, 0.0], dtype=np.float32)
        emb_b = np.array([0.92, 0.08, 0.0], dtype=np.float32)

        first = mgr.bind_track_from_face("camera-a", 1, {"face_embedding": emb_a})
        second = mgr.bind_track_from_face("camera-b", 5, {"face_embedding": emb_b})

        self.assertTrue(str(first.get("global_person_id", "")).startswith("unknown:"))
        self.assertEqual(first.get("global_person_id"), second.get("global_person_id"))

    def test_camera_visit_detection_is_stable(self) -> None:
        mgr = _GlobalPersonIdentityManager()
        mgr.visit_absence_seconds = 5.0

        gid = "person:77"
        self.assertTrue(mgr.mark_camera_visit(gid, "cam-1", now_ts=100.0))
        self.assertFalse(mgr.mark_camera_visit(gid, "cam-1", now_ts=102.0))
        self.assertTrue(mgr.mark_camera_visit(gid, "cam-1", now_ts=107.1))


if __name__ == "__main__":
    unittest.main()
