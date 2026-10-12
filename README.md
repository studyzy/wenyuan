# wenyuan (文渊)

A Go implementation of the Hindsight agent-memory HTTP API and its PostgreSQL
storage layer.

> 文渊 (wényuān) — "the deep repository of writing", a classical name for a
> place where knowledge is gathered and preserved. The name reflects what the
> service does: a durable memory store that an agent can write to and recall
> from.

[中文文档](README.zh-CN.md)

This project implements an **API-compatible** version of the Hindsight
agent-memory API: existing clients integrate without modification because the
public HTTP surface (routes, JSON field names, status codes, headers) and the
PostgreSQL schema (tables, columns, enums) are preserved. It is written in Go
using the standard library first.

## Scope

Implemented in this slice:

- **Bank lifecycle** — create/list/replace/update/delete, aliases (list/add/
  promote/remove), per-bank configuration, retired-route responses.
- **Retain** — memory writes (sync and async), file uploads, chunk/entity/
  memory persistence, async operation tracking (list/fetch/cancel/retry).
- **Recall** — multi-arm retrieval, fused re-ranking, token-budget selection,
  tag filters, temporal windows, side channels.
- **Reflect** — synthesized answers with cited evidence, execution traces,
  structured output, directives, tool-loop error taxonomy.

Non-goals for this slice: worker/queue consumer, control-plane UI, deployment
packaging, endpoints outside the first slice, and new performance features.

## Requirements

- Go 1.24 or newer
- PostgreSQL with `pgvector` and a full-text search extension (only when using
  the PostgreSQL store; the default in-memory store runs offline)

## Build and test

```sh
make build     # go build ./...
make test      # go test -count=1 ./...
make vet       # go vet ./...
make pgx       # go build -tags pgx ./...   (compiles the PostgreSQL driver path)
make check     # all of the above
```

The default build is standard-library-only and performs no network access. The
PostgreSQL driver (`pgx`) is isolated behind the `pgx` build tag so that the
default build stays dependency-light and offline.

## Running the server

```sh
go run ./cmd/server                     # serves on the configured address
go run ./cmd/server --host 0.0.0.0 --port 8888
```

### Server flags

| Flag | Purpose |
| --- | --- |
| `--host` | Listen host |
| `--port` | Listen port |
| `--workers` | Worker parallelism |
| `--access-log` | Enable request access logging |
| `--proxy-headers` | Honor forwarded/proxy headers |
| `--daemon` | Run in daemon mode |
| `--log-level` | Log verbosity |
| `--ssl-certfile`, `--ssl-keyfile` | TLS certificate and key |

### Admin CLI

```sh
bankctl bank rename --source OLD --to NEW [--dry-run]
bankctl bank archive export --archive FILE [--target ID]
bankctl bank import  --archive FILE [--target ID]
```

## Configuration

Flags take precedence over environment variables, which take precedence over
defaults.

| Variable | Purpose |
| --- | --- |
| `HINDSIGHT_API_HOST` | Listen host |
| `HINDSIGHT_API_PORT` | Listen port |
| `HINDSIGHT_API_DATABASE_URL` | PostgreSQL connection string |
| `HINDSIGHT_API_DATABASE_SCHEMA` | Database schema name |
| `HINDSIGHT_API_VECTOR_EXTENSION` | Vector extension name |
| `HINDSIGHT_API_TEXT_SEARCH_EXTENSION` | Text-search extension name |
| `HINDSIGHT_API_RUN_MIGRATIONS_ON_STARTUP` | Apply migrations at boot |
| `HINDSIGHT_API_MCP_ENABLED` | Enable the MCP namespace |
| `HINDSIGHT_API_ENABLE_BANK_CONFIG_API` | Serve the bank-config write route |
| `HINDSIGHT_API_GZIP_MIN_SIZE` | Response-body gzip size floor (negative disables) |
| `HINDSIGHT_API_DROP_NULL_FIELDS` | Omit null JSON fields in responses |
| `HINDSIGHT_API_RETAIN_BATCH_ENABLED` | Enables the batch retain gate |
| `HINDSIGHT_API_RECALL_STRATEGY_BOOSTS` | Per-arm recall strategy boosts |
| `HINDSIGHT_API_REFLECT_SEARCH_OBSERVATIONS_INCLUDE_ENTITIES` | Reflect observation default |
| `HINDSIGHT_API_REFLECT_SEARCH_OBSERVATIONS_MAX_TOKENS` | Reflect observation token default |

