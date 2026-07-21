"""Person ReID helpers for relay-authoritative identity resolution.

Accuracy-first path:
  - expanded person crop
  - aspect-preserving resize
  - luminance equalization / color normalization
  - quality scoring (size + sharpness)
  - best-effort embedding backend selection

Primary requested backend:
  - PERSON_REID_BACKEND=fastreid
  - PERSON_REID_MODEL=osnet_ain_x1_0

When the requested backend is unavailable, the helper falls back to SigLIP and
then to a lightweight histogram embedding so workers remain operational.
"""

from __future__ import annotations

import os
import threading
from typing import Any, Dict, List, Optional, Tuple

import numpy as np
from PIL import Image, ImageOps

try:
    import cv2

    _CV2_AVAILABLE = True
except Exception:
    cv2 = None  # type: ignore[assignment]
    _CV2_AVAILABLE = False

try:
    import torch
    from transformers import AutoModel, AutoProcessor

    _TRANSFORMERS_AVAILABLE = True
except Exception:
    torch = None  # type: ignore[assignment]
    AutoModel = None  # type: ignore[assignment]
    AutoProcessor = None  # type: ignore[assignment]
    _TRANSFORMERS_AVAILABLE = False

try:
    try:
        from torchreid.utils import FeatureExtractor as _TorchreidFeatureExtractor
    except ModuleNotFoundError:
        from torchreid.reid.utils import FeatureExtractor as _TorchreidFeatureExtractor

    _TORCHREID_AVAILABLE = True
except Exception:
    _TorchreidFeatureExtractor = None  # type: ignore[assignment]
    _TORCHREID_AVAILABLE = False

_SIGLIP_MODEL_ID = "google/siglip2-base-patch16-224"
_MODEL_LOCK = threading.Lock()
_MODEL_STATE: Dict[str, Any] = {
    "loaded": False,
    "failed": False,
    "backend": "",
    "model_name": "",
    "model": None,
    "processor": None,
    "device": "cpu",
}


def _legacy_mode_enabled() -> bool:
    return str(os.getenv("LEGACY_MODE", "")).strip().lower() in ("1", "true", "yes", "on")


def _requested_backend() -> str:
    if _legacy_mode_enabled():
        return "siglip2"
    return str(os.getenv("PERSON_REID_BACKEND", "fastreid")).strip().lower() or "fastreid"


def _requested_model_name() -> str:
    if _legacy_mode_enabled():
        return _siglip_model_id()
    return str(os.getenv("PERSON_REID_MODEL", "osnet_ain_x1_0")).strip() or "osnet_ain_x1_0"


def _siglip_model_id() -> str:
    return str(os.getenv("PERSON_REID_MODEL_ID", _SIGLIP_MODEL_ID)).strip() or _SIGLIP_MODEL_ID


def _pick_device() -> str:
    forced = str(os.getenv("PERSON_REID_DEVICE", "")).strip().lower()
    if forced in ("cpu", "cuda", "0", "1", "2", "3"):
        return "cuda:0" if forced == "cuda" else ("cpu" if forced == "cpu" else f"cuda:{forced}")
    if torch is None:
        return "cpu"
    try:
        if torch.cuda.is_available() and torch.cuda.device_count() > 0:
            return "cuda:0"
    except Exception:
        pass
    return "cpu"


def _load_backend_once() -> Tuple[str, str, Optional[Any], Optional[Any], str]:
    requested_backend = _requested_backend()
    requested_model = _requested_model_name()
    device = _pick_device()

    with _MODEL_LOCK:
        if (
            _MODEL_STATE["loaded"]
            and _MODEL_STATE["backend"] == requested_backend
            and _MODEL_STATE["model_name"] == requested_model
        ):
            return (
                str(_MODEL_STATE["backend"]),
                str(_MODEL_STATE["model_name"]),
                _MODEL_STATE["model"],
                _MODEL_STATE["processor"],
                str(_MODEL_STATE["device"]),
            )

        if requested_backend == "fastreid" and _TORCHREID_AVAILABLE and _TorchreidFeatureExtractor is not None:
            try:
                extractor = _TorchreidFeatureExtractor(model_name=requested_model, device=device)
                _MODEL_STATE.update(
                    {
                        "loaded": True,
                        "failed": False,
                        "backend": "fastreid",
                        "model_name": requested_model,
                        "model": extractor,
                        "processor": None,
                        "device": device,
                    }
                )
                print(f"[person_reid] Loaded backend=fastreid model={requested_model} device={device}")
                return "fastreid", requested_model, extractor, None, device
            except Exception as exc:
                print(f"[person_reid] FastReID backend unavailable ({exc}); falling back to SigLIP")

        if _TRANSFORMERS_AVAILABLE and AutoProcessor is not None and AutoModel is not None:
            try:
                model_id = _siglip_model_id()
                processor = AutoProcessor.from_pretrained(model_id)
                model = AutoModel.from_pretrained(model_id)
                model.to(device)
                model.eval()
                _MODEL_STATE.update(
                    {
                        "loaded": True,
                        "failed": False,
                        "backend": "siglip2",
                        "model_name": model_id,
                        "model": model,
                        "processor": processor,
                        "device": device,
                    }
                )
                print(f"[person_reid] Loaded backend=siglip2 model={model_id} device={device}")
                return "siglip2", model_id, model, processor, device
            except Exception as exc:
                print(f"[person_reid] SigLIP load failed ({exc}); using histogram fallback")

        _MODEL_STATE.update(
            {
                "loaded": True,
                "failed": True,
                "backend": "histogram",
                "model_name": "rgb_hist_16x3",
                "model": None,
                "processor": None,
                "device": "cpu",
            }
        )
        return "histogram", "rgb_hist_16x3", None, None, "cpu"


