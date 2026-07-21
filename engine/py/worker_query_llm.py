"""Local Ask-AI LLM worker.

Manual-start FastAPI service used by relay /query endpoint.
Primary target model: google/gemma-3-1b-it.
If model loading/generation fails, deterministic fallback is returned.
"""

import os
import logging
import re
import threading
import time
from typing import Any, List, Optional

from fastapi import FastAPI
from pydantic import BaseModel

APP = FastAPI(title="VYRA Ask-AI Worker", version="1.0")

LLM_MODEL_ID = os.getenv("LLM_MODEL_ID", "google/gemma-3-1b-it")
MAX_ANSWER_TOKENS = int(os.getenv("MAX_ANSWER_TOKENS", "1200"))
TEMPERATURE = float(os.getenv("LLM_TEMPERATURE", "0.2"))
MAX_NEW_TOKENS_SOFT = int(os.getenv("LLM_MAX_NEW_TOKENS_SOFT", "512"))
GENERATION_MAX_TIME_SEC = float(os.getenv("LLM_GENERATION_MAX_TIME_SEC", "10"))

_model_lock = threading.Lock()
_inflight = 0
_generator: Optional[Any] = None
_last_error = ""
_last_latency_ms = 0.0
_warmup_started = False
_warmup_lock = threading.Lock()


class ContextEvent(BaseModel):
    event_id: str
    severity: str = ""
    brief: str = ""
    snapshot_path: str = ""
    service_id: str = ""
    person_name: str = ""
    global_person_id: str = ""
    camera_name: str = ""
    camera_timestamp: str = ""
    created_at: str = ""


class ContextChunk(BaseModel):
    chunk_id: str
    title: str = ""
    source_uri: str = ""
    heading: str = ""
    section_path: str = ""
    version: str = ""
    environment: str = ""
    language: str = ""
    text: str = ""
    score: float = 0.0
    score_reason: str = ""


class ContextTimeline(BaseModel):
    id: str
    global_person_id: str = ""
    person_id: str = ""
    person_name: str = ""
    identity_source: str = ""
    identity_confidence: float = 0.0
    service_id: str = ""
    camera_name: str = ""
    event_id: str = ""
    track_id: int = -1
    snapshot_path: str = ""
    activity_brief: str = ""
    event_kind: str = ""
    severity: str = ""
    created_at: str = ""


class QueryRequest(BaseModel):
    question: str
    date: str = ""
    context_events: List[ContextEvent] = []
    context_timeline: List[ContextTimeline] = []
    context_chunks: List[ContextChunk] = []
    known_persons: List[str] = []
    top_snapshot_path: str = ""
    deterministic_scaffold: str = ""
    chat_history: List[dict] = []


class QueryResponse(BaseModel):
    answer: str
    confidence: str = "medium"
    referenced_event_ids: List[str] = []
    referenced_chunk_ids: List[str] = []


def _safe_time_fragment(ts: str) -> str:
    ts = (ts or "").strip()
    if len(ts) >= 16:
        return ts[11:16]
    return ""


def _truncate(s: str, n: int) -> str:
    s = (s or "").strip()
    if len(s) <= n:
        return s
    return s[: max(0, n - 1)].rstrip() + "…"


def _wants_hindi(question: str) -> bool:
    q = (question or "").strip().lower()
    if not q:
        return False
    if "hindi" in q or "in hindi" in q or "हिंदी" in question or "हिन्दी" in question:
        return True
    return False


def _is_unknown_person_name(name: str) -> bool:
    n = (name or "").strip().lower()
    return n == "unknown" or n.startswith("unknown #") or n.startswith("unknown:")


def _timeline_person_label(moment: ContextTimeline) -> str:
    name = (moment.person_name or "").strip()
    if name:
        return name
    gid = (moment.global_person_id or "").strip()
    if gid:
        lower = gid.lower()
        if "unknown" in lower:
            parts = re.split(r"[:/#\s]+", gid)
            for part in reversed(parts):
                if part.isdigit():
                    return f"Unknown #{part}"
            return "Unknown person"
        return gid
    if moment.person_id:
        return f"Person {moment.person_id}"
    return "Unknown person"