## HTTP API

All bank-scoped routes are under `/v1/default/banks`. An alias may be used
anywhere a bank id is accepted; responses always report the canonical bank id.

### Monitoring

| Method | Path |
| --- | --- |
| GET | `/health` |
| GET | `/health/live` |
| GET | `/health/ready` |
| GET | `/version` |
| GET | `/metrics` |

### Banks

| Method | Path |
| --- | --- |
| GET | `/v1/default/banks` |
| POST | `/v1/default/banks` |
| PUT | `/v1/default/banks/{bank_id}` |
| PATCH | `/v1/default/banks/{bank_id}` |
| DELETE | `/v1/default/banks/{bank_id}` |
| GET | `/v1/default/banks/{bank_id}/aliases` |
| POST | `/v1/default/banks/{bank_id}/aliases` |
| PATCH | `/v1/default/banks/{bank_id}/aliases/{alias}` |
| DELETE | `/v1/default/banks/{bank_id}/aliases/{alias}` |
| GET | `/v1/default/banks/{bank_id}/config` |
| PATCH | `/v1/default/banks/{bank_id}/config` |

The `GET/PUT /v1/default/banks/{bank_id}/profile` and
`POST /v1/default/banks/{bank_id}/background` routes are retired and answer
`410 Gone`, naming the config read/write routes as the replacement.

### Memory operations

| Method | Path |
| --- | --- |
| POST | `/v1/default/banks/{bank_id}/memories` |
| POST | `/v1/default/banks/{bank_id}/files/retain` |
| POST | `/v1/default/banks/{bank_id}/memories/recall` |
| POST | `/v1/default/banks/{bank_id}/reflect` |
| GET | `/v1/default/banks/{bank_id}/operations` |
| GET | `/v1/default/banks/{bank_id}/operations/{operation_id}` |
| DELETE | `/v1/default/banks/{bank_id}/operations/{operation_id}` |
| POST | `/v1/default/banks/{bank_id}/operations/{operation_id}/retry` |

## Storage

The PostgreSQL schema lives in `migrations/`; the same files are embedded under
`internal/storage/embedded/` for `go:embed`. Tables: `banks`, `bank_aliases`,
`documents`, `chunks`, `memory_units`, `entities`, `unit_entities`,
`memory_links`, `entity_cooccurrences`, `async_operations`.

## Layout

```
cmd/server        process entrypoint (HTTP API)
cmd/bankctl       administrative bank command surface
internal/app      application composition and middleware
internal/bankapi  bank lifecycle handlers and routes
internal/bankadmin bank rename / archive commands
internal/config   flag > environment > default resolution
internal/httpx    router, middleware, JSON and error envelopes
internal/memoryops retain / recall / reflect handlers and models
internal/monitoring health, readiness, version, metrics
internal/storage  storage interface, in-memory and PostgreSQL stores
migrations        PostgreSQL schema migrations
```

## Project files

| File | Purpose |
| --- | --- |
| `Makefile` | Build, test, vet, and pgx-build targets |
| `CHANGELOG.md` | Notable changes per release (Keep a Changelog) |
| `CONTRIBUTING.md` | How to propose changes |
| `CODE_OF_CONDUCT.md` | Contributor Covenant v2.1 |
| `SECURITY.md` | How to report a vulnerability privately |
| `.github/workflows/ci.yml` | Build, test, vet, gofmt on every push and PR |
| `.github/ISSUE_TEMPLATE/` | Bug report and feature request forms |
| `docs/ROADMAP.md` | Outstanding work and known limitations (handover) |
| `docs/MIGRATION.md` | Migrating an existing Hindsight bank into Wenyuan |

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Run `make check` before opening a pull
request. Report security issues privately per [SECURITY.md](SECURITY.md).

## License

MIT — see [LICENSE](LICENSE).