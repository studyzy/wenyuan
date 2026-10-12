# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Bank lifecycle API: create, list, replace, update, delete, aliases, and
  per-bank configuration.
- Retain pipeline: synchronous and asynchronous memory writes, file uploads,
  chunk and entity persistence, and async operation tracking.
- Recall API: multi-arm retrieval, fused re-ranking, token-budget selection,
  tag filters, and temporal windows.
- Reflect API: synthesized answers with cited evidence, execution traces,
  structured output, and directives.
- Monitoring surface: health, liveness, readiness, version, and metrics.
- PostgreSQL storage layer with embedded migrations, plus an in-memory store
  for offline development and tests.
- `bankctl` administrative CLI: bank rename and archive export/import.

[Unreleased]: https://github.com/studyzy/wenyuan/commits/main