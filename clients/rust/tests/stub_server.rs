//! The client, exercised end to end against a stub deployment.
//!
//! Every request is answered by a hand-rolled minimal HTTP server bound to
//! `127.0.0.1:0`, so the test needs no network and no running service: what it
//! verifies is that the wrapper, the generated client and the generated types
//! agree with each other -- the request goes out in the shape the API
//! documents, and the documented response decodes into the documented type.

use std::sync::{Arc, Mutex};

use serde_json::json;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::{TcpListener, TcpStream};
use wenyuan_client::{types, Error, Wenyuan};

const BANK: &str = "notes";
const API_KEY: &str = "test-key";
const MEMORY: &str = "The schema must stay a superset of the reference.";
const ANSWER: &str = "Keep the schema a superset.";

/// A request as the stub saw it.
#[derive(Clone, Debug, Default)]
struct Seen {
    method: String,
    path: String,
    headers: Vec<(String, String)>,
    body: String,
}

impl Seen {
    fn header(&self, name: &str) -> Option<&str> {
        self.headers
            .iter()
            .find(|(key, _)| key.eq_ignore_ascii_case(name))
            .map(|(_, value)| value.as_str())
    }
}

/// A stub deployment: the base URL to point a client at, and everything the
/// client sent while the test ran.
struct Stub {
    base_url: String,
    seen: Arc<Mutex<Vec<Seen>>>,
}

impl Stub {
    async fn start() -> Self {
        let listener = TcpListener::bind("127.0.0.1:0")
            .await
            .expect("the loopback interface accepts a listener");
        let base_url = format!("http://{}", listener.local_addr().expect("a bound address"));
        let seen = Arc::new(Mutex::new(Vec::new()));

        let recorder = Arc::clone(&seen);
        tokio::spawn(async move {
            while let Ok((mut socket, _)) = listener.accept().await {
                let recorder = Arc::clone(&recorder);
                tokio::spawn(async move {
                    let Some(request) = read_request(&mut socket).await else {
                        return;
                    };
                    let (status, body) = answer(&request);
                    let _ = socket.write_all(response(status, &body).as_bytes()).await;
                    let _ = socket.shutdown().await;
                    recorder
                        .lock()
                        .expect("the recorder is not poisoned")
                        .push(request);
                });
            }
        });

        Self { base_url, seen }
    }

    fn requests(&self) -> Vec<Seen> {
        self.seen
            .lock()
            .expect("the recorder is not poisoned")
            .clone()
    }
}

/// Read one request (headers, then `Content-Length` bytes).
async fn read_request(socket: &mut TcpStream) -> Option<Seen> {
    let mut raw = Vec::new();
    let mut chunk = [0u8; 4096];
    let (head_end, body_len) = loop {
        let read = socket.read(&mut chunk).await.ok()?;
        if read == 0 {
            return None;
        }
        raw.extend_from_slice(&chunk[..read]);
        if let Some(head_end) = find(&raw, b"\r\n\r\n") {
            let head = String::from_utf8_lossy(&raw[..head_end]).into_owned();
            let body_end = head_end + 4;
            let expected = content_length(&head);
            if raw.len() >= body_end + expected {
                break (head_end, expected);
            }
        }
    };

    let head = String::from_utf8_lossy(&raw[..head_end]).into_owned();
    let mut lines = head.split("\r\n");
    let mut request_line = lines.next().unwrap_or_default().split_whitespace();
    let method = request_line.next().unwrap_or_default().to_owned();
    let path = request_line.next().unwrap_or_default().to_owned();
    let headers = lines
        .filter_map(|line| line.split_once(':'))
        .map(|(name, value)| (name.trim().to_owned(), value.trim().to_owned()))
        .collect();
    let body = String::from_utf8_lossy(&raw[head_end + 4..head_end + 4 + body_len]).into_owned();

    Some(Seen {
        method,
        path,
        headers,
        body,
    })
}

