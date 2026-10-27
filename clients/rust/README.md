# WenYuan Rust client

A Rust client for the WenYuan agent-memory HTTP API: banks, retain, recall and
reflect, plus the rest of the operations the service documents.

The type-level API is generated at build time from this repository's OpenAPI
document (`clients/openapi.json`), so the client always matches the server it
ships beside. On top of it, a small hand-written wrapper (`src/lib.rs`) adds the
deployment's base URL, an optional credential, and one call per core operation.

## Install

The crate is not published to crates.io; depend on it by path from inside this
repository, or from a checkout of it:

```toml
[dependencies]
wenyuan-client = { path = "clients/rust" }
tokio = { version = "1", features = ["rt-multi-thread", "macros"] }
```

Building the crate generates the client from `clients/openapi.json`, which is
itself committed, so no network access is needed to build.

`reqwest` is built without a TLS backend by default (the API is normally
reached over plain HTTP inside a deployment). For an `https` base URL, enable
the feature:

```toml
wenyuan-client = { path = "clients/rust", features = ["rustls-tls"] }
```

## Connecting

```rust
use wenyuan_client::Wenyuan;

// Base URL from WENYUAN_API_URL, falling back to http://localhost:18888.
let client = Wenyuan::new()?;
```

`WENYUAN_API_URL` is the deployment contract's spelling for the service URL,
so it keeps that name. The other constructors are:

| Call | Use |
| --- | --- |
| `Wenyuan::new()` | `WENYUAN_API_URL`, else `http://localhost:18888` |
| `Wenyuan::with_base_url(url)` | an explicit URL |
| `Wenyuan::with_base_url_and_user_agent(url, ua)` | as above, identifying your program to the server |
| `.with_api_key(key)` | send `Authorization: <key>` on every request |
| `.client()` | the generated client, for operations without a convenience method |

Driving the generated client directly, without the handle:

| Call | Use |
| --- | --- |
| `default_client(base_url)` | a generated `Client` with this crate's `User-Agent` |
| `client_with_user_agent(base_url, ua)` | as above, with your identifier in the header too |
| `reqwest_client_with_user_agent(ua)` | just the `reqwest::Client`, to pass to `Client::new_with_client` |

The `User-Agent` a caller supplies is followed by the client's own
(`my-agent/2.1.0 wenyuan-client-rust/0.1.0`), so a deployment behind a
`User-Agent` filter accepts the traffic and the server still learns the client
version. Neither the handle nor the generated client retries a request.

## Example

```rust
use wenyuan_client::{types, Wenyuan};

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let client = Wenyuan::new()?.with_api_key("shared-secret");

    // Create the bank (or replace its profile if it already exists).
    client
        .create_bank(
            "notes",
            &types::CreateBankRequest {
                name: Some("Notes".to_owned()),
                mission: Some("keep decisions".to_owned()),
                ..Default::default()
            },
        )
        .await?;

    // Retain: extraction and embedding happen server-side.
    client
        .retain(
            "notes",
            &types::RetainRequest {
                items: vec![types::MemoryItem {
                    content: types::Content::Variant0("Schema stays a superset.".to_owned()),
                    context: None,
                    document_id: None,
                    entities: None,
                    metadata: None,
                    observation_scopes: None,
                    resolve_entities: true,
                    strategy: None,
                    tags: None,
                    timestamp: None,
                    update_mode: None,
                }],
                async_: false,
                document_tags: None,
                operation_id: None,
            },
        )
        .await?;

    // Recall: retrieval, fused and re-ranked server-side.
    let recalled = client
        .recall(
            "notes",
            &types::RecallRequest {
                query: "what must the schema stay?".to_owned(),
                max_tokens: 4096,
                trace: false,
                prefer_observations: false,
                tags_match: types::TagsMatch::Any,
                budget: None,
                include: None,
                query_timestamp: None,
                types: None,
                tags: None,
                tag_groups: None,
                min_scores: None,
                temporal_window: None,
            },
        )
        .await?;
    for memory in recalled.into_inner().results {
        println!("{}: {}", memory.id, memory.text);
    }

    // Reflect: a synthesized answer with its evidence.
    let reflected = client
        .reflect(
            "notes",
            &types::ReflectRequest {
                query: "what must the schema stay?".to_owned(),
                max_tokens: 4096,
                tags_match: types::TagsMatch::Any,
                apply_all_directives: false,
                exclude_mental_models: false,
                budget: None,
                context: None,
                exclude_mental_model_ids: None,
                fact_types: None,
                include: None,
                reflect_search_observations_include_entities: None,
                reflect_search_observations_max_tokens: None,
                response_schema: None,
                tag_groups: None,
                tags: None,
            },
        )
        .await?;
    println!("{}", reflected.into_inner().text);

    Ok(())
}
```