def _timeline_summary(req: QueryRequest) -> str:
    timeline = req.context_timeline
    q = (req.question or "").strip()
    if not timeline:
        return ""

    cameras: List[str] = []
    seen_cameras = set()
    people: List[str] = []
    seen_people = set()
    times: List[str] = []
    high = 0
    medium = 0

    for moment in timeline:
        sev = (moment.severity or "").strip().lower()
        if sev == "high":
            high += 1
        elif sev == "medium":
            medium += 1

        cam = (moment.camera_name or moment.service_id or "").strip()
        if cam and cam not in seen_cameras:
            seen_cameras.add(cam)
            cameras.append(cam)

        person = _timeline_person_label(moment)
        if person and person not in seen_people:
            seen_people.add(person)
            people.append(person)

        t = _safe_time_fragment(moment.created_at or "")
        if t:
            times.append(t)

    if len(times) >= 2:
        time_range = f"between {min(times)} and {max(times)}"
    elif len(times) == 1:
        time_range = f"around {times[0]}"
    else:
        time_range = "during the selected time window"

    pieces = [
        f"On {req.date or 'the selected date'}, I found {len(timeline)} person timeline "
        f"moment{'s' if len(timeline) != 1 else ''} related to '{q}' {time_range}."
    ]
    if cameras:
        pieces.append("Cameras involved: " + ", ".join(cameras[:4]) + ("." if len(cameras) <= 4 else ", and others."))
    if people:
        pieces.append("People seen: " + ", ".join(people[:6]) + ("." if len(people) <= 6 else ", and others."))
    if high or medium:
        pieces.append(f"Severity mix: {high} high and {medium} medium linked moments.")
    else:
        pieces.append("No high-severity alert appears in the matching timeline evidence.")
    if req.context_chunks:
        sources = []
        for chunk in req.context_chunks[:3]:
            label = (chunk.title or chunk.source_uri or "Knowledge source").strip()
            if chunk.heading:
                label = f"{label} / {chunk.heading}"
            sources.append(label)
        if sources:
            pieces.append("Relevant documentation sources: " + "; ".join(sources) + ".")
    return " ".join(pieces)


