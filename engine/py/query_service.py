"""VYRA Query Service — Natural language security queries via Ollama LLM.

FastAPI service on port 5002 that answers questions like:
  "Did someone steal something today?"
  "Was Abhishek in the office today?"

It receives pre-filtered context events from the relay and uses a local
Ollama model (text-only) to generate a concise answer.

ENV vars:
  QUERY_SERVICE_PORT   — port to listen on (default: 5002)
  OLLAMA_HOST          — Ollama base URL (default: http://localhost:11434)
  OLLAMA_MODEL         — model name to use (default: llama3.2)
  MAX_ANSWER_TOKENS    — max generation tokens (default: 400)
"""

import os
import requests
from typing import List

from fastapi import FastAPI
from pydantic import BaseModel

app = FastAPI(title="VYRA Query Service", version="2.0")

OLLAMA_HOST = os.getenv("OLLAMA_HOST", "http://localhost:11434")
OLLAMA_MODEL = os.getenv("OLLAMA_MODEL", "llama3.2")
MAX_ANSWER_TOKENS = int(os.getenv("MAX_ANSWER_TOKENS", "400"))

_TIMEOUT = 30  # seconds


# ── Models ────────────────────────────────────────────────────────────────────

class ContextEvent(BaseModel):
    event_id: str
    severity: str = ""
    brief: str = ""
    snapshot_path: str = ""
    service_id: str = ""
    person_name: str = ""
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


class QueryRequest(BaseModel):
    question: str
    date: str = ""
    context_events: List[ContextEvent] = []
    context_chunks: List[ContextChunk] = []
    known_persons: List[str] = []
    top_snapshot_path: str = ""


class QueryResponse(BaseModel):
    answer: str
    confidence: str = "medium"
    referenced_event_ids: List[str] = []
    referenced_chunk_ids: List[str] = []


# ── Ollama helpers ────────────────────────────────────────────────────────────

def _ollama_available() -> bool:
    """Check if Ollama is reachable and has the configured model."""
    try:
        r = requests.get(f"{OLLAMA_HOST}/api/tags", timeout=3)
        if r.status_code != 200:
            return False
        tags = r.json().get("models", [])
        names = [m.get("name", "") for m in tags]
        # Accept exact match or prefix match (e.g. "llama3.2" matches "llama3.2:latest")
        for name in names:
            if name == OLLAMA_MODEL or name.startswith(OLLAMA_MODEL + ":"):
                return True
        print(f"[QueryService] Ollama running but model '{OLLAMA_MODEL}' not found. Available: {names}")
        return False
    except Exception as e:
        print(f"[QueryService] Ollama not available: {e}")
        return False


def _ask_ollama(prompt: str) -> str:
    """Send a prompt to Ollama and return the text response."""
    payload = {
        "model": OLLAMA_MODEL,
        "prompt": prompt,
        "stream": False,
        "options": {
            "num_predict": MAX_ANSWER_TOKENS,
            "temperature": 0.3,
        },
    }
    r = requests.post(f"{OLLAMA_HOST}/api/generate", json=payload, timeout=_TIMEOUT)
    r.raise_for_status()
    return r.json().get("response", "").strip()


# ── Context builder ───────────────────────────────────────────────────────────

