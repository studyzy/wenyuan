//! Rust client for the WenYuan agent-memory HTTP API.
//!
//! The type-level API -- one method per operation, one type per schema -- is
//! generated at build time from the OpenAPI document this repository commits
//! (`clients/openapi.json`); see `build.rs`. What this module adds on top is a
//! [`Wenyuan`] handle: it remembers the deployment's base URL and optional
//! credential, so the core operations read as calls rather than as request
//! plumbing.
//!
//! ```
//! use wenyuan_client::{types, Wenyuan};
//!
//! # async fn example() -> Result<(), Box<dyn std::error::Error>> {
//! // Base URL from HINDSIGHT_API_URL, falling back to http://localhost:18888.
//! let client = Wenyuan::new()?.with_api_key("shared-secret");
//!
//! client
//!     .create_bank(
//!         "notes",
//!         &types::CreateBankRequest {
//!             name: Some("Notes".to_owned()),
//!             mission: Some("keep decisions".to_owned()),
//!             ..Default::default()
//!         },
//!     )
//!     .await?;
//!
//! // Retain: `async_: true` would return an operation id to poll instead.
//! client
//!     .retain(
//!         "notes",
//!         &types::RetainRequest {
//!             items: vec![types::MemoryItem {
//!                 content: types::Content::Variant0("Schema stays a superset.".to_owned()),
//!                 context: None,
//!                 document_id: None,
//!                 entities: None,
//!                 metadata: None,
//!                 observation_scopes: None,
//!                 resolve_entities: true,
//!                 strategy: None,
//!                 tags: None,
//!                 timestamp: None,
//!                 update_mode: None,
//!             }],
//!             async_: false,
//!             document_tags: None,
//!             operation_id: None,
//!         },
//!     )
//!     .await?;
//!
//! let recalled = client
//!     .recall(
//!         "notes",
//!         &types::RecallRequest {
//!             query: "what must the schema stay?".to_owned(),
//!             max_tokens: 4096,
//!             trace: false,
//!             prefer_observations: false,
//!             tags_match: types::TagsMatch::Any,
//!             budget: None,
//!             include: None,
//!             query_timestamp: None,
//!             types: None,
//!             tags: None,
//!             tag_groups: None,
//!             min_scores: None,
//!             temporal_window: None,
//!         },
//!     )
//!     .await?;
//! println!("{} memories", recalled.into_inner().results.len());
//!
//! let reflected = client
//!     .reflect(
//!         "notes",
//!         &types::ReflectRequest {
//!             query: "what must the schema stay?".to_owned(),
//!             max_tokens: 4096,
//!             tags_match: types::TagsMatch::Any,
//!             apply_all_directives: false,
//!             exclude_mental_models: false,
//!             budget: None,
//!             context: None,
//!             exclude_mental_model_ids: None,
//!             fact_types: None,
//!             include: None,
//!             reflect_search_observations_include_entities: None,
//!             reflect_search_observations_max_tokens: None,
//!             response_schema: None,
//!             tag_groups: None,
//!             tags: None,
//!         },
//!     )
//!     .await?;
//! println!("{}", reflected.into_inner().text);
//! # Ok(())
//! # }
//! ```
//!
//! Every generated method remains reachable through [`Wenyuan::client`], for
//! the operations the four convenience methods do not cover.

include!(concat!(env!("OUT_DIR"), "/wenyuan_client_generated.rs"));

/// This client's version, matching the crate version.
pub const CLIENT_VERSION: &str = env!("CARGO_PKG_VERSION");

/// `User-Agent` sent on every request unless the caller supplies their own.
pub const DEFAULT_USER_AGENT: &str = concat!("wenyuan-client-rust/", env!("CARGO_PKG_VERSION"));

/// Environment variable naming the base URL of the deployment to talk to.
///
/// The spelling is part of the deployment contract -- it is the same variable
/// this service's other clients and its own CLI read -- so it is deliberately
/// not renamed to match the crate.
pub const ENV_BASE_URL: &str = "HINDSIGHT_API_URL";

/// Base URL used when [`ENV_BASE_URL`] is unset: the port the server listens on
/// by default.
pub const DEFAULT_BASE_URL: &str = "http://localhost:18888";

/// A WenYuan API handle: a generated [`Client`] plus the base URL and
/// credential to call it with.
#[derive(Clone, Debug)]
pub struct Wenyuan {
    client: Client,
    base_url: String,
    api_key: Option<String>,
}

impl Wenyuan {
    /// Connect to the deployment named by [`ENV_BASE_URL`], falling back to
    /// [`DEFAULT_BASE_URL`] when it is unset.
    ///
    /// This builds the default `reqwest` client, which only fails when the
    /// process has no usable TLS backend; use
    /// [`with_base_url_and_user_agent`](Self::with_base_url_and_user_agent) to
    /// handle that case instead of panicking.
    pub fn new() -> Result<Self, reqwest::Error> {
        Self::with_base_url(base_url_from_env())
    }

    /// Connect to `base_url`, identifying the client as
    /// [`DEFAULT_USER_AGENT`].
    pub fn with_base_url(base_url: impl Into<String>) -> Result<Self, reqwest::Error> {
        Self::with_base_url_and_user_agent(base_url, DEFAULT_USER_AGENT)
    }

