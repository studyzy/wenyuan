# Wenyuan — Outstanding Work and Known Limitations (Handover Document)

> **⚠️ Partially superseded (2026-10-13).** This document was written at an
> earlier baseline. Its §1 ("Unfinished API capabilities") and the §2 schema
> comparison predate a large body of work: the route surface is now **99/99
> complete**, vector search, the provider layer, the background worker, the
> export/import endpoints, MCP, and the extraction/reflect wiring all exist.
> **`docs/GAPS.md` is the authoritative current-state document** — its §7 lists
> what actually remains. The §3 contract-item list and §4 engineering notes
> below are still broadly useful, but check any "missing" claim against the
> code before acting on it.
>
> Baseline at time of writing: 277 tests green, and `go build` / `go vet` /
> `gofmt` / `go build -tags pgx` all pass; CI (`.github/workflows/ci.yml`) runs
> the same four checks on push and PR.

---

## 0. The one-sentence status

Wenyuan is a Go service that is **compatible with the Hindsight HTTP API**:
**the public HTTP contract surface (routes, JSON fields, status codes, request
headers) is implemented and covered by tests**; **the storage layer is not
compatible with upstream Hindsight** (you cannot point it at an existing
Hindsight database), so instead it offers a **path to migrate data through the
official export format**.

So "drop-in replacement" means:
- ✅ Clients integrate without changing any code.
- ✅ Existing Hindsight memories can be imported via `bankctl bank migrate`.
- ❌ You cannot point Wenyuan directly at Hindsight's PostgreSQL database.

---

## 1. Unfinished API capabilities

### 1.1 Semantic retrieval (vectors) is a placeholder — Priority: high

**Current state**: the semantic / keyword scoring in recall is a deterministic
placeholder algorithm, **not real vector similarity**.

- Evidence: `internal/storage/memory_recall_arms.go:167-169`
  ```go
  // semanticScore is a deterministic stand-in for vector similarity: shared word ...
  func semanticScore(m RecallMatch, term string) float64
  ```
- `keywordScore` at `:187` in the same file is likewise a placeholder.
- There is **no vector column anywhere** in the database: the entire
  `migrations/` series has no `vector(N)`; the `embedding` column formerly
  listed in `internal/storage/schema.go` was a ghost column and has been removed
  in this round of fixes.

**Impact**: recall ranking quality differs materially from upstream Hindsight;
queries that are "semantically close but differently worded" will miss results.

**Recommended implementation path**:
1. Add migration `0007_memory_unit_embedding.sql` introducing
   `embedding vector(384)` (the dimension must match the deployment; upstream
   uses 384 — see `DefaultVectorExtension` in `config.go`).
2. `CREATE EXTENSION IF NOT EXISTS vector` plus an HNSW/IVFFlat index.
3. Generate embeddings on the retain write path (requires wiring in an
   embedding provider; the project currently has **no LLM/provider integration
   at all** — see §1.4).
4. Replace the placeholder scoring with a `<=>` distance query in the semantic
   arm of `internal/storage/postgres_recall.go`.

**Note**: this requires introducing external model calls and is **the single
largest missing capability** in the project.

### 1.2 Async operations are not truly background execution — Priority: medium

**Current state**: `async=true` on `POST .../memories` **accepts the request and
returns an operation record**, but no background worker actually consumes it,
and there is no separate worker process entrypoint.

- Evidence: no `go func` / goroutine dispatch in
  `internal/memoryops/retain.go`; `cmd/` contains only `server` and `bankctl`,
  with no worker entrypoint.
- Upstream Hindsight has a separate worker service (consuming the PG task queue).

**Impact**: the async write "completion signal" works, but the actual processing
completes synchronously (semantically equivalent today; there is simply no real
async queue or retry scheduling).

**Recommendation**: add `cmd/worker` implementing a claim → process →
complete/retry loop over the `async_operations` table, reusing
`AdvanceOperation` / `ListOperations` from `storage`.

### 1.3 Export / import endpoints are not implemented — Priority: medium

**Current state**: Wenyuan has **no** `/v1/default/banks/{id}/export` or
`/import` endpoint (these two endpoints were also not included in this slice's
`public_surface` in the spec).