def _build_prompt(req: QueryRequest) -> str:
    lines = []

    if req.date:
        lines.append(f"You are the VYRA assistant. Today's date is {req.date}.")
    else:
        lines.append("You are the VYRA assistant.")

    lines.append("Use only the recorded events and knowledge-base excerpts below.")
    lines.append("If the context does not support the answer, say you do not know.")

    if req.known_persons:
        lines.append(f"Known registered persons: {', '.join(req.known_persons)}.")

    lines.append("")

    if not req.context_events:
        lines.append("No security events were found matching this query.")
    else:
        lines.append("Security events (most recent first):")
        for ev in req.context_events:
            t = ev.created_at[11:16] if len(ev.created_at) >= 16 else ev.created_at
            sev = f"[{ev.severity.upper()}]" if ev.severity else ""
            cam = f"Cam-{ev.service_id[:6]}" if ev.service_id else ""
            person = f" Person: {ev.person_name}." if ev.person_name else ""
            brief = ev.brief[:250] if ev.brief else "(no description)"
            lines.append(f"  {t} {cam} {sev}{person} — {brief}")

    lines.append("")
    lines.append("Knowledge-base excerpts:")
    if not req.context_chunks:
        lines.append("No KB excerpts were found matching this query.")
    else:
        for idx, chunk in enumerate(req.context_chunks[:10], start=1):
            source = chunk.title or chunk.source_uri or f"Source {idx}"
            heading = f" / {chunk.heading}" if chunk.heading else ""
            text = chunk.text[:1000] if chunk.text else ""
            lines.append(f"[KB{idx}] id={chunk.chunk_id} source={source}{heading}: {text}")

    lines.append("")
    lines.append(f"Question: {req.question}")
    lines.append("")
    lines.append("Answer concisely and directly. If you have evidence, cite the time and camera. "
                 "If using KB context, cite the document title/source. If no relevant context was found, say so clearly.")
    lines.append("Answer:")

    return "\n".join(lines)


# ── Deterministic fallback ────────────────────────────────────────────────────

def _deterministic_answer(req: QueryRequest) -> str:
    question = req.question
    events = req.context_events
    date = req.date
    if not events:
        if req.context_chunks:
            parts = [
                f"Found relevant documentation for '{question}', but no matching security events on {date or 'the selected date'}."
            ]
            for chunk in req.context_chunks[:3]:
                label = chunk.title or chunk.source_uri or "Knowledge source"
                if chunk.heading:
                    label += f" / {chunk.heading}"
                parts.append(f"{label}: {(chunk.text or '')[:220]}")
            return "\n".join(parts)
        return (
            f"No relevant security events were found for the query '{question}' "
            f"on {date or 'the selected date'}."
        )
    high = [e for e in events if e.severity == "high"]
    med = [e for e in events if e.severity == "medium"]
    parts = [f"Found {len(events)} relevant event(s) for '{question}' on {date or 'today'}."]
    if high:
        parts.append(f"High-severity incidents: {len(high)}")
        for e in high[:3]:
            t = e.created_at[11:16] if len(e.created_at) >= 16 else ""
            parts.append(f"  • {t}: {e.brief[:150]}")
    if med:
        parts.append(f"Medium-severity incidents: {len(med)}")
        for e in med[:2]:
            t = e.created_at[11:16] if len(e.created_at) >= 16 else ""
            parts.append(f"  • {t}: {e.brief[:150]}")
    return "\n".join(parts)


# ── Routes ────────────────────────────────────────────────────────────────────

@app.get("/health")
def health():
    available = _ollama_available()
    return {
        "status": "ok",
        "ollama_available": available,
        "ollama_host": OLLAMA_HOST,
        "ollama_model": OLLAMA_MODEL,
    }


@app.post("/query", response_model=QueryResponse)
def query(req: QueryRequest):
    referenced = [e.event_id for e in req.context_events if e.event_id]
    referenced_chunks = [c.chunk_id for c in req.context_chunks if c.chunk_id]

    if _ollama_available():
        try:
            prompt = _build_prompt(req)
            answer = _ask_ollama(prompt)
            confidence = "high" if (req.context_events or req.context_chunks) else "medium"
            if not answer:
                raise ValueError("Empty response from Ollama")
        except Exception as e:
            print(f"[QueryService] Ollama inference error: {e}")
            answer = _deterministic_answer(req)
            confidence = "low"
    else:
        answer = _deterministic_answer(req)
        confidence = "low"

    return QueryResponse(
        answer=answer,
        confidence=confidence,
        referenced_event_ids=referenced[:10],
        referenced_chunk_ids=referenced_chunks[:10],
    )


if __name__ == "__main__":
    import uvicorn
    port = int(os.getenv("QUERY_SERVICE_PORT", "5002"))
    uvicorn.run(app, host="0.0.0.0", port=port)