def _sanitize_answer_text(text: str, question: str) -> str:
    out = (text or "").replace("\r", "").strip()
    if not out:
        return ""

    # Remove template/control artifacts.
    out = re.sub(r"<\|[^>]*\|>", " ", out)
    out = re.sub(r"</?s>", " ", out)

    # Strip fenced blocks (and any trailing garbage after an opening fence).
    if "```" in out:
        out = out.split("```", 1)[0]
    out = out.strip()

    # Strip leaked chat transcript suffixes if the model started echoing prompt context.
    out = re.sub(r"(?is)\s*---\s*user:.*$", "", out)
    out = re.sub(r"(?is)\s*---\s*assistant:.*$", "", out)

    out = re.sub(r"^\s*answer\s*:\s*", "", out, flags=re.IGNORECASE)
    out = re.sub(r"(?i)\banswer\s*:\s*", " ", out)

    # Drop instruction fragments even if they appear mid-line.
    out = re.sub(
        r"(?i)you are vyra[^.]*\.?",
        " ",
        out,
    )
    out = re.sub(
        r"(?i)answer using only[^.]*\.?",
        " ",
        out,
    )
    out = re.sub(
        r"(?i)if the evidence does not support[^.]*\.?",
        " ",
        out,
    )
    out = re.sub(
        r"(?i)do not invent[^.]*\.?",
        " ",
        out,
    )
    out = re.sub(
        r"(?i)write like a human operator update[^.]*\.?",
        " ",
        out,
    )
    out = re.sub(
        r"(?i)include concrete evidence[^.]*\.?",
        " ",
        out,
    )
    out = re.sub(
        r"(?i)reply in english unless[^.]*\.?",
        " ",
        out,
    )
    out = re.sub(
        r"(?i)reply in hindi because[^.]*\.?",
        " ",
        out,
    )
    out = re.sub(
        r"(?i)focus on the overall pattern\.?",
        " ",
        out,
    )
    out = re.sub(
        r"(?i)do not begin with phrases like[^.]*\.?",
        " ",
        out,
    )
    out = re.sub(
        r"(?i)keep the answer concise[^.]*\.?",
        " ",
        out,
    )
    out = re.sub(
        r"(?i)never emit code fences[^.]*\.?",
        " ",
        out,
    )
    out = re.sub(
        r"(?i)do not include any code fences[^.]*\.?",
        " ",
        out,
    )
    out = re.sub(
        r"(?i)do not include code fences[^.]*\.?",
        " ",
        out,
    )
    out = re.sub(
        r"(?i)do not list every event line-by-line[^.]*\.?",
        " ",
        out,
    )
    out = re.sub(
        r"(?i)avoid listing every event line-by-line[^.]*\.?",
        " ",
        out,
    )
    out = re.sub(
        r"(?i)additional notes\s*:\s*",
        " ",
        out,
    )
    out = re.sub(
        r"(?i)do not output code fences or xml[^.]*\.?",
        " ",
        out,
    )
    out = re.sub(
        r"(?i)do not repeat or restate prompt instructions[^.]*\.?",
        " ",
        out,
    )
    out = re.sub(
        r"(?i)do not repeat the prompt[^.]*\.?",
        " ",
        out,
    )
    out = re.sub(
        r"(?i)respond in english only[^.]*\.?",
        " ",
        out,
    )
    out = re.sub(
        r"(?i)return only the final operator-facing answer[^.]*\.?",
        " ",
        out,
    )
    out = re.sub(
        r"(?i)date context\s*:\s*\d{4}-\d{2}-\d{2}\.?",
        " ",
        out,
    )

    # Remove meta-instruction/prompt-echo lines.
    bad_line_patterns = [
        r"(?i)^focus on the overall pattern",
        r"(?i)^you are vyra",
        r"(?i)^answer using only",
        r"(?i)^if the evidence does not support",
        r"(?i)^do not invent",
        r"(?i)^write like a human operator update",
        r"(?i)^include concrete evidence",
        r"(?i)^do not begin with phrases like",
        r"(?i)^explain what happened",
        r"(?i)^keep the answer concise",
        r"(?i)^never emit code fences",
        r"(?i)^do not include any code fences",
        r"(?i)^do not include code fences",
        r"(?i)^do not output code fences or xml",
        r"(?i)^do not repeat or restate prompt instructions",
        r"(?i)^do not repeat the prompt",
        r"(?i)^do not list every event line-by-line",
        r"(?i)^avoid listing every event line-by-line",
        r"(?i)^respond in english only",
        r"(?i)^reply in english unless",
        r"(?i)^respond in hindi",
        r"(?i)^reply in hindi because",
        r"(?i)^additional notes",
        r"(?i)^return only the final operator-facing answer",
        r"(?i)^date context:",
        r"(?i)^recent conversation context",
        r"(?i)^known persons:",
        r"(?i)^question:",
        r"(?i)^user question:",
        r"(?i)^events:",
        r"(?i)^knowledge-base excerpts:",
        r"(?i)^---\s*user:",
        r"(?i)^---\s*assistant:",
        r"(?i)^user:\s*",
        r"(?i)^assistant:\s*",
    ]
    cleaned = []
    for line in out.split("\n"):
        s = line.strip()
        if not s:
            continue
        if any(re.search(p, s) for p in bad_line_patterns):
            continue
        cleaned.append(s)
    out = " ".join(cleaned).strip()

    if not _wants_hindi(question):
        # Drop obvious non-English noise lines unless user asked for Hindi.
        cleaned_lines: List[str] = []
        for line in out.split("\n"):
            s = line.strip()
            if not s:
                continue
            if re.search(r"[\u0900-\u097F\u0980-\u0D7F]", s):
                continue
            cleaned_lines.append(s)
        out = " ".join(cleaned_lines)

    out = re.sub(r"(?:\s*\*\s*){2,}", " ", out)
    out = re.sub(r"\s+", " ", out).strip()
    return _truncate(out, 6000)


def _looks_like_instruction_echo(text: str) -> bool:
    t = (text or "").strip().lower()
    if not t:
        return True
    hard_markers = [
        "you are vyra",
        "on-site safety analyst",
        "answer using only",
        "if the evidence does not support",
        "do not invent",
        "write like a human operator update",
        "include concrete evidence",
        "focus on the overall pattern",
        "do not begin with phrases like",
        "keep the answer concise",
        "never emit code fences",
        "do not include any code fences",
        "do not output code fences or xml",
        "do not repeat or restate prompt instructions",
        "do not repeat the prompt",
        "do not list every event line-by-line",
        "avoid listing every event line-by-line",
        "return only the final operator-facing answer",
        "date context:",
        "known persons:",
        "user question:",
        "knowledge-base excerpts:",
        "--- user:",
        "--- assistant:",
    ]
    if any(m in t for m in hard_markers):
        return True
    markers = [
        "respond in english only",
        "respond in hindi",
        "recent conversation context",
        "additional notes:",
        "user:",
        "assistant:",
    ]
    hits = sum(1 for m in markers if m in t)
    return hits >= 2