- Evidence: no export/import routes in `internal/memoryops/routes.go` or
  `internal/bankapi/routes.go`.
- However, **the migration tooling already implements parsing and importing of
  the upstream export format** (`internal/hindsightarchive` +
  `internal/migration/hindsight`), so adding the endpoints is mostly a matter of
  wiring it into the HTTP layer.

**Recommendation**: add `POST /v1/default/banks/{id}/export` (producing a ZIP in
the same format) and `/import` (consuming a ZIP), reusing the existing
parsing/mapping code.

### 1.4 No LLM / provider integration — Priority: high (coupled with §1.1)

**Current state**: the reflect "synthesized answer" and the retain
"entity extraction / fact extraction" are both **deterministic local
implementations** that call no real model.

- Evidence: `internal/storage/memory_reflect.go` (synthesis logic is local),
  `internal/memoryops/reflect_synthesis.go`.
- Upstream Hindsight reaches multiple LLM vendors through a provider abstraction.

**Impact**: this is where reflect answer quality and retain extraction quality
diverge most from upstream.

**Recommendation**: define a `Provider` interface (completion / embedding),
integrate one vendor first (e.g. an OpenAI-compatible endpoint), then route
retain/reflect through that interface; add provider configuration to
`internal/config`.

### 1.5 MCP transport is not implemented — Priority: low

**Current state**: the `/mcp` namespace is mounted but returns 422
"not implemented".
- Evidence: `internal/httpx/router.go:123`.

### 1.6 `files/retain` multipart upload — Priority: low (partially implemented)

**Current state**: the route is bound, and async semantics plus per-request
field rejection are implemented (`internal/memoryops/file_retain.go`), but the
attachment content persistence/storage path is incomplete (upstream has
`file_storage` / `attachments` tables).

---

## 2. Storage layer: differences from upstream Hindsight (no reuse of an existing DB)

**Conclusion: Wenyuan's schema differs from Hindsight's; you cannot point it
directly at an existing Hindsight database.**

Verified differences (these are **design facts**, not bugs):

| Dimension | Hindsight | Wenyuan |
|---|---|---|
| Migration system | 112 Alembic migrations + `alembic_version` table | 6 hand-written SQL files, **no version table**, re-run on every startup (idempotent via `IF NOT EXISTS`) |
| `banks` primary key | `bank_id` | `id` |
| `banks` profile columns | `disposition JSONB`, `mission`, `config`, `internal_id` | `name`, `disposition_skepticism/literalism/empathy`, `reflect_mission` |
| `memory_units` | includes `embedding vector(384)`, `context`, `event_date`, `fact_type`, `confidence_score`, `access_count` | `type` (not `fact_type`), `chunk_id`, `tags`, `source_fact_ids`; `context` / `event_date` / `confidence_score` / `access_count` have since been added by `0006` |
| `entities` name column | `canonical_name` | `name` |
| Vectors | `vector(384)` + HNSW/IVFFlat | none |

### 2.1 Migration tooling (already implemented) — how to use it

```sh
# 1) Export the whole database from Hindsight
curl -X POST http://<hindsight>:8888/v1/default/banks/<bank_id>/export -o bank.zip

# 2) Import into Wenyuan
bankctl bank migrate --archive bank.zip --replace          # idempotent rebuild (default)
bankctl bank migrate --archive bank.zip --append           # append/merge
bankctl bank migrate --archive bank.zip --dry-run          # report only, write nothing
bankctl bank migrate --archive bank.zip --bank-id TARGET   # target bank id
```

Code locations:
- Archive parsing: `internal/hindsightarchive/` (`archive.go` types, `zip.go` extraction)
- Mapping and import: `internal/migration/hindsight/` (`map.go` mapping, `run.go` import)
- CLI: `internal/bankadmin/migrate.go` (parsing), `cmd/bankctl/main.go` (dispatch)

### 2.2 Known limitations of the migration tooling — Priority: medium

1. **Depends on the upstream export format**: migration reads a ZIP at Hindsight
   `SCHEMA_VERSION = 1`. If upstream bumps the format version,
   `SupportedSchemaVersion` at `internal/hindsightarchive/archive.go:24` must be
   updated in step, along with the types.
