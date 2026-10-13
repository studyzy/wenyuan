# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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