/// The canned answer for a request: a documented success where a core
/// operation went, an undeclared 404 anywhere else.
fn answer(request: &Seen) -> (u16, String) {
    let path = request.path.as_str();
    let body = match (request.method.as_str(), path) {
        ("PUT", p) if p == format!("/v1/default/banks/{BANK}") => json!({
            "bank_id": BANK,
            "name": "Notes",
            "mission": "keep decisions",
            "background": null,
            "disposition": { "skepticism": 4, "literalism": 3, "empathy": 2 },
        }),
        ("POST", p) if p == format!("/v1/default/banks/{BANK}/memories") => json!({
            "success": true,
            "bank_id": BANK,
            "items_count": 1,
            "async": false,
            "operation_id": null,
        }),
        ("POST", p) if p == format!("/v1/default/banks/{BANK}/memories/recall") => json!({
            "results": [{ "id": "mem-1", "text": MEMORY, "tags": ["schema"] }],
            "source_facts_truncated": false,
        }),
        ("POST", p) if p == format!("/v1/default/banks/{BANK}/reflect") => json!({
            "text": ANSWER,
            "usage": { "input_tokens": 12, "output_tokens": 5 },
        }),
        // The refresh details' own `outcome`: a value the first model that
        // claims the `outcome` title does not list.
        ("GET", p) if p.starts_with(&format!("/v1/default/banks/{BANK}/operations/op-1")) => {
            json!({
                "operation_id": "op-1",
                "status": "completed",
                "operation_type": "refresh_mental_model",
                "details": {
                    "operation_type": "refresh_mental_model",
                    "outcome": "refresh_failed_error",
                    "failure_reason": "empty_candidate",
                },
            })
        }
        // The prompt block's own `kind`, likewise absent from the first model
        // that claims the `kind` title.
        ("POST", p) if p == format!("/v1/default/banks/{BANK}/prompts/preview") => json!({
            "messages": [{
                "role": "user",
                "blocks": [{ "kind": "text", "source": "config", "text": "" }],
            }],
            "run_settings": [{ "field": "chunk_size", "kind": "choice" }],
        }),
        _ => return (404, json!({ "detail": "not found" }).to_string()),
    };
    (200, body.to_string())
}

fn response(status: u16, body: &str) -> String {
    format!(
        "HTTP/1.1 {status} {reason}\r\n\
         content-type: application/json\r\n\
         content-length: {length}\r\n\
         connection: close\r\n\
         \r\n\
         {body}",
        reason = if status == 200 { "OK" } else { "Not Found" },
        length = body.len(),
    )
}

fn find(haystack: &[u8], needle: &[u8]) -> Option<usize> {
    haystack
        .windows(needle.len())
        .position(|window| window == needle)
}

fn content_length(head: &str) -> usize {
    head.split("\r\n")
        .filter_map(|line| line.split_once(':'))
        .find(|(name, _)| name.eq_ignore_ascii_case("content-length"))
        .and_then(|(_, value)| value.trim().parse().ok())
        .unwrap_or(0)
}

fn create_bank_request() -> types::CreateBankRequest {
    types::CreateBankRequest {
        name: Some("Notes".to_owned()),
        mission: Some("keep decisions".to_owned()),
        ..Default::default()
    }
}

fn retain_request() -> types::RetainRequest {
    types::RetainRequest {
        items: vec![types::MemoryItem {
            content: types::Content::Variant0(MEMORY.to_owned()),
            context: None,
            document_id: None,
            entities: None,
            metadata: None,
            observation_scopes: None,
            resolve_entities: true,
            strategy: None,
            tags: Some(vec!["schema".to_owned()]),
            timestamp: None,
            update_mode: None,
        }],
        async_: false,
        document_tags: None,
        operation_id: None,
    }
}

fn recall_request() -> types::RecallRequest {
    types::RecallRequest {
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
    }
}

fn reflect_request() -> types::ReflectRequest {
    types::ReflectRequest {
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
    }
}