def _narrative_summary(req: QueryRequest) -> str:
    events = req.context_events
    timeline = req.context_timeline
    chunks = req.context_chunks
    q = (req.question or "").strip()
    ql = q.lower()
    if timeline and not events:
        return _timeline_summary(req)
    if not events:
        if chunks:
            lead = (
                f"I found relevant VYRA documentation for '{q}', but no matching recorded events "
                f"on {req.date or 'the selected date'}."
            )
            evidence = []
            for chunk in chunks[:3]:
                label = (chunk.title or chunk.source_uri or "Knowledge source").strip()
                if chunk.heading:
                    label = f"{label} / {chunk.heading}"
                snippet = _truncate(chunk.text, 220)
                if snippet:
                    evidence.append(f"{label}: {snippet}")
            if evidence:
                return lead + " " + " ".join(evidence)
            return lead
        target_person = ""
        for kp in req.known_persons:
            name = (kp or "").strip()
            if name and name.lower() in ql:
                target_person = name
                break
        if target_person:
            return (
                f"I couldn't find evidence that {target_person} appeared on "
                f"{req.date or 'the selected date'}."
            )
        if any(k in ql for k in ["everything", "alright", "all right", "okay", "all good", "fine"]):
            return (
                f"I don't see any incident evidence for {req.date or 'the selected date'}. "
                "Everything appears normal from the available events."
            )
        return (
            f"I did not find meaningful activity related to '{req.question}' on "
            f"{req.date or 'the selected date'}."
        )

    cameras: List[str] = []
    seen_cameras = set()
    people: List[str] = []
    seen_people = set()
    highlights: List[str] = []
    seen_highlights = set()
    high = 0
    medium = 0
    low = 0
    times: List[str] = []

    for ev in events:
        sev = (ev.severity or "").strip().lower()
        if sev == "high":
            high += 1
        elif sev == "medium":
            medium += 1
        elif sev:
            low += 1

        cam = (ev.camera_name or ev.service_id or "").strip()
        if cam and cam not in seen_cameras:
            seen_cameras.add(cam)
            cameras.append(cam)

        name = (ev.person_name or "").strip()
        if name and not _is_unknown_person_name(name) and name not in seen_people:
            seen_people.add(name)
            people.append(name)

        t = _safe_time_fragment(ev.created_at or ev.camera_timestamp or "")
        if t:
            times.append(t)

        brief = _truncate(ev.brief, 150)
        if brief and brief not in seen_highlights:
            seen_highlights.add(brief)
            highlights.append(brief)

    if len(times) >= 2:
        time_range = f"between {min(times)} and {max(times)}"
    elif len(times) == 1:
        time_range = f"around {times[0]}"
    else:
        time_range = "during the selected time window"

    target_person = ""
    for kp in req.known_persons:
        name = (kp or "").strip()
        if name and name.lower() in ql:
            target_person = name
            break

    if not target_person:
        m = re.search(r"\bwhat did ([a-z][a-z .'-]{1,50}) do\b", ql)
        if m:
            target_person = m.group(1).strip().title()

    if target_person:
        person_events = [
            ev for ev in events
            if (ev.person_name or "").strip().lower() == target_person.lower()
        ]
        if not person_events:
            for ev in events:
                brief = (ev.brief or "").lower()
                if target_person.lower() in brief:
                    person_events.append(ev)
        if person_events:
            person_cams: List[str] = []
            seen_person_cams = set()
            person_times: List[str] = []
            person_highlights: List[str] = []
            for ev in person_events:
                cam = (ev.camera_name or ev.service_id or "").strip()
                if cam and cam not in seen_person_cams:
                    seen_person_cams.add(cam)
                    person_cams.append(cam)
                t = _safe_time_fragment(ev.created_at or ev.camera_timestamp or "")
                if t:
                    person_times.append(t)
                brief = _truncate(ev.brief, 110)
                if brief and brief not in person_highlights:
                    person_highlights.append(brief)
            if len(person_times) >= 2:
                person_range = f"between {min(person_times)} and {max(person_times)}"
            elif len(person_times) == 1:
                person_range = f"around {person_times[0]}"
            else:
                person_range = "during the selected time window"

            cam_phrase = ", ".join(person_cams[:3]) if person_cams else "multiple cameras"
            lead = (
                f"I can see {target_person} was active on {req.date or 'the selected date'} "
                f"{person_range}, mainly on {cam_phrase}."
            )
            if person_highlights:
                lead += f" From the evidence: {person_highlights[0]}."
            return lead

    issue_events = []
    for ev in events:
        sev = (ev.severity or "").strip().lower()
        brief = (ev.brief or "").strip().lower()
        if sev in ("high", "medium"):
            issue_events.append(ev)
            continue
        if "violation" in brief or "without helmet" in brief or "without vest" in brief or "sneak" in brief:
            issue_events.append(ev)

    safety_question = any(
        k in ql for k in ("everything", "alright", "all right", "okay", "all good", "fine", "issue")
    )
    if safety_question:
        if not issue_events:
            msg = (
                f"Yes, everything looks fine to me on {req.date or 'the selected date'}. "
                f"I reviewed {len(events)} evidence item{'s' if len(events) != 1 else ''} "
                f"{time_range}"
            )
            if cameras:
                msg += f" across {', '.join(cameras[:3])}"
                if len(cameras) > 3:
                    msg += " and other cameras"
            msg += "."
            return msg

        top_issue = issue_events[0]
        top_cam = (top_issue.camera_name or top_issue.service_id or "an available camera").strip()
        top_time = _safe_time_fragment(top_issue.created_at or top_issue.camera_timestamp or "")
        top_brief = _truncate(top_issue.brief or "an incident was detected", 140)
        when = f" at {top_time}" if top_time else ""
        return (
            "I think there is an issue. According to the evidence, "
            f"{top_brief} on {top_cam}{when} "
            f"({len(issue_events)} concern event{'s' if len(issue_events) != 1 else ''} in total)."
        )

    pieces: List[str] = []
    pieces.append(
        f"On {req.date or 'the selected date'}, I found {len(events)} relevant event"
        f"{'s' if len(events) != 1 else ''} {time_range}."
    )
    if cameras:
        pieces.append(
            "Cameras involved: " + ", ".join(cameras[:4]) + ("." if len(cameras) <= 4 else ", and others.")
        )
    if people:
        pieces.append(
            "People seen: " + ", ".join(people[:4]) + ("." if len(people) <= 4 else ", and others.")
        )
    if high > 0:
        pieces.append(
            f"I see {high} high-severity and {medium} medium-severity events, so this period needs attention."
        )
    elif medium > 0:
        pieces.append(
            f"I see {medium} medium-severity events and no high-severity incidents."
        )
    else:
        pieces.append("I don't see high- or medium-severity incidents in this set.")

    if highlights:
        pieces.append(f"Top evidence: {highlights[0]}.")
    return " ".join(pieces)


