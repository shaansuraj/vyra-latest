# VYRA Ask AI RAG

This document describes the production RAG design for VYRA Ask AI and the
implementation now present in the relay.

## Current Diagnosis

- Data and chunking: the old Ask AI path only searched recent event JSON with
  keyword `LIKE` matching. There was no durable knowledge index, no document
  chunking, and no source metadata for internal docs.
- Embeddings and storage: VYRA already has an embedding worker, but Ask AI did
  not store text embeddings or track the model/dimension used per chunk.
- Retrieval strategy: retrieval was event-only and lexical. It did not combine
  document search, vector similarity, metadata filters, or reranking signals.
- Prompting and grounding: the worker prompt grounded answers in events, but it
  had no explicit internal-doc context channel and no citation requirement for
  KB answers.
- Observability and evaluation: there was no request trace linking query,
  retrieved context, answer, and user feedback.

## Relay Implementation

The relay now has authenticated KB endpoints:

- `POST /kb/index`: index one text/Markdown document.
- `POST /kb/reindex-docs`: index `application/docs/*.md` from `VYRA_ROOT`.
- `POST /kb/search`: search the KB directly.
- `GET /kb/stats`: count indexed documents/chunks.
- `POST /kb/feedback`: attach `up`, `down`, or `neutral` feedback to an Ask AI trace.

`POST /query` now retrieves:

- event evidence from `agent_events`
- KB chunks from `kb_chunks` using FTS/lexical scoring and optional vector scoring

The response remains backward compatible and adds:

- `sources`: retrieved KB chunks shown in the UI
- `trace_id`: persisted request trace for later evaluation

Embeddings are optional and fail open. When `RAG_ENABLE_EMBEDDINGS` is enabled
(default), the relay calls `RAG_EMBEDDING_URL` or the local engine endpoint
`/api/worker/fast_embedding`. If the engine or embedding worker is unavailable,
Ask AI still uses FTS and lexical search.

## Production Pgvector Schema

For large enterprise deployments, move KB storage from relay SQLite/libSQL to
PostgreSQL with pgvector:

```sql
CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE kb_documents (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL,
  source_uri text NOT NULL,
  source_type text NOT NULL DEFAULT 'markdown',
  title text NOT NULL,
  version text DEFAULT '',
  environment text DEFAULT '',
  language text DEFAULT 'en',
  access_level text DEFAULT 'internal',
  checksum text NOT NULL,
  metadata jsonb NOT NULL DEFAULT '{}',
  indexed_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, source_uri)
);

CREATE TABLE kb_chunks (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  document_id uuid NOT NULL REFERENCES kb_documents(id) ON DELETE CASCADE,
  tenant_id uuid NOT NULL,
  chunk_index integer NOT NULL,
  text text NOT NULL,
  tsv tsvector GENERATED ALWAYS AS (
    setweight(to_tsvector('english', coalesce(title, '')), 'A') ||
    setweight(to_tsvector('english', coalesce(heading, '')), 'A') ||
    setweight(to_tsvector('english', coalesce(section_path, '')), 'B') ||
    setweight(to_tsvector('english', text), 'C')
  ) STORED,
  embedding vector(768),
  embedding_model text NOT NULL,
  embedding_dim integer NOT NULL,
  title text DEFAULT '',
  heading text DEFAULT '',
  section_path text DEFAULT '',
  source_uri text DEFAULT '',
  version text DEFAULT '',
  environment text DEFAULT '',
  language text DEFAULT 'en',
  access_level text DEFAULT 'internal',
  metadata jsonb NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (document_id, chunk_index)
);

CREATE INDEX kb_chunks_tsv_gin ON kb_chunks USING gin (tsv);
CREATE INDEX kb_chunks_metadata_gin ON kb_chunks USING gin (metadata);
CREATE INDEX kb_chunks_scope_idx
  ON kb_chunks (tenant_id, version, environment, language, access_level);

CREATE INDEX kb_chunks_embedding_hnsw
  ON kb_chunks USING hnsw (embedding vector_cosine_ops)
  WITH (m = 16, ef_construction = 128);

CREATE TABLE kb_query_traces (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL,
  user_id uuid,
  question text NOT NULL,
  rewritten_query text DEFAULT '',
  filters jsonb NOT NULL DEFAULT '{}',
  retrieved_chunks jsonb NOT NULL DEFAULT '[]',
  retrieved_events jsonb NOT NULL DEFAULT '[]',
  final_prompt text,
  answer text,
  confidence text,
  latency_ms integer,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE kb_feedback (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  trace_id uuid NOT NULL REFERENCES kb_query_traces(id) ON DELETE CASCADE,
  tenant_id uuid NOT NULL,
  user_id uuid,
  rating text NOT NULL CHECK (rating IN ('up', 'down', 'neutral')),
  comment text DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now()
);
```

Use HNSW for always-on low-latency recall. For bulk static corpora, IVFFlat can
reduce memory, but it needs representative training data and query-time
`ivfflat.probes` tuning. Start with HNSW and set `hnsw.ef_search` per query
when recall needs to increase.

## Retrieval Method

1. Normalize and optionally rewrite the user question.
2. Apply metadata filters first: tenant/user, access level, version,
   environment, language, and source.
3. Run full-text search over weighted title/heading/body fields.
4. Run vector search with cosine distance over the same filtered scope.
5. Fuse candidates with reciprocal-rank fusion or weighted scoring.
6. Rerank top 30-50 candidates with a cross-encoder or small LLM reranker.
7. Pack final top 5-10 chunks with source IDs, titles, headings, and snippets.
8. Generate with a grounding prompt: answer only from provided context; say
   unknown when context is weak.

## Prompt Contract

The worker prompt must keep evidence channels separate:

- Events: time, camera, severity, person, brief.
- KB excerpts: chunk ID, document title, source URI, heading, version,
  environment, text.

The model must cite concrete event evidence for operational answers and document
titles/source URIs for KB answers. If neither channel supports the answer, it
must say it does not know.

## Evaluation Harness

Build a small labeled set from real user traffic:

- 50 event questions
- 50 documentation/procedure questions
- 25 mixed event + policy questions
- 25 negative/unknown questions

For every test case store expected source IDs and a short reference answer.
Track:

- retrieval recall@10
- reranker recall@5
- answer correctness
- citation correctness
- hallucination rate
- p50/p95 latency

Use `kb_query_traces` and `kb_feedback` to find bad cases and promote them into
the labeled set.

## Implementation Plan

Phase 1, foundation:

- Index VYRA docs through `POST /kb/reindex-docs`.
- Keep the current SQLite/libSQL KB tables for local deployments.
- Add feedback buttons in the UI wired to `/kb/feedback`.
- Review traces for low-confidence answers and missing sources.

Phase 2, retrieval quality:

- Add a dedicated text embedding model for documents instead of SigLIP text
  embeddings.
- Add a reranker worker and rerank top 30 hybrid candidates to final top 8.
- Add query rewriting for follow-up questions and acronym expansion.
- Add stricter access/version/environment filters in the UI and API.

Phase 3, production scale:

- Move KB tables to Postgres + pgvector.
- Enable row-level security by tenant and access level.
- Add HNSW indexes and tune `ef_search` from evaluation results.
- Run nightly evaluation and fail deployments that regress retrieval recall or
  hallucination rate.
