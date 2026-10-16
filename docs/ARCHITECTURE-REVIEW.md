# Architecture Review

**Date:** 2026-10-15
**Scope:** Full architecture review of the Go codebase (`cmd/`, `internal/`). The `frontend/` tree is out of scope except where it touches the API surface.
**Method:** Import-graph analysis (`go list`), package-size survey, targeted code reading of the composition root, router, storage interface, worker, and error/auth paths; `go build`, `go vet`, and the full offline test suite were run as part of this review and pass.

---

## 1. Executive summary

The codebase is a well-executed layered Go service with an unusually disciplined
boundary story for its size (~97.6k source lines, ~57.9k test lines, 45 internal
packages). The dependency graph is acyclic, strictly downward, and partially
**enforced by tests** rather than convention. The standard-library-first rule is
genuinely honored (five direct dependencies, each justified inline), and the
verification gate (`make check`, coverage floors, parity suite) is above-average
for a project of this kind.

The main structural liabilities are:

1. a **121-method `storage.Store` god interface** that every consumer depends on wholesale,
2. **business logic living in the composition root** (`internal/app`), and
3. **stale "known gaps" documentation** that contradicts the shipped code.

None of these is a defect today; all three tax the next contributor.

**Overall grade: B+ / A-** — architecture serves the compatibility contract well;
the findings below are consolidation opportunities, not emergencies.

| Dimension | Rating | Notes |
|---|---|---|
| Layering & dependency direction | A | Clean DAG, enforced by tests |
| Cohesion | B | `storage` is one 28k-line package; `app` mixes wiring with domain logic |
| Coupling | B- | God interface; httpx→audit→storage pull-through |
| Testability | A | Injected doubles, e2e parity tests, coverage floors |
| Error handling | A- | Central envelope, sentinel errors, one gap in context propagation |
| Concurrency & lifecycle | A- | Lease-based worker, graceful shutdown; one polling-loop nit |
| Docs & hygiene | B | Zero TODOs, good gitignore; stale AGENTS.md gaps, version drift |

---

## 2. Architecture overview

### 2.1 Module map

```
cmd/server, cmd/worker, cmd/bankctl, cmd/schematool, cmd/wenyuan   entrypoints
        │
internal/app                                            composition root (32 deps)
        │
    ┌───┴────────────────────────────────────────────────┐
    │ handler namespaces                                  │
    │ bankapi · memoryops · readsurface · reporting       │
    │ webhooks · mentalmodels · directives · mcp          │
    │ transfer · monitoring                               │
    └───┬────────────────────────────────────────────────┘
        │
internal/httpx     router, middleware, JSON/error envelope, OpenAPI, extension host
internal/audit     audit trail (fire-and-forget)
        │
internal/storage   one Store interface, memory + postgres backends (pgx tag)
        ▲
    domain engines: reflect · consolidation · extraction · entitylabels ·
    fileparse · mentalmodels · webhooks · worker · maintenance · provider/*
```

### 2.2 What is done well

**Layering is real, not nominal.** The `go list` import graph shows no cycles
and one consistent direction. `internal/storage` sits at the bottom (its only
internal imports are `entitylabels` and `hindsightarchive`), `httpx` next, and
handlers never reach past the `storage.Store` interface. The composition root
is the only package allowed to fan out wide (32 imports), which is exactly its
job.

**Boundaries are enforced by executable tests**, the strongest practice in the
repo:

- `internal/storage/boundary_test.go` walks every file outside `storage/` and
  fails on any SQL-shaped reference to a memory-domain table.
- `internal/app/query_declaration_test.go`, `route_coverage_test.go` and
  `api_parity_test.go` pin the route surface and query contract.
- `internal/cli` imports only `internal/cli/fsengine` — the API-client boundary
  (no server packages in the CLI binary) holds at the import-graph level, not
  just on paper.

**Provider port/adapter.** `internal/provider` defines the model-provider port
(`provider.go`: `Complete`, `Embed`, `VerifyConnection`, `Dimensions`); vendor
adapters (`openai`, `anthropic`, `embed`, `rerank`, `none`, `mock`) depend only
on the port. Selection, routing (SWRR), and refusal policy are separated from
transport. This is textbook hexagonal and keeps the LLM boundary swappable.

**Operations and lifecycle.** The worker (`internal/worker/consumer.go`) has
lease-based claims, attempt budgets, exponential backoff with a ceiling,
deferral vs. permanent-error semantics, and graceful release of in-flight work —
production-grade queue semantics. Server shutdown uses `signal.NotifyContext`
plus a bounded `server.Shutdown` (`cmd/server/main.go:60`,
`internal/app/entrypoint.go:93`).

