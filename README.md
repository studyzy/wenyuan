# wenyuan (文渊)

A Go implementation of the reference implementation's agent-memory HTTP API
and its PostgreSQL storage layer.

> 文渊 (wényuān) — "the deep repository of writing", a classical name for a
> place where knowledge is gathered and preserved. The name reflects what the
> service does: a durable memory store that an agent can write to and recall
> from.

[中文文档](README.zh-CN.md)

This project implements an **API-compatible** version of the reference
implementation's agent-memory API: clients written against the reference
integrate without modification because the public HTTP surface (routes, JSON
field names, status codes, headers) and the PostgreSQL schema (tables, columns,
enums) are preserved. It is written in Go using the standard library first.

## What is implemented

- **Bank lifecycle** — create/list/replace/update/delete, aliases (list/add/
  promote/remove), per-bank configuration, retired-route responses.
- **Retain** — memory writes (sync and async), file uploads with attachments,
  chunk/entity/memory persistence, async operation tracking (list/fetch/cancel/
  retry) with a real queue consumer (`cmd/worker`: leases, backoff, retries).
- **Recall** — multi-arm retrieval with real vector search (pgvector), fusion
  re-ranking with an optional cross-encoder reranker, token-budget selection,
  tag filters and scopes, temporal windows parsed from the query.
- **Reflect** — a bounded retrieval-and-synthesis tool loop with cited
  evidence, execution traces, structured output, directives.
- **Memory sub-surface** — list/get/patch memories, per-memory history and
  observations, dry-run extraction, prompt preview.
- **Mental models** — CRUD, refresh triggers (cron, tags-match, manual),
  scheduled and on-write refresh through the worker, refresh history,
  dry-run refresh.
- **Knowledge base** — folders/pages tree, hybrid text + vector search,
  export.
- **Directives** — CRUD, surfaced into the reflection loop.
- **Read surface** — documents, chunks, tags, entities and both memory and
  entity graphs.
- **Observations & consolidation** — consolidation passes, strategy preview,
  recovery, stale-observation cleanup.
- **Observability** — bank stats, memories timeseries, audit trail,
  model-request trace, OTel tracing, Prometheus-style metrics.
- **Webhooks** — registration CRUD plus delivery with retries and delivery
  records.
- **Transfer** — bank export/import over HTTP and via `bankctl`, document
  transfer, whole-bank clone, attachment round-trip.
- **MCP** — JSON-RPC endpoint under `/mcp` (feature-gated).
- **Model providers** — chat/embedding/reranker ports with adapters for
  OpenAI-compatible endpoints, Anthropic, Cohere/Gemini/TEI embeddings and
  more; a deterministic no-model selection when nothing is configured.
- **Background maintenance** — retention sweeps, stranded-work recovery,
  scheduled mental-model refreshes; tenant/extension schema support.
- **Object storage** — S3-compatible backend for stored files.
- **Frontend** — a Next.js web UI under `frontend/` (the served API and
  `wenyuan-admin ui` drive it).
- **CLIs** — `bankctl` (admin: migrations, backup/restore, export/import,
  repair) and `wenyuan-admin` (client: every API family, filesystem mirror,
  explore TUI).

**Intentionally out of scope:** OAuth provider integration and GCS / Azure
object stores (the S3-compatible backend covers object storage).

## Requirements

- Go 1.25 or newer
- PostgreSQL with `pgvector` and a full-text search extension (only when using
  the PostgreSQL store; the default in-memory store runs offline)

## Build and test

```sh
make build     # go build ./...
make test      # go test -count=1 ./...
make vet       # go vet ./...
make pgx       # go build -tags pgx ./...   (compiles the PostgreSQL driver path)
make check     # all of the above + lint
make install   # build server, worker, bankctl and wenyuan-admin into bin/ (pgx-enabled)
```

The default build is standard-library-only and performs no network access. The
PostgreSQL driver (`pgx`) is isolated behind the `pgx` build tag so that the
default build stays dependency-light and offline. `make check` is the same
gate CI runs; `make test-pg` runs the PostgreSQL-backed suite against a live
database (`WENYUAN_TEST_DATABASE_URL`), and `make parity` runs the upstream
system-test stories against this build.

## Running the server

```sh
make db-up                              # this project's own PostgreSQL on 127.0.0.1:15432
export WENYUAN_API_DATABASE_URL=postgresql://wenyuan:wenyuan@127.0.0.1:15432/wenyuan
go run -tags pgx ./cmd/server           # serves on the configured address
go run -tags pgx ./cmd/server --host 0.0.0.0 --port 18888
go run -tags pgx ./cmd/worker           # async operation consumer
```

