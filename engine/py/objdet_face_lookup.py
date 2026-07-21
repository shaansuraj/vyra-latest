from typing import Any, Dict


def select_face_lookup_frame_id(item: Dict[str, Any]) -> str:
    """Pick the frame identifier used for relay face lookup requests.

    In the common adapter path, `item["id"]` is the actual frame id and must be
    used for `/cv/face-lookup`. `_job_id` is only a fallback for legacy shapes.
    """
    input_obj = item.get("input") if isinstance(item.get("input"), dict) else {}
    data_obj = item.get("data") if isinstance(item.get("data"), dict) else {}
    # Prefer explicit frame identifiers first.  In some objdet relay paths,
    # top-level `id` may be a job/batch id.
    candidates = (
        item.get("frame_id"),
        item.get("_frame_id"),
        input_obj.get("id"),
        data_obj.get("id"),
        item.get("id"),
        item.get("_job_id"),
    )
    for value in candidates:
        if value is None:
            continue
        frame_id = str(value).strip()
        if frame_id:
            return frame_id
    return ""
