#!/usr/bin/env python3
"""Export recent kb_query_traces into a starter RAG eval dataset."""

from __future__ import annotations

import argparse
import json
import sqlite3
from pathlib import Path
from typing import Any


TARGETS = {
    "event": 50,
    "docs": 50,
    "hybrid": 25,
    "unknown": 25,
}


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--db", default="application/data/relay.db", help="SQLite relay database path")
    parser.add_argument("--out", default="-", help="Output JSON path, or '-' for stdout")
    parser.add_argument("--limit", type=int, default=1000, help="Maximum traces to inspect")
    for category, count in TARGETS.items():
        parser.add_argument(f"--{category}", type=int, default=count, help=f"Target {category} case count")
    return parser.parse_args()


def loads_json(raw: str | None, fallback: Any) -> Any:
    if not raw:
        return fallback
    try:
        return json.loads(raw)
    except json.JSONDecodeError:
        return fallback


def string_from(item: dict[str, Any], *keys: str) -> str:
    for key in keys:
        value = item.get(key)
        if isinstance(value, str) and value.strip():
            return value.strip()
    return ""


def expected_source_ids(chunks: list[dict[str, Any]]) -> list[str]:
    out: list[str] = []
    for chunk in chunks:
        source_id = string_from(chunk, "source_uri", "SourceURI", "id", "ID", "chunk_id", "ChunkID")
        if source_id and source_id not in out:
            out.append(source_id)
    return out


def expected_event_ids(events: list[dict[str, Any]]) -> list[str]:
    out: list[str] = []
    for event in events:
        event_id = string_from(event, "id", "ID", "event_id", "EventID")
        if event_id and event_id not in out:
            out.append(event_id)
    return out


def infer_category(intent: str, sources: list[str], events: list[str], answer: str) -> str:
    intent = intent.lower().strip()
    if intent in TARGETS:
        return intent
    if not sources and not events:
        low = answer.lower()
        if "did not find" in low or "no matching" in low or "unknown" in low:
            return "unknown"
        return "unknown"
    if sources and events:
        return "hybrid"
    if sources:
        return "docs"
    return "event"


def main() -> int:
    args = parse_args()
    db_path = Path(args.db)
    if not db_path.exists():
        raise SystemExit(f"database not found: {db_path}")

    targets = {
        "event": args.event,
        "docs": args.docs,
        "hybrid": args.hybrid,
        "unknown": args.unknown,
    }
    buckets: dict[str, list[dict[str, Any]]] = {key: [] for key in TARGETS}

    conn = sqlite3.connect(str(db_path))
    conn.row_factory = sqlite3.Row
    rows = conn.execute(
        """
        SELECT id, intent, question, answer, retrieved_chunks_json, retrieved_events_json, created_at
        FROM kb_query_traces
        ORDER BY created_at DESC
        LIMIT ?
        """,
        (args.limit,),
    ).fetchall()

    for row in rows:
        chunks = loads_json(row["retrieved_chunks_json"], [])
        events = loads_json(row["retrieved_events_json"], [])
        if not isinstance(chunks, list):
            chunks = []
        if not isinstance(events, list):
            events = []
        sources = expected_source_ids([c for c in chunks if isinstance(c, dict)])
        event_ids = expected_event_ids([e for e in events if isinstance(e, dict)])
        answer = row["answer"] or ""
        category = infer_category(row["intent"] or "", sources, event_ids, answer)
        if len(buckets[category]) >= targets[category]:
            continue
        buckets[category].append(
            {
                "id": row["id"],
                "category": category,
                "question": row["question"] or "",
                "date": str(row["created_at"] or "")[:10],
                "expected_source_ids": sources,
                "expected_event_ids": event_ids,
                "reference_answer": answer,
                "must_say_unknown": category == "unknown",
            }
        )

    dataset: list[dict[str, Any]] = []
    for category in ("event", "docs", "hybrid", "unknown"):
        dataset.extend(buckets[category])

    rendered = json.dumps(dataset, indent=2, ensure_ascii=False) + "\n"
    if args.out == "-":
        print(rendered, end="")
    else:
        Path(args.out).write_text(rendered, encoding="utf-8")
    print(
        "exported "
        + ", ".join(f"{category}={len(buckets[category])}/{targets[category]}" for category in TARGETS)
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
