import asyncio
from ws_client_handler import client_handler
import json
import os
import re

import torch
from transformers import AutoProcessor, AutoModelForImageTextToText
from PIL import Image


def _env_flag(name: str, default: bool = False) -> bool:
    raw = os.getenv(name)
    if raw is None:
        return default
    return str(raw).strip().lower() in ("1", "true", "yes", "on")


VLM_DEBUG_LOGS = _env_flag("WORKER_DEBUG_LOGS", False) or _env_flag("VYRA_DEBUG_LOGS", False)


def _debug(*args):
    if VLM_DEBUG_LOGS:
        print(*args)


_SEVERITY_RE = re.compile(r"\bseverity\s*:\s*(high|medium|low)\b", re.IGNORECASE)
_STATUS_RE = re.compile(r"\bstatus\s*:\s*(safe|alert)\b", re.IGNORECASE)
_CONTROL_MARKERS_RE = re.compile(r"\b(?:severity|status)\s*:\s*[a-z]+\b", re.IGNORECASE)

_HIGH_HAZARD_PATTERNS = [
    r"\bfire\b", r"\bsmoke\b", r"\bflame\b", r"\bexplosion\b",
    r"\bfight(?:ing)?\b", r"\bweapon(?:s)?\b", r"\battack\b", r"\bviolent\b",
    r"\bunconscious\b", r"\bbleeding\b", r"\binjur(?:y|ies|ed)\b",
    r"\bcollapse(?:d)?\b", r"\bfall(?:en)?\s+(?:down|from|off)\b",
    r"\bchemical leak\b", r"\btoxic\b", r"\bgas leak\b",
]

_MEDIUM_HAZARD_PATTERNS = [
    r"\bspill(?:ed|s)?\b", r"\bslippery\b", r"\bwet floor\b",
    r"\bobstruction\b", r"\bblocked exit\b",
    r"\bunauthori[sz]ed(?: access)?\b", r"\bintrusion\b", r"\bsuspicious\b",
    r"\bhazard(?:ous)?\b", r"\bunsafe\b", r"\brisk\b",
]

_SAFE_PATTERNS = [
    r"\bno (?:immediate )?(?:safety )?(?:hazard|hazards|danger|issue|issues|problem|problems)\b",
    r"\bno signs? of\b",
    r"\bnothing unusual\b",
    r"\bappears? normal\b",
    r"\blooks? normal\b",
    r"\bappears? safe\b",
    r"\bno visible (?:threat|risk|violation|violations)\b",
]

_NEGATED_HAZARD_RE = re.compile(
    r"\b(?:no|not|without)\s+(?:visible\s+)?(?:signs?\s+of\s+)?"
    r"(?:fire|smoke|flame|explosion|fight(?:ing)?|weapon(?:s)?|attack|injur(?:y|ies|ed)|"
    r"bleeding|collapse(?:d)?|fall(?:en)?|spill(?:ed|s)?|hazard(?:s)?|danger|unsafe|risk)\b",
    re.IGNORECASE,
)


def _extract_severity_marker(text: str) -> str | None:
    m = _SEVERITY_RE.search(text)
    return m.group(1).lower() if m else None


def _extract_status_marker(text: str) -> str | None:
    m = _STATUS_RE.search(text)
    return m.group(1).lower() if m else None


def _strip_control_markers(text: str) -> str:
    return _CONTROL_MARKERS_RE.sub(" ", text)


def _count_any(patterns: list[str], text: str) -> int:
    return sum(1 for p in patterns if re.search(p, text, re.IGNORECASE))


def _infer_severity(response: str) -> str:
    raw = response or ""
    lowered = raw.lower()
    marked_severity = _extract_severity_marker(lowered)
    marked_status = _extract_status_marker(lowered)

    scan_text = _strip_control_markers(lowered)
    scan_text = _NEGATED_HAZARD_RE.sub(" ", scan_text)
    safe_hits = _count_any(_SAFE_PATTERNS, lowered)
    high_hits = _count_any(_HIGH_HAZARD_PATTERNS, scan_text)
    medium_hits = _count_any(_MEDIUM_HAZARD_PATTERNS, scan_text)

    if marked_status == "safe" and high_hits == 0 and medium_hits == 0:
        return "low"
    if marked_status == "alert" and high_hits == 0 and medium_hits == 0:
        return "medium"

    if marked_severity in ("high", "medium", "low"):
        if marked_severity in ("high", "medium") and high_hits == 0 and medium_hits == 0 and safe_hits > 0:
            return "low"
        return marked_severity

    if high_hits > 0:
        return "high"
    if medium_hits > 0:
        return "medium"
    return "low"


def _safe_get_model_id() -> str:
    """
    Prefer VLM_MODEL_ID for this worker.
    Fall back to MODEL_ID only if VLM_MODEL_ID is missing.
    Guard against accidental .pt values.
    """
    model_id = os.getenv("VLM_MODEL_ID") or os.getenv("MODEL_ID") or "HuggingFaceTB/SmolVLM2-2.2B-Instruct"

    # If someone accidentally sets yolov8m.pt etc, don't send that to HF transformers
    if model_id.strip().lower().endswith(".pt"):
        print(f"[WARN] MODEL_ID/VLM_MODEL_ID points to a .pt file ({model_id}). "
              f"This VLM worker expects a HuggingFace repo id. Falling back to SmolVLM2.")
        model_id = "HuggingFaceTB/SmolVLM2-2.2B-Instruct"

    return model_id


