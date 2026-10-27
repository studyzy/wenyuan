//! Option 1: keep a synchronous CLI and bridge onto this crate's async client
//! with a `tokio` runtime the wrapper owns.
//!
//! The wrapper holds the runtime and the handle together, so a command stays a
//! plain call that blocks until the answer arrives. Build it with
//! `cargo build --example sync_cli`; run it against a deployment with
//! `WENYUAN_API_URL=http://localhost:18888 cargo run --example sync_cli`.
//!
//! The cost of this shape is one runtime thread parked per client: work that
//! is naturally a sequence of calls pays to switch runtimes on every one. If
//! the program is new, prefer the async shape in `async_cli.rs`.

use wenyuan_client::{types, Wenyuan};

/// A synchronous facade: each method `block_on`s the async call it wraps.
struct ApiClient {
    client: Wenyuan,
    runtime: tokio::runtime::Runtime,
}

impl ApiClient {
    fn new() -> Result<Self, Box<dyn std::error::Error>> {
        let runtime = tokio::runtime::Runtime::new()?;
        // Base URL from WENYUAN_API_URL, falling back to
        // http://localhost:18888.
        let client = Wenyuan::new()?;
        Ok(Self { client, runtime })
    }

    fn list_banks(&self) -> Result<Vec<types::BankListItem>, Box<dyn std::error::Error>> {
        let banks = self.runtime.block_on(async {
            self.client
                .client()
                .list_banks(None, None, None, None)
                .await
        })?;
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

fn main() -> Result<(), Box<dyn std::error::Error>> {
    let api = ApiClient::new()?;

    for bank in api.list_banks()? {
        println!("{}", bank.bank_id);
    }

    api.create_bank("sync-cli-demo", "Sync CLI demo")?;
    println!("created sync-cli-demo");

    Ok(())
}
