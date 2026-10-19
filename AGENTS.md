# AGENTS.md

Guidance for AI coding agents working in this repository.

## What this project is

`wenyuan` (文渊) is a Go implementation of the **reference implementation's
agent-memory HTTP API** and its PostgreSQL storage layer: a drop-in replacement
that a deployment can put where the reference service was.

The point of the project is **API compatibility**: existing clients integrate
without modification because the public HTTP surface and the PostgreSQL schema
are preserved. Compatibility is the contract — treat it as more important than
internal elegance.

Where the two disagree, the reference implementation's behavior wins, and the
disagreement is a bug.

### Naming — what still says `HINDSIGHT`, and why

Everything this repository calls its own is `wenyuan` / `WenYuan`, and the
service it replaces is "the reference implementation" (「参考实现」/「上游」 in the
Chinese docs). A handful of strings still carry the old name on purpose, each
because it is a contract or a pointer at the counterpart rather than branding:

| String | Why it stays |
|---|---|
| `HINDSIGHT_API_*`, `HINDSIGHT_CP_*`, `HINDSIGHT_PROFILE`, `HINDSIGHT_BANK_ID`, `HINDSIGHT_FS_*` | environment-variable and cookie names are the deployment contract (the key list is `internal/config/reference.go`); renaming them breaks every existing `.env`, compose file and chart that already sets them |
| the document served at `/openapi.json`, `info.title` included | that document *is* the reference contract; `scripts/reference_openapi.py --check` compares it byte for byte |
| `hindsight_http_requests_total`, `hindsight_event_loop_lag`, … | the Prometheus rendering of the reference's OTel metric names; dashboards and the parity comparison read them |
| `X-Hindsight-Event` / `-Signature` / `-Signature-V2` / `-Bank-Id` | the webhook wire contract: a receiver verifying a signature reads these header names |
| the MCP `serverInfo.name`, the OTEL default service name, the daemon log default `~/.hindsight/daemon.log` | observable values a client or a log pipeline reads |
| `hindsight_api.extensions.builtin.tenant:ApiKeyTenantExtension` | a value deployments pass in `HINDSIGHT_API_TENANT_EXTENSION` |
| redaction labels naming the counterpart's credential formats (`hindsight_key` for its `hsk_…` keys) | the label says whose key the pattern catches; the reference's list carries the same name |
| `internal/hindsightarchive`, `internal/migration/hindsight`, `bankctl … --from-hindsight` | named after what they handle: the reference's export format, and the direction of the import |
| citations such as `engine/audit.py:1-5`, `hindsight_api/…`, `hindsight-docs/static/openapi.json` | evidence — a citation that cannot be followed is not evidence |
| `tests/system/**` | the reference's own black-box suite, vendored verbatim |

Adding a new identifier, log line, metric, header or document of our own must
not use the old name; if something has to keep it, add it to this table with
the reason.

## Ground rules

1. **Compatibility surface is frozen.** Routes, JSON field names, status codes,
   response headers, PG table names, PG column names, and PG enum values must
   be preserved exactly. Changing any of them is a breaking change and must be
   called out explicitly.
2. **Standard library first.** Reach for `net/http`, `encoding/json`, `database/sql`
   before adding anything. New dependencies need a real justification and must be
   MIT / Apache-2.0 / BSD licensed.
3. **Smallest correct diff.** One change per change. Do not mix a fix with an
   unrelated refactor.
4. **Match existing style.** Small focused files, table-driven tests, standard
   Go idioms. `gofmt` and `golangci-lint` are enforced in CI.
5. **Every behavior change ships a test** that fails without it. New public
   behavior needs an end-to-end test through the served handler.

## Verification gate

Run this before considering any change done:

```sh
make check     # build + test + vet + pgx build + lint
```

Individually:

```sh
make build     # CGO_ENABLED=0 go build ./...
make test      # go test -count=1 ./...
make vet       # go vet ./...
make pgx       # CGO_ENABLED=0 go build -tags pgx ./...   <- do not skip
make lint      # golangci-lint run ./...   (config in .golangci.yml)
gofmt -l .     # must print nothing
```

`make check` is the same checks CI runs on push and PR to `main` (job
`build-and-test` for the four build/test/vet/pgx steps, job `lint` for
`gofmt` and `golangci-lint`). `make lint` needs `golangci-lint` on `PATH`
(override with `GOLANGCI_LINT=/path/to/golangci-lint`); the config uses the
v2 schema and is version-sensitive.

### The `pgx` build tag is not optional

The default build is standard-library-only and offline. The PostgreSQL driver
is compiled **only** under the `pgx` build tag (`internal/storage/driver_pgx.go`).
Code behind that tag is invisible to a plain `go build ./...`, so a change can
pass `make build` and still break `make pgx`. Always run both.

