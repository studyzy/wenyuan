# AGENTS.md

Guidance for AI coding agents working in this repository.

## What this project is

`wenyuan` (文渊) is a Go implementation of the **Hindsight agent-memory HTTP API**
and its PostgreSQL storage layer.

The point of the project is **API compatibility**: existing Hindsight clients
integrate without modification because the public HTTP surface and the
PostgreSQL schema are preserved. Compatibility is the contract — treat it as
more important than internal elegance.

Where the two disagree, the upstream Hindsight behavior wins, and the
disagreement is a bug.

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
cmd/bankctl/           admin CLI (bank operations, migration)
internal/app/          composition root: wires config, storage, routes
internal/bankapi/      bank lifecycle HTTP handlers + route table
internal/memoryops/    retain / recall / reflect HTTP handlers + route table
internal/httpx/        router, JSON helpers, error envelope, middleware
internal/config/       config resolution: flags, env, .env, defaults
internal/storage/      storage interfaces + memory + postgres backends
internal/storage/embedded/  embedded copy of the SQL migrations (see below)
internal/monitoring/   health / metrics / version endpoints
internal/hindsightarchive/   Hindsight export format parsing (zip)
internal/migration/hindsight/  import an exported archive into this storage
internal/bankadmin/    bank admin operations shared by CLI
migrations/            numbered SQL migrations — authoritative but NOT what runs
docs/                  ROADMAP.md (handover) and MIGRATION.md
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

## Storage layer conventions

The storage package has two backends behind one interface (`internal/storage/store.go`):

- **memory** — in-memory, used by the default test path (no database needed)
- **postgres** — the real backend, compiled under the `pgx` tag

Any new storage method must be implemented in **both** backends, or the
interface will not compile. Files are split by area:

- `memory_banks.go` / `postgres_banks.go` — bank lifecycle
- `memory_retain.go` / `postgres_retain.go` — writes
- `memory_recall.go` / `postgres_recall.go` — retrieval
- `memory_reflect.go` / `postgres_reflect.go` — reflection
- `memory_operations.go` / `postgres.go` — async operations

### Migrations — read this before touching SQL

**The migration series is generated, not written.** The PostgreSQL schema this
project serves *is* the upstream Hindsight schema, so the series is emitted from
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

`docs/SCHEMA-ALIGNMENT.md` is the standing comparison against the live
reference, produced by `scripts/schema_diff.sql` and
`scripts/schema_inventory.py`. It must stay empty: no object missing, extra or
differing. `internal/storage/schema.go` declares the expected table set;
`schema_test.go` cross-checks the declarations against the series.

## Known gaps — do not assume these work

These are real, documented limitations. Check `docs/ROADMAP.md` for the full
list with file:line evidence before building on any of this.

- **Semantic search is a placeholder.** `internal/storage/memory_recall_arms.go`
  computes `semanticScore` / `keywordScore` deterministically; there is no
  embedding column and no vector similarity. Adding real vectors needs a new
  migration, the `vector` extension, an embedding provider, and a change to the
  semantic arm in `postgres_recall.go`.
- **Async is not actually async.** `async=true` on a memory write returns an
  operation record, but there is no worker consuming the queue and no
  `cmd/worker`. Writes complete synchronously.
- **No export / import HTTP endpoints.** The archive parsing (`hindsightarchive`)
  and import (`migration/hindsight`) exist and are exercised by `bankctl`; the
  HTTP routes do not.
- **Storage is not wire-compatible with upstream Hindsight.** Moving existing
  data requires `bankctl bank migrate` via the official export format. You
  cannot point this server at an existing Hindsight database.
- **No LLM / provider integration anywhere** in the project.

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
- Go version: `1.24` (see `go.mod`)
- Indent: tabs in Go, 2 spaces elsewhere (`.editorconfig`)
- Line endings: LF, final newline required
- Linters: `errcheck`, `govet`, `ineffassign`, `staticcheck`, `unused`, `gofmt`,
  `goimports`, `misspell`, `unconvert`, `bodyclose` (`.golangci.yml`)
- `goimports` groups this module under `local-prefixes: github.com/studyzy/wenyuan`
- Config resolution precedence lives in `internal/config/` — check it before
  adding a new config key