def _deterministic_answer(req: QueryRequest) -> str:
    base = _narrative_summary(req)
    return base


def _build_prompt(req: QueryRequest) -> str:
    wants_hindi = _wants_hindi(req.question)
    lines: List[str] = [
        "You are VYRA, an on-site safety analyst and internal-docs assistant helping an operator.",
        "Answer using only the event evidence and knowledge-base excerpts provided below.",
        "If the evidence does not support an answer, say you do not know or ask for clarification.",
        "Do not invent cameras, people, policies, versions, or procedures that are not in the provided context.",
        "Write like a human operator update: clear, concise, and specific.",
        "Include concrete evidence: time and camera for events; document title/source for KB excerpts.",
    ]
    if wants_hindi:
        lines.append("Reply in Hindi because the user explicitly requested Hindi.")
    else:
        lines.append("Reply in English unless the user explicitly asks for Hindi.")
    if req.date:
        lines.append(f"Date context: {req.date}.")
    if req.deterministic_scaffold:
        lines.append(req.deterministic_scaffold)
    if req.known_persons:
        lines.append("Known persons: " + ", ".join(req.known_persons[:50]))
    if req.chat_history:
        lines.append("")
        lines.append("Recent chat context:")
        for turn in req.chat_history[-8:]:
            if not isinstance(turn, dict):
                continue
            role = str(turn.get("role", "")).strip().lower()
            content = str(turn.get("content", "")).strip()
            if not content:
                continue
            if role != "user":
                continue
            lines.append(f"- {_truncate(content, 240)}")
    lines.append("")
    lines.append("Person timeline moments:")
    if not req.context_timeline:
        lines.append("No timeline moments available.")
    else:
        for moment in req.context_timeline[:30]:
            cam = moment.camera_name or moment.service_id
            person = _timeline_person_label(moment)
            sev = f"[{moment.severity.upper()}]" if moment.severity else ""
            conf = f" confidence={moment.identity_confidence:.2f}" if moment.identity_confidence else ""
            brief = moment.activity_brief or moment.event_kind or "person observed"
            lines.append(
                f"- {moment.created_at} {cam} {sev} person={person}"
                f" global_id={moment.global_person_id}{conf}: {brief}"
            )
    lines.append("")
    lines.append("Events:")
    if not req.context_events:
        lines.append("No events available.")
    else:
        for ev in req.context_events[:30]:
            cam = ev.camera_name or ev.service_id
            person = f" person={ev.person_name}" if ev.person_name else ""
            sev = f"[{ev.severity.upper()}]" if ev.severity else ""
            lines.append(
                f"- {ev.created_at} {cam} {sev}{person}: {ev.brief or '(no description)'}"
            )
    lines.append("")
    lines.append("Knowledge-base excerpts:")
    if not req.context_chunks:
        lines.append("No KB excerpts available.")
    else:
        for idx, chunk in enumerate(req.context_chunks[:10], start=1):
            source = chunk.title or chunk.source_uri or f"Source {idx}"
            heading = f" / {chunk.heading}" if chunk.heading else ""
            version = f" version={chunk.version}" if chunk.version else ""
            env = f" environment={chunk.environment}" if chunk.environment else ""
            lines.append(
                f"[KB{idx}] id={chunk.chunk_id} source={source}{heading}{version}{env} "
                f"score={chunk.score:.3f}: {_truncate(chunk.text, 1200)}"
            )
    lines.append("")
    lines.append(f"User question: {req.question}")
    lines.append(
        "If asked whether everything is alright, give a clear yes/no-style assessment and cite evidence. "
        "If asked about a person, summarize what they did and where."
        " If the answer comes from KB excerpts, cite the document title or source."
    )
    return "\n".join(lines)