Any new import used only under the tag still needs to be in `go.mod`.

## Repository layout

```
cmd/server/            API server entrypoint
cmd/worker/            async operation worker entrypoint
cmd/bankctl/           admin CLI (bank operations, migration, backup/restore)
cmd/wenyuan/           client CLI (wenyuan-admin binary) — API client, never
                       imports server packages; lives in internal/cli/
cmd/schematool/        schema dump helper for the migration generator
internal/app/          composition root: wires config, storage, routes
internal/bankapi/      bank lifecycle HTTP handlers + route table
internal/memoryops/    retain / recall / reflect HTTP handlers + route table
internal/httpx/        router, JSON helpers, error envelope, middleware,
                       OpenAPI surface, extension host
internal/config/       config resolution: flags, env, .env, defaults
internal/storage/      storage interfaces + memory + postgres backends
internal/storage/embedded/  embedded copy of the SQL migrations (see below)
internal/provider/     model-provider port + adapters (openai, anthropic,
                       embed, rerank, none, mock) — see "Provider boundary"
internal/reflect/      bounded retrieval-and-synthesis reflection loop
internal/consolidation/  observation consolidation engine
internal/extraction/   memory extraction pipeline
internal/entitylabels/ entity label resolution
internal/fileparse/    file/document parsing for file retain
internal/mentalmodels/ mental model domain logic (triggers, patches)
internal/directives/   directive HTTP handlers
internal/readsurface/  documents / chunks / entities / graph read handlers
internal/reporting/    reporting & observability read handlers
internal/webhooks/     webhook HTTP handlers + delivery guard
internal/worker/       operation queue consumer (lease, backoff, retry)
internal/maintenance/  background maintenance loop
internal/mcp/          MCP endpoint handlers
internal/transfer/     archive export / import HTTP routes + apply logic
internal/objectstore/  S3-compatible object storage backend
internal/oteltrace/    OTel tracing support
internal/audit/        audit trail (fire-and-forget)
internal/tenant/       tenant / extension schema abstraction
internal/monitoring/   health / metrics / version endpoints
internal/hindsightarchive/   the reference's whole-bank export format (zip)
internal/migration/hindsight/  import the reference's exported archive into
                               this storage (named after what it imports)
internal/bankadmin/    bank admin operations shared by CLI
internal/cli/          wenyuan-admin implementation (API client only)
frontend/              control-plane frontend (Next.js, dev on port 19999;
                       talks to the API through BFF routes under app/api/)
clients/               generated client SDKs (Go, Python, TypeScript, Rust) built
                       from clients/openapi.json, a derived copy of the served
                       document — see clients/README.md; the Go SDK is a nested
                       module, so the service module and its gates never see it
migrations/            numbered SQL migrations — authoritative but NOT what runs
docs/                  ROADMAP, GAPS/FUNCTIONAL-GAPS, CONTRACT-AUDIT,
                       PARITY-PLAN/PARITY-SUITE, SCHEMA-ALIGNMENT, MIGRATION,
                       ARCHITECTURE-REVIEW, CONFIGURATION (generated reference —
                       regenerate with scripts/render_config_reference.py)
```

Tests live next to the code as `*_test.go`.

## Architectural boundaries that are enforced by tests

**Do not reference memory-domain table names outside `internal/storage/`.**

`internal/storage/boundary_test.go` walks every `.go` file outside that package
and fails if it finds SQL-shaped references (`FROM banks`, `INTO chunks`,
`UPDATE memory_units`, ...) to any table in `storage.MemoryDomainTables`.

The owned tables are listed in `storage.MemoryDomainTables`
(`internal/storage/schema.go`); the set is the reference schema's, and
`internal/storage/schema_test.go` pins it.

Practical consequence: handlers in `internal/bankapi` and `internal/memoryops`
must go through the `storage` interface. Never write SQL in a handler package.

There is also `schema_test.go` and `migrate_test.go` guarding schema consistency.
`migrate_test.go` only checks the embedded series is non-empty, so it will **not**
catch a migration added to one copy and not the other — see the migrations
section below.

**`internal/cli` is an API client: it must not import server packages.** The
CLI talks to the served HTTP API like any external client. It declares its own
response structs instead of importing `internal/monitoring`, `internal/app`,
`internal/storage`, or other server-side packages (the e2e `_test.go` files
are the only exception, via `internal/app`). Keeping the dependency edge out
keeps the `wenyuan-admin` binary small and the CLI decoupled from server
refactors.

## Storage layer conventions

The storage package has two backends behind one interface (`internal/storage/store.go`):