#[tokio::test]
async fn core_operations_round_trip_against_a_stub_deployment() {
    let stub = Stub::start().await;
    let client = Wenyuan::with_base_url(&stub.base_url)
        .expect("the base URL is usable")
        .with_api_key(API_KEY);
    assert_eq!(client.base_url(), stub.base_url);

    let bank = client
        .create_bank(BANK, &create_bank_request())
        .await
        .expect("the bank profile decodes")
        .into_inner();
    assert_eq!(bank.bank_id, BANK);
    assert_eq!(bank.name, "Notes");
    assert_eq!(bank.mission, "keep decisions");
    assert_eq!(u64::from(bank.disposition.skepticism), 4);

    let retained = client
        .retain(BANK, &retain_request())
        .await
        .expect("the retain response decodes")
        .into_inner();
    assert!(retained.success);
    assert!(!retained.async_);
    assert_eq!(retained.items_count, 1);

    let recalled = client
        .recall(BANK, &recall_request())
        .await
        .expect("the recall response decodes")
        .into_inner();
    assert_eq!(recalled.results.len(), 1);
    assert_eq!(recalled.results[0].id, "mem-1");
    assert_eq!(recalled.results[0].text, MEMORY);
    assert_eq!(
        recalled.results[0].tags.as_deref(),
        Some(["schema".to_owned()].as_slice())
    );
    assert!(recalled.results[0].scores.is_none());

    let reflected = client
        .reflect(BANK, &reflect_request())
        .await
        .expect("the reflect response decodes")
        .into_inner();
    assert_eq!(reflected.text, ANSWER);

    // What the wrapper put on the wire, in order.
    let sent = stub.requests();
    let routes: Vec<(&str, String)> = sent
        .iter()
        .map(|request| (request.method.as_str(), request.path.clone()))
        .collect();
    assert_eq!(
        routes,
        [
            ("PUT", format!("/v1/default/banks/{BANK}")),
            ("POST", format!("/v1/default/banks/{BANK}/memories")),
            ("POST", format!("/v1/default/banks/{BANK}/memories/recall")),
            ("POST", format!("/v1/default/banks/{BANK}/reflect")),
        ]
    );

    // The credential the handle holds is presented on every request.
    for request in &sent {
        assert_eq!(request.header("authorization"), Some(API_KEY));
        assert!(request.header("api-version").is_some());
        assert!(request
            .header("user-agent")
            .is_some_and(|ua| ua.starts_with("wenyuan-client-rust/")));
    }

    // The retain body carries the memory as the request type spells it.
    let retain_body: serde_json::Value =
        serde_json::from_str(&sent[1].body).expect("the retain body is JSON");
    assert_eq!(retain_body["async"], serde_json::json!(false));
    assert_eq!(
        retain_body["items"][0]["content"],
        serde_json::json!(MEMORY)
    );
    assert_eq!(
        retain_body["items"][0]["tags"],
        serde_json::json!(["schema"])
    );

    let recall_body: serde_json::Value =
        serde_json::from_str(&sent[2].body).expect("the recall body is JSON");
    assert_eq!(
        recall_body["query"],
        serde_json::json!("what must the schema stay?")
    );
    assert_eq!(recall_body["max_tokens"], serde_json::json!(4096));
}

#[tokio::test]
async fn a_status_the_document_does_not_declare_reaches_the_caller() {
    // The stub answers an undeclared 404 for this route; the client surfaces it
    // as an untyped error rather than losing the status.
    let stub = Stub::start().await;
    let client = Wenyuan::with_base_url(&stub.base_url).expect("the base URL is usable");

    let error = client
        .client()
        .list_banks(None, None, None, None)
        .await
        .expect_err("the stub answers 404 for this route");

    match error {
        Error::UnexpectedResponse(response) => {
            assert_eq!(response.status(), reqwest::StatusCode::NOT_FOUND);
        }
        other => panic!("expected an undeclared-status error, got {other}"),
    }
}

/// Two inline enums that share a `title` but not their values must keep their
/// own value sets. The spec rewrite in `build.rs` disambiguates their titles
/// for that reason: without it the generator names both after the shared title,
/// the second collapses into the first, and the second then rejects values its
/// own schema allows. Seen through the API, that loss is a documented `outcome`
/// and a documented `kind` failing to decode.
#[tokio::test]
async fn same_titled_inline_enums_keep_their_own_values() {
    let stub = Stub::start().await;
    let client = Wenyuan::with_base_url(&stub.base_url).expect("the base URL is usable");

    // `refresh_failed_error` belongs to the refresh details' `outcome`; the
    // other model titled `outcome` does not list it.
    let operation = client
        .client()
        .get_operation_status(BANK, "op-1", None, None)
        .await
        .expect("the operation status decodes")
        .into_inner();
    let details = operation
        .details
        .expect("the stub reports the operation's typed details");
    assert_eq!(details.outcome.to_string(), "refresh_failed_error");
    assert_eq!(
        serde_json::to_value(&details).expect("the details serialize")["outcome"],
        json!("refresh_failed_error"),
    );

    // `text` and `choice` belong to the prompt block's and run setting's own
    // `kind`; the other model titled `kind` lists neither.
    let preview = client
        .client()
        .preview_prompt(BANK, None, &types::PromptPreviewRequest::default())
        .await
        .expect("the prompt preview decodes")
        .into_inner();
    assert_eq!(preview.messages[0].blocks[0].kind.to_string(), "text",);
    assert_eq!(preview.run_settings[0].kind.to_string(), "choice",);
}
