import math
import os
import unittest

import numpy as np

import worker_face_recognition as face_worker


class TestWorkerFaceRecognitionRegression(unittest.TestCase):
    def test_flatten_inputs_preserves_face_lookup_metadata(self) -> None:
        payload = {
            "media_id": "cam-a",
            "inputs": [
                {
                    "id": "job-1",
                    "media_id": "cam-b",
                    "inputs": [
                        {
                            "id": "frame-1",
                            "filepath": "/tmp/frame-1.jpg",
                            "_face_lookup": True,
                            "track_id": 17,
                            "person_bbox": {
                                "x_min": 10,
                                "y_min": 20,
                                "x_max": 110,
                                "y_max": 220,
                            },
                        }
                    ],
                }
            ],
        }

        flat = face_worker._flatten_inputs(payload)
        self.assertEqual(len(flat), 1)
        self.assertTrue(flat[0]["_face_lookup"])
        self.assertEqual(flat[0]["_track_id"], 17)
        self.assertEqual(
            flat[0]["_person_bbox"],
            {"x_min": 10.0, "y_min": 20.0, "x_max": 110.0, "y_max": 220.0},
        )
        self.assertEqual(flat[0]["_media_id"], "cam-b")

    def test_make_lookup_detection_keeps_requested_track_id_and_bbox(self) -> None:
        det = face_worker._make_lookup_detection(
            track_id=23,
            box_px={"x_min": 40.0, "y_min": 30.0, "x_max": 140.0, "y_max": 180.0},
            image_width=200,
            image_height=200,
            confidence=0.91,
            person_id="person-7",
            person_name="Alice",
            match_score=0.77,
        )

        self.assertEqual(det["track_id"], 23)
        self.assertEqual(det["person_id"], "person-7")
        self.assertEqual(det["person_name"], "Alice")
        self.assertAlmostEqual(det["box"]["x_min"], 0.2)
        self.assertAlmostEqual(det["box"]["y_max"], 0.9)
        self.assertEqual(det["label"], "Alice")

    def test_weak_match_metadata_is_preserved_for_relay_evidence(self) -> None:
        old_shared = face_worker.SHARED_FACE_RECOGNIZER_AVAILABLE
        old_threshold = os.environ.get("FACE_MATCH_THRESHOLD")
        old_weak_threshold = os.environ.get("FACE_MATCH_WEAK_THRESHOLD")
        try:
            face_worker.SHARED_FACE_RECOGNIZER_AVAILABLE = False
            os.environ["FACE_MATCH_THRESHOLD"] = "0.50"
            os.environ["FACE_MATCH_WEAK_THRESHOLD"] = "0.40"
            with face_worker._registry_lock:
                face_worker._known_faces.clear()
                face_worker._known_faces.append(
                    {
                        "person_id": "person-1",
                        "name": "Known Person",
                        "embedding": np.array([1.0, 0.0], dtype=np.float32),
                    }
                )

            emb = np.array([0.43, math.sqrt(1.0 - (0.43 * 0.43))], dtype=np.float32)
            result = face_worker._match_embedding_result(emb)
            self.assertEqual(result.get("confidence_tier"), "weak")
            self.assertEqual(result.get("candidate_person_id"), "person-1")

            det = face_worker._make_lookup_detection(
                track_id=31,
                box_px={"x_min": 10.0, "y_min": 20.0, "x_max": 100.0, "y_max": 140.0},
                image_width=200,
                image_height=200,
                confidence=0.9,
            )
            face_worker._apply_match_metadata(det, result)
            self.assertNotIn("person_id", det)
            self.assertEqual(det.get("candidate_person_id"), "person-1")
            self.assertEqual(det.get("candidate_person_name"), "Known Person")
            self.assertEqual(det.get("confidence_tier"), "weak")
        finally:
            face_worker.SHARED_FACE_RECOGNIZER_AVAILABLE = old_shared
            if old_threshold is None:
                os.environ.pop("FACE_MATCH_THRESHOLD", None)
            else:
                os.environ["FACE_MATCH_THRESHOLD"] = old_threshold
            if old_weak_threshold is None:
                os.environ.pop("FACE_MATCH_WEAK_THRESHOLD", None)
            else:
                os.environ["FACE_MATCH_WEAK_THRESHOLD"] = old_weak_threshold
            with face_worker._registry_lock:
                face_worker._known_faces.clear()


if __name__ == "__main__":
    unittest.main()