- **memory** — in-memory, used by the default test path (no database needed)
- **postgres** — the real backend, compiled under the `pgx` tag

Any new storage method must be implemented in **both** backends, or the
interface will not compile. Files are split by area with parallel prefixes:
`memory_<area>.go` for the in-memory backend, `postgres_<area>.go` for the
PostgreSQL backend — banks, retain, recall, reflect, operations, webhooks,
mental models, knowledge, attachments, maintenance, and the rest. Follow the
prefix convention when adding a file.

### Migrations — read this before touching SQL

**The migration series is generated, not written.** The PostgreSQL schema this
project serves *is* the reference schema, so the series is emitted from
the reference database's own catalogue:

```sh
python3 scripts/reference_schema_migrations.py   # regenerates both copies
```

It writes `migrations/` and `internal/storage/embedded/` as five files —
`0001_extensions`, `0002_reference_tables`, `0003_reference_constraints`,
`0004_reference_indexes`, `0005_reference_functions`. Two consequences:

- **Do not hand-edit a migration.** To change the schema, change the reference
  and regenerate. A hand edit reintroduces exactly the drift the generation
  exists to prevent.
- **The two copies are written together** by the generator; there is nothing to
  keep in sync by hand. `internal/storage/migrate.go` embeds
  `internal/storage/embedded/` via `//go:embed all:embedded`, and that is the
  copy the binary reads.
- **The embedding column width is not part of the series.** The dumped
  reference declares whichever width that deployment's embedding model had,
  and upstream resolves the column width at startup, so this service does the
  same: `internal/storage/embedding_dimension.go` reconciles every `embedding`
  column to `storage.EmbeddingDimensions` inside the migration transaction -
  equal width is a no-op, a mismatch with stored vectors is refused with both
  widths named, a mismatch on an empty table drops the vector index, alters the
  column, and rebuilds the index from its captured definition. Do not hand-edit
  the width in a migration: a regeneration would silently revert it and every
  vector write would then fail on a width nobody configured.

`docs/SCHEMA-ALIGNMENT.md` is the standing comparison against the live
reference, produced by `scripts/schema_diff.sql` and
`scripts/schema_inventory.py`. It must stay empty: no object missing, extra or
differing. `internal/storage/schema.go` declares the expected table set;
`schema_test.go` cross-checks the declarations against the series.

## Known gaps and open divergences — check before building on these

The big parity gaps are closed: semantic (vector) search, the async retain
pipeline with a real worker (`cmd/worker`), export/import HTTP routes
(`internal/transfer`), LLM/provider integration (`internal/provider`), and the
PostgreSQL schema all match the reference — the served route surface and the
schema alignment are pinned by tests and parity suites. What remains is the
short list below. `docs/FUNCTIONAL-GAPS.md` and `docs/GAPS.md` carry the
detailed ledger with evidence; keep them in sync when you close or open a gap.
`docs/PARITY-OPEN-ITEMS.md` carries the per-route residual list from the
2026-10-17 route-by-route comparison against the live reference (each item with
its reference evidence, the file:line the fix belongs at, and how to verify it);
work from that file when picking up route-level parity work, and strike items
out of it as they close.

- **Mental-model history content carries the reference's shape, in either JSON
  encoding.** A row stores `{"previous_content": …, "previous_reflect_response":
  …}` (plus the failure markers), the same keys the reference writes, so a
  history read is identical on both sides. The defect found on 2026-10-17 was
  narrower than this entry used to claim: the reference writes those values as
  JSON *text* (`bank_rows_json_encoding: "serialized"`) while this service wrote
  decoded objects, and the reader only understood the object form — so every
  jsonb field of an imported reference archive (history content, a mental
  model's trigger, a bank's config) silently fell back to its default. Both
  encodings now read the same; `TestRestoreReadsSerializedRowJSON` pins it.
- **Mental-model patches clear a member by nulling it, as the reference does.**
  A patch that states `refresh_cron: null` (or `tags_match: null`) stores the
  member as an explicit null, which is what the reference's dict merge produces
  (`{**base, **supplied}`) and what every reader treats as unset, so the CLI's
  clear lands. Re-checked 2026-10-17 — the `nonNullMembers` this entry used to
  name no longer exists; `TestUpdateClearsTriggerMembersOnExplicitNull` pins the
  behavior, including that unnamed members survive the patch.
