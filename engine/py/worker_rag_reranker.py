import asyncio
import os
from typing import Any, Dict, List

import torch
from transformers import AutoModelForSequenceClassification, AutoTokenizer

from ws_client_handler import client_handler

MODEL_ID = os.getenv("RAG_RERANKER_MODEL", "cross-encoder/ms-marco-MiniLM-L-6-v2")
MAX_LENGTH = int(os.getenv("RAG_RERANKER_MAX_LENGTH", "512"))


def _candidate_text(candidate: Dict[str, Any]) -> str:
    parts = [
        str(candidate.get("title") or "").strip(),
        str(candidate.get("heading") or "").strip(),
        str(candidate.get("text") or "").strip(),
    ]
    return "\n".join([p for p in parts if p])


def load_ai_model():
    """Initializes a local cross-encoder reranker worker."""
    device = "cuda" if torch.cuda.is_available() else "cpu"
    print(f"Loading RAG reranker ({MODEL_ID}) on {device}...")
    tokenizer = AutoTokenizer.from_pretrained(MODEL_ID)
    model = AutoModelForSequenceClassification.from_pretrained(MODEL_ID).to(device)
    model.eval()

    def worker_function(data: dict):
        inputs = data.get("inputs", [])
        if not inputs:
            raise ValueError("No inputs provided for reranking")

        outputs: List[Dict[str, Any]] = []
        for job in inputs:
            job_id = str(job.get("id") or "")
            query = str(job.get("query") or "").strip()
            candidates = job.get("candidates") or []
            if not query or not isinstance(candidates, list):
                outputs.append({"id": job_id, "error": "query and candidates are required"})
                continue

            pairs = []
            valid_candidates = []
            for candidate in candidates:
                if not isinstance(candidate, dict):
                    continue
                cid = str(candidate.get("id") or "").strip()
                text = _candidate_text(candidate)
                if not cid or not text:
                    continue
                valid_candidates.append(candidate)
                pairs.append((query, text))

            if not pairs:
                outputs.append({"id": job_id, "results": []})
                continue

            encoded = tokenizer(
                pairs,
                padding=True,
                truncation=True,
                max_length=MAX_LENGTH,
                return_tensors="pt",
            ).to(device)

            with torch.no_grad():
                logits = model(**encoded).logits
                if logits.ndim == 2 and logits.shape[-1] == 1:
                    scores = logits[:, 0]
                elif logits.ndim == 2:
                    scores = logits[:, -1]
                else:
                    scores = logits

            ranked = []
            for candidate, score in zip(valid_candidates, scores.detach().cpu().tolist()):
                ranked.append({"id": str(candidate.get("id")), "score": float(score)})
            ranked.sort(key=lambda item: item["score"], reverse=True)
            outputs.append({
                "id": job_id,
                "results": ranked,
                "reranker_model": MODEL_ID,
            })

        return {"output": outputs}

    return worker_function


if __name__ == "__main__":
    worker_function = load_ai_model()
    asyncio.run(client_handler(worker_function))