2. **Entity IDs are derived**: `entityID()` in `map.go` generates the entity id
   as a hash of `bank_id + name` (because the export carries the entity **name**
   only, not its id). If a bank contains distinct entities that share a name,
   they will be merged.
3. **Relation targets are located by index**: `CausalRelation.TargetFactIndex`
   is the index of the fact within the document (upstream is designed this way);
   import maps by that index, and if upstream changes the semantics this must
   follow.
4. **No incremental sync**: migration is snapshot-style. If the old instance
   keeps writing during the migration, the new data is lost.
   **Recommendation: stop writes to the old instance after exporting, then run
   the migration.**
5. **Unmapped fields**: at the end of a migration a list of "fields with no
   target column" is printed (see `unmappedFields()` in `map.go`). It currently
   includes `source_id`, `observation_scopes`,
   `consolidated_at` / `consolidation_failed_at`, and `document.retain_params`.
   To preserve them, columns must first be added to Wenyuan.

### 2.3 Unimplemented storage capabilities — Priority: low

- **Cross-process cache invalidation**: the per-process cache expires on time;
  consistency guarantees across worker processes are undefined.
- **Exact index/constraint alignment**: Wenyuan has only 3 explicit indexes
  (`idx_bank_aliases_bank`, `idx_bank_aliases_one_primary`,
  `idx_documents_retain_params`), whereas upstream has a full set (trigram, GIN,
  vector). Add them if performance demands it.
- **`async_operations.error` is a legacy column**: `0001` created `error`, and
  `0005` then added `error_message`; the code uses only the latter, leaving
  `error` dead. Can be cleaned up when convenient.

---

## 3. Contract surface items still to be confirmed (the deferred list from the spec)

The following 14 items are **public contract boundaries the spec never pinned
down**. The current implementation picked reasonable defaults but has **not
verified them item by item against upstream**. If "byte-for-byte compatibility"
is required, each one needs confirming.

| # | Item to confirm | Current implementation | Area affected |
|---|---|---|---|
| 1 | Authoritative mapping from public route → operation identity, and which operation identities are public compatibility surface | Handled by route path+method; operation identity is not exposed | Generated clients / tracing |
| 2 | Which deprecated request-field aliases must be accepted indefinitely vs have a removal horizon | Known aliases accepted (e.g. `background` / `reflect_mission`) | Request compatibility |
| 3 | Exact shape of non-validation error bodies (not-found/conflict/gone/unavailable/timeout) | Uniform `{"detail": ...}` envelope | Client error handling |
| 4 | Exact default values for disposition traits | Neutral value 3 | Behavior compatibility |
| 5 | Whether the recall trace object is public surface, and its field set | Full trace object fields not implemented | Client dependence |
| 6 | Complete authoritative set of per-bank configuration override keys | Only disposition traits + reflect mission | Config API |
| 7 | Cross-process cache invalidation guarantee | Time-based only | Distributed consistency |
| 8 | Exact shape of non-validation write error bodies (whether all carry an item list) | Only blocked-write carries violations | Client error handling |
| 9 | Exact index/constraint set deployments depend on; whether entity-kind → index selection is public surface | That selection logic is not implemented | Performance/storage compatibility |
| 10 | Boundary of which configuration names are considered public on the write path | Only a single batch gate | Config compatibility |
| 11 | Whether automatic document-id generation must match an exact form, or only its grouping behavior | Grouping behavior only | Write semantics |
| 12 | Whether the processed-content-token count is a public response field | Not exposed as a public field | Response compatibility |
| 13 | The reflection loop needs a public read contract for per-bank perspectives/directives | That read path is not implemented | Reflect capability |
| 14 | Unknown **body** fields are not reported on `X-Ignored-Params` (only query parameters are) | Only query parameters are reported | Forward compatibility |

> Implementation note for #14: `IgnoredParamsMiddleware` in
> `internal/httpx/middleware.go` currently handles only query parameters
> (`Store.queryparams.go`). Upstream reports unknown body fields as well, which
> requires defining a per-route "accepted body field set" before it can be
> completed.

---

## 4. Engineering quality and operational gaps

### 4.1 Explicitly excluded goals (by design, no work needed)

