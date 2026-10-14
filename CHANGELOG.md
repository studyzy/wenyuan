# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed — BREAKING

- **The PostgreSQL schema is now the reference schema, exactly.** The migration
  series is generated from the upstream database's own catalogue by
  `scripts/reference_schema_migrations.py` and consists of `0001_extensions`,
  `0002_reference_tables`, `0003_reference_constraints`,
  `0004_reference_indexes` and `0005_reference_functions` — 0001..0027 are
  superseded. A database built from it is indistinguishable from one built by
  upstream: `docs/SCHEMA-ALIGNMENT.md` reports no missing, extra or different
  object, and a `pg_dump --schema-only` of each side diffs clean.
- Three tables the reference does not have are gone, along with the code that
  used them: `knowledge_nodes` (the knowledge tree is `knowledge_pages`, whose
  pages are backed by a mental model that holds the body), `webhook_deliveries`
  (a delivery is an `async_operations` row of type `webhook_delivery`) and
  `observation_sources` (an observation carries its evidence in
  `memory_units.source_memory_ids`).
- `banks` no longer stores `last_write_at` / `last_document_at`. The collection
  view derives both from the newest document and the newest memory unit, and
  orders as the reference orders.
- `mental_models` no longer stores `history`; the change log is the
  `mental_model_history` table, and its key is an identity column.
- `memory_units.search_vector` is a plain column the application maintains, not
  a generated one, and every write path sets it.
- Object definitions that had the reference's name but not its shape are
  corrected: the attachment short-id and knowledge-page-name unique indexes, the
  tag and link array widths, the two history identity keys, two check
  constraints and the `bank_aliases` column order. The four maintenance
  discovery routines are the reference's bodies; the previous
  `banks_needing_consolidation` referenced columns that do not exist and, with
  its own exception handler swallowing the error, silently found no work.
- Installing an extension is serialized with the same advisory lock the series
  takes, and each migration sets its own search path, so a deployment whose
  connection pool hands out fresh connections still resolves the right schema.

## [0.1.0] - 2026-10-13

First tagged release. The public HTTP surface and the PostgreSQL schema are the
compatibility contract; this release covers the full route set the port targets.

### Added

- Bank lifecycle API: create, list, replace, update, delete, aliases, and
  per-bank configuration.
- Retain pipeline: synchronous and asynchronous memory writes, inline file
  uploads with content-addressed attachment persistence, chunk and entity
  persistence, and async operation tracking.
- Recall API: multi-arm retrieval, fused re-ranking, token-budget selection,
  tag filters, temporal windows, and real vector similarity over a pgvector
  HNSW index.
- Reflect API: a bounded, model-driven agentic loop with cited evidence,
  execution traces, structured output, and directives.
- Consolidation engine: a queue-driven pass that folds raw facts into
  observations with evidence links and marks the facts it consumed.
- Model provider boundary with an OpenAI-compatible adapter and a no-model
  selection for offline operation.
- Background queue consumer (`cmd/worker`) with atomic claim, lease and retry.
- Export / import / clone / transfer and attachment-download routes over the
  archive pipeline.
- Mental models, directives, knowledge base, observations, webhooks, audit
  logs, and the read surface.
- An MCP namespace answering JSON-RPC `initialize`, `tools/list` and
  `tools/call` over the same store the HTTP surface uses.
- Monitoring surface: health, liveness, readiness, version, and metrics.
- PostgreSQL storage layer with embedded, versioned migrations (recorded in
  `schema_migrations`) and a cross-process bank-statistics cache.
- `bankctl` administrative CLI: bank rename and archive export/import.

[Unreleased]: https://github.com/studyzy/wenyuan/compare/v0.1.0...main
[0.1.0]: https://github.com/studyzy/wenyuan/releases/tag/v0.1.0