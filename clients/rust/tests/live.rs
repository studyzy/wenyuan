//! The client against a real deployment.
//!
//! Every test here talks to a running server over HTTP, so the suite skips
//! unless `WENYUAN_API_URL` names one:
//!
//! ```sh
//! WENYUAN_API_URL=http://127.0.0.1:18888 cargo test --test live
//! ```
//!
//! A server started with `WENYUAN_API_DATABASE_URL=memory` is enough: no
//! model provider is configured, so the assertions are structural — an
//! operation succeeded, an id round-tripped, a list carries what was written,
//! a tag filter selects its own write back out, a retired route reports that
//! it is gone. Nothing here asserts extracted facts or synthesized prose.
//! Without the variable each test prints a notice and returns, so the default
//! `cargo test` stays offline.

use std::future::Future;
use std::pin::Pin;
use std::time::{SystemTime, UNIX_EPOCH};

use wenyuan_client::{types, Error, Wenyuan};

/// The variable naming the deployment to test against; the same spelling the
/// server and this crate's other clients read.
const ENV_BASE_URL: &str = "WENYUAN_API_URL";

/// The base URL to test against, or `None` when the suite should skip.
fn live_base_url() -> Option<String> {
    match std::env::var(ENV_BASE_URL) {
        Ok(url) if !url.is_empty() => Some(url),
        _ => {
            eprintln!("skipping: set {ENV_BASE_URL} to a running deployment to run this test");
            None
        }
    }
}

/// A bank id this run owns, so a concurrent run cannot collide with it and the
/// name says who left it behind if a run dies mid-test.
fn unique_bank_id(prefix: &str) -> String {
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .expect("the clock reads after the epoch")
        .as_nanos();
    format!("rust-sdk-{prefix}-{}-{nanos}", std::process::id())
}

const MISSION: &str = "serve the integration suite";

fn create_bank_request() -> types::CreateBankRequest {
    types::CreateBankRequest {
        name: Some("Rust SDK integration".to_owned()),
        mission: Some(MISSION.to_owned()),
        ..Default::default()
    }
}

fn memory(content: &str, tags: Option<Vec<String>>) -> types::MemoryItem {
    types::MemoryItem {
        content: types::Content::Variant0(content.to_owned()),
        context: None,
        document_id: None,
        entities: None,
        metadata: None,
        observation_scopes: None,
        resolve_entities: true,
        strategy: None,
        tags,
        timestamp: None,
        update_mode: None,
    }
}

fn retain_request(items: Vec<types::MemoryItem>, asynchronous: bool) -> types::RetainRequest {
    types::RetainRequest {
        items,
        async_: asynchronous,
        document_tags: None,
        operation_id: None,
    }
}

fn recall_request(query: &str, tags: Option<Vec<String>>) -> types::RecallRequest {
    types::RecallRequest {
        query: query.to_owned(),
        max_tokens: 4096,
        trace: false,
        prefer_observations: Some(false),
        tags_match: types::TagsMatch::Any,
        budget: None,
        include: None,
        query_timestamp: None,
        types: None,
        tags,
        tag_groups: None,
        min_scores: None,
        temporal_window: None,
    }
}

fn reflect_request(query: &str) -> types::ReflectRequest {
    types::ReflectRequest {
        query: query.to_owned(),
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
    }
}

type LiveBody = Pin<Box<dyn Future<Output = ()> + Send>>;

