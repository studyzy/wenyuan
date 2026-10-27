# Client SDKs

Generated clients for this service's HTTP API, one per language:

| Directory | Language | Generator (pinned) | Package identifier |
|---|---|---|---|
| `go/` | Go | openapi-generator `v7.10.0` (`go`) | `github.com/studyzy/wenyuan/clients/go` (package `wenyuan`) |
| `python/` | Python | openapi-generator `v7.10.0` (`python`) | distribution `wenyuan-client`, import `wenyuan_client` / `wenyuan_client_api` |
| `typescript/` | TypeScript | `@hey-api/openapi-ts` (version pinned in `package.json`) | npm `wenyuan-client` |
| `rust/` | Rust | `progenitor`, run by `build.rs` at build time | crate `wenyuan-client` |

They are *generated from the contract this service serves*, not written against
it by hand: `clients/openapi.json` is the generator input, and every generated
service method comes from an operation in that document.

## Where the generator input comes from

The document served at `/openapi.json` is the reference contract and keeps the
reference's own branding, because a client that fetches it must see the same
document a reference deployment serves (`internal/httpx/openapi.json` is that
document, captured by `scripts/reference_openapi.py`).

`clients/` holds this project's own artifacts, so the generator reads a derived
copy instead — `clients/openapi.json`, produced by
`scripts/derive_client_spec.py`, which rewrites the reference's name to this
project's name, keeping the original's case (`WenYuan` for a capitalized
occurrence, `wenyuan` for a lowercase one, `WENYUAN` for an all-caps one) in
every string *and* every object key.

One carve-out: `WENYUAN_API_*` environment-variable names are preserved
verbatim. They are the deployment contract this service reads, so a client
document that renamed them would name keys no deployment answers. That is the
only place the old name survives in this tree.

`make clients-check` fails when the derived copy no longer matches the served
document; regenerate it alone with `scripts/generate_clients.sh spec`.

## Regenerating

```sh
make clients                      # every SDK
scripts/generate_clients.sh go    # one SDK (spec|go|python|typescript|rust)
```

Requirements: docker (Go and Python), node/npm (TypeScript), cargo (Rust). The
generator versions are pinned — the openapi-generator tag by
`OPENAPI_GENERATOR_VERSION` (default `v7.10.0`), the TypeScript plugin and the
Rust generator crates by their manifests — so regeneration is reproducible.

## Generated versus maintained

Each generator owns only part of its directory. Packaging metadata, the thin
wrapper and the tests are hand-written and survive regeneration:

| SDK | Generator-owned | Maintained |
|---|---|---|
| Go | `api_*.go`, `model_*.go`, `client.go`, `configuration.go`, `response.go`, `utils.go`, `api/` | `go.mod`, `go.sum`, `wenyuan_client.go`, `README.md`, tests |
| Python | `wenyuan_client_api/**` | `wenyuan_client/**`, `pyproject.toml`, `openapi-generator-config.yaml`, `scripts/patch_generated.py`, `README.md`, `tests/**` |
| TypeScript | `generated/**` | `src/**`, `package.json` + `package-lock.json`, `openapi-ts.config.ts`, `tsconfig.json`, `tsup.config.ts`, `vitest.config.ts`, `README.md`, `tests/**` |
| Rust | `$OUT_DIR/wenyuan_client_generated.rs` (build-time, never committed) | `Cargo.toml`, `Cargo.lock`, `build.rs`, `src/**`, `README.md`, `tests/**` |

`scripts/generate_clients.sh` documents the post-generation fixes each generator
needs (a generator wart is patched there, with the reason, rather than edited
silently in the output); the Rust rewrites live in its `build.rs`, because
generation there is a build.

## Coverage of the contract

Go, Python and TypeScript generate a method for every one of the document's 99
operations. Rust covers 96: `progenitor` models JSON request bodies only, so the
three `multipart/form-data` operations — `files/retain`, `document-transfer`
import and `transfer/import` — are dropped from the generated client by
`build.rs` (the download side of `document-transfer` stays). Their absence is
listed in `clients/rust/README.md`; a caller that needs them uses another SDK.

## What the hand-written layer adds

Above the generated methods, each SDK carries the convenience a caller would
otherwise write: one call per multi-step flow, the request shapes the contract's
own option names imply, and the response helpers the reference's clients offer.

| SDK | Shape of the convenience layer |
|---|---|
| Go | `Client` embeds the generated `*APIClient` (every service reachable) and adds `NewClient*`, `TextContent`, and `Retain`/`Recall`/`Reflect`/`CreateBank`/`GetBankConfig` with the response body closed for you |
| Python | the `WenYuan` façade: one method per operation family (version, memories, banks and bank config, mental models, knowledge base, directives, transfers) each with an `a…` async twin, the sub-clients for everything else, `max_attempts` retry for the idempotent calls, `suspend_retains()`, and the recall helpers |
| TypeScript | the `WenYuanClient` class mirroring the same method families, including the transfer flows (submit → poll → download, with a signed URL fetched without credentials), plus the free helper functions (`retain`, `retainBatch`, `retainFiles`, `recall`, `reflect`, `recallResponseToPromptString`, retry helpers) over the same core |
| Rust | the `Wenyuan` handle over the generated client, plus the user-agent constructors; `INTEGRATION.md` and `examples/` show how a synchronous CLI drives it |

The per-language README states which option spelling a given entry point takes
where the two styles differ.

## Tests

Each SDK ships two suites: an offline one (stub/httptest servers only, no
network) and a live one that talks to a running service.

| SDK | Offline | Live |
|---|---|---|
| Go | `make test-sdk-go` | `make test-sdk-go-integration` |
| Python | `cd clients/python && python3 -m unittest discover -s tests` | same, with `WENYUAN_API_URL` set |
| TypeScript | `cd clients/typescript && npx tsc --noEmit && npm test` | same, with `WENYUAN_API_URL` set |
| Rust | `cd clients/rust && cargo test` | same, with `WENYUAN_API_URL` set |

`make test-sdk` runs every offline suite; `make test-sdk-integration` runs every
live suite against `SDK_API_URL` (default `http://127.0.0.1:18888`). A live
suite that finds `WENYUAN_API_URL` unset skips itself rather than failing, so
the offline gate stays usable on a machine with no server. A server started with
`WENYUAN_API_DATABASE_URL=memory` is enough for all of them: the assertions are
structural (an operation succeeded, an id round-tripped, a list carries what was
written, a retired route answers 410, a tag filter selects its own write back
out) rather than about extracted facts or synthesized prose, which need a
retention model.

## Delivery

The SDKs ship as source in this repository. They are **not published** to npm,
PyPI or crates.io: there is no release pipeline, no registry scope and no
version bump automation, so the manifests carry the package names a consumer
would use if this repository were vendored or a `go get` target, nothing more.

## Versioning

The SDKs carry this project's own version (`0.1.0`). The API contract version
they generate against is the document's own `info.version`, which tracks the
reference contract, not the SDK release.