The `pgx` tag matters whenever a `database_url` is configured: without it the
binary has no PostgreSQL driver and refuses to start.

Or with Docker for the dependency services only — PostgreSQL plus the two TEI
model sidecars; the server, worker and bankctl run on the host:

```sh
make db-up                              # PostgreSQL on 127.0.0.1:15432
make tei-up                             # TEI embedding (18080) and reranker (18081)
```

The TEI first boot downloads the models into the compose volumes; later boots
reuse them.

### Control-plane frontend

```sh
make frontend-build                     # production build of frontend/
make frontend-run                       # serves on 19999 (Next.js production mode)
```

The frontend talks to the API through its BFF routes, which read
`WENYUAN_CP_DATAPLANE_API_URL` (default `http://127.0.0.1:8000`; point it at
the wenyuan API, e.g. `http://127.0.0.1:18888`) and
`WENYUAN_CP_DATAPLANE_API_KEY`. Run the production build: dev-mode HMR hangs
Origin-bearing WebSocket upgrades in this environment and the dashboard never
finishes loading.

Both variables are read on the server at request time, so they must be set on
the **standalone server process**, not baked into the browser bundle. When the
API has a tenant key configured (`tenant_api_key` in the configuration file),
the frontend must send the same key or every BFF route answers 401 and the
dashboard renders empty. A frontend built before an environment variable was
renamed keeps reading the old name from its compiled bundle — rebuild with
`make frontend-standalone` after pulling; the server logs
`[Control Plane] Connecting to dataplane at: …` once at startup, which is the
quickest way to confirm which URL it is using.

The console has no passphrase of its own: sign in with the API's data key
(`tenant_api_key`) for the console and the API, or with the operator key
(`admin_api_key`) when you need tenant administration (only that session may call
`/api/admin/**`). A deployment in `tenant_mode: multi` ignores the data key, so
signing in with it is refused and each tenant signs in with its own `wyk_...`
key.

The frontend's own environment is generated from this same configuration file —
see `make service-env` below — so a key is configured once, in
`etc/wenyuan.yaml`.

## Installing as a service (systemd)

For a host that keeps the API and the frontend running across sessions and
reboots, install both as systemd units. The templates live in
`deploy/systemd/`; `make service-install` substitutes this checkout's path
and the invoking account and copies them to `/etc/systemd/system/`. It needs
`sudo`, and `node` on `PATH` for the frontend unit.

```sh
make db-up                              # dependencies first: PostgreSQL + TEI sidecars
make install                            # bin/wenyuan-server, worker, bankctl, wenyuan-admin
make frontend-standalone                # only if the control-plane UI is wanted
make service-install                    # write the unit files
make service-enable                     # start now and at every boot
```

Then:

| Command | Effect |
| --- | --- |
| `make service-status` | state of both units plus `GET /health` on 18888 |
| `make service-restart` | restart both — run after `make install` or `make frontend-standalone` |
| `make service-stop` | stop both, keeping the boot links |
| `make service-start` | start both again |
| `make service-disable` | stop both and stop starting them at boot |
| `make service-logs` | follow `/var/log/wenyuan-server.log` (`UNIT=frontend` for the UI) |
| `make service-uninstall` | disable and remove the unit files; data, builds and logs stay |

What the units assume:

- **The API reads its configuration from the checkout.** `WorkingDirectory` is
  the repository root, so `etc/wenyuan.yaml`, a relative `.env` and the
  relative paths inside them resolve as they do for `make run`. Moving the
  checkout means reinstalling (`make service-install`).
- **The binaries are prebuilt.** `ExecStart` points at `bin/wenyuan-server`,
  produced by `make install`; a service installed without it exits and is
  restarted every few seconds until the binary exists.
- **The frontend's environment is generated, never hand-edited.**
  `make service-env` renders `/etc/default/wenyuan-frontend` from
  `etc/wenyuan.yaml` — the API URL from `port`, the management URL from
  `admin_listen`, and the two keys from `tenant_api_key` / `admin_api_key` —
  and `make service-restart` runs it first, so editing a key in one place is
  enough. (The same values can still be set explicitly in that file when the
  frontend runs somewhere the generator cannot read the backend configuration.)