/// Create a bank this test owns, run `body` against it, delete the bank, and
/// report a failure in `body`.
///
/// `body` runs in its own task so that a failing assertion — which unwinds —
/// does not skip the deletion: the task's panic is caught by `join`, the bank
/// is removed, and the panic is then re-raised as this test's failure.
async fn with_bank(prefix: &str, body: impl FnOnce(Wenyuan, String) -> LiveBody + Send + 'static) {
    let Some(base) = live_base_url() else {
        return;
    };
    let client = Wenyuan::with_base_url(&base).expect("the base URL is usable");
    let bank_id = unique_bank_id(prefix);
    client
        .create_bank(&bank_id, &create_bank_request())
        .await
        .expect("the bank is created");

    let task_client = client.clone();
    let task_bank = bank_id.clone();
    let task = tokio::spawn(async move { body(task_client, task_bank).await });
    let outcome = task.await;

    // Cleanup runs whichever way the body went, so a failing assertion does
    // not leave the bank behind. Deleting a bank that is already gone is not an
    // error — the route is idempotent — so the result is checked rather than
    // discarded.
    let deletion = client.client().delete_bank(&bank_id, None).await;

    if let Err(join) = outcome {
        std::panic::resume_unwind(join.into_panic());
    }
    deletion.expect("the bank this test owns is deleted");
}

/// `banks` carries the bank whose id is `bank_id`.
fn lists(response: &types::BankListResponse, bank_id: &str) -> bool {
    response.banks.iter().any(|bank| bank.bank_id == bank_id)
}

#[tokio::test]
async fn bank_lifecycle_round_trips() {
    with_bank("lifecycle", |client, bank| {
        Box::pin(async move {
            let listed = client
                .client()
                .list_banks(None, None, None, None)
                .await
                .expect("the bank list reads")
                .into_inner();
            assert!(lists(&listed, &bank), "bank {bank} missing from the list");

            // The configuration view names the bank's mission `reflect_mission`:
            // reading it back is how a client confirms what a bank was created
            // with.
            let config = client
                .client()
                .get_bank_config(&bank, None)
                .await
                .expect("the bank config reads")
                .into_inner();
            assert_eq!(
                config
                    .config
                    .get("reflect_mission")
                    .and_then(|v| v.as_str()),
                Some(MISSION)
            );

            client
                .client()
                .delete_bank(&bank, None)
                .await
                .expect("the bank deletes");

            let after = client
                .client()
                .list_banks(None, None, None, None)
                .await
                .expect("the bank list reads after deletion")
                .into_inner();
            assert!(
                !lists(&after, &bank),
                "bank {bank} is still listed after its deletion"
            );
        })
    })
    .await;
}

#[tokio::test]
async fn a_retained_memory_is_recalled() {
    with_bank("recall", |client, bank| {
        Box::pin(async move {
            let retained = client
                .retain(
                    &bank,
                    &retain_request(
                        vec![memory(
                            "Alice prefers tea over coffee in the afternoon",
                            None,
                        )],
                        false,
                    ),
                )
                .await
                .expect("retain succeeds")
                .into_inner();
            assert!(retained.success);
            assert_eq!(retained.items_count, 1);

            let recalled = client
                .recall(
                    &bank,
                    &recall_request("what does Alice prefer to drink?", None),
                )
                .await
                .expect("recall succeeds")
                .into_inner();
            assert!(
                !recalled.results.is_empty(),
                "recall found nothing for a phrase that was just retained"
            );
            for result in &recalled.results {
                assert!(!result.id.is_empty(), "recall result carries no id");
                assert!(
                    !result.text.trim().is_empty(),
                    "recall result carries no text"
                );
            }
        })
    })
    .await;
}

#[tokio::test]
async fn reflect_answers() {
    with_bank("reflect", |client, bank| {
        Box::pin(async move {
            client
                .retain(
                    &bank,
                    &retain_request(
                        vec![memory(
                            "The migration series is generated from the reference catalogue.",
                            None,
                        )],
                        false,
                    ),
                )
                .await
                .expect("retain succeeds");

            // The assertion is that the call reaches the handler and decodes
            // what comes back: the answer's text belongs to whatever model the
            // deployment configured, not to this client. A model that is
            // unreachable or slow makes reflection answer its documented
            // wall-clock-budget refusal (504), which counts as exercised here.
            match client
                .reflect(
                    &bank,
                    &reflect_request("how is the migration series produced?"),
                )
                .await
            {
                Ok(reflected) => {
                    let _ = reflected.into_inner().text;
                }
                Err(Error::UnexpectedResponse(response))
                    if response.status() == reqwest::StatusCode::GATEWAY_TIMEOUT => {}
                Err(other) => panic!("reflect failed: {other}"),
            }
        })
    })
    .await;
}

