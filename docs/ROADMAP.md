# Wenyuan — Outstanding Work and Known Limitations (Handover Document)

> **⚠️ Superseded (2026-10-14).** This document was written at an earlier
> baseline and every "unfinished" claim in it has since been worked through.
> The audit, the per-item implementation records, the acceptance commands and
> the live-run evidence now live in **`docs/PARITY-PLAN.md`** (all five phases —
> A contract surface, B engine behaviour, C subsystems, D operational — are
> closed; §8 is the log). `docs/GAPS.md` is the earlier baseline's checklist,
> with its status table corrected to match. Nothing in this file should be
> treated as current state: check the code and `PARITY-PLAN.md` before acting on
> any claim below.
>
> What remains is **explicitly out of scope**, recorded in `PARITY-PLAN.md`:
> interactive-auth provider adapters, the GCS/Azure object-store backends and
> presigned download URLs, upstream's dynamic plug-in loader, a reranker
> failover chain and local MLX, the `dateparser` dependency and Chinese
> numerals, and upstream's `reflect/observations.py` (which nothing upstream
> calls). (The document-parsing chain is implemented — see
> `docs/PARITY-PLAN.md` §8, C7.)
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

## 2. Storage layer: the schema is the reference's

**Conclusion: the schema is upstream Hindsight's schema. A database built by
Wenyuan's migration series is indistinguishable from one built by the reference,
object for object.**

The series is generated rather than hand-written.
`scripts/reference_schema_migrations.py` reads the reference database's own
catalogue and emits the series — `0001_extensions`, `0002_reference_tables`,
`0003_reference_constraints`, `0004_reference_indexes`,
`0005_reference_functions` — into both copies (`migrations/` and
`internal/storage/embedded/`). Emitting from the catalogue rather than replaying
a `pg_dump` is what makes the result identical: PostgreSQL does not store back
everything it is given, so a replayed dump produces, for example,
`memory_units_bank_id_not_null1` where the reference says
`memory_units_bank_id_not_null`.

`docs/SCHEMA-ALIGNMENT.md` is the standing report of how the built schema
compares to the live reference, produced by `scripts/schema_diff.sql` and
`scripts/schema_inventory.py`. As of the last run it is empty: no table, column,
index, constraint, function, view, sequence, trigger or extension is missing,
extra or different. A `pg_dump --schema-only` of each side diffs clean as well.

The code addresses the reference's tables and columns by their reference names.
There is no parallel naming layer, no renamed column and no table the reference
does not have.

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

1. **`documents`' `retain_params` / `tags` are written; `metadata` is not a
   column at all**: the reference's `documents` table has no `metadata` column,
   so the Go struct field has nowhere to go. Check before building on it.

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
cmd/wenyuan            client CLI entrypoint (wenyuan-admin binary)
internal/app           application assembly, middleware, authentication
internal/bankapi       bank lifecycle + aliases + config + retired routes
internal/bankadmin    rename / archive / migrate command implementations
internal/cli           client CLI: registry, HTTP client, error classifier,
                       resource commands, fs mirror engine, explore TUI
internal/config        flags > environment variables > defaults
internal/httpx        router, middleware, JSON/error envelope, ignored-param reporting
internal/hindsightarchive  Hindsight export ZIP parsing (§2.1)
internal/memoryops    retain / recall / reflect handlers and request/response models
internal/migration/hindsight  export → Wenyuan mapping and import (§2.1)
internal/monitoring   health / live / ready / version / metrics
internal/storage      storage interfaces + in-memory implementation + PostgreSQL implementation + migrations
migrations/           PostgreSQL schema, generated from the reference (0001..0005)
```

**Migration series constraint**: `internal/storage/migrate.go` requires version
numbers to run contiguously from 1 to N, and `internal/storage/embedded/` holds
the copy the binary actually reads (`go:embed`). The series is generated by
`scripts/reference_schema_migrations.py`, so a schema change is made in the
reference and the series is regenerated, not edited by hand.

---

## 7. How to verify the current state

```sh
make check                 # build + test + vet + pgx build
gofmt -l .                 # should print nothing
go test -count=1 ./...     # 277 tests
gh run list --repo studyzy/wenyuan   # CI status
```