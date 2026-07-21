import unittest

from objdet_face_lookup import select_face_lookup_frame_id


class TestObjDetFaceLookupFrameId(unittest.TestCase):
    def test_prefers_explicit_frame_id_over_top_level_id(self) -> None:
        item = {"id": "job-abc", "frame_id": "frame-123"}
        self.assertEqual(select_face_lookup_frame_id(item), "frame-123")

    def test_prefers_frame_id_from_flat_adapter_item(self) -> None:
        # Regression guard: objdet relay lookups must use frame id, not job id.
        item = {"id": "frame-123", "_job_id": "job-999"}
        self.assertEqual(select_face_lookup_frame_id(item), "frame-123")

    def test_uses_legacy_frame_fields_when_present(self) -> None:
        item = {"_frame_id": "frame-legacy", "_job_id": "job-111"}
        self.assertEqual(select_face_lookup_frame_id(item), "frame-legacy")

    def test_uses_nested_input_or_data_frame_id(self) -> None:
        self.assertEqual(
            select_face_lookup_frame_id({"id": "job-1", "input": {"id": "frame-input"}}),
            "frame-input",
        )
        self.assertEqual(
            select_face_lookup_frame_id({"id": "job-1", "data": {"id": "frame-data"}}),
            "frame-data",
        )

    def test_falls_back_to_job_id_for_legacy_payloads(self) -> None:
        item = {"_job_id": "job-222"}
        self.assertEqual(select_face_lookup_frame_id(item), "job-222")

    def test_returns_empty_when_no_identifier_exists(self) -> None:
        self.assertEqual(select_face_lookup_frame_id({}), "")


if __name__ == "__main__":
    unittest.main()