The following were declared **non-goals** in the spec and need no work unless
requirements change:
- Control Plane UI (port 9999)
- docker/compose/pip deployment forms and installers
- API endpoints beyond the first slice
- New performance-related features

### 4.2 Recommended engineering additions — Priority: low

| Item | Current state | Recommendation |
|---|---|---|
| End-to-end integration tests | 277 unit/package tests; `internal/app/parity_integration_test.go` has 8 HTTP contract cases ported from upstream | Add a true e2e that starts the service and connects to PG (the current sandbox forbids `net.Listen`, so it must run in CI or locally) |
| PostgreSQL path tests | Most tests use the in-memory store; `-tags pgx` only verifies compilation | Add testcontainers or a PG service in CI and exercise the real SQL in `postgres_*` |
| Benchmarks | None | Add benchmarks for recall's multi-arm fusion and migration import |
| Coverage gate | None | Add a `go test -cover` threshold to CI |
| Containerization | No Dockerfile | Add a multi-stage Dockerfile if deployment is needed |
| Versioning and releases | `CHANGELOG.md` has `[Unreleased]`; no tag, no release | Tag `v0.1.0` and run the release process |
| Dependency upgrades | `go.mod` pins pgx v5.5.4 etc. | Periodic `go get -u` plus regression |

### 4.3 Known small code-level issues — Priority: low

1. **`schema.go`'s column lists were once inconsistent with the real DDL**:
   `MemoryUnitsColumns` listed `embedding` (which does not exist), and
   `EntitiesColumns` lists `metadata` (which does not exist). `embedding` has
   been fixed; **`entities.metadata` is still a ghost column** and should be
   checked and cleaned up alongside.
   - Evidence: `EntitiesColumns` in `internal/storage/schema.go`.
   - The related tests only assert "whether a name appears in the list" and do
     not compare against the real DDL, so they pass CI.
2. **`async_operations.error` is a dead column** (see §2.3).
3. **`documents`' `metadata` / `retain_params` are never written**: the
   `PutDocument` INSERT in `postgres_retain.go` does not include these two
   columns, even though the Go structs carry values for them. Wire up the writes
   if they are needed.

---

## 5. Recommended order of work

To keep pushing Wenyuan toward being a "complete drop-in replacement", the
recommended order is:

1. **Add PG end-to-end tests** (§4.2) — establish confidence that "the storage
   layer is actually correct" before touching the schema.
2. **Implement the provider interface + embeddings** (§1.4 → §1.1) — the single
   highest-value item.
3. **Async worker process** (§1.2) — make the async semantics real.
4. **export/import HTTP endpoints** (§1.3) — the migration tooling is ready;
   this is just wiring.
5. **Confirm the 14 deferred contract items one by one** (§3) — only needed if
   "byte-for-byte compatibility" is required.
6. **Clean up ghost columns and dead columns** (§4.3).

---

## 6. Relevant code map

```
cmd/server             process entrypoint (HTTP API)
cmd/bankctl            admin commands: bank rename / archive / migrate
internal/app           application assembly, middleware, authentication
internal/bankapi       bank lifecycle + aliases + config + retired routes
internal/bankadmin    rename / archive / migrate command implementations
internal/config        flags > environment variables > defaults
internal/httpx        router, middleware, JSON/error envelope, ignored-param reporting
internal/hindsightarchive  Hindsight export ZIP parsing (§2.1)
internal/memoryops    retain / recall / reflect handlers and request/response models
internal/migration/hindsight  export → Wenyuan mapping and import (§2.1)
internal/monitoring   health / live / ready / version / metrics
internal/storage      storage interfaces + in-memory implementation + PostgreSQL implementation + migrations
migrations/           PostgreSQL schema (0001..0006)
```

**Migration series constraint**: `internal/storage/migrate.go` requires version
numbers to run contiguously from 1 to N, so a new migration must be
`0007_*.sql` and a copy must also be placed in `internal/storage/embedded/`
(used by `go:embed`).

---

## 7. How to verify the current state

```sh
make check                 # build + test + vet + pgx build
gofmt -l .                 # should print nothing
go test -count=1 ./...     # 277 tests
gh run list --repo studyzy/wenyuan   # CI status
```