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

One carve-out: `HINDSIGHT_API_*` environment-variable names are preserved
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

## Tests

Each SDK keeps its own tests beside its code. The Go one ships two suites: an
offline unit suite (`make test-sdk-go`) and a live end-to-end suite behind the
`integration` build tag (`make test-sdk-go-integration`, pointed at a running
server with `SDK_API_URL`). See `clients/go/README.md` for what the live suite
asserts and why it needs no model provider.

## Versioning

The SDKs carry this project's own version (`0.1.0`). The API contract version
they generate against is the document's own `info.version`, which tracks the
reference contract, not the SDK release.