"""Face profile quality assessment + EXIF orientation normalization.

Used by relay upload endpoint to enforce enrollment quality before storing
person face profiles.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
from dataclasses import dataclass
from typing import Any, Dict, List, Tuple

import cv2
import numpy as np
from PIL import Image, ImageOps


def _resolve_insightface_runtime() -> Tuple[List[str], int]:
    disable_gpu = os.getenv("DISABLE_GPU", "0") == "1"
    if disable_gpu:
        return ["CPUExecutionProvider"], -1

    try:
        import onnxruntime as ort  # type: ignore
        providers = ort.get_available_providers()
    except Exception:
        return ["CPUExecutionProvider"], -1

    if "CUDAExecutionProvider" in providers:
        return ["CUDAExecutionProvider", "CPUExecutionProvider"], 0
    return ["CPUExecutionProvider"], -1


def _get_face_app(model_name: str):
    from insightface.app import FaceAnalysis  # type: ignore

    providers, ctx_id = _resolve_insightface_runtime()
    app = FaceAnalysis(name=model_name, providers=providers)
    app.prepare(ctx_id=ctx_id, det_size=(640, 640))
    return app


@dataclass
class QualityConfig:
    min_face_px: float
    min_blur_var: float
    min_det_conf: float
    dominant_ratio: float


def _cfg_from_env() -> QualityConfig:
    return QualityConfig(
        min_face_px=float(os.getenv("FACE_ENROLLMENT_MIN_FACE_PX", "96")),
        min_blur_var=float(os.getenv("FACE_ENROLLMENT_MIN_BLUR_VAR", "90")),
        min_det_conf=float(os.getenv("FACE_ENROLLMENT_MIN_DET_CONF", "0.45")),
        dominant_ratio=float(os.getenv("FACE_ENROLLMENT_DOMINANT_RATIO", "0.55")),
    )


def _face_area(face: Any) -> float:
    bbox = getattr(face, "bbox", None)
    if bbox is None or len(bbox) < 4:
        return 0.0
    w = float(max(0.0, bbox[2] - bbox[0]))
    h = float(max(0.0, bbox[3] - bbox[1]))
    return w * h


def _face_min_side(face: Any) -> float:
    bbox = getattr(face, "bbox", None)
    if bbox is None or len(bbox) < 4:
        return 0.0
    w = float(max(0.0, bbox[2] - bbox[0]))
    h = float(max(0.0, bbox[3] - bbox[1]))
    return min(w, h)


def _laplacian_variance(rgb: np.ndarray) -> float:
    gray = cv2.cvtColor(rgb, cv2.COLOR_RGB2GRAY)
    return float(cv2.Laplacian(gray, cv2.CV_64F).var())


def assess(input_path: str, output_path: str, model_name: str) -> Dict[str, Any]:
    cfg = _cfg_from_env()

    src = Image.open(input_path)
    normalized = ImageOps.exif_transpose(src).convert("RGB")
    normalized.save(output_path, "JPEG", quality=95)

    rgb = np.array(normalized)
    h, w = rgb.shape[:2]

    app = _get_face_app(model_name)
    faces = app.get(rgb)
    n_faces = len(faces)

    metrics: Dict[str, Any] = {
        "image_width": int(w),
        "image_height": int(h),
        "face_count": int(n_faces),
        "dominant_face_min_px": 0.0,
        "dominant_det_conf": 0.0,
        "blur_var": round(_laplacian_variance(rgb), 3),
    }

    if n_faces == 0:
        return {
            "accepted": False,
            "reason": "No face detected. Upload a clear front-face photo.",
            "metrics": metrics,
        }

    faces_sorted = sorted(faces, key=_face_area, reverse=True)
    dominant = faces_sorted[0]
    dom_area = _face_area(dominant)
    second_area = _face_area(faces_sorted[1]) if n_faces > 1 else 0.0

    dominant_min_px = _face_min_side(dominant)
    dominant_det_conf = float(getattr(dominant, "det_score", 0.0) or 0.0)
    metrics["dominant_face_min_px"] = round(dominant_min_px, 3)
    metrics["dominant_det_conf"] = round(dominant_det_conf, 6)

    if n_faces > 1 and dom_area > 0.0 and (second_area / dom_area) >= cfg.dominant_ratio:
        return {
            "accepted": False,
            "reason": "Multiple prominent faces detected. Use a photo with one clear person.",
            "metrics": metrics,
        }

    if dominant_min_px < cfg.min_face_px:
        return {
            "accepted": False,
            "reason": f"Face too small ({dominant_min_px:.0f}px). Use a closer photo.",
            "metrics": metrics,
        }

    if metrics["blur_var"] < cfg.min_blur_var:
        return {
            "accepted": False,
            "reason": "Image too blurry. Use a sharper profile photo.",
            "metrics": metrics,
        }

    if dominant_det_conf < cfg.min_det_conf:
        return {
            "accepted": False,
            "reason": "Face confidence too low. Improve lighting/angle and retry.",
            "metrics": metrics,
        }

    return {
        "accepted": True,
        "reason": "ok",
        "metrics": metrics,
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--input", required=True)
    parser.add_argument("--output", required=True)
    default_model = "buffalo_sc" if str(os.getenv("LEGACY_MODE", "")).strip().lower() in ("1", "true", "yes", "on") else os.getenv("FACE_PRIMARY_MODEL", "buffalo_l")
    parser.add_argument("--model", default=default_model)
    args = parser.parse_args()

    try:
        result = assess(args.input, args.output, args.model)
        print(json.dumps(result))
        return 0
    except Exception as exc:
        print(
            json.dumps(
                {
                    "accepted": False,
                    "reason": f"quality_check_failed: {exc}",
                    "metrics": {},
                }
            )
        )
        return 1


if __name__ == "__main__":
    sys.exit(main())