    /// Connect to `base_url` behind a caller-supplied `User-Agent`.
    ///
    /// Pass a product identifier here when embedding this client in a larger
    /// program (for example `"my-agent/2.1.0"`), so the server's logs can tell
    /// which caller produced a request.
    pub fn with_base_url_and_user_agent(
        base_url: impl Into<String>,
        user_agent: impl Into<String>,
    ) -> Result<Self, reqwest::Error> {
        // A trailing slash would be doubled by the generated URL construction,
        // which appends "/v1/..." to the base.
        let base_url = base_url.into().trim_end_matches('/').to_owned();
        let http = reqwest::Client::builder()
            .user_agent(user_agent.into())
            .build()?;
        Ok(Self {
            client: Client::new_with_client(&base_url, http),
            base_url,
            api_key: None,
        })
    }

    /// Present `api_key` as the `Authorization` header on every request.
    ///
    /// The deployment validates this header when it is configured with an
    /// access key; a client without one reaches only instances that run
    /// without authentication.
    pub fn with_api_key(mut self, api_key: impl Into<String>) -> Self {
        self.api_key = Some(api_key.into());
        self
    }

    /// The base URL requests are sent to.
    pub fn base_url(&self) -> &str {
        &self.base_url
    }

    /// The generated client, for operations without a convenience method.
    pub fn client(&self) -> &Client {
        &self.client
    }

    /// Create the bank named `bank_id`, or replace its profile if it already
    /// exists (`PUT /v1/default/banks/{bank_id}`).
    pub async fn create_bank(
        &self,
        bank_id: &str,
        request: &types::CreateBankRequest,
    ) -> Result<ResponseValue<types::BankProfileResponse>, Error<types::HttpValidationError>> {
        self.client
            .create_or_update_bank(bank_id, self.authorization(), request)
            .await
    }

    /// Write memories into a bank (`POST /v1/default/banks/{bank_id}/memories`).
    ///
    /// Set [`types::RetainRequest::async_`] to submit the work in the
    /// background: the call then returns an operation id to poll instead of
    /// waiting for extraction to finish.
    pub async fn retain(
        &self,
        bank_id: &str,
        request: &types::RetainRequest,
    ) -> Result<ResponseValue<types::RetainResponse>, Error<types::HttpValidationError>> {
        self.client
            .retain_memories(bank_id, self.authorization(), request)
            .await
    }

    /// Search a bank's memories (`POST /v1/default/banks/{bank_id}/memories/recall`).
    pub async fn recall(
        &self,
        bank_id: &str,
        request: &types::RecallRequest,
    ) -> Result<ResponseValue<types::RecallResponse>, Error<types::HttpValidationError>> {
        self.client
            .recall_memories(bank_id, self.authorization(), request)
            .await
    }

    /// Synthesize an answer from a bank's memories
    /// (`POST /v1/default/banks/{bank_id}/reflect`).
    pub async fn reflect(
        &self,
        bank_id: &str,
        request: &types::ReflectRequest,
    ) -> Result<ResponseValue<types::ReflectResponse>, Error<types::HttpValidationError>> {
        self.client
            .reflect(bank_id, self.authorization(), request)
            .await
    }

    /// The generated methods take a credential as `Option<Option<&str>>`:
    /// the outer option means "parameter present", the inner one means "has a
    /// value".
    fn authorization(&self) -> Option<Option<&str>> {
        self.api_key.as_deref().map(Some)
    }
}

impl Default for Wenyuan {
    fn default() -> Self {
        Self::new().expect("the default reqwest client can be built")
    }
}

/// The base URL to connect to: [`ENV_BASE_URL`] when it holds a non-empty
/// value, [`DEFAULT_BASE_URL`] otherwise.
pub fn base_url_from_env() -> String {
    match std::env::var(ENV_BASE_URL) {
        Ok(url) if !url.is_empty() => url,
        _ => DEFAULT_BASE_URL.to_owned(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn base_url_from_env_falls_back_to_the_default_port() {
        let configured = std::env::var(ENV_BASE_URL);
        std::env::set_var(ENV_BASE_URL, "http://example.test:1");
        assert_eq!(base_url_from_env(), "http://example.test:1");
        std::env::set_var(ENV_BASE_URL, "");
        assert_eq!(base_url_from_env(), DEFAULT_BASE_URL);
        match configured {
            Ok(value) => std::env::set_var(ENV_BASE_URL, value),
            Err(_) => std::env::remove_var(ENV_BASE_URL),
        }
    }

    #[test]
    fn base_url_loses_a_trailing_slash() {
        let client = Wenyuan::with_base_url("http://example.test:18888/").unwrap();
        assert_eq!(client.base_url(), "http://example.test:18888");
    }

    #[test]
    fn user_agent_identifies_this_client() {
        assert!(DEFAULT_USER_AGENT.starts_with("wenyuan-client-rust/"));
        assert!(DEFAULT_USER_AGENT.ends_with(CLIENT_VERSION));
    }
}