- **Transfer archives carry the whole bank, except its transient maintenance
  markers.** Full-bank export/import moves the documents and their chunks,
  facts, observations, attachments, bank config, mental models, knowledge
  pages, directives and webhooks, plus the `audit_log` / `llm_requests` history
  segments and the raw invalidated-memory / operation-queue data segments when
  the caller selects them (the reference's `include_history` default is off, so
  an archive that omits them is a caller's choice, not a gap). Re-checked
  2026-10-17: chunks are rebuilt on import from the archive's own chunk text,
  the history pair round-trips under `history/`, and the queue/invalidated rows
  ride under `data/`.
  The two maintenance-queue tables ride the same way: `graph_maintenance_queue`
  and `entity_maintenance_queue` are carried under `data/` and restored by
  remapping each row onto an id the target's own replay regenerated (graph rows
  through its unit-id map, entity rows through `canonical_name`), dropping the
  rows the target did not reproduce — which is what the reference does, verified
  against the live service on 2026-10-17 (a seeded row in each table survives a
  reference export/import round trip and raises
  `maintenance_queue_rows_imported`). An earlier version of this entry claimed
  those rows were deliberately dropped here and rederived by the target; that
  was wrong on both sides and is fixed in
  `internal/storage/maintenance_transfer.go` (`MaintenanceQueueTransfer`),
  `internal/transfer/{export_build.go,import_apply.go}` and
  `internal/hindsightarchive/sections.go`, pinned by
  `TestRestoreRemapsMaintenanceQueues`. Also matching the reference's
  reader: an archive's jsonb row values may arrive as JSON text (its
  `bank_rows_json_encoding: "serialized"`) or as decoded objects (ours), and
  both read the same — see `TestRestoreReadsSerializedRowJSON`, which caught a
  real loss where every jsonb field of a reference archive fell back to its
  default.
- **Intentionally out of scope:** OAuth provider integration and GCS / Azure
  object stores (the S3-compatible backend covers object storage).
- **Default listen ports are offset from the reference on purpose.** wenyuan
  defaults to API `18888`, worker HTTP `18889`, and frontend `19999` —
  upstream's 8888/8889 plus 10000 (the upstream Control Plane UI is 9999) —
  so the reference service and wenyuan can run side by side for comparison. The
  compose stack's own PostgreSQL publishes host port `15432` (5432 + 10000)
  and never touches the reference's instance on 5432. The
  env var names (`HINDSIGHT_API_PORT`,
  `HINDSIGHT_API_WORKER_HTTP_PORT`) and every other default still match; do
  not "fix" the port defaults back to 8888.

## Provider boundary

Model access goes through the port in `internal/provider` (`Provider`,
`Embedder`, `Reranker`). Vendor adapters under `internal/provider/{openai,
anthropic,embed,rerank}` depend only on the port; handlers and engines never
import a vendor package. `none` is the deterministic no-model selection used
when nothing is configured. Add a new vendor as another adapter registered in
the provider registry — not as a new import edge in a handler package.

## Testing notes

- Most tests run against the in-memory backend and need **no database**.
- Tests are table-driven where a behavior has several cases.
- `internal/app/parity_integration_test.go` is the end-to-end compatibility
  test through the served handler — new public behavior belongs here.
- Tests must pass offline. `go test -mod=readonly ./...` is part of the
  module-tidiness check in CI; if you add an import, run `make tidy`.

## File size limits

Code files stay under **500 lines**, test files under **800**. Split along
module boundaries rather than growing a file. This is checked in review, not
in CI.

## Commits and PRs

- Imperative mood: "add bank alias routing", not "added" or "adds".
- Describe *why* in the body; the diff shows *what*.
- Reference the issue: `Closes #123`.
- Mark breaking changes explicitly.

See `CONTRIBUTING.md` for the human-facing version of these rules.

## Conventions quick reference

- Module path: `github.com/studyzy/wenyuan`
- Go version: `1.25` (see `go.mod`)
- Indent: tabs in Go, 2 spaces elsewhere (`.editorconfig`)
- Line endings: LF, final newline required
- Linters: `errcheck`, `govet`, `ineffassign`, `staticcheck`, `unused`, `gofmt`,
  `goimports`, `misspell`, `unconvert`, `bodyclose` (`.golangci.yml`)
- `goimports` groups this module under `local-prefixes: github.com/studyzy/wenyuan`
- Config resolution precedence lives in `internal/config/` — check it before
  adding a new config key. The order is flags, then the `.env` file (a
  discovered `.env` is authoritative over the process environment, the
  reference's own entrypoint rule), then the rest of the process environment,
  then the YAML configuration file
  (`etc/wenyuan.yaml`, or `HINDSIGHT_API_CONFIG_FILE`), then
  the built-in defaults. The file is read through Viper in
  `internal/config/configfile.go` and answers a key as the environment name
  lowercased without the `HINDSIGHT_API_` prefix (`llm_provider`); a nested
  key spelling is flattened to that flat form, and a list value is refused at
  load rather than ignored.