def _expand_box(box_px: Dict[str, Any], width: int, height: int, expand_x: float = 0.12, expand_y: float = 0.08) -> Optional[Tuple[int, int, int, int]]:
    try:
        x1 = float(box_px.get("x_min", 0) or 0)
        y1 = float(box_px.get("y_min", 0) or 0)
        x2 = float(box_px.get("x_max", 0) or 0)
        y2 = float(box_px.get("y_max", 0) or 0)
    except Exception:
        return None
    if x2 <= x1 or y2 <= y1:
        return None
    pad_x = (x2 - x1) * expand_x
    pad_y = (y2 - y1) * expand_y
    return (
        max(0, int(x1 - pad_x)),
        max(0, int(y1 - pad_y)),
        min(width, int(x2 + pad_x)),
        min(height, int(y2 + pad_y)),
    )


def _crop_person(frame_path: str, box_px: Dict[str, Any]) -> Optional[Image.Image]:
    try:
        img = ImageOps.exif_transpose(Image.open(frame_path).convert("RGB"))
    except Exception:
        return None
    expanded = _expand_box(box_px, img.width, img.height)
    if expanded is None:
        return None
    x1, y1, x2, y2 = expanded
    if x2 <= x1 or y2 <= y1:
        return None
    return img.crop((x1, y1, x2, y2))


def _resize_with_aspect(crop: Image.Image, target_size: Tuple[int, int] = (128, 256)) -> Image.Image:
    target_w, target_h = target_size
    src_w, src_h = crop.size
    if src_w <= 0 or src_h <= 0:
        return crop.resize(target_size)
    scale = min(target_w / float(src_w), target_h / float(src_h))
    new_w = max(1, int(round(src_w * scale)))
    new_h = max(1, int(round(src_h * scale)))
    resized = crop.resize((new_w, new_h), Image.BICUBIC)
    canvas = Image.new("RGB", target_size, color=(127, 127, 127))
    left = (target_w - new_w) // 2
    top = (target_h - new_h) // 2
    canvas.paste(resized, (left, top))
    return canvas


def _normalize_crop(crop: Image.Image) -> Image.Image:
    resized = _resize_with_aspect(crop, (128, 256))
    arr = np.asarray(resized, dtype=np.uint8)
    if arr.size == 0:
        return resized
    if _CV2_AVAILABLE and cv2 is not None:
        lab = cv2.cvtColor(arr, cv2.COLOR_RGB2LAB)
        l_channel, a_channel, b_channel = cv2.split(lab)
        clahe = cv2.createCLAHE(clipLimit=2.0, tileGridSize=(8, 8))
        l_channel = clahe.apply(l_channel)
        merged = cv2.merge((l_channel, a_channel, b_channel))
        arr = cv2.cvtColor(merged, cv2.COLOR_LAB2RGB)
    else:
        # Lightweight fallback without OpenCV: normalize brightness globally.
        gray = arr.mean(axis=2, keepdims=True)
        mean_val = max(1.0, float(gray.mean()))
        scale = min(1.25, max(0.85, 128.0 / mean_val))
        arr = np.clip(arr.astype(np.float32) * scale, 0, 255).astype(np.uint8)
    return Image.fromarray(arr, mode="RGB")


def _laplacian_variance(rgb_arr: np.ndarray) -> float:
    if rgb_arr.size == 0:
        return 0.0
    if _CV2_AVAILABLE and cv2 is not None:
        try:
            gray = cv2.cvtColor(rgb_arr, cv2.COLOR_RGB2GRAY)
            return float(cv2.Laplacian(gray, cv2.CV_64F).var())
        except Exception:
            return 0.0
    gray = rgb_arr.mean(axis=2)
    if gray.size == 0:
        return 0.0
    dy = np.diff(gray, axis=0)
    dx = np.diff(gray, axis=1)
    return float(np.var(dx) + np.var(dy))