def _resolve_generation_limits(gen: Any, prompt: str) -> int:
    requested = max(1, int(MAX_ANSWER_TOKENS))
    if MAX_NEW_TOKENS_SOFT > 0:
        requested = min(requested, MAX_NEW_TOKENS_SOFT)

    tokenizer = getattr(gen, "tokenizer", None)
    prompt_tokens = 0
    if tokenizer is not None:
        try:
            prompt_tokens = len(tokenizer.encode(prompt, add_special_tokens=False))
        except Exception:
            prompt_tokens = 0

    if tokenizer is None:
        return requested

    # Try to keep requested cap while respecting tokenizer/model hard limits.
    model_max = getattr(tokenizer, "model_max_length", None)
    if not isinstance(model_max, int):
        return requested

    # Ignore sentinel "infinite" values used by tokenizers.
    if model_max <= 0 or model_max > 10_000_000:
        return requested

    budget = model_max - prompt_tokens - 1
    if budget <= 0:
        clipped_new = 32
    else:
        clipped_new = max(32, min(requested, budget))
    return clipped_new


def _ensure_generator() -> Optional[Any]:
    global _generator, _last_error
    if _generator is not None:
        return _generator
    with _model_lock:
        if _generator is not None:
            return _generator
        try:
            from transformers import pipeline  # type: ignore

            _generator = pipeline(
                "text-generation",
                model=LLM_MODEL_ID,
                tokenizer=LLM_MODEL_ID,
                device_map="auto",
            )
            # Prevent transformers warning about both max_length and max_new_tokens.
            try:
                model = getattr(_generator, "model", None)
                gen_cfg = getattr(model, "generation_config", None)
                if gen_cfg is not None:
                    setattr(gen_cfg, "max_length", None)
                    setattr(gen_cfg, "max_new_tokens", None)
            except Exception:
                pass
            _last_error = ""
            print(f"[worker_query_llm] Loaded model: {LLM_MODEL_ID}")
            return _generator
        except Exception as exc:
            _last_error = f"Model load failed: {exc}"
            print(f"[worker_query_llm] {_last_error}")
            return None