Long-running writes can be submitted in the background by setting
`RetainRequest::async_` to `true`; the response then carries an operation id to
poll through the generated operations endpoints.

## Regeneration

```sh
cd clients/rust && cargo build        # or, from the repo root:
scripts/generate_clients.sh rust
```

`build.rs` reads `clients/openapi.json` on every build in which it changed
(`cargo:rerun-if-changed`), rewrites it into the shape the generator accepts,
runs `progenitor` over it, and writes the result into `OUT_DIR`, which
`src/lib.rs` includes. Nothing is generated into the source tree, so a spec
change shows up as a normal build.

The document is OpenAPI 3.1 while the generator speaks 3.0, and its schemas use
constructs the generator cannot model. `build.rs` therefore makes six
documented rewrites before generating, each with the reason it exists:

1. **Version downgrade** — the generator's parser is the 3.0 object model and
   rejects a 3.1 document outright.
2. **`anyOf` with `null` → `nullable`** — the 3.1 spelling of an optional value;
   left alone it becomes an untagged union type that does not round-trip
   `null`.
3. **String-only unions collapse to `string`** — otherwise the generator emits
   a struct that flattens a primitive and fails at runtime with
   `can only flatten structs and maps (got a string)`.
4. **Same-titled inline enums are renamed** — the generator names an inline
   enum after the field's title, so a second field with the same title reuses
   the first type and rejects its own documented values.
5. **`multipart/form-data` operations are dropped** — the generator models JSON
   bodies only and cannot carry a file upload.
6. **Bodyless error responses are dropped** — the generator allows one error
   type per operation, and the typed error every operation documents is worth
   more than an empty one.

Consequences of 5 and 6: the client covers 75 of the document's 77 paths and 96
of its 99 operations; the three omitted operations are the file/document
imports. A status the document does not declare still reaches the caller as
`Error::UnexpectedResponse`, with the status code intact.

## Tests

```sh
cargo test
```

The integration test drives the wrapper against a minimal stub HTTP server
bound to a loopback port, so it needs no network and no running service. It
checks that each core call goes out on the documented route with the credential
the handle holds, that the request body carries what the request type says, and
that the documented responses decode.

A second suite exercises the client against a real deployment:

```sh
WENYUAN_API_URL=http://127.0.0.1:18888 cargo test --test live
```

It covers the bank lifecycle, a retain followed by a recall that finds it,
reflect, a tagged write whose tag selects it back out (and an unwritten tag that
selects nothing), an async retain whose operation appears in the bank's queue,
and the `410` the retired profile route answers. It skips, with a printed
notice, unless `WENYUAN_API_URL` is set, so `cargo test` stays offline. A
server started with `WENYUAN_API_DATABASE_URL=memory` is enough: without a
model provider the assertions are structural (an operation succeeded, an id
round-tripped), never about extracted facts or synthesized prose.

## Integrating into a program

Bridging this async client onto a synchronous command-line tool — either with a
`tokio` runtime wrapper or by going async end to end — is covered in
[`INTEGRATION.md`](INTEGRATION.md), with compiling examples in `examples/`
(`cargo build --examples`).