def _load_images_into_messages(messages: list) -> tuple[list, list[Image.Image]]:
    """
    Converts {"type":"image","image":"/path"} content items into PIL.Image
    objects in-place (image key becomes PIL.Image), and collects images in order.

    Returns:
      - updated messages
      - list of PIL images found in the conversation (in order)
    """
    collected_images = []

    for msg in messages:
        content = msg.get("content", [])
        if not isinstance(content, list):
            continue

        for item in content:
            if not isinstance(item, dict):
                continue
            if item.get("type") == "image":
                img_path = item.get("image")
                if not img_path or not os.path.exists(img_path):
                    raise FileNotFoundError(f"Image path not found: {img_path}")
                pil_img = Image.open(img_path).convert("RGB")

                # Put PIL image back into the message (many VLM processors accept this)
                item["image"] = pil_img
                collected_images.append(pil_img)

    return messages, collected_images


def load_ai_model():
    device = "cuda" if torch.cuda.is_available() else "cpu"
    model_id = _safe_get_model_id()
    print(f"Loading {model_id} model on {device}...")

    processor = AutoProcessor.from_pretrained(model_id)

    # Optional perf: shrink images
    if hasattr(processor, "image_processor") and hasattr(processor.image_processor, "size"):
        _debug(f"Original image size: {processor.image_processor.size}")
        processor.image_processor.size = {"longest_edge": 600}
        _debug(f"Optimized image size: {processor.image_processor.size}")

    model = AutoModelForImageTextToText.from_pretrained(
        model_id,
        torch_dtype=torch.float16 if device == "cuda" else torch.float32
    ).to(device)

    def worker_function(data):
        _debug(f"[AI Thread] Starting heavy AI workload with data keys: {list(data.keys())}")

        message_inputs_raw = data.get("inputs", [])
        if not isinstance(message_inputs_raw, list) or not message_inputs_raw:
            return {"output": []}

        # Flatten nested structure: { id: job_id, inputs: [{ id: frame_id, messages: [...] }] }
        message_inputs = []
        job_ids = []
        for it in message_inputs_raw:
            if isinstance(it, dict) and isinstance(it.get("inputs"), list):
                # Nested structure
                job_id = it.get("id")
                for inner in it.get("inputs", []):
                    if isinstance(inner, dict):
                        message_inputs.append(inner)
                        job_ids.append(job_id)
            elif isinstance(it, dict):
                # Flat structure
                message_inputs.append(it)
                job_ids.append(None)

        conversations = []
        images_per_conv = []
        ids = []

        for idx, inp in enumerate(message_inputs):
            req_id = inp.get("id")
            msgs = inp.get("messages")

            if not req_id or not isinstance(msgs, list):
                _debug(f"[AI Thread] Skipping invalid input: {inp}")
                continue

            # Load images from paths into PIL
            msgs, conv_images = _load_images_into_messages(msgs)

            conversations.append(msgs)
            images_per_conv.append(conv_images)   # list-of-images per conversation
            ids.append(req_id)

        if not conversations:
            return {"output": []}

        # Build text prompts for each conversation
        texts = [
            processor.apply_chat_template(conv, add_generation_prompt=True, tokenize=False)
            for conv in conversations
        ]

        # Tokenize with images
        inputs = processor(
            text=texts,
            images=images_per_conv if any(len(x) > 0 for x in images_per_conv) else None,
            return_tensors="pt",
            padding=True
        ).to(device)

        max_new_tokens = int(os.getenv("MAX_NEW_TOKENS", "256"))
        with torch.inference_mode():
            generated = model.generate(**inputs, max_new_tokens=max_new_tokens)

        # Decode ONLY the newly generated tokens (more reliable than splitting "Assistant:")
        prompt_len = inputs["input_ids"].shape[1]
        new_tokens = generated[:, prompt_len:]
        responses = processor.batch_decode(new_tokens, skip_special_tokens=True)

        outputs = []
        for i, response in enumerate(responses):
            response = response.strip()

            severity = _infer_severity(response)

            # Clean up control markers from the response text for display
            clean_response = _strip_control_markers(response).strip()
            clean_response = clean_response.strip().rstrip('.').strip()
            if clean_response:
                clean_response = clean_response[0].upper() + clean_response[1:]

            outputs.append({
                "id": ids[i],
                "response": clean_response or response,
                "severity": severity,
                "summary": clean_response or response
            })

        # Group outputs by job_id if present
        if any(job_ids):
            job_id_map = {}
            for idx, out_item in enumerate(outputs):
                job_id = job_ids[idx] if idx < len(job_ids) else None
                if job_id:
                    if job_id not in job_id_map:
                        job_id_map[job_id] = []
                    job_id_map[job_id].append(out_item)

            if job_id_map:
                grouped_outputs = []
                for jid, items in job_id_map.items():
                    grouped_outputs.append({
                        "id": jid,
                        "output": items
                    })
                _debug("[AI Thread] Heavy AI workload finished (grouped).")
                return {"output": grouped_outputs}

        _debug("[AI Thread] Heavy AI workload finished.")
        return {"output": outputs}

    return worker_function


if __name__ == "__main__":
    worker_function = load_ai_model()
    asyncio.run(client_handler(worker_function))
