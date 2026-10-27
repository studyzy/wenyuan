//! Option 2: make the CLI async end to end.
//!
//! `#[tokio::main]` starts one runtime for the whole program, so every call
//! awaits in place: no per-call runtime switch, and commands that are
//! independent of each other can be joined. This is the shape to choose for a
//! new program.
//!
//! Build it with `cargo build --example async_cli`; run it against a
//! deployment with
//! `WENYUAN_API_URL=http://localhost:18888 cargo run --example async_cli`.

use wenyuan_client::{types, Wenyuan};

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    // Base URL from WENYUAN_API_URL, falling back to
    // http://localhost:18888.
    let client = Wenyuan::new()?;

    let banks = client
        .client()
        .list_banks(None, None, None, None)
        .await?
        .into_inner();
    for bank in banks.banks {
        println!("{}", bank.bank_id);
    }

    // A command sequence is just a sequence of awaits: the runtime stays up
    // for the process, so nothing is started or stopped per call.
    let bank = "async-cli-demo";
    client
        .create_bank(
            bank,
            &types::CreateBankRequest {
                name: Some("Async CLI demo".to_owned()),
                ..Default::default()
            },
        )
        .await?;
    client
        .retain(
            bank,
            &types::RetainRequest {
                items: vec![types::MemoryItem {
                    content: types::Content::Variant0(
                        "The async CLI keeps one runtime for the process.".to_owned(),
                    ),
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

    let recalled = client
        .recall(
            bank,
            &types::RecallRequest {
                query: "what does the async CLI keep?".to_owned(),
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

    Ok(())
}
