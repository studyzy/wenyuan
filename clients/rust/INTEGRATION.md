# Integrating this client into a program

`wenyuan-client` is async: every operation is an `async fn` on the generated
[`Client`](https://docs.rs/progenitor-client), and the calls have to be driven
by an async runtime. A program that is already async awaits them directly. A
program that is not — a command-line tool whose command handlers are ordinary
blocking functions — has two ways to bridge the gap, and this guide covers
both, with the trade-off between them.

The examples here compile as part of the crate: `cargo build --examples`
builds `examples/sync_cli.rs` (Option 1) and `examples/async_cli.rs`
(Option 2). Run either against a deployment with

```sh
WENYUAN_API_URL=http://localhost:18888 cargo run --example sync_cli
WENYUAN_API_URL=http://localhost:18888 cargo run --example async_cli
```

## Option 1: a thin synchronous wrapper

Keep the command handlers synchronous and give the wrapper a `tokio` runtime it
uses to `block_on` each call:

```rust
use wenyuan_client::{types, Wenyuan};

struct ApiClient {
    client: Wenyuan,
    runtime: tokio::runtime::Runtime,
}

impl ApiClient {
    fn new() -> Result<Self, Box<dyn std::error::Error>> {
        let runtime = tokio::runtime::Runtime::new()?;
        let client = Wenyuan::new()?; // WENYUAN_API_URL, else localhost:18888
        Ok(Self { client, runtime })
    }

    fn list_banks(&self) -> Result<Vec<types::BankListItem>, Box<dyn std::error::Error>> {
        let banks = self
            .runtime
            .block_on(async { self.client.client().list_banks(None, None, None, None).await })?;
        Ok(banks.into_inner().banks)
    }

    fn create_bank(&self, bank_id: &str, name: &str) -> Result<(), Box<dyn std::error::Error>> {
        let request = types::CreateBankRequest {
            name: Some(name.to_owned()),
            ..Default::default()
        };
        self.runtime
            .block_on(async { self.client.create_bank(bank_id, &request).await })?;
        Ok(())
    }
}
```

Two rules make this wrapper behave:

- **One runtime for the wrapper, not one per call.** `Runtime::new` starts a
  worker pool; creating it inside a method would pay that startup on every
  command. Hold it as a field.
- **`block_on` only from a thread the runtime does not own.** Calling it from
  inside another `block_on` panics (`Cannot start a runtime from within a
  runtime`). The wrapper is the boundary: everything above it is synchronous,
  everything below it is async.

This shape is the migration path. It is also the one to reach for when the
surrounding program is blocking for reasons outside your control — a plugin
host, a synchronous framework's request handler.

## Option 2: go async end to end

Make the entry point async, and let every command await in place:

```rust
use wenyuan_client::Wenyuan;

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let client = Wenyuan::new()?;

    for bank in client
        .client()
        .list_banks(None, None, None, None)
        .await?
        .into_inner()
        .banks
    {
        println!("{}", bank.bank_id);
    }

    Ok(())
}
```

One runtime serves the whole process. Commands that are independent of each
other can be joined (`tokio::join!`, `futures::future::try_join_all`) instead of
run one after another, and a long write submitted with `async_: true` does not
freeze the CLI while the server works. This is the shape to choose for a new
program; the wrapper in Option 1 exists for programs that cannot move to it.

## Which shape to pick

| | Option 1 (runtime wrapper) | Option 2 (fully async) |
| --- | --- | --- |
| Command handlers | stay synchronous | become `async fn` |
| Runtimes started | one per wrapper instance | one per process |
| Independent calls | run one at a time | can be joined |
| Reach for it when | the surrounding program is blocking | the program is new, or can move |

## Mapping commands onto the generated client

The hand-written handle covers the core operations; everything else is a method
on the generated client reached through `.client()`. The bindings most often
needed when porting a command layer:

| Command layer says | This crate offers |
| --- | --- |
| create or replace a bank | `Wenyuan::create_bank(bank_id, &CreateBankRequest)` |
| list banks | `client.list_banks(limit, offset, q, auth)` |
| bank settings | `client.get_bank_config` / `update_bank_config` / `reset_bank_config` |
| write memories | `Wenyuan::retain(bank_id, &RetainRequest)` (set `async_: true` to queue) |
| search memories | `Wenyuan::recall(bank_id, &RecallRequest)` |
| synthesize an answer | `Wenyuan::reflect(bank_id, &ReflectRequest)` |
| watch queued work | `client.list_operations` / `get_operation_status` |
| delete a bank | `client.delete_bank(bank_id, auth)` |

Arguments that are not part of the request body (pagination bounds, filters)
are plain `Option<...>` parameters; the `authorization` parameter is
`Option<Option<&str>>`, where the outer option means "the parameter is present"
and the inner one means "it has a value". The handle fills both in from the
credential given to `with_api_key`, so callers driving the generated client
directly pass `None` unless they carry their own credential.

## Retries

Neither the handle nor the generated client retries a request or backs off
between attempts: one call is one request, and a failure is returned to the
caller as it came. A program that wants retries should implement them around
the call, where it can decide which failures are worth repeating.