def _trigger_warmup() -> bool:
    """Start model warmup in a background thread (idempotent)."""
    global _warmup_started
    with _warmup_lock:
        if _warmup_started:
            return False
        _warmup_started = True

    def _runner() -> None:
        _ensure_generator()

    t = threading.Thread(target=_runner, daemon=True, name="llm-warmup")
    t.start()
    return True


def _generate_answer(req: QueryRequest) -> QueryResponse:
    global _last_error, _last_latency_ms
    referenced = [e.event_id for e in req.context_events if e.event_id][:10]
    referenced_chunks = [c.chunk_id for c in req.context_chunks if c.chunk_id][:10]

    gen = _ensure_generator()
    if gen is None:
        return QueryResponse(
            answer=_deterministic_answer(req),
            confidence="low",
            referenced_event_ids=referenced,
            referenced_chunk_ids=referenced_chunks,
        )

    prompt = _build_prompt(req)
    t0 = time.time()
    try:
        max_new_tokens = _resolve_generation_limits(gen, prompt)
        eos_token_id = getattr(getattr(gen, "tokenizer", None), "eos_token_id", None)
        max_time = max(1.0, GENERATION_MAX_TIME_SEC)

        out = gen(
            prompt,
            max_new_tokens=max_new_tokens,
            do_sample=True,
            temperature=TEMPERATURE,
            top_p=0.9,
            max_time=max_time,
            eos_token_id=eos_token_id,
            return_full_text=False,
        )
        _last_latency_ms = (time.time() - t0) * 1000.0
        text = ""
        if isinstance(out, list) and out:
            first = out[0]
            if isinstance(first, dict):
                text = str(first.get("generated_text") or "").strip()
            else:
                text = str(first).strip()
        if text.startswith(prompt):
            text = text[len(prompt):].strip()
        if _looks_like_instruction_echo(text):
            raise ValueError("instruction-echo output")
        text = _sanitize_answer_text(text, req.question)
        if _looks_like_instruction_echo(text):
            raise ValueError("instruction-echo output")
        if not text:
            raise ValueError("empty response")
        _last_error = ""
        return QueryResponse(
            answer=text,
            confidence="high" if (req.context_events or req.context_chunks) else "medium",
            referenced_event_ids=referenced,
            referenced_chunk_ids=referenced_chunks,
        )
    except Exception as exc:
        _last_error = f"Inference failed: {exc}"
        print(f"[worker_query_llm] {_last_error}")
        return QueryResponse(
            answer=_deterministic_answer(req),
            confidence="low",
            referenced_event_ids=referenced,
            referenced_chunk_ids=referenced_chunks,
        )


@APP.get("/health")
def health():
    ready = _generator is not None
    return {
        "status": "ok",
        "ready": ready,
        "model_id": LLM_MODEL_ID,
        "queue_depth": _inflight,
        "last_error": _last_error,
        "latency_ms": round(_last_latency_ms, 1),
        "warmup_started": _warmup_started,
    }


@APP.post("/warmup")
def warmup():
    started = _trigger_warmup()
    return {
        "ok": True,
        "started": started,
        "ready": _generator is not None,
        "model_id": LLM_MODEL_ID,
    }


@APP.post("/query", response_model=QueryResponse)
def query(req: QueryRequest):
    global _inflight
    _inflight += 1
    try:
        return _generate_answer(req)
    finally:
        _inflight = max(0, _inflight - 1)


class _DropHealthAccessLogs(logging.Filter):
    def filter(self, record: logging.LogRecord) -> bool:
        try:
            msg = record.getMessage()
        except Exception:
            return True
        if "GET /health" in msg:
            return False
        if "POST /warmup" in msg or "GET /warmup" in msg:
            return False
        return True


if __name__ == "__main__":
    import uvicorn

    if os.getenv("LLM_WARMUP_ON_START", "1").strip().lower() in ("1", "true", "yes", "on"):
        _trigger_warmup()

    logging.getLogger("uvicorn.access").addFilter(_DropHealthAccessLogs())

    port = int(os.getenv("LLM_WORKER_PORT", "5003"))
    uvicorn.run(APP, host="0.0.0.0", port=port, access_log=False)