- **Both units bind all interfaces.** The API listens on `0.0.0.0:18888`
  (from `host: 0.0.0.0` in the configuration file) and the frontend on
  `0.0.0.0:19999`, so the dashboard is reachable from another host — restrict
  the cloud security group if that is not wanted. `make frontend-run` binds
  `127.0.0.1` only.
- **Both restart on failure** (`Restart=always`, 3 s backoff) and log to
  `/var/log/wenyuan-server.log` and `/var/log/wenyuan-frontend.log`. The
  frontend unit has `Requires=wenyuan.service`, so it stops if the API is
  stopped.

The dependency services (PostgreSQL, the TEI sidecars) are not covered by these
units: they are the compose stack, started with `make db-up` and `make tei-up`.
If they are down, the API still starts but `GET /health` reports the database
as disconnected.

### Server flags

| Flag | Purpose |
| --- | --- |
| `--host` | Listen host |
| `--port` | Listen port |
| `--workers` | Worker parallelism |
| `--access-log` / `--no-access-log` | Enable/disable request access logging |
| `--proxy-headers` | Honor forwarded/proxy headers |
| `--forwarded-allow-ips` | Comma-separated IPs trusted with proxy headers |
| `--daemon` | Run in daemon mode |
| `--log-level` | Log verbosity |
| `--ssl-certfile`, `--ssl-keyfile` | TLS certificate and key |

`--reload` and `--idle-timeout` are accepted for compatibility and are no-ops
in this build.

### Admin CLI

```sh
bankctl bank rename --from OLD --to NEW [--dry-run]
bankctl bank export-bank --bank ID --output FILE [--include-history]
bankctl bank import-bank --archive FILE [--target-bank ID] [--include-history]
bankctl bank backup [--output] FILE
bankctl bank restore [--input] FILE [--yes]
bankctl bank run-db-migration [--embedding-dimension N] [--skip-extension-reconcile]
bankctl bank repair-bank (--bank ID | --all) [--dry-run]
bankctl bank worker-status
bankctl bank decommission-worker [--worker] ID [--yes]
bankctl bank decommission-workers [--yes]
bankctl bank migrate (--archive FILE | --source-url URL --source-bank ID) [--bank-id ID] [--replace|--append|--incremental] [--dry-run]
```

Every database command takes `--schema/-s` to point at a schema other than the
configured one, and `--schema`-less migrations cover the base schema plus every
schema the tenant extension reports.

### Client CLI

`wenyuan-admin` is the client command-line tool for the served API. It is
built from `cmd/wenyuan` into `bin/` by `make install`.

```sh
wenyuan-admin configure --api-url http://localhost:18888 [--api-key KEY]
wenyuan-admin bank list
wenyuan-admin memory retain BANK_ID "text"
wenyuan-admin memory recall BANK_ID "query"
wenyuan-admin knowledge-base tree BANK_ID
wenyuan-admin fs mount BANK_ID /path/to/folder   # live markdown mirror
wenyuan-admin explore                            # interactive TUI
wenyuan-admin ui                                 # launch the web frontend
```

Output is `pretty` by default; `-o json` / `-o yaml` switch to machine
formats. Configuration lives in `~/.wenyuan/config` with named profiles in
`~/.wenyuan/cli-profiles/`; `WENYUAN_API_URL` and `WENYUAN_API_KEY`
override both.

## Configuration