**Error handling.** One JSON envelope (`internal/httpx/errors.go`) with typed
validation items and custom `MarshalJSON` for wire fidelity; sentinel errors in
`storage` mapped to statuses at the edge; error message wording centralized so
wire compatibility is a one-file concern.

**Dependency hygiene.** Five direct deps (`pgx`, `bubbletea`, `lipgloss`,
`robfig/cron`, `tiktoken-go/tokenizer`), each with an inline justification in
`go.mod`. Zero `TODO`/`FIXME` markers in the tree. Binaries and coverage files
are gitignored. `go build`, `go vet`, and the full offline suite pass.

---

## 3. Findings

### F1. `storage.Store` is a god interface — HIGH (structural debt)

`internal/storage/store.go:107-494` declares ~121 methods spanning banks,
retention, recall, reflection, the operation queue, webhooks, knowledge base,
mental models, directives, attachments, observations, read surface,
observability, and stored files.

Consequences:

- Every consumer depends on the entire surface; a one-method consumer is
  compiled and tested against 120 methods it never calls.
- Both backends must implement all ~121 methods; the memory backend carries
  ~40 files purely to satisfy the interface.
- Adding any area churns the single interface and every double of it.

The repo already models the fix partially: `ArchivedMemoryStore` and
`SchemaMigrator` are optional capabilities. Extend that pattern: split the
surface into area interfaces (`BankStore`, `RecallStore`, `OperationQueue`,
`WebhookStore`, …) that `Store` composes, and prefer **consumer-side
interfaces** (declare the methods a package uses, per Go idiom) in handlers and
engines. This is a mechanical, incremental refactor — no behavior change, and
the boundary tests keep guarding the SQL edge throughout.

### F2. `internal/app` mixes composition with domain logic — MEDIUM

`internal/app` is the composition root, but it also hosts the worker's
operational logic: `worker.go` (805 lines) implements webhook delivery with its
own transport tuning and retry delay, mental-model refresh handling, and
consolidation enqueueing. Domain packages `internal/webhooks` and
`internal/mentalmodels` already exist next door.

The cost is cohesion and reuse: the delivery engine cannot be exercised or
reused without importing the entire wiring graph, and `app` now has 32
dependencies plus 6.5k lines of tests that are really unit tests of processor
logic. Moving the processor implementations into their domain packages (leaving
`app` as pure assembly) shrinks the root and makes the processors testable in
isolation.

### F3. Stale "known gaps" documentation — MEDIUM

`AGENTS.md` ("Known gaps — do not assume these work") and the corresponding
section of `docs/ROADMAP.md` still claim: semantic search is a placeholder,
async writes have no worker, there are no export/import HTTP endpoints, and
there is no LLM/provider integration. **All four are implemented now**
(`internal/provider/*` with embeddings and rerankers, `cmd/worker` +
`internal/worker`, `internal/transfer` with served routes, real vector search
in `postgres_recall_vector.go`). Related drift: `AGENTS.md` states Go `1.24`
while `go.mod` declares `go 1.25`.

This is more than cosmetic here: `AGENTS.md` is the instruction surface for
AI agents and new contributors, and it currently tells them to *avoid building
on capabilities that exist*. Delete the closed items, keep the live ones, and
add a rule that the gaps section may only be edited together with the code
that closes or opens a gap.

### F4. `httpx` (infra layer) depends on `audit` → `storage` — LOW

`internal/httpx/audit.go:12` imports `internal/audit`, which imports
`internal/storage`. The lowest HTTP layer therefore transitively pulls the
storage layer, so every handler package depends on storage through two paths.
The middleware needs only "log an entry" behavior; defining a one-method
interface in `httpx` and letting `app` inject `audit.Logger` would restore the
dependency direction at the cost of one adapter.

### F5. Sibling handler coupling through `memoryops` — LOW

- `internal/bankapi/config_keys.go:196` calls `memoryops.ParseDefensePolicy`.
- `internal/readsurface` calls `memoryops.ScopeFromRequest` and mirrors its
  tag-scope logic (`readsurface/params.go:21` explicitly notes the mirroring).

Small, but handler→handler imports and a self-admitted duplicated rule are the
seed of a tangle. The request-scope/tag-visibility rules belong in a shared
package below both (they are domain rules, not handler rules), which also
deletes the mirroring comment.

### F6. `context.Background()` inside the request path — LOW/MEDIUM