def _quality_score(crop: Image.Image) -> float:
    arr = np.asarray(crop, dtype=np.uint8)
    if arr.size == 0:
        return 0.0
    min_side = float(min(crop.size))
    size_q = min(1.0, max(0.0, min_side / 96.0))
    blur_q = min(1.0, max(0.0, _laplacian_variance(arr) / 140.0))
    return (0.60 * size_q) + (0.40 * blur_q)


def _normalize_vector(values: List[float]) -> List[float]:
    if not values:
        return []
    arr = np.asarray(values, dtype=np.float32)
    norm = float(np.linalg.norm(arr))
    if norm <= 0.0:
        return []
    arr = arr / norm
    return [float(v) for v in arr.tolist()]


def _fallback_hist_embedding_from_crop(crop: Image.Image) -> List[float]:
    try:
        arr = np.asarray(crop, dtype=np.float32) / 255.0
        if arr.size == 0:
            return []
        emb: List[float] = []
        for channel in range(3):
            hist, _ = np.histogram(arr[:, :, channel], bins=16, range=(0.0, 1.0), density=True)
            emb.extend(float(v) for v in hist.tolist())
        return _normalize_vector(emb)
    except Exception:
        return []


def _fastreid_embedding_from_crop(crop: Image.Image) -> List[float]:
    backend, _model_name, extractor, _processor, _device = _load_backend_once()
    if backend != "fastreid" or extractor is None:
        return []
    img_np = np.asarray(crop, dtype=np.uint8)
    try:
        features = extractor([img_np])
    except Exception:
        try:
            features = extractor(img_np)
        except Exception:
            return []
    try:
        if hasattr(features, "detach"):
            features = features.detach().cpu().numpy()
        arr = np.asarray(features)
        if arr.ndim == 2 and arr.shape[0] >= 1:
            return _normalize_vector([float(v) for v in arr[0].tolist()])
        if arr.ndim == 1:
            return _normalize_vector([float(v) for v in arr.tolist()])
    except Exception:
        return []
    return []


def _siglip_embedding_from_crop(crop: Image.Image) -> List[float]:
    backend, _model_name, model, processor, device = _load_backend_once()
    if backend != "siglip2" or model is None or processor is None or torch is None:
        return []
    try:
        inputs = processor(images=crop, return_tensors="pt")
        if hasattr(inputs, "to"):
            inputs = inputs.to(device)
        else:
            for key, value in list(inputs.items()):
                if hasattr(value, "to"):
                    inputs[key] = value.to(device)
        with torch.no_grad():
            feats = model.get_image_features(**inputs)
        if feats is None or int(getattr(feats, "shape", [0])[0]) <= 0:
            return []
        emb = feats[0]
        denom = emb.norm(p=2)
        if float(denom) == 0.0:
            return []
        emb = emb / denom
        return [float(v) for v in emb.detach().cpu().tolist()]
    except Exception:
        return []


def extract_observation_from_crop(frame_path: str, box_px: Dict[str, Any]) -> Dict[str, Any]:
    crop = _crop_person(frame_path, box_px)
    if crop is None:
        return {
            "embedding": [],
            "quality_score": 0.0,
            "embedding_backend": "none",
            "embedding_model": "",
        }

    normalized_crop = _normalize_crop(crop)
    quality = _quality_score(normalized_crop)

    embedding = _fastreid_embedding_from_crop(normalized_crop)
    backend, model_name, _model, _processor, _device = _load_backend_once()
    if not embedding:
        embedding = _siglip_embedding_from_crop(normalized_crop)
    if embedding and backend == "fastreid":
        return {
            "embedding": embedding,
            "quality_score": round(float(quality), 4),
            "embedding_backend": "fastreid",
            "embedding_model": model_name,
        }

    if embedding:
        return {
            "embedding": embedding,
            "quality_score": round(float(quality), 4),
            "embedding_backend": "siglip2",
            "embedding_model": _siglip_model_id(),
        }

    return {
        "embedding": _fallback_hist_embedding_from_crop(normalized_crop),
        "quality_score": round(float(quality), 4),
        "embedding_backend": "histogram",
        "embedding_model": "rgb_hist_16x3",
    }


def extract_embedding_from_crop(frame_path: str, box_px: Dict[str, Any]) -> List[float]:
    observation = extract_observation_from_crop(frame_path, box_px)
    embedding = observation.get("embedding")
    if isinstance(embedding, list):
        return [float(v) for v in embedding]
    return []