Precedence, highest first: flags, then the `.env` file (a discovered `.env` is
authoritative over the environment, the reference's own entrypoint rule), then
the rest of the process environment, then the configuration file, then the
defaults.

```sh
wenyuan                                  # reads ./.env, then ./etc/wenyuan.yaml
WENYUAN_API_CONFIG_FILE=/etc/wenyuan/prod.yaml wenyuan   # named file, must exist
```

The configuration file is YAML, read from `etc/wenyuan.yaml` relative to the
working directory — `/etc/wenyuan.yaml` inside the container image. It answers
any key the environment answers, spelled without the `WENYUAN_API_` prefix and
lowercased; a nested spelling is accepted and flattened to the same key (`llm:
{provider: openai}` *is* `llm_provider: openai`), both spellings must agree if
both appear, and a list value is refused because an environment value cannot
carry one. The checked-in template is `etc/example.wenyuan.yaml`: copy it to
`etc/wenyuan.yaml` and uncomment what the deployment needs — the real file
carries real values and is not checked in. A file named by
`WENYUAN_API_CONFIG_FILE` must exist and parse; an absent `etc/wenyuan.yaml`
changes nothing.

| Variable | Purpose |
| --- | --- |
| `WENYUAN_API_CONFIG_FILE` | Path of the YAML configuration file (default `etc/wenyuan.yaml`) |
| `WENYUAN_API_HOST` | Listen host |
| `WENYUAN_API_PORT` | Listen port |
| `WENYUAN_API_DATABASE_URL` | PostgreSQL connection string |
| `WENYUAN_API_DATABASE_SCHEMA` | Database schema name |
| `WENYUAN_API_VECTOR_EXTENSION` | Vector extension name |
| `WENYUAN_API_TEXT_SEARCH_EXTENSION` | Text-search extension name |
| `WENYUAN_API_RUN_MIGRATIONS_ON_STARTUP` | Apply migrations at boot |
| `WENYUAN_API_MCP_ENABLED` | Enable the MCP namespace |
| `WENYUAN_API_ENABLE_BANK_CONFIG_API` | Serve the bank-config write route |
| `WENYUAN_API_GZIP_MIN_SIZE` | Response-body gzip size floor (negative disables) |
| `WENYUAN_API_RETAIN_BATCH_ENABLED` | Enables the batch retain gate |
| `WENYUAN_API_RECALL_STRATEGY_BOOSTS` | Per-arm recall strategy boosts |
| `WENYUAN_API_REFLECT_SEARCH_OBSERVATIONS_INCLUDE_ENTITIES` | Reflect observation default |
| `WENYUAN_API_REFLECT_SEARCH_OBSERVATIONS_MAX_TOKENS` | Reflect observation token default |

### Model providers

Model access is configured per scope with environment variables that follow
the reference's naming, so upstream documentation applies unchanged:

- Chat: `WENYUAN_API_RETAIN_LLM_*`, `WENYUAN_API_REFLECT_LLM_*`,
  `WENYUAN_API_MENTAL_MODEL_REFRESH_LLM_*` (`_PROVIDER`, `_API_KEY`,
  `_MODEL`, `_BASE_URL`, retry/backoff knobs, ...).
- Embeddings: `WENYUAN_API_EMBEDDINGS_<VENDOR>_*` (openai, cohere, gemini,
  tei, onnx, ...).
- Reranking: `WENYUAN_API_RERANKER_*` (cohere, openrouter, ...).

Unset means the deterministic no-model selection is used, which keeps memory
writes and reflection working offline. `docs/CONFIGURATION.md` is the
generated full key table — every environment variable with its file spelling,
reference default and status — and `.env.example` ships the same surface in
dotenv form; regenerate both with `scripts/render_config_reference.py`.

## HTTP API

All bank-scoped routes are under `/v1/default/banks`. An alias may be used
anywhere a bank id is accepted; responses always report the canonical bank id.
The served surface is pinned by tests (`internal/app/route_coverage_test.go`)
and matches the reference 100/100; the machine-readable contract is
`internal/httpx/openapi.json`.

| Family | Routes (representative) |
| --- | --- |
| Monitoring | `GET /health`, `/health/live`, `/health/ready`, `/version`, `/metrics` |
| Banks | `GET/POST /v1/default/banks`, `GET/PUT/PATCH/DELETE .../{bank_id}`, aliases, config (`GET/PATCH/DELETE`) |
| Memory operations | `POST .../memories`, `DELETE .../memories` (clear), `POST .../files/retain`, `POST .../memories/recall`, `POST .../reflect` |
| Memory sub-surface | `GET .../memories/list`, `GET/PATCH .../memories/{id}`, `.../history`, `DELETE .../memories/{id}/observations`, `POST .../memories/dry-run-extract`, `POST .../prompts/preview` |
| Async operations | `GET .../operations`, `GET/DELETE .../operations/{id}`, `DELETE .../operations/{id}/delete`, `POST .../operations/{id}/retry` |
| Mental models | `GET/POST .../mental-models`, `GET/PATCH/DELETE .../mental-models/{id}`, `.../history`, `.../refresh`, `.../dry-run-refresh`, `.../clear` |
| Knowledge base | `GET .../knowledge-base/tree`, `POST .../folders`, `POST .../pages`, `GET .../export`, `GET .../search`, `GET/PATCH/DELETE` nodes |
| Directives | `GET/POST .../directives`, `GET/PATCH/DELETE .../directives/{id}` |
| Read surface | `GET/PATCH/DELETE .../documents/{id}`, `GET .../documents/{id}/chunks`, `POST .../documents/{id}/reprocess`, `GET .../tags`, `GET .../graph`, `GET .../entities`, `GET .../entities/graph`, `POST .../entities/{id}/regenerate`, `GET /v1/default/chunks/{id}` |
| Observations & consolidation | `DELETE .../observations`, `GET .../observations/scopes`, `POST .../consolidate`, `POST .../consolidation/recover`, `POST .../consolidation-strategies/preview` |
| Reporting | `GET .../stats`, `GET .../stats/memories-timeseries`, `POST .../health/llm`, `GET .../audit-logs` (+`/stats`), `GET .../llm-requests` (+`/stats`) |
| Webhooks | `GET/POST .../webhooks`, `GET/PATCH/DELETE .../webhooks/{id}`, `GET .../webhooks/{id}/deliveries` |
| Transfer | `POST .../import`, `GET .../export`, `POST .../transfer/export`, `POST .../transfer/import`, `POST .../document-transfer` (+`/export`), `POST .../clone`, `GET .../attachments/{id}`, `GET /v1/default/files/download/{...}` |
| MCP | `POST /mcp` (JSON-RPC, gated by `WENYUAN_API_MCP_ENABLED`) |
| Contract | `GET /v1/bank-template-schema`, `GET /v1/version` |

`GET/PUT /v1/default/banks/{bank_id}/profile` and
`POST /v1/default/banks/{bank_id}/background` are retired and answer
`410 Gone`, naming the config read/write routes as the replacement.

## Storage

The PostgreSQL schema lives in `migrations/` (embedded under
`internal/storage/embedded/` for `go:embed`). The series is **generated** from
the reference database's own catalogue — five files, `0001_extensions` through
`0005_reference_functions` — and matches the upstream schema object for
object: 24 tables, 234 columns, 75 indexes, 191 constraints, 236 functions
(`docs/SCHEMA-ALIGNMENT.md` is the standing comparison and reports zero
differences). Existing databases created by the reference implementation can be
served directly.

## Layout

```
cmd/server        process entrypoint (HTTP API)
cmd/worker        async operation consumer entrypoint
cmd/bankctl       administrative bank command surface
cmd/wenyuan       client CLI entrypoint (wenyuan-admin binary)
cmd/schematool    schema dump helper for the migration generator
internal/app      application composition, worker processors, e2e parity tests
internal/bankapi  bank lifecycle handlers and routes
internal/bankadmin bank rename / archive commands shared by bankctl
internal/cli      client CLI implementation (registry, HTTP client, commands)
internal/config   flag > environment > default resolution
internal/httpx    router, middleware, JSON and error envelopes, OpenAPI
internal/memoryops retain / recall / reflect handlers and models
internal/monitoring health, readiness, version, metrics
internal/provider model-provider port + vendor adapters (openai, anthropic,
                  embed, rerank, none, mock)
internal/storage  storage interface, in-memory and PostgreSQL stores
migrations        PostgreSQL schema migrations
deploy/systemd    systemd unit templates used by `make service-install`
```

See `AGENTS.md` for the full package map and the architectural boundaries.

## Project files

| File | Purpose |
| --- | --- |
| `Makefile` | Build, test, vet, pgx-build, lint and parity targets |
| `CHANGELOG.md` | Notable changes per release (Keep a Changelog) |
| `CONTRIBUTING.md` | How to propose changes |
| `CODE_OF_CONDUCT.md` | Contributor Covenant v2.1 |
| `SECURITY.md` | How to report a vulnerability privately |
| `Dockerfile`, `docker-compose.yml` | Server + worker image; compose dependency services (pgvector, TEI model sidecars) |
| `.github/workflows/ci.yml` | Build, test, vet, gofmt on every push and PR |
| `.github/ISSUE_TEMPLATE/` | Bug report and feature request forms |
| `docs/ROADMAP.md` | Outstanding work and known limitations (handover) |
| `docs/GAPS.md`, `docs/FUNCTIONAL-GAPS.md` | Functional/contract gap ledgers with evidence |
| `docs/PARITY-PLAN.md`, `docs/PARITY-SUITE.md` | Parity strategy and the executable suite |
| `docs/SCHEMA-ALIGNMENT.md` | Standing schema comparison against the reference (must stay empty) |
| `docs/MIGRATION.md` | Migrating an existing bank from the reference implementation into Wenyuan |
| `docs/ARCHITECTURE-REVIEW.md` | Architecture review: layering, coupling, findings |

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Run `make check` before opening a pull
request. Report security issues privately per [SECURITY.md](SECURITY.md).

## License

MIT — see [LICENSE](LICENSE).
