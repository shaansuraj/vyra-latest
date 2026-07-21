import asyncio
import os
from ws_client_handler import client_handler
import torch
from transformers import AutoTokenizer, AutoImageProcessor, AutoModel, SiglipModel
from PIL import Image

"""
INPUT/OUTPUT SHAPE EXAMPLES:

INPUT FORMAT:
{
  "inputs": [
    {
      "id": "text_1",
      "text": "Example text for embedding",
      "prompt_name": "query"   # or "passage" (ignored by SigLIP2)
    },
    {
      "id": "img_1",
      "filepath": "path/to/image.jpg"
    }
  ]
}

OUTPUT FORMAT:
{
  "output": [
    {
      "id": "text_1",
      "embedding": [0.1, 0.2, 0.3, ...]
    },
    {
      "id": "img_1",
      "embedding": [0.7, 0.8, 0.9, ...]
    }
  ]
}
"""

TEXT_MODEL_ID = os.getenv("RAG_TEXT_EMBEDDING_MODEL", "BAAI/bge-small-en-v1.5")
QUERY_PREFIX = os.getenv("RAG_QUERY_PREFIX", "Represent this sentence for searching relevant passages: ")
PASSAGE_PREFIX = os.getenv("RAG_PASSAGE_PREFIX", "")
TEXT_POOLING = os.getenv("RAG_TEXT_POOLING", "auto").strip().lower()
SIGLIP_MODEL_ID = os.getenv("SIGLIP_EMBEDDING_MODEL_ID", "google/siglip2-base-patch16-224")

_text_model = None
_text_tokenizer = None
_siglip_model = None
_image_processor = None


def _mean_pool(last_hidden_state, attention_mask):
    mask = attention_mask.unsqueeze(-1).expand(last_hidden_state.size()).float()
    summed = torch.sum(last_hidden_state * mask, dim=1)
    counts = torch.clamp(mask.sum(dim=1), min=1e-9)
    return summed / counts


def _pool_text_embeddings(model_output, attention_mask):
    pooling = TEXT_POOLING
    if pooling == "auto":
        model_id = TEXT_MODEL_ID.lower()
        pooling = "cls" if "bge" in model_id else "mean"
    if pooling == "cls":
        return model_output.last_hidden_state[:, 0]
    return _mean_pool(model_output.last_hidden_state, attention_mask)


def _load_text_model(device: str):
    global _text_model, _text_tokenizer
    if _text_model is None or _text_tokenizer is None:
        print(f"Loading text embedding model ({TEXT_MODEL_ID}) on {device}...")
        _text_tokenizer = AutoTokenizer.from_pretrained(TEXT_MODEL_ID)
        _text_model = AutoModel.from_pretrained(TEXT_MODEL_ID).to(device)
        _text_model.eval()
    return _text_model, _text_tokenizer


def _load_siglip_model(device: str):
    global _siglip_model, _image_processor
    if _siglip_model is None or _image_processor is None:
        print(f"Loading SigLIP2 image embedding model ({SIGLIP_MODEL_ID}) on {device}...")
        _siglip_model = SiglipModel.from_pretrained(SIGLIP_MODEL_ID).to(device)
        _siglip_model.eval()
        _image_processor = AutoImageProcessor.from_pretrained(SIGLIP_MODEL_ID)
    return _siglip_model, _image_processor


def load_ai_model():
    """Initializes embedding helpers and returns the worker function."""
    device = "cuda" if torch.cuda.is_available() else "cpu"
    print(f"Embedding worker starting on {device}; text_model={TEXT_MODEL_ID}")

    def worker_function(data: dict):
        """Processes embedding requests using text retrieval embeddings for text and SigLIP2 for images."""
        print(f"[Embedding Thread] Starting embedding workload with data: {data}")

        inputs = data.get("inputs", [])
        if not inputs:
            raise ValueError("No inputs provided for embedding")

        text_inputs = []
        image_inputs = []

        # Separate text and image inputs
        for inp in inputs:
            if "text" in inp and inp.get("text") is not None:
                text_inputs.append(inp)
            elif "filepath" in inp and inp.get("filepath"):
                image_inputs.append(inp)

        result_embeddings = []

        # ---- Text embeddings ----
        if text_inputs:
            model, tokenizer = _load_text_model(device)
            texts = []
            for inp in text_inputs:
                prompt_name = str(inp.get("prompt_name") or "query").strip().lower()
                prefix = QUERY_PREFIX if prompt_name == "query" else PASSAGE_PREFIX
                texts.append(prefix + str(inp["text"]))
            ids = [inp["id"] for inp in text_inputs]

            text_processed = tokenizer(
                texts,
                return_tensors="pt",
                padding=True,
                truncation=True,
                max_length=int(os.getenv("RAG_TEXT_EMBEDDING_MAX_LENGTH", "512")),
            ).to(device)

            with torch.no_grad():
                output = model(**text_processed)
                text_features = _pool_text_embeddings(output, text_processed["attention_mask"])
                text_embeddings = text_features / torch.clamp(text_features.norm(dim=-1, keepdim=True), min=1e-9)

            for i, result_id in enumerate(ids):
                result_embeddings.append(
                    {
                        "id": result_id,
                        "embedding": text_embeddings[i].detach().cpu().tolist(),
                        "embedding_model": TEXT_MODEL_ID,
                    }
                )

        # ---- Image embeddings ----
        if image_inputs:
            model, image_processor = _load_siglip_model(device)
            ids = [inp["id"] for inp in image_inputs]

            images = []
            for inp in image_inputs:
                img = Image.open(inp["filepath"]).convert("RGB")
                images.append(img)

            image_processed = image_processor(images=images, return_tensors="pt").to(device)

            with torch.no_grad():
                image_features = model.get_image_features(**image_processed)
                image_embeddings = image_features / image_features.norm(dim=-1, keepdim=True)

            for i, result_id in enumerate(ids):
                result_embeddings.append(
                    {
                        "id": result_id,
                        "embedding": image_embeddings[i].detach().cpu().tolist(),
                    }
                )

        result = {"output": result_embeddings}
        print("[Embedding Thread] Embedding workload finished.")
        return result

    return worker_function


if __name__ == "__main__":
    worker_function = load_ai_model()
    asyncio.run(client_handler(worker_function))
