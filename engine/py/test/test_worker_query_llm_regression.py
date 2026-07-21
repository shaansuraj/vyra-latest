import unittest
from unittest.mock import patch

import worker_query_llm as llm


class _InlineThread:
    def __init__(self, target=None, **kwargs):
        self._target = target

    def start(self):
        if self._target:
            self._target()


class TestWorkerQueryLLMRegression(unittest.TestCase):
    def setUp(self) -> None:
        self._orig_started = llm._warmup_started
        self._orig_generator = llm._generator
        llm._warmup_started = False
        llm._generator = None

    def tearDown(self) -> None:
        llm._warmup_started = self._orig_started
        llm._generator = self._orig_generator

    def test_trigger_warmup_is_idempotent(self) -> None:
        calls = {"count": 0}

        def fake_ensure():
            calls["count"] += 1
            return None

        with patch.object(llm, "_ensure_generator", side_effect=fake_ensure):
            with patch.object(llm.threading, "Thread", side_effect=lambda **kw: _InlineThread(**kw)):
                first = llm._trigger_warmup()
                second = llm._trigger_warmup()

        self.assertTrue(first)
        self.assertFalse(second)
        self.assertEqual(calls["count"], 1)

    def test_warmup_endpoint_reports_started_flag(self) -> None:
        with patch.object(llm, "_trigger_warmup", return_value=True):
            out = llm.warmup()
        self.assertTrue(out["ok"])
        self.assertTrue(out["started"])
        self.assertEqual(out["model_id"], llm.LLM_MODEL_ID)

    def test_sanitize_strips_instruction_echo_and_transcript_noise(self) -> None:
        raw = (
            "Satwik was observed on Camera 6 around 10:30 with medium-priority alerts. "
            "Focus on the overall pattern. Do not begin with phrases like 'Found N events'. "
            "--- User: What did satwik do today? Assistant: Focus on the overall pattern. "
            "```noise```"
        )
        cleaned = llm._sanitize_answer_text(raw, "What did satwik do today?")
        self.assertIn("Satwik was observed on Camera 6", cleaned)
        self.assertNotIn("Focus on the overall pattern", cleaned)
        self.assertNotIn("--- User:", cleaned)
        self.assertNotIn("```", cleaned)

    def test_instruction_echo_from_prompt_is_rejected(self) -> None:
        raw = (
            "If the evidence does not support an answer, say you do not know or ask for clarification. "
            "Example: Today, Main was observed by Unknown #11 and Unknown #3."
        )
        cleaned = llm._sanitize_answer_text(raw, "is everything alright?")
        self.assertNotIn("If the evidence does not support", cleaned)
        self.assertTrue(llm._looks_like_instruction_echo(raw))

    def test_generate_answer_falls_back_on_instruction_echo(self) -> None:
        class FakeGenerator:
            tokenizer = None

            def __call__(self, *args, **kwargs):
                return [
                    {
                        "generated_text": (
                            "If the evidence does not support an answer, say you do not know. "
                            "Today, Main was observed by Unknown #11."
                        )
                    }
                ]

        req = llm.QueryRequest(
            question="is everything alright?",
            date="2026-06-29",
            context_events=[
                llm.ContextEvent(
                    event_id="e1",
                    severity="medium",
                    brief="Person detected on Main; visit observed.",
                    camera_name="Main",
                    created_at="2026-06-29T11:47:07Z",
                    person_name="Unknown #11",
                )
            ],
        )

        with patch.object(llm, "_ensure_generator", return_value=FakeGenerator()):
            out = llm._generate_answer(req)

        self.assertNotIn("If the evidence does not support", out.answer)
        self.assertIn("Person detected on Main", out.answer)
        self.assertEqual(out.confidence, "low")

    def test_deterministic_answer_does_not_prepend_scaffold(self) -> None:
        req = llm.QueryRequest(
            question="What did satwik do today?",
            date="2026-02-21",
            context_events=[
                llm.ContextEvent(
                    event_id="e1",
                    severity="medium",
                    brief="Satwik detected working at a desk",
                    camera_name="Camera 6",
                    created_at="2026-02-21T10:30:00Z",
                    person_name="Satwik",
                )
            ],
            deterministic_scaffold="Focus on the overall pattern.",
        )
        text = llm._deterministic_answer(req)
        self.assertIn("2026-02-21", text)
        self.assertIn("Satwik detected working at a desk", text)
        self.assertNotIn("Focus on the overall pattern", text)


if __name__ == "__main__":
    unittest.main()
