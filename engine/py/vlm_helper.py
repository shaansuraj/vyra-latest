"""VLM Helper - On-demand image analysis for violation detection

This module provides a singleton VLM instance that can be used by detection workers
(helmet, object detection) to analyze images when violations or persons are detected.
"""
import os
import torch
from PIL import Image
from transformers import AutoProcessor, AutoModelForImageTextToText
from typing import Optional


class VLMHelper:
    """Singleton VLM instance for triggered analysis"""

    _instance = None

    def __init__(self):
        if VLMHelper._instance is not None:
            raise RuntimeError("Use VLMHelper.get_instance()")

        model_id = os.getenv("VLM_MODEL_ID", "HuggingFaceTB/SmolVLM2-2.2B-Instruct")
        self.device = "cuda" if torch.cuda.is_available() else "cpu"

        print(f"[VLMHelper] Loading {model_id} on {self.device}...")
        self.processor = AutoProcessor.from_pretrained(model_id)
        self.model = AutoModelForImageTextToText.from_pretrained(
            model_id,
            torch_dtype=torch.float16 if self.device == "cuda" else torch.float32
        ).to(self.device)

        # Optimize for speed
        self.processor.image_processor.size = {"longest_edge": 600}
        print(f"[VLMHelper] Model loaded successfully on {self.device}")

    @classmethod
    def get_instance(cls):
        """Get the singleton VLM instance. Creates it if it doesn't exist."""
        if cls._instance is None:
            cls._instance = VLMHelper()
        return cls._instance

    def analyze_image(self, image_path: str, prompt: str, max_tokens: int = 256) -> str:
        """Analyze image with prompt and return text summary

        Args:
            image_path: Path to the image file
            prompt: Text prompt describing what to analyze
            max_tokens: Maximum tokens to generate (default: 256)

        Returns:
            Text summary from the VLM
        """
        try:
            image = Image.open(image_path).convert("RGB")
        except Exception as e:
            return f"Error loading image: {e}"

        messages = [{
            "role": "user",
            "content": [
                {"type": "image", "image": image},
                {"type": "text", "text": prompt}
            ]
        }]

        try:
            inputs = self.processor.apply_chat_template(
                [messages],
                add_generation_prompt=True,
                tokenize=True,
                return_dict=True,
                return_tensors="pt"
            ).to(self.device)

            outputs = self.model.generate(**inputs, max_new_tokens=max_tokens)
            response = self.processor.batch_decode(outputs, skip_special_tokens=True)[0]

            # Extract assistant reply
            if "Assistant: " in response:
                response = response.split("Assistant: ")[-1].strip()

            return response
        except Exception as e:
            return f"VLM inference error: {e}"

    def is_available(self) -> bool:
        """Check if VLM is loaded and ready"""
        return self.model is not None and self.processor is not None
