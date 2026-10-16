# Wenyuan — Outstanding Work and Known Limitations (Handover Document)

> **Status (2026-10-15).** The gap list this handover was written against is
> closed: all five parity phases — A contract surface, B engine behaviour,
> C subsystems, D operational — are implemented and logged in
> **`docs/PARITY-PLAN.md`** (§8 is the log), and the HTTP contract was
> re-audited item by item against upstream's OpenAPI document — see
> **`docs/CONTRACT-AUDIT.md`**. `docs/GAPS.md` is the current state overview.
>
> What remains is **explicitly out of scope**, not unfinished: interactive-auth
> provider adapters, providers that need networked routers or local processes
> (litellm/bedrock/llamacpp) and local embedding models unless gatewayed, the
> GCS/Azure object-store backends and presigned download URLs, upstream's
> dynamic plug-in loader (compiled registration points instead), a reranker
> failover chain and local MLX, the `dateparser` dependency and Chinese
> numerals, markitdown OCR (files are handled as attachments), and upstream's
> `reflect/observations.py` (which nothing upstream calls).
>
> Rule: the sections below state current facts and may only be edited together
> with the code that closes or opens the gap they describe.

---

## 0. The one-sentence status

Wenyuan is a Go service that is **a drop-in replacement for Hindsight**:
**the public HTTP contract surface matches upstream's OpenAPI document
operation for operation** (`docs/CONTRACT-AUDIT.md`), and **the storage layer
is the reference schema, object for object** (`docs/SCHEMA-ALIGNMENT.md` is
empty — you can point Wenyuan at a database built by the same schema).

So "drop-in replacement" means:
- ✅ Clients integrate without changing any code.
- ✅ Existing Hindsight memories can be imported via `bankctl bank migrate`.
- ✅ The served schema is identical to the reference's.

---

## 1. Formerly unfinished API capabilities — all closed

The capabilities this section once listed as missing are implemented now.
Each is pinned by the parity suites; the implementation record lives in
`docs/PARITY-PLAN.md` §8.

| Was listed as missing | Now |
|---|---|
| Semantic retrieval was a keyword-score placeholder | Real vector similarity (`internal/storage/postgres_recall_vector.go`), embeddings and rerank through `internal/provider` |
| `async=true` writes had no background consumer | `cmd/worker` + `internal/worker`: lease-based queue consumer with retry/backoff; the server can run the consumer in-process |
| No export/import HTTP endpoints | Served by `internal/transfer` (full-bank archive export/import) |
| No LLM / provider integration | `internal/provider` port with openai / anthropic / embed / rerank / none adapters |
| `/mcp` returned 422 "not implemented" | `internal/mcp` serves the full tool roster |
| `files/retain` attachment storage incomplete | `internal/fileparse` parsing chain + objectstore-backed attachments |

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

## 3. Contract surface — retired by the OpenAPI audit

The 14 "to be confirmed" items this section once listed were the original
spec's deferred contract questions. They were retired by the item-by-item
audit against upstream's OpenAPI document (`docs/CONTRACT-AUDIT.md`,
2026-10-15): every operation's params, bodies, status codes, headers and error
envelopes were compared, the findings were fixed, and any residuals are
recorded there. `docs/GAPS.md` is the current state overview.

---

## 4. Engineering quality and operational gaps

### 4.1 Explicitly excluded goals (by design, no work needed)

The following were declared **non-goals** in the spec and need no work unless
requirements change:
- Control Plane UI (port 9999)
- docker/compose/pip deployment forms and installers
- API endpoints beyond the first slice
- New performance-related features

### 4.2 Recommended engineering additions — all landed

The additions this table once recommended are in place:

- End-to-end tests through the served handler
  (`internal/app/parity_integration_test.go`) plus the `tests/` integration and
  system e2e suites.
- Real-PostgreSQL path tests with coverage floors (`COVERAGE_MIN`,
  `COVERAGE_MIN_PG` in the Makefile).
- Benchmarks (`internal/storage/bench_test.go`: recall fusion, retain write,
  document listing, graph traversal).
- A multi-stage `Dockerfile` and the `v0.1.0` tag.

Dependency upgrades remain ordinary ongoing maintenance.

### 4.3 Known small code-level issues — Priority: low

1. **`documents`' `retain_params` / `tags` are written; `metadata` is not a
   column at all**: the reference's `documents` table has no `metadata` column,
   so the Go struct field has nowhere to go. Check before building on it.

---

## 5. Recommended order of work

The original order — PG e2e tests → provider/embeddings → worker →
export/import endpoints → contract confirmation — was followed and is
complete. What remains is the explicitly-out-of-scope list in the status
banner, plus anything recorded as a residual in `docs/CONTRACT-AUDIT.md` or
`docs/GAPS.md`.

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
make check                 # build + test + vet + pgx build + lint
gofmt -l .                 # should print nothing
go test -count=1 ./...     # offline suite
gh run list --repo studyzy/wenyuan   # CI status
```