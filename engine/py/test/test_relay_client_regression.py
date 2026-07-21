import unittest
from unittest.mock import MagicMock, patch

import relay_client


class TestRelayClientRegression(unittest.TestCase):
    def setUp(self) -> None:
        relay_client._identity_cache.clear()
        relay_client._identity_cache_ts.clear()
        relay_client._person_identity_cache.clear()
        relay_client._person_identity_cache_ts.clear()

    @patch("relay_client.requests.get")
    def test_fetch_person_identities_uses_ttl_cache(self, mock_get: MagicMock) -> None:
        resp = MagicMock()
        resp.status_code = 200
        resp.json.return_value = {"7": {"global_person_id": "person:42"}}
        mock_get.return_value = resp

        original_ttl = relay_client._PERSON_IDENTITY_CACHE_TTL
        relay_client._PERSON_IDENTITY_CACHE_TTL = 60.0
        try:
            a = relay_client.fetch_person_identities("cam-1")
            b = relay_client.fetch_person_identities("cam-1")
        finally:
            relay_client._PERSON_IDENTITY_CACHE_TTL = original_ttl

        self.assertEqual(a, b)
        self.assertEqual(mock_get.call_count, 1)

    @patch("relay_client.requests.get")
    def test_fetch_face_identities_uses_ttl_cache(self, mock_get: MagicMock) -> None:
        resp = MagicMock()
        resp.status_code = 200
        resp.json.return_value = {"5": {"person_name": "Abhishek", "pending": False}}
        mock_get.return_value = resp

        original_ttl = relay_client._IDENTITY_CACHE_TTL
        relay_client._IDENTITY_CACHE_TTL = 60.0
        try:
            a = relay_client.fetch_face_identities("cam-2")
            b = relay_client.fetch_face_identities("cam-2")
        finally:
            relay_client._IDENTITY_CACHE_TTL = original_ttl

        self.assertEqual(a, b)
        self.assertEqual(mock_get.call_count, 1)

    @patch("relay_client.requests.post")
    @patch("relay_client._executor.submit")
    def test_request_person_identity_resolve_updates_local_cache(
        self,
        mock_submit: MagicMock,
        mock_post: MagicMock,
    ) -> None:
        # Execute submitted callable inline for deterministic test behavior.
        mock_submit.side_effect = lambda fn, *args, **kwargs: fn()

        resp = MagicMock()
        resp.status_code = 200
        resp.json.return_value = {
            "global_person_id": "person:42",
            "person_id": "42",
            "person_name": "Abhishek",
            "identity_source": "face_recognition",
            "identity_confidence": 0.97,
        }
        mock_post.return_value = resp

        relay_client.request_person_identity_resolve(
            media_id="cam-9",
            track_id=7,
            frame_id="f-1",
            person_id="42",
            person_name="Abhishek",
            match_score=0.97,
            camera_name="Camera 9",
            camera_timestamp="2026-02-23T10:00:00Z",
        )

        out = relay_client.fetch_person_identities("cam-9")
        self.assertIn("7", out)
        self.assertEqual(out["7"]["global_person_id"], "person:42")
        payload = mock_post.call_args.kwargs["json"]
        self.assertEqual(payload["media_id"], "cam-9")
        self.assertEqual(payload["track_id"], 7)

    @patch("relay_client.requests.post")
    def test_resolve_person_identity_sync_updates_local_cache(self, mock_post: MagicMock) -> None:
        resp = MagicMock()
        resp.status_code = 200
        resp.json.return_value = {
            "global_person_id": "unknown:12",
            "person_id": "",
            "person_name": "",
            "identity_source": "reid_match",
            "identity_confidence": 0.88,
        }
        mock_post.return_value = resp

        out = relay_client.resolve_person_identity_sync(
            media_id="cam-5",
            track_id=11,
            frame_id="f-2",
            embedding=[0.1, 0.2, 0.3],
            timeout_seconds=0.2,
        )

        self.assertEqual(out.get("global_person_id"), "unknown:12")
        cached = relay_client.fetch_person_identities("cam-5")
        self.assertIn("11", cached)
        self.assertEqual(cached["11"]["global_person_id"], "unknown:12")

    @patch("relay_client.requests.post")
    @patch("relay_client._executor.submit")
    def test_request_person_identity_resolve_includes_observation_metadata(
        self,
        mock_submit: MagicMock,
        mock_post: MagicMock,
    ) -> None:
        mock_submit.side_effect = lambda fn, *args, **kwargs: fn()
        resp = MagicMock()
        resp.status_code = 200
        resp.json.return_value = {"global_person_id": "unknown:77"}
        mock_post.return_value = resp

        relay_client.request_person_identity_resolve(
            media_id="cam-x",
            track_id=3,
            embedding=[0.1, 0.2],
            embedding_kind="body_reid",
            embedding_model="osnet_ain_x1_0",
            quality_score=0.91,
            observation_ts="2026-05-08T12:00:00Z",
        )

        payload = mock_post.call_args.kwargs["json"]
        self.assertEqual(payload["embedding_kind"], "body_reid")
        self.assertEqual(payload["embedding_model"], "osnet_ain_x1_0")
        self.assertEqual(payload["quality_score"], 0.91)
        self.assertEqual(payload["observation_ts"], "2026-05-08T12:00:00Z")


if __name__ == "__main__":
    unittest.main()