#[tokio::test]
async fn a_tag_filter_selects_its_own_write_and_an_unwritten_tag_selects_nothing() {
    with_bank("tags", |client, bank| {
        Box::pin(async move {
            let tag = "rust-sdk-tagged".to_owned();
            client
                .retain(
                    &bank,
                    &retain_request(
                        vec![memory(
                            "The tagged note records that Alice reviews the schema before every release.",
                            Some(vec![tag.clone()]),
                        )],
                        false,
                    ),
                )
                .await
                .expect("tagged retain succeeds");

            let tagged = client
                .recall(
                    &bank,
                    &recall_request("what does the tag record?", Some(vec![tag.clone()])),
                )
                .await
                .expect("tagged recall succeeds")
                .into_inner();
            assert!(
                !tagged.results.is_empty(),
                "the tag {tag} selected nothing back out"
            );

            // A tag nobody wrote must select nothing: that is what proves the
            // filter is the tag and not "any query returns the bank's content".
            let untagged = client
                .recall(
                    &bank,
                    &recall_request(
                        "what does the tag record?",
                        Some(vec!["rust-sdk-never-written".to_owned()]),
                    ),
                )
                .await
                .expect("recall with an unwritten tag succeeds")
                .into_inner();
            assert!(
                untagged.results.is_empty(),
                "an unwritten tag matched {} results",
                untagged.results.len()
            );
        })
    })
    .await;
}

#[tokio::test]
async fn an_async_retain_names_an_operation_in_the_banks_queue() {
    with_bank("async", |client, bank| {
        Box::pin(async move {
            let response = client
                .retain(
                    &bank,
                    &retain_request(vec![memory("queued work", None)], true),
                )
                .await
                .expect("async retain succeeds")
                .into_inner();
            assert!(response.async_, "async retain answered with async=false");
            let operation_id = response
                .operation_id
                .expect("an async retain names an operation");
            assert!(!operation_id.is_empty());

            let operations = client
                .client()
                .list_operations(&bank, None, None, None, None, None, None)
                .await
                .expect("the operation queue reads")
                .into_inner();
            assert!(
                operations
                    .operations
                    .iter()
                    .any(|operation| operation.operation_id.as_deref() == Some(&*operation_id)),
                "operation {operation_id} missing from the bank's queue"
            );

            let status = client
                .client()
                .get_operation_status(&bank, &operation_id, None, None)
                .await
                .expect("the operation status reads")
                .into_inner();
            assert_eq!(status.operation_id, operation_id);
            assert!(
                !matches!(status.status, types::Status::NotFound),
                "the queued operation reports not_found"
            );

            // Nothing to drain: deleting this test's bank takes its queued work
            // with it (the server deletes the bank's operations, and a worker
            // that already claimed one aborts instead of writing), so a
            // leftover operation cannot pick the bank back up after the
            // deletion. The cleanup in `with_bank` therefore deletes straight
            // away.
        })
    })
    .await;
}

#[tokio::test]
async fn the_retired_profile_route_answers_gone() {
    with_bank("profile", |client, bank| {
        Box::pin(async move {
            // The document keeps the profile read, marked "removed", so this
            // pins the 410 that makes the configuration route the supported way
            // to read a bank's settings. The status is undeclared, so it
            // arrives as `UnexpectedResponse` with the code intact.
            let error = client
                .client()
                .get_bank_profile(&bank, None)
                .await
                .expect_err("the retired profile read must not answer successfully");
            match error {
                Error::UnexpectedResponse(response) => assert_eq!(
                    response.status(),
                    reqwest::StatusCode::GONE,
                    "retired profile read answered {}, want 410",
                    response.status()
                ),
                other => panic!("expected the undeclared 410 to reach the caller, got {other}"),
            }
        })
    })
    .await;
}
