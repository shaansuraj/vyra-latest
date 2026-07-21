import unittest

import numpy as np

from tracker import _box_iou, _match_tracked_detections


class TestTrackerRegression(unittest.TestCase):
    def test_iou_matching_tolerates_motion_model_shift(self) -> None:
        original = np.array(
            [
                [0.0, 0.0, 100.0, 100.0],
                [130.0, 0.0, 230.0, 100.0],
            ],
            dtype=np.float32,
        )
        tracked = np.array(
            [
                [8.0, 0.0, 108.0, 100.0],
                [122.0, 0.0, 222.0, 100.0],
            ],
            dtype=np.float32,
        )

        self.assertGreater(_box_iou(tracked[0], original[0]), 0.8)
        self.assertEqual(_match_tracked_detections(tracked, original), {0: 0, 1: 1})

    def test_iou_matching_is_one_to_one(self) -> None:
        original = np.array(
            [
                [0.0, 0.0, 100.0, 100.0],
                [90.0, 0.0, 190.0, 100.0],
            ],
            dtype=np.float32,
        )
        tracked = np.array(
            [
                [5.0, 0.0, 105.0, 100.0],
                [95.0, 0.0, 195.0, 100.0],
            ],
            dtype=np.float32,
        )

        matches = _match_tracked_detections(tracked, original)
        self.assertEqual(set(matches.keys()), {0, 1})
        self.assertEqual(set(matches.values()), {0, 1})


if __name__ == "__main__":
    unittest.main()