`internal/reflect/loop.go:574` and `:704` fetch directives with
`context.Background()` while the reflection loop is serving a request. A client
disconnect or read-timeout will not cancel those storage reads. The loop
already carries the request context (`l.provider.Complete(ctx, …)` at `:708`),
so threading the same context into `Directives` is a two-line fix with a
one-line test.

### F7. File-size limit is documented but unenforced — LOW

`AGENTS.md` caps code files at 500 lines ("checked in review, not in CI").
Thirty non-test files exceed it; the largest are
`internal/memoryops/query_time.go` (1178), `internal/transfer/import_apply.go`
(1155), `internal/config/provider.go` (1094). Either enforce the rule with a
CI check (a ten-line script over `wc -l`) or amend the limit in the doc — an
unfollowed rule is worse than no rule, because it trains reviewers to ignore
the doc.

### F8. Permissive default authenticator — LOW (documented)

`internal/app/auth.go` states the foundation unit "keeps a permissive default so
internal callers can reach the configuration surface; deployments add their own
gate at the edge." This is a deliberate, documented trade-off, but the
default-deny posture for *mutating* config routes would be safer as a default
that deployments opt out of, rather than the reverse. Flagged for deployment
checklists rather than as a code defect.

### F9. `internal/storage` is a 207-file, 28k-line package — LOW

The package is cohesive by *mechanism* (persistence) but not by *domain area*
(banks, recall, queue, webhooks, knowledge, …). The file-prefix convention
(`postgres_recall.go`, `memory_webhooks.go`, …) keeps it navigable, and Go
compile times are not yet a problem. Worth watching rather than acting on;
if it is ever split, do it along the same area lines proposed in F1 so the
interface split and the package split land together.

---

## 4. Best-practices checklist

| Practice | Status | Evidence |
|---|---|---|
| Acyclic, layered dependency graph | ✅ | `go list` import graph; no cycles |
| Boundaries enforced by tests, not convention | ✅ | `storage/boundary_test.go`, app coverage/parity tests |
| Dependency injection / composition root | ✅ | `app.Dependencies` with injectable Store/Provider/Listen/Now |
| Ports & adapters for external systems | ✅ | `internal/provider`, `objectstore`, `oteltrace` |
| Stdlib-first dependency policy | ✅ | 5 direct deps, each justified in `go.mod` |
| Central error envelope & sentinel errors | ✅ | `httpx/errors.go`, `storage/store.go` sentinels |
| Structured logging | ✅ | `log/slog` throughout |
| Graceful shutdown | ✅ | `signal.NotifyContext`, bounded `Shutdown`, worker claim release |
| Build-tag isolation of driver code, CI-checked | ✅ | `pgx` tag; `make pgx` in the gate |
| Coverage floors in the verification gate | ✅ | `COVERAGE_MIN=56`, `COVERAGE_MIN_PG=66` |
| Table-driven tests; e2e through served handler | ✅ | `app/parity_integration_test.go` and friends |
| Context propagation complete | ⚠️ | F6 (`reflect/loop.go` background reads) |
| Interface segregation | ❌ | F1 (121-method Store) |
| Doc stays in sync with code | ⚠️ | F3 (AGENTS.md gaps), Go version drift |
| Enforced file-size discipline | ⚠️ | F7 (30 files over the stated limit) |

---

## 5. Recommendations, prioritized

1. **Now (doc correctness):** Update `AGENTS.md` and `docs/ROADMAP.md` known-gap
   sections to match shipped reality; fix the Go version line (F3).
2. **Now (small, safe):** Thread the request context into the reflect loop's
   directive reads (F6).
3. **Next quarter (mechanical refactor):** Split `storage.Store` into composable
   area interfaces; adopt consumer-side interfaces in handlers/engines (F1).
   Do it incrementally, one area at a time; the boundary tests make this safe.
4. **Next quarter:** Move worker processor implementations (webhook delivery,
   mental-model refresh, consolidation enqueue) out of `internal/app` into
   `internal/webhooks` / `internal/mentalmodels` (F2); lift the shared
   tag-scope/defense-policy rules out of `memoryops` into a common package (F5).
5. **Ongoing:** Decide the file-size rule — enforce or amend (F7). Consider a
   one-method audit hook in `httpx` (F4). Add the strict auth default to the
   deployment checklist (F8).

---

## 6. Verdict

The architecture does the one thing this project must do — hold a frozen
compatibility surface — exceptionally well: the wire surface is pinned by
tests, the schema is generated and diffed against the reference, and the
boundaries that matter are executable. The structural debt that exists is the
predictable cost of that strategy (one wide Store, one wide app package) and is
addressable incrementally. Fix the documentation drift first: it is the only
finding that actively misleads every future reader.
