# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

### Added

- The four client SDKs under `clients/` — Go, Python, TypeScript and Rust —
  generated from this service's own contract instead of written by hand. The
  generator input is `clients/openapi.json`, a derived copy of the document
  served at `/openapi.json`: the served document keeps the reference's branding
  because it *is* the contract, while the derived copy names this project (the
  `HINDSIGHT_API_*` environment names are preserved there, since they are the
  deployment contract a client must name). `scripts/derive_client_spec.py`
  produces the copy (`make clients-check` fails when it goes stale), `make
  clients` regenerates every SDK with its pinned generator — openapi-generator
  v7.10.0 in docker for Go and Python, `@hey-api/openapi-ts` for TypeScript,
  `progenitor` in `build.rs` for Rust — and each SDK keeps its packaging
  metadata, thin wrapper and tests outside the generator's ownership. The Go
  SDK is a module of its own (`github.com/studyzy/wenyuan/clients/go`), so the
  service module and its gates are untouched by it. See `clients/README.md`.
  Each SDK carries the client-side behaviour the generated code does not state.
  Go: an offline suite pinning the two generator quirks the build patches, a
  live suite behind the `integration` tag, and the convenience layer
  (`NewClient*`, `TextContent`, the core-operation methods). Python: the
  thirteen sub-clients, `max_attempts` retry for the idempotent calls with the
  reference's capacity and `Retry-After` semantics, `suspend_retains()`, and the
  recall helpers (`to_prompt_string`, iteration/len/index and reprs) installed
  from the wrapper rather than by editing generated code. TypeScript:
  `retryAfterMs`/`retryOnCapacity` wired to recall and reflect, `retainBatch`,
  `retainFiles`, `recallResponseToPromptString` and the
  `{baseUrl, apiKey, userAgent, headers, maxAttempts}` client options.
  Rust: the user-agent constructors, `INTEGRATION.md` plus compiling
  `examples/` for bridging a synchronous CLI onto the async client, and the
  no-retry contract stated where a caller looks for it.
  Every SDK ships two suites: an offline one (stubs only) and a live one gated
  on `HINDSIGHT_API_URL` that skips itself when unset, so the offline gate stays
  usable without a server. `make test-sdk` runs all four offline suites,
  `make test-sdk-integration` runs all four live ones against `SDK_API_URL`
  (default `http://127.0.0.1:18888`); a memory-backed server is enough, since
  the assertions are structural (an operation succeeded, an id round-tripped, a
  list carries what was written, the retired profile route answers 410, a tag
  filter selects its own write back out) rather than about extracted facts or
  synthesized prose. The SDKs are source in this repository: no npm/PyPI/
  crates.io publication, no release pipeline.
- Make targets for the deployment surface: `make tei-up` starts the compose
  `tei` profile (the TEI embedding and reranker sidecars now sit behind that
  profile, so a plain `docker compose up -d` starts PostgreSQL alone), and
  `make frontend-build` / `make frontend-run` build and serve the control-plane
  frontend in production mode (dev-mode HMR hangs Origin-bearing WebSocket
  upgrades in this environment, so dev is not usable against the dashboard).
- A YAML configuration file answers any key the environment answers. It is read
  through Viper from `etc/wenyuan.yaml` relative to the working directory (also
  `/etc/wenyuan.yaml`, where the image puts the shipped template), or from the
  path `HINDSIGHT_API_CONFIG_FILE` names, which must exist. Keys are the
  environment names without the `HINDSIGHT_API_` prefix, lowercased; a nested
  spelling is accepted and flattened to the same key, both spellings of one key
  must agree, and a list value is refused at startup naming the key rather than
  silently
  ignored. Precedence is now complete: flags, then the `.env` file, then the
  rest of the process environment, then this file, then the built-in defaults —
  so an existing deployment that never
  wrote a file resolves byte-for-byte as before.
- The `.env` file is now loaded by every entrypoint — server, worker and
  bankctl — and a discovered `.env` is authoritative over the ambient process
  environment, which is the rule the reference's own entrypoints keep; the
  worker and bankctl previously read neither. The shared loader is
  `config.LoadDotenvForEntrypoint`. A shipped `.env.example` documents the
  whole surface in dotenv form, `etc/example.wenyuan.yaml` is the commented
  YAML template (copy it to `etc/wenyuan.yaml` — the real file stays local and
  is not checked in), and `docs/CONFIGURATION.md` is the generated
  full key table (key, default, resolved/refused and why) behind the templates
  and this changelog; regenerate them with
  `scripts/render_config_reference.py`.
- The process entrypoint is a drop-in for the reference's launcher. `--daemon`
  now actually detaches: the foreground parent preflights the port (an occupied
  port fails in the terminal, not in a log), strips the flag, re-execs itself in
  a new session with stdio appended to `HINDSIGHT_API_DAEMON_LOG` (default
  `~/.hindsight/daemon.log`), and exits 0; the daemon binds loopback unless a
  host was configured and port 8888 unless `--port` was passed, which is the
  reference's own resolution rule. `--workers N` (or `HINDSIGHT_API_WORKERS`)
  runs N worker processes instead of being parsed and ignored: the supervisor
  binds once and hands the socket to every worker, so the whole set shares one
  listening socket the way uvicorn's supervisor does, the first worker to stop
  stops the rest, and `SIGTERM` reaches them all. The reference's remaining
  flags are accepted too — `--reload` (reported as unsupported rather than
  silently ignored), `--no-access-log` (last of the pair wins, as in the
  reference's parser), `--forwarded-allow-ips`, and `--idle-timeout` (inert in
  both implementations, with the reference's sentence). `--ssl-keyfile` and
  `--ssl-certfile` now serve HTTPS; a solo value is a configuration error.
- `--proxy-headers` and `--forwarded-allow-ips` are honoured. A request's
  forwarded scheme and client address are believed only when the immediate peer
  is trusted (`127.0.0.1` by default, `*` for any peer, CIDR networks and
  literals accepted; the reference's `FORWARDED_ALLOW_IPS` variable is read the
  same way), so the access log names the real client behind a load balancer
  without letting an untrusted caller name itself.
- The reference's tuning keys whose behaviour already existed are now resolved
  instead of refused: the four retrieval-arm switches, the retain chunk sizes,
  limits, mission and custom instructions, the consolidation mission, round
  cap, source-fact budgets, strategies, scope limits and LLM batch size and
  parallelism, the reflect mission and source-fact budget, the bank-config
  process defaults those fields feed, the per-operation-type worker slot
  reservations (`HINDSIGHT_API_WORKER_*_RESERVED_SLOTS`, each landing on the
  operation type it names), the event-loop watchdog
  (`HINDSIGHT_API_LOOP_WATCHDOG_ENABLED` and its poll and stall intervals,
  which turn the stall probe on by default as the reference does), the dry-run
  extraction switch (off answers the reference's 404 and its sentence),
  `HINDSIGHT_API_WORKERS` and `HINDSIGHT_API_DAEMON_LOG`. A copied reference
  `.env` now starts the service where it used to refuse startup. The retrieval,
  write and reflection tuning follows: `HINDSIGHT_API_GRAPH_SEED_MIN_SIMILARITY`,
  `HINDSIGHT_API_SEMANTIC_LINK_MIN_SIMILARITY`, `HINDSIGHT_API_BM25_MAX_QUERY_TERMS`,
  the fixed recall budget levels, the per-arm score floors
  (`HINDSIGHT_API_SEMANTIC_MIN_SIMILARITY`,
  `HINDSIGHT_API_TEMPORAL_SEMANTIC_MIN_SIMILARITY`,
  `HINDSIGHT_API_BM25_MIN_SCORE`), `HINDSIGHT_API_REFLECT_MAX_ITERATIONS` and
  `HINDSIGHT_API_REFLECT_MAX_CONTEXT_TOKENS` now reach a shared `storage.Tuning`
  both backends read (which also corrected two drifted defaults: semantic links
  0.75 → 0.7, the reflection loop's 4 iterations / 4000 tokens → 10 / 100000),
  the two history records honour their switches and entry bounds, admission
  waits are per lane, the retain path honours its completion-token cap,
  sub-batch budget, entity-lookup mode and candidate bound, and the database
  session carries `HINDSIGHT_API_DB_STATEMENT_TIMEOUT`,
  `HINDSIGHT_API_DB_MAX_PARALLEL_WORKERS_PER_GATHER`,
  `HINDSIGHT_API_DB_SESSION_SETUP_ON_ACQUIRE` and
  `HINDSIGHT_API_ENTITY_TRGM_SIMILARITY_THRESHOLD` as connection parameters, the
  way the reference's per-connection callback does. The graph expansion honours
  its per-entity target cap and its own statement timeout
  (`HINDSIGHT_API_LINK_EXPANSION_PER_ENTITY_LIMIT`,
  `HINDSIGHT_API_LINK_EXPANSION_TIMEOUT`), and the per-process bank-row cache
  honours `HINDSIGHT_API_BANK_INFO_CACHE_MAX_ENTRIES` by dropping expired
  entries before a live one. The MCP surface honours
  `HINDSIGHT_API_MCP_AUTH_TOKEN` (a legacy bearer that replaces tenant auth for
  the MCP namespace, with the reference's two 401 sentences),
  `HINDSIGHT_API_MCP_ENABLED_TOOLS` (a server-level allowlist that hides tools
  from `tools/list` and refuses their invocation) and
  `HINDSIGHT_API_MCP_INSTRUCTIONS` (appended to the retain and recall tool
  descriptions). The vector arm applies the deployment's pgvector tuning
  (`HINDSIGHT_API_ANN_ITERATIVE_SCAN`, `HINDSIGHT_API_ANN_MAX_SCAN_TUPLES`)
  transaction-locally and remembers the settings a server refuses; the migration
  series can run over its own connection
  (`HINDSIGHT_API_MIGRATION_DATABASE_URL`); a bank's list row reports its
  resolved disposition and mission
  (`HINDSIGHT_API_DISPOSITION_{SKEPTICISM,LITERALISM,EMPATHY}`, the reference's
  overlay of configuration on the stored columns); and a new knowledge page's
  refresh policy layers the process, tenant and bank values of
  `HINDSIGHT_API_KNOWLEDGE_PAGE_DEFAULT_TRIGGER` under the request's own.
  The deployment's server-level webhook
  (`HINDSIGHT_API_WEBHOOK_URL` and its secret, event filter and
  response-body switch) now delivers alongside each bank's own registrations,
  the metrics surface honours its backlog, bank-id, tenant and event-loop-lag
  switches (the lag histogram carries the reference's buckets), extensions
  receive the allowlisted request headers
  (`HINDSIGHT_API_EXTENSION_PASSTHROUGH_HEADERS`), a configured default bank
  template is applied from the single bank-creation hook on every path that
  creates a bank (with the create request's own fields winning), reads that
  tolerate a replica can run on one
  (`HINDSIGHT_API_READ_DATABASE_URL` and its pool bounds), and the keyword arm
  can run on ParadeDB pg_search
  (`HINDSIGHT_API_TEXT_SEARCH_EXTENSION_PG_SEARCH_*`). Refusing keys went from
  A second parallel pass closed the recall ingress and internal-recall knobs
  (`HINDSIGHT_API_RECALL_MAX_QUERY_TOKENS`, `_MAX_CONCURRENT`, and the chunk and
  token budgets of the internal recall), the reflection completion cap and its
  default options, the write side's causal-extraction switch, its default retain
  strategy and the entity resolver's second trigram gate, and the worker /
  consolidation family (`HINDSIGHT_API_BACKPRESSURE_DEFER_SECONDS`, the
  `bank-pattern:priority` consolidation claim order, the consolidation batch,
  attempt, budget and completion settings, and the stats-cache bound).
  `HINDSIGHT_API_DATABASE_BACKEND` accepts the dialects this build implements and
  names the one it does not. A fifth parallel pass closed the multi-worker
  metrics label (`HINDSIGHT_API_METRICS_WORKER_LABEL`: slot claim, snapshots,
  per-worker series), the adaptive recall budget function and its ratio and clamp
  keys, the recall phase diagnostics,
  `HINDSIGHT_API_ENTITY_INTRABATCH_MERGE_SIMILARITY`, the startup model
  verification with `HINDSIGHT_API_MODEL_INIT_TIMEOUT` and
  `HINDSIGHT_API_SKIP_LLM_VERIFICATION`, and the pool-acquire timeout and
  slow-acquire warnings. Four keys became partial support: the value this build
  implements is accepted and the rest are refused by name
  (`GRAPH_RETRIEVER=link_expansion`, `MIGRATION_ISOLATION=false`,
  `MIGRATION_CONCURRENCY=1`, `MCP_STATELESS=false`). Refusing keys went from 150
  to 12. Running the configuration package's own contract suite again (it had
  been skipped while another editor's throwaway probe file hung the package)
  immediately found three real defects, all fixed: the pool slow-acquire
  threshold's reference default is a *fractional* second (0.05) and was being
  parsed as an integer, `HINDSIGHT_API_SKIP_LLM_VERIFICATION` had no probe value
  that proved it is read, and that key needed a resolvable value in the provider
  family contract. A sixth parallel pass closed the document-parser OCR chain
  (`HINDSIGHT_API_FILE_PARSER_MARKITDOWN_OCR_*`: embedded images are transcribed
  through a new vision adapter and the transcription joins the parsed text, with
  the reference's construction-time validation), the alias-resolution cache
  (`HINDSIGHT_API_BANK_ALIAS_CACHE_TTL_SECONDS` / `_MAX_ENTRIES`), the
  consolidation near-duplicate threshold (`HINDSIGHT_API_CONSOLIDATION_DEDUP_THRESHOLD`
  now decides by cosine similarity, with a documented no-embedding fallback) and
  the recall connection budget (`HINDSIGHT_API_RECALL_CONNECTION_BUDGET`: the
  retrieval arms now run concurrently under that budget, with identical ordering,
  ranks and phase accounting). The same pass fixed two semantic problems:
  `HINDSIGHT_API_RETAIN_BATCH_ENABLED` meant "force async" here where the
  reference routes extraction through the LLM Batch API (the invented rule is
  gone, the key now accepts `false` and refuses `true` with the reason), and the
  provider adapters now request prompt caching the way the reference does by
  default (Anthropic's system block carries `cache_control: ephemeral`; the
  OpenAI-compatible side sends `prompt_cache_key` and the host-specific session
  headers). An earlier attempt to wire
  `HINDSIGHT_API_FAIL_ON_EXTRACTION_ERRORS` was
  reverted it: the reference escalates *batch-API* extraction errors and this
  build has no batch path, so the key has no seam and stays refused rather than
  becoming a silent no-op), and
  `docs/PARITY-PLAN.md` §10 lists every key that remains refused with its class
  (wiring, small feature, or structural) so the choice stays visible.

- The control-plane frontend is copied from the reference rather than
  reconstructed: `frontend/components/*` (the views, modals and editors),
  `app/[locale]/**` with the reference's `localePrefix: "never"` routing (clean
  URLs, the locale never a path segment), its `tests/**` suite (43 files, run
  beside ours), the 14 lib modules the views need, and the merged message
  catalogs (2446 keys per locale, up from 471, with a coverage test that fails if
  a used key is missing). Product-visible strings stay ours; the deployment
  contract — `HINDSIGHT_CP_*`, the session cookie names, the health route's
  `SERVICE_NAME` — is byte-identical. `scripts/frontend_integration.sh` is the new
  cross-stack gate: it starts this service on a private database, builds and
  serves the frontend against it, and proves the access gate, the BFF read and
  write paths, and server-side page rendering.


- The log stream's shape is configurable the way the reference's is:
  `HINDSIGHT_API_LOG_FORMAT=json` writes one JSON object per record with the
  reference's keys and spellings (`severity`, `message`, `timestamp`, `logger`,
  with `WARN` reported as `WARNING`), `HINDSIGHT_API_LOG_JSON_FIELDS` narrows
  those keys, and records go to stdout. `HINDSIGHT_API_ACCESS_LOG` (or
  `--access-log`) writes the reference's per-request access line —
  `LEVEL:     <client> - "<request line>" <status>` — which the reference's JSON
  formatter does not touch either.
- A reflection no longer deadlocks a deployment with tracing on: the in-process
  store held its read lock across the model loop, so the trace write that the
  provider call makes blocked on the reader the same goroutine still held. The
  loop now runs outside the store's lock, and the diagnostic buffers (audit
  trail, model-request trace) have their own lock so a store operation can never
  deadlock against its own diagnostics again.

- The default bind address and port are the reference's: `0.0.0.0:8888` (was
  `127.0.0.1:8000`), and `HINDSIGHT_API_RUN_MIGRATIONS_ON_STARTUP` defaults on.
  A deployment that sets neither now binds all interfaces on the port a
  reference deployment uses.
- An unset `HINDSIGHT_API_DATABASE_URL` refuses startup, naming both ways out: a
  `postgresql://` URL, or the explicit `memory` opt-in for a non-persistent
  backend. The reference provisions an embedded PostgreSQL there, which this
  build cannot manage, and silently serving from memory would lose data on
  restart without saying so.
- Document uploads are converted to text: the parser chain
  (`HINDSIGHT_API_FILE_PARSER`, default `markitdown`) now has a local
  implementation covering PDF, DOCX, XLSX, PPTX, HTML, CSV/TSV, JSON/JSONL and
  plain text with no new dependencies, plus the remote `llama_parse` and `iris`
  parsers over their HTTP APIs. The allowlist, the batch limits
  (`HINDSIGHT_API_FILE_CONVERSION_MAX_BATCH_SIZE(_MB)`) and
  `HINDSIGHT_API_FILE_DELETE_AFTER_RETAIN` are honoured; a format nothing can
  convert is refused by name.
- The separate reranker and embedding provider families exist:
  `HINDSIGHT_API_RERANKER_PROVIDER` selects `siliconflow`, `cohere`,
  `zeroentropy`, `openrouter`, `tei`, `typesafe` or the in-process `rrf`
  (failover chain supported), and `HINDSIGHT_API_EMBEDDINGS_PROVIDER` selects
  `openai`, `openrouter`, `requesty`, `litellm`, `cohere`, `tei`, `zeroentropy`
  or `gemini`. Members needing a local runtime (`onnx`, `local`, `flashrank`,
  jina-mlx, litellm-sdk, google, alibaba) are refused at startup naming the key.
  `HINDSIGHT_API_EMBEDDING_DIMENSION` resolves the width (default 384).
- `HINDSIGHT_API_LLM_STRATEGY` and the per-role `*_LLM_STRATEGY` keys select
  multi-member chat routing: failover, weighted round-robin, or routing by a
  request's metadata, over members configured with the reference's
  `HINDSIGHT_API_<ROLE>LLM_<n>_*` convention.
- A reranker that marks a candidate 0.0 prunes it (the reference's
  `prunes_candidates`); unscored candidates survive.
- The retain extraction mode selects the reference's prompt and schema:
  `concise`, `verbose`, `custom`, `verbatim` and `chunks` each carry their own
  prompt, their own dimension fields (`what`/`when`/`where`/`who`/`why`) and the
  reference's collapse rule into the stored text; `chunks` stores one unit per
  chunk without a model call.
- Consolidation is configurable per bank the way the reference is:
  `observations_mission`, the strategy that claims a scope (with its own mission
  and limits), the per-scope observation cap, the source-fact token budgets and
  the batch/parallelism knobs all reach the pass, so the preview reports what
  the pass does.
- Token counts use the reference's vocabulary by default: `o200k_base`
  (`HINDSIGHT_API_TOKENIZER_ENCODING` selects between it and `cl100k_base`).
- The keyword arm selects its terms by document frequency from the planner's
  statistics instead of weighing every term in a long query, and the statement
  runs with a timeout, which is the reference's guard against a broad query
  ranking a whole bank.
- `HINDSIGHT_API_PROFILE` is the reference's JSON profiling request, validated at
  startup, and the periodic report is a real CPU profile: per-thread CPU plus a
  `tottime`/`cumtime`/`ncalls` table per window, parsed with the Go toolchain's
  own profile reader.
- Audit logging covers the reference's action set across all three transports:
  an HTTP middleware resolves the matched route to its action, the MCP dispatch
  records its auditable tools, and the worker records each processed operation.
  The global switch, the per-bank override and the action allowlist
  (`HINDSIGHT_API_AUDIT_LOG_ACTIONS`) gate it, and a failing write never fails
  the operation it describes.

- The server can be pointed at a database the reference service created and
  migrated itself. Startup reads the target schema's catalogue instead of
  trusting a bookkeeping table: a database that already carries every table,
  column, constraint and function the migration series creates has the series
  recorded as applied and nothing is re-created, an empty schema runs the series
  as before, and a schema holding only some of those objects is refused with an
  error naming the first missing one. The whole decision and the series run in
  one transaction under an advisory lock, so concurrent starts are safe, and an
  index the database lacks is reported rather than built (the reference creates
  its vector indexes outside its migrations, so their names differ between
  databases at the same recorded revision).
- `HINDSIGHT_API_FILE_STORAGE_TYPE` accepts `gcs` and `azure` in addition to
  `s3`: the payload backend is now selectable across Google Cloud Storage
  (`HINDSIGHT_API_FILE_STORAGE_GCS_BUCKET`, `..._GCS_SERVICE_ACCOUNT_KEY`) and
  Azure Blob Storage (`..._AZURE_CONTAINER`, `..._AZURE_ACCOUNT_NAME`,
  `..._AZURE_ACCOUNT_KEY`), each spoken over its own REST API with no vendor
  SDK, and each verified by its own stub-endpoint suite.
- Every object-store backend can sign a time-limited download URL (S3 SigV4
  query string, GCS V4 signed URL, Azure service SAS), and the archive download
  route hands the client a redirect to that URL when the configured store can
  sign one, falling back to proxying the payload otherwise.
- A database URL is normalized before the driver sees it
  (`postgresql+asyncpg://…?ssl=require` → `postgresql://…?sslmode=require`), and
  the connection pool is bounded by `HINDSIGHT_API_DB_POOL_MAX_SIZE` (100) and
  `..._MIN_SIZE` (5) instead of growing without limit.
- A bank's configuration-dependent behaviour is read fresh rather than from the
  process-local bank cache, so a change written by another server takes effect
  on the next request.
- Configuration layers: a tenant extension may supply overrides that sit between
  the process environment and the bank's own configuration, with the bank
  winning per key. A deployment without a tenant layer is unaffected.
- Webhook delivery is complete: a completed write raises `retain.completed`, and
  each delivery records the receiver's status (`last_status_code`) on its
  operation, so a refusing endpoint is distinguishable from an unreachable one.
- Uploads are bounded: `HINDSIGHT_API_RETAIN_ATTACHMENT_MAX_SIZE_MB` (20) and
  `..._MAX_COUNT` (50) cap one request's body, each file and the file count,
  each refusal naming the limit.
- Byte serving streams: the S3 object store hands back its live response body
  and the download routes stream it instead of buffering the whole payload,
  keeping every response header identical to the buffered path.
- An operation-validator extension point: a deployment can veto a retain or
  recall at the request boundary (with its own status and reason) and confine a
  caller to a tag scope that can narrow but never widen what it sees.
- `bankctl` gains `backup`, `restore`, `run-db-migration`,
  `decommission-worker` and `decommission-workers`.
- A provider setting this build cannot honour
  (`HINDSIGHT_API_LLM_STRATEGY`, `..._LLM_LITELLMROUTER_CONFIG`,
  `..._EMBEDDINGS_LOCAL_MODEL`) now fails startup by name instead of being
  silently ignored.
- Retain configuration is now reachable: a bank's `retain_mission`,
  `retain_custom_instructions`, `retain_chunk_size`, `retain_structured_chunk_size`
  and `entity_labels` are accepted by the config API (validated, 422 on an
  unusable vocabulary or a non-positive segment size) and actually reach the
  write. The label vocabulary constrains the extraction schema, and a selected
  label becomes a `key:value` entity (plus a tag when the group asks for one).
  A file upload in a format this build cannot parse (PDF, DOCX, PPT, XLS) is
  refused instead of being stored as binary noise, an attachment inside
  retained content renders as a stable `⟦hs-att:<id>⟧` placeholder, small
  consecutive items fold into one extraction call under a byte budget, and the
  bank's `memory_defense` policy screens content for secrets and PII before
  extraction (redacting in place or dropping the item, and firing
  `memory_defense.triggered`).
- Extraction retries a token-limited response by splitting the text, infers a
  coarse date ("in 2015", "in March 2026") as the period it spans, and carries
  the bank's retain mission, custom instructions and per-item strategy into its
  prompt. Embedding calls truncate an over-long input.
- Observation scopes take effect: a consolidation pass folds facts only with
  facts of the same scope and stores the scope on the observation.
- A mental model whose cited evidence has gone is stale even with no new write,
  a reflection records its grounding, and a reflection presents its evidence
  under short aliases with the forced retrieval hierarchy and the fresh-page
  short-circuit.
- Recency decay is configurable (`HINDSIGHT_API_RECENCY_DECAY_FUNCTION` =
  `linear|exponential|none`, plus `..._LINEAR_WINDOW_DAYS`/`..._HALFLIFE_DAYS`),
  and a coarse date ("in 2015") ages from the end of its period, capped at
  neutral, instead of reading as stale from the first instant of the period.
- Postgres recall renders compound `tag_groups` into its predicate (they were
  silently ignored), tag `resolve` uses the reference's `exact` token, an arm's
  `min_scores` floor is applied before fusion, and a bank's
  `enable_reranking=false` now really drops the cross-encoder pass.
- Retention persists `context`, `mentioned_at`, `event_date`, `occurred_end` and
  real `metadata` on Postgres (they were dropped), and an entity name longer
  than 512 characters is truncated instead of failing the write.
- Recall fusion gains an interleave mode (round-robin across arms, so one arm's
  best hit is never averaged down) and an optional per-source cap
  (`HINDSIGHT_API_RECALL_MAX_CANDIDATES_PER_SOURCE`) applied before fusion.
  Consolidation now widens its merge-candidate pool with a near-duplicate lookup
  that uses both, so a near-identical twin the recency-ordered pool missed is
  still offered to the model.
- Fact retraction: a refresh records its grounding (`reflect_response.based_on`),
  a mental model citing evidence that has since gone now counts as stale even
  though no watermark moved, and a delta merge is told to unsay the retracted
  statements. A document whose every citation is gone is treated as a broken
  link, not a retraction, and cited siblings or directives are never mistaken
  for missing memories.
- Rewriting an observation snapshots its previous text into `observation_history`
  (bounded at 20 entries per observation), and `GET
  /memories/{id}/history` merges those snapshots with the audit trail, so a
  consolidation rewrite is visible where it used to be invisible.
- The reasoning loop runs a turn's independent retrieval calls in parallel
  (bounded at four), recording their results in the order the model asked for
  them; a failed lookup still fails the run.
- A reflection answer over its `max_tokens` budget is shortened after the fact
  (the budget is a target for the visible answer, not a provider cap that a
  thinking model would eat), with the rewrite billed and an empty rewrite never
  emptying the answer.
- `POST /prompts/preview` renders the real prompt of `retain`, `consolidation`
  and `reflect` from the same builders those operations call, with bracketed
  `«…»` stand-ins for runtime data, and refuses an unknown operation instead of
  inventing one.
- Reflection renders the bank's directives as a mandatory opening section and a
  numbered reminder closing the answering prompt, so a later turn cannot
  out-rank them. `HINDSIGHT_API_LLM_OUTPUT_LANGUAGE` is now actually applied —
  as a directive on the system message of every completion, covering extracted
  facts, observations, reflections and mental-model bodies alike.
- Reflection presents retrieved evidence under short aliases (`f1`, `o1`,
  `p1`, `c1`) instead of full identifiers, resolves an alias the model echoes —
  in its answer or in a tool's arguments — back to the real id, and replaces an
  identifier the model was never shown with `[unverified id]`.
- Extraction now honours `fact_type: "assistant"` (stored as `experience`, with
  a `fact_kind` fallback) and `from_attachments` (resolved to the units'
  attachment ids by position or identifier).
- Per-`(bank, fact_type)` partial vector indexes with a row threshold
  (`HINDSIGHT_API_VECTOR_INDEX_MIN_ROWS`, default 0 = off): a partition above
  the threshold earns its own index, one below loses it, built and dropped
  `CONCURRENTLY`. Reconciled by a maintenance sweep and
  `bankctl bank repair-bank --vector-index-min-rows`.
- OpenTelemetry tracing: with `HINDSIGHT_API_OTEL_TRACES_ENABLED=true` and an
  `HINDSIGHT_API_OTEL_EXPORTER_OTLP_ENDPOINT`, every model call exports a GenAI
  span over OTLP/HTTP JSON. Dependency-free, off the latency path, and shed
  rather than buffered when the collector stalls.
- Document detail reports the retain-time `observation_scopes` spec, and chunk
  reads report the `attachments` their units were drawn from. The Postgres
  `GetDocument` now returns `retain_params`, `tags` and `updated_at`.
- Creating a knowledge page whose name already exists in the same folder is
  refused with 409.
- Consolidation passes are serialized per bank (`consolidate:<bank>` key), so
  two passes over one bank cannot read the same facts and create twin
  observations; different banks still run concurrently.
- The worker reserves a concurrency floor per operation type
  (`HINDSIGHT_API_WORKER_CONSOLIDATION_RESERVED_SLOTS`, default 2), so a bank
  mid-bulk-ingest cannot starve observation folding.
- Consolidation drops a near-duplicate observation create (token Jaccard ≥ 0.7
  against existing or same-pass observations), not only an exact restatement.
- Webhook delivery retries on the reference's backoff ladder
  (`[5s, 5m, 30m, 2h, 5h]`); a handler can name an explicit deferral delay with
  `worker.DeferAt`.
- The S3 object store can delete a key prefix, and deleting a bank reclaims
  that bank's external payloads.
- The provider roster gains the `volcano` vendor and a deterministic `mock`
  provider for offline model-driven paths.
- A handler can mark a failure deferrable (`worker.Defer`); a provider rate
  limit is mapped to one, so the operation is rescheduled without consuming its
  retry budget. Retention sweeps pause 250 ms between chunks.
- Queue claiming is bank-fair: the claim serves the bank with the oldest
  pending work, so one bank's backlog cannot starve the others.
- Fact extraction retries once with a self-correcting instruction when the
  model's first response cannot be parsed as the required JSON shape.
- `/metrics` reports `wenyuan_async_operations_pending` and
  `wenyuan_async_operations_failed`, aggregated across banks at scrape time.
- A per-operation-type wall ceiling
  (`HINDSIGHT_API_WORKER_{RETAIN,CONSOLIDATION,REFLECT}_WALL_TIMEOUT_SECONDS`)
  bounds a wedged task below the claim's lease, freeing its slot sooner.
- A loop-lag probe reports a scheduler stall past
  `HINDSIGHT_API_LOOP_LAG_STALL_SECONDS` at
  `HINDSIGHT_API_LOOP_LAG_REPORT_SECONDS` cadence.
- A mental-model refresh honours a `delta` trigger mode: it folds new evidence
  into the existing document (preserving unchanged sections) and falls back to
  the full answer when it cannot, recording `delta`/`delta_fallback` in history.
- The worker treats store backpressure (`ErrWritesUnavailable`, `ErrReadShed`)
  as a deferral rather than a failure: the operation is rescheduled without
  consuming its retry budget, so a sustained shed cannot permanently fail work.
- Settings honour the reference's worker key names as aliases
  (`WORKER_POLL_INTERVAL_MS`, `WORKER_MAX_SLOTS`,
  `WORKER_TASK_RETRY_BACKOFF_SECONDS`), and `HINDSIGHT_API_PROFILE=true` mounts
  the `/debug/pprof/*` endpoints.
- A knowledge tree node reports `managed` (set for an API-created page) and its
  `sort_order` round-trips through storage; a page's body was already carried by
  its backing mental model.
- A `graph.maintenance` job drains the graph-relink and entity-prune queues: a
  relinked unit rebuilds its semantic edges from its embedding, and an entity no
  unit mentions any more is pruned.
- `bankctl` gains `export-bank`/`import-bank`, `repair-bank` (re-schedules
  stranded consolidation) and `worker-status` (per-bank pending/failed counts).
- Recall, retain and reflect are admitted through per-operation concurrency
  lanes: a saturated lane sheds with `503` and `Retry-After`, and a request that
  goes away while queued is reported as `499` (`HINDSIGHT_API_ADMISSION_*`).
- The MCP tool roster covers the reference surface: bank lifecycle, memories and
  documents, operations, tags and stats, mental models, directives, the
  knowledge base, entities, consolidation, observation scopes and webhooks.
- The worker process serves `/health`, `/health/ready` and `/health/live` on
  `HINDSIGHT_API_WORKER_HTTP_PORT` (default 8889), with a liveness payload
  carrying `worker_id`, `is_shutdown` and `seconds_since_last_poll`.
- Webhook delivery is wired: a `consolidation.completed` event queues a
  `webhook_delivery` operation for each subscribed webhook, and the worker
  delivers it with an HMAC-SHA256 signature (`X-Hindsight-Signature`,
  `X-Hub-Signature-256`), retrying transport and 5xx failures.
- A re-retain of an unchanged document (`document_id` set, hash unchanged, no
  `force_reextract`) is a no-op that returns the existing units; a per-bank
  config can disable recall arms (`enable_*` toggles); and `temporal_window`
  requires both bounds in order.
- Consolidation drops an exact-duplicate observation create or a no-op update,
  so a restated conclusion does not accumulate as a twin.
- Chunking keeps structured inputs whole: a JSON conversation is chunked per
  turn and a JSONL body per record, so a chunk never splits a turn or a record.
- Fuzzy tag matching uses trigram Jaccard (≥ 0.45) within a namespace rather
  than substring containment.
- Fact extraction declares `where`, `fact_kind`, `fact_type` (world|experience)
  and `from_attachments`; an experience fact is stored as an experience unit and
  its `where`/`fact_kind` round-trip through metadata; punctuation-only facts
  are rejected.
- The retain write path creates `semantic` links between units whose embeddings
  are within the similarity floor (0.75 cosine), in the in-process store by
  cosine and in PostgreSQL by the pgvector distance operator; consecutive units
  of one item are linked as `temporal` rather than mislabelled `semantic`.
- PostgreSQL recall runs the same multi-arm pipeline as the in-process store:
  a pgvector semantic arm, a `ts_rank_cd` BM25 keyword arm and a
  link-expansion graph arm, fused by reciprocity and scored through the shared
  path, honouring tags, the temporal window and per-arm floors.
- The API process runs the queue consumer and the maintenance loop when
  `HINDSIGHT_API_WORKER_ENABLED` is set (default true), matching the reference's
  standalone mode; the `worker` feature flag is reported by the version
  endpoint.
- The retain write path honours `update_mode`: `replace` rebuilds a document's
  units, chunks and links and `append` concatenates onto the stored document;
  retains persist `context` and `mentioned_at`; and a chunk id is the
  injective `bank_doc_index` form the reference builds (`BuildChunkID`).
- The retain write path embeds each stored unit and recall embeds the query text
  when an embedding model is configured, so the vector recall arm and its ANN
  index have vectors to rank.
- `llm_requests` is populated: each completion is recorded at the provider
  boundary when `HINDSIGHT_API_LLM_TRACE_ENABLED` is set.
- `/version` and `/v1/version` report all eleven reference feature keys
  (`observations`, `mcp`, `worker`, `bank_config_api`, `bank_llm_health`,
  `file_upload_api`, `document_export_api`, `document_import_api`, `audit_log`,
  `llm_trace`, `store_document_text`), and the versioned route returns the same
  payload as the root endpoint.
- The readiness and liveness probes report the reference's fields: readiness
  names the database state and the connection-acquire latency, liveness reports
  the version and process uptime.
- The per-bank `/health/llm` route is gated by
  `HINDSIGHT_API_ENABLE_BANK_LLM_HEALTH` (default off → 404) and the file retain
  route by `HINDSIGHT_API_ENABLE_FILE_UPLOAD_API` (default on → 404 when off).
- The bank configuration read/write/reset responses include `bank_id`.

### Fixed

- `DELETE /v1/default/banks/{bank_id}` deletes the bank's content, not merely its
  row. The memory domain carries no foreign key to `banks` — `memory_units`,
  `documents` and `entities` reference each other, and only the async queue,
  attachments, aliases, directives, knowledge pages, mental models and webhooks
  cascade — so the previous `DELETE FROM banks` orphaned every memory the bank
  owned and left it readable. Both backends now purge the domain in the
  reference's order (documents, then the rows no cascade reaches, then the bank
  row) inside one transaction, and a bank id can be reused immediately. The
  envelope is the reference's too: `deleted_count` is
  `memory_units + entities + documents` (it was the count of banks matched), and
  `message` is one string for every outcome, including a bank that never existed
  — which reports zero rather than faulting.
- Async operation types carry the reference's names: `consolidation`,
  `export_documents`, `import_documents`, `export_bank`, `import_bank`,
  `clone_bank`. They are a public field on the operations API and the frontend
  filters and labels on them, so the six values this service had invented
  (`consolidate`, `document_export`, `document_import`, `transfer_export`,
  `transfer_import`, `clone`) were a wire divergence — the copied operations view
  could not have filtered on its own engine's output. The dead `reflect`
  operation type is gone with them, the consolidation LLM call reports the
  reference's `consolidation` scope, and the worker's wall ceilings are keyed by
  operation type so a refresh is bounded (it shares reflect's budget, as the
  reference records: without a ceiling a wedged refresh held its worker slot).
- A memory's indexed embedding text is the reference's augmentation, field for
  field: the date is the unit's own `occurred_start` (falling back to
  `mentioned_at`) rather than the retain clock, a span renders as a range,
  entities are bracketed, and a unit with no date gets no date clause. Indexing a
  2021 fact with the month it was retained asserts a time the fact does not.
- `HINDSIGHT_API_TEXT_SEARCH_EXTENSION` is refused for the values this build
  cannot honour (only the native tsvector backend is implemented) instead of
  being accepted and ignored, and its dictionary key is refused only for a
  dictionary other than `english` rather than for being set at all.
- A retain whose items the bank's Memory Defense policy blocks **in full** is
  refused with 422 and the violations under `detail`, as the reference does,
  rather than answered 200 having written nothing. The screen runs on the items
  the caller submitted, before folding, so a blocked item no longer takes a clean
  neighbour with it and the reported positions are the caller's.
- The frontend's `useAsyncData` keyed its requests by the identity of a caller's
  inline dependency array, so every panel refetched on every render (211 requests
  in 3s); it is value-keyed now, with a regression test.

- Validation failures carry the reference's vocabulary: the entries were named by
  the *input* type before the target type was consulted (`{}` sent to an integer
  was named `dict_type`), and every entry carried a `url` the reference never
  sends — FastAPI builds its entries with `exc.errors(include_url=False)`, and
  its published `ValidationError` declares only `loc/msg/type/input/ctx`. An
  empty body is `missing` with `input: null` preserved through the null-dropping
  encoder, and recall/reflect name the offending `input`.
- A bank id is refused at creation, not on read: the memory backend's lazy create
  accepted an over-long or control-character id that postgres and the reference
  both refuse with 400, and `PATCH /banks/{id}/config` discarded the create error
  and answered 404. `DELETE /banks/{id}/config` on a bank that does not exist is a
  no-op that reports the resolved defaults with 200, as the reference's
  unconditional clear does, and it creates no row.
- The metrics `endpoint` label names the route template the reference names: Go's
  multi-segment wildcard (`{chunk_id...}`) was rendered verbatim where the
  reference strips the converter (`{chunk_id}`), so one route produced two series.
- Fuzzy tag resolution resolves a query's leaves against the bank's tag
  vocabulary and then matches exactly, on both the knowledge and the recall path
  (the recall path scored similarity per row, so an exact hit widened to its
  neighbours and a near miss matched with an empty vocabulary). The vocabulary and
  `exact`-combination caps now apply, with the reference's refusal text.
- MCP accepts what the reference accepts: it rewrites `Accept` to include
  `text/event-stream` when it is absent, so a missing header, `application/json`
  and `*/*` are all served — this repo answered 406 for the last two. The 406
  envelope and the GET event-stream framing are the SDK's.
- `HINDSIGHT_API_STORE_DOCUMENT_TEXT=false` stores no document text as NULL
  rather than an empty string, its append refusal names the setting in the
  reference's words, and the reference-direct reader no longer scans the nullable
  column into a string (which failed outright on a bank that opted out).

- A knowledge-base `tag_groups` leaf with `resolve:"fuzzy"` resolves its tokens
  against the bank's tag vocabulary and then matches exactly, as the reference
  does, instead of comparing the token to each page's tags by similarity. A token
  that is already a real tag is no longer widened to its neighbours, a token with
  no near vocabulary tag stays unsatisfiable rather than dropping the filter, and
  a bank holding more distinct tags than the resolution cap is refused with the
  reference's 422.
- The MCP transport applies the reference's `Accept` handling: the two media
  types a response may carry are supplied when the caller's `Accept` does not ask
  for an event stream, so a missing header and a JSON-only header are both served,
  while a request that asks for an event stream and refuses JSON is refused with
  the reference's 406 and a `null` JSON-RPC id. The GET event stream frames its
  keep-alive comment the way the reference's SSE writer does.
- The refusal an append-mode retain gets when document-text storage is off
  carries the reference's wording verbatim, naming the setting
  (`update_mode='append' is not supported when document text storage
  (store_document_text / HINDSIGHT_API_STORE_DOCUMENT_TEXT) is disabled: …`)
  rather than a paraphrase that dropped the setting's name.
- The PostgreSQL write stores a withheld document body as `NULL original_text`
  when `HINDSIGHT_API_STORE_DOCUMENT_TEXT=false`, which is the reference's own
  representation (the in-memory backend reads back empty either way), and the
  archive reader that pulls a bank straight out of a reference database tolerates
  the same `NULL` rather than failing the scan.
- The metrics `endpoint` label names the route template the way the reference
  does for a path parameter that spans segments: Go spells it `{chunk_id...}`
  where the reference declares `{chunk_id:path}` and labels by the
  converter-stripped `{chunk_id}`, so the wildcard is stripped from the label
  (`/v1/default/chunks/{chunk_id}`, `/v1/default/files/download/{key}`).
- A mental model's refresh reads the whole scope when a delta cannot be taken:
  a delta needs content to anchor on and a source query that has not moved, and
  the reference falls back to a full read otherwise (`no_baseline_content`,
  `source_query_changed`). Two consequences of the missing gate: a knowledge
  page — created empty, with a `delta` trigger — never wrote its first document,
  and a template reapply that rewrote the source query left the old answer in
  place. The refresh also records the query it ran (`last_refreshed_source_query`),
  which is what makes a later topic change detectable at all, and its operation
  metadata carries `mode_fallback_reason` like the reference's.
- A model's refresh window is bounded by `last_memory_seen_at` alone. Reading
  the creation stamp (`last_refreshed_at`, which the schema sets to `now()`) as
  "already seen every memory" bounded the first delta refresh to memories
  written after the model was created, so it read nothing and preserved the
  empty document.
- The two existence checks a refresh and a staleness test hang on filter by
  `fact_type`, not `type`. `memory_units` gives the fact type its own column
  name, so the statement failed — and the bool-returning wrapper reported the
  failure as "nothing in scope", which left every `fact_types`-scoped model
  (knowledge pages) never refreshing and never going stale. Both now log the
  error rather than swallowing it.
- The BM25 arm orders tied ranks by physical order (`ctid`), which is what the
  reference gets by ordering on the rank alone: an explicit uuid tie-break
  reported the pipeline's ranks in an order the reference never produces.
- The embedding column width is reconciled at startup instead of hand-edited in
  a generated migration: the dumped reference declares its own deployment's
  width, and the reference itself resolves the column from the configured model.
  Equal width is a no-op, a mismatch with stored vectors is refused with both
  widths named, and a mismatch on an empty table drops the vector index, alters
  the column, and rebuilds the index from its captured definition.

- A bank export carries its history at last: `mental_model_history.json` rides
  with the data section (a model's refresh log belongs to the model), and
  `--include-history` adds `history/audit_log.json` and `history/llm_requests.json`.
  An import replays all three, so the manifest's `includes_history` and the
  import summary's `mm-history row(s)` / `history row(s)` counts are now true.
- A bank export carries its operations log (`data/async_operations.json`), and
  the manifest's `operation_count` is now real. Only finished operations travel:
  a pending one names work the source instance still owes, and an import that
  restored it would make the target's worker run work that was never its own.
- A bank export carries its curation archive: `data/invalidated_memory_units.json`
  holds the units a caller invalidated but can still revert, verbatim rows because
  they carry columns no unit model projects (entity ids, causal links, when and
  why). An import re-archives them under the target bank and reports
  `invalidated fact(s)`.
- Attachment bytes travel with a bank export: the archive carries an
  `attachments.json` section and one `blobs/NNNNNN.bin` entry per attachment, and
  an import writes the bytes back under a storage key recomputed for the target
  bank. A row whose bytes storage no longer holds is skipped rather than carried
  as an empty blob, a row naming bytes the archive does not hold is refused, and
  an attachment whose document did not come across is skipped because its row
  would name a document the target does not have.
- An uploaded file's attachment is filed under the document the upload is
  retained as — the caller's `document_id`, or a minted one — instead of the
  file's name. Filing it under the name left the attachment unreachable from the
  document that owns it, so a document detail reported none and the placeholder
  in its text could not resolve.
- `bankctl export-bank`/`import-bank` move a whole bank again: they drive the
  transfer engine (`internal/transfer` + `internal/hindsightarchive`) instead of
  serializing only the bank profile and its aliases, so documents, facts,
  observations, bank config, mental models, knowledge pages, directives and
  webhooks cross instances in a reference-readable ZIP. `import-bank` re-embeds
  with the deployment's own embedding backend and refuses an existing target, and
  the profile-only `bank archive export|import` group it replaces is gone.
- `bankctl restore` works on PostgreSQL: the tables are cleared in one
  `TRUNCATE` statement, because a per-table truncate is refused as soon as any
  table is referenced by another — every restore failed before this, however well
  ordered the calls were.
- `bankctl repair-bank` matches the reference: `--bank/-b` xor `--all`,
  `--dry-run`, the per-bank counters and the aggregate `Done:` line, orphaned
  indexes dropped in `--all` mode, and a reported no-op when the backend keeps
  its vector indexes store-owned.
- `bankctl worker-status` reports what the reference reports: every `processing`
  operation grouped by the worker holding it, with its type, bank, run time and
  last update, instead of per-bank pending/failed counters.
- `bankctl run-db-migration` takes `--embedding-dimension` (validated against the
  width the migration series fixes) and `--skip-extension-reconcile`; every
  database command takes a global `--schema/-s`, and a migration sweep without
  one covers the base schema plus every schema the tenant extension reports.
- `bankctl` argument handling: `backup`/`restore` and `decommission-worker` accept
  the reference's positional file/worker argument with flags on either side,
  `rename` takes `--from/-f`, and supplying an argument both ways is refused.

- The client CLI (`wenyuan-admin`) matches the reference command surface:
  `bank export-template` takes `-o/--out`, `audit list` and `webhook deliveries`
  take `-l`/`-s` paging shorts, `explore` answers to `tui`, and `-V/--version`
  reports the CLI's own version. `-o` after `bank export-template` is the
  command's own output file rather than the global format, as the reference's
  global arguments resolve, and `--help` at a group path lists that group.
- `mental-model create --tags-match` reaches the request: the value travels in
  the trigger, as the reference sends it, instead of only gating the trigger.
- `directive update --priority 0` is sent: tri-state flags now distinguish an
  absent flag from an explicit zero, and `memory reflect --max-tokens 0` keeps
  the given value rather than substituting the default.
- `webhook deliveries` omits an unset `limit` and no longer clamps a given one;
  `knowledge-base search` omits an unset `limit`; `fs status --stale-after 0`
  applies the literal threshold and reports `last_sync_ok` as a boolean.
- `document list --date ""` is an invalid date rather than a silent plain
  listing, date mode steps its offsets by the page size, `document get` titles
  the response's own id, and `document list --date all` is labelled "all dates".
- Validation raised inside a command body exits 1 (a usage error still exits 2),
  matching the reference's split between its parser and its command failures.
  Failure lines print to stderr (`print_error`), so `document delete`,
  `memory clear`, `retain-files` and `webhook delete` report there.
- Client-side messages carry the reference's exact text: `Path does not exist:`,
  `Failed to read schema file:`, `Failed to parse JSON schema from:`.
- `bankctl` asks for confirmation before `restore`, `decommission-worker` and
  `decommission-workers` (`--yes`/`-y` skips it), exits 2 on a malformed flag,
  and the README documents the dispatcher's real command spellings.

- The bank-scoped extraction configuration reaches the model: `retain_extraction_mode`
  now selects the prompt and response schema (`concise` default, `verbose`,
  `custom`, `verbatim`, `chunks`), `retain_custom_instructions` is consulted only
  in `custom` mode as the reference does, `retain_optional_fact_dimensions` relaxes
  the four descriptive dimensions, and `chunks` stores each segment as its own
  memory with no model call. Verbose answers collapse their what/when/who/why
  dimensions into the stored fact text, and verbatim answers keep the original
  chunk text with the model's metadata. The prompt preview renders the bank's mode.
- The HTTP contract was audited operation by operation against the reference's
  own OpenAPI document (99 operations, 195 schemas) in eleven parallel slices.
  Every divergence it found is recorded with evidence in `docs/CONTRACT-AUDIT.md`,
  and the fixes below landed with it.
- The reference's descriptive surface is served: `GET /openapi.json` (its own
  contract document, captured by `scripts/reference_openapi.py`), `/docs`,
  `/docs/oauth2-redirect` and `/redoc`, each under `HINDSIGHT_API_BASE_PATH` when
  one is configured.
- `HINDSIGHT_API_BASE_PATH` serves the whole surface under a prefix as well as at
  the root, which is how a deployment moves behind a path-prefixing proxy.
- `GET /version` reports the reference release and the reference's feature-flag
  defaults (MCP, bank-config API, document import/export on; audit log off;
  llm_trace on), with `HINDSIGHT_API_ENABLE_OBSERVATIONS`,
  `HINDSIGHT_API_AUDIT_LOG_ENABLED` and `HINDSIGHT_API_STORE_DOCUMENT_TEXT` now
  driving their flags.
- Null fields are dropped by default, as the reference drops them, except for the
  models that must keep them (graph rows, memory/document list items, operations,
  audit and model-request entries, webhooks, strategy previews).
- An unmatched path or method answers the reference's JSON envelope
  (`{"detail":"Not Found"}` / `{"detail":"Method Not Allowed"}`) instead of the
  router's plain text.
- Tenant authentication is enforced when `HINDSIGHT_API_TENANT_EXTENSION` selects
  the built-in API-key authenticator: every engine-backed route requires
  `HINDSIGHT_API_TENANT_API_KEY` (401 `{"detail":"Authentication failed: Invalid
  API key"}`), the MCP namespace is exempt under
  `HINDSIGHT_API_TENANT_MCP_AUTH_DISABLED`, and any other extension class refuses
  startup rather than serving unauthenticated.
- A malformed or wrong-typed request body is the framework's structured 422 with a
  `json_invalid`/`*_parsing` item (`httpx.DecodeBody`), not a `400` sentence, and a
  list position in `loc` is an integer.
- The three retired bank routes answer `410` without a credential, each with the
  reference's own message for the route.
- Admission lanes name the operation and match one method, so clearing a bank no
  longer consumes the retain lane, and the shed messages are the reference's.
- `HINDSIGHT_API_PROFILE` is the reference's JSON profiling request (validated at
  startup, reported periodically on the log stream); the Go pprof endpoints moved
  to `HINDSIGHT_API_PPROF_ENABLED`.
- The knowledge-base routes match the reference: page and folder creation answer
  `201`, page creation returns its backing mental model, search results carry
  `snippet`, the export starts with `index.md`, `PATCH nodes/{id}` accepts the
  reference's fields, the tree and the search honour the tag filters, and search
  requires `q` with `limit` in 1..50.
- The memory read surface matches the reference: `fact_type` (not `type`),
  `state`/`consolidation_state` enums, `tags_match` defaulting to `any`, the
  memory detail document, `PATCH` with the reference's fields, and a bare-array
  observation history.
- `POST /consolidate` answers `200`; the consolidation-strategy preview and the
  prompt preview carry the reference's shapes; `dry-run-extract` returns the
  reference's facts, chunks and usage.
- Mental models take `trigger` and a caller-supplied `id`, refresh answers the
  operation-submit shape, dry-run refresh carries the reference's projection, and
  history is the reference's bare array.
- `/import` and `/export` are the JSON bank-template routes again, `transfer/import`
  defaults to `restore` with the reference's enums, and webhooks answer `201`,
  emit `secret: null`, and page deliveries as `{items,next_cursor}`.
- The entity graph draws only entities an edge joins, with the reference's
  presentation, and `POST /entities/{id}/regenerate` is `410` as the reference is.
- `/stats/memories-timeseries` uses the reference's period windows (1h/12h/1d/7d/
  30d/90d with the right bucket width, an unknown name falling back to 7d) and its
  `created_at` axis is populated, so the series counts the memories it should.
- The memory write path's documented defaults match the reference (`resolve_entities`,
  naive ISO timestamps), and the recall body accepts no pagination fields the
  reference does not declare.
- A missing bank is reported as `Bank '<id>' not found`.
- Exported metrics use the reference's names (`hindsight_http_requests_total`,
  `hindsight_http_requests_in_progress`).
- Postgres recall: the arm queries' parameter positions are contiguous again, and
  `tags_match=any_strict` matches by overlap (it required every requested tag),
  which had made a tag-scoped recall return too few rows.
- `observation_scopes` reaches the stored unit, so consolidation really folds
  per scope instead of merging across the scopes a caller asked to keep apart;
  `fact_type:"assistant"` survives extraction and is stored as the
  agent-perspective type.
- A deferred webhook delivery keeps the receiver status it reported, an invalid
  or orphaned per-bank vector index is rebuilt or dropped, a graph-maintenance
  batch survives one failing target, and `CancelOperation`/`RetryOperation`
  return a fully scanned operation (they returned too few columns to scan).
- Memory write and read defaults match the reference again: retain treats an
  omitted `resolve_entities` as true (it resolved nothing before) and accepts the
  naive ISO timestamp form, and recall's omitted `include.entities` still means
  include entities rather than exclude them.
- The async-operation routes are bank-scoped and shaped like the reference: the
  single read, cancel and retry resolve the operation under the path bank, an
  unknown or wrong-bank read answers `200` with `status:"not_found"` rather than
  `404`, the read returns the full operation status (with `child_operations` and
  `task_payload`), the list honours `exclude_parents`, and a non-UUID identifier
  is a `400` on every member route.
- `GET /banks/{bank}/attachments/{id}` serves a retained attachment: it resolves
  the attachment row for the storage key (the route previously guessed a key that
  never matched a retained attachment, so it always answered 404) and answers with
  the media type declared at retain.
- A PostgreSQL server reporting `standard_conforming_strings=off` is refused at
  startup: two statements render client values as SQL literals and their escaping
  is only correct under the default setting.
- Webhook delivery refuses to follow a redirect and re-validates the target —
  including the address actually dialled — so a receiver cannot redirect the
  server at an internal address; Memory Defense no longer fails open when the
  bank configuration cannot be read.
- The Anthropic adapter forwards the caller's tools and decodes `tool_use`
  blocks, so a reflection on that provider retrieves instead of answering from
  the model's own knowledge.
- Validation failures now use the reference's `{"detail":[...]}` envelope and
  carry `ctx`/`url` on each item, so a typed client reading `detail` works.
- `X-Ignored-Params` reaches the wire: the header is injected when the response
  status is committed (it was set after `WriteHeader` and dropped), unknown
  names keep their encounter order and join with `", "`, and unknown body
  fields recorded by a handler are visible to the middleware.
- `bankctl bank rename` no longer destroys the bank's data. It moves the bank
  row and every bank-scoped row in one operation (`storage.RenameBank`) instead
  of creating an empty target and deleting the source, and refuses a target
  that already names a bank.
- The ignored-parameter header no longer reports a parameter that a route
  genuinely reads (`period`, `time_field`, `status`, `action`, `transport`,
  `subtype`, `kind`, `document_id` and others were absent from the accepted
  set), and recall/reflect report an unknown JSON body field the same way an
  unknown query parameter is reported.
- The provider families (`HINDSIGHT_API_LLM_*` and the four per-role
  `*_LLM_*` scopes, `HINDSIGHT_API_EMBEDDINGS_*`, `HINDSIGHT_API_RERANKER_*`
  and the global litellm credentials) are accepted key by key. A key this build
  implements resolves into configuration (the mixed-case scoped chat keys, the
  per-role scopes, and the chat/embeddings transport and retry envelope); a key
  for a backend this build does not ship — the reranker provider subsystem, the
  ONNX/TEI/Gemini/Cohere/OpenRouter/Requesty/ZeroEntropy/litellm embedding
  backends, an in-process local embedder, `HINDSIGHT_API_EMBEDDING_DIMENSION`,
  the multi-model routing strategies, or an unimplemented chat provider name —
  now fails startup and names the key instead of being silently ignored. The
  key list is enumerated from the reference's constants by
  `scripts/gen_provider_env_keys.py`, so a newly added upstream key surfaces as
  a failing table test rather than drifting.
- Entity intake drops a name past the 512-character btree-safe cap (and a blank
  one) instead of truncating it, so an extraction artifact — base64, SVG path
  data — no longer becomes a registry entity a later write could merge onto,
  and the over-long name no longer risks failing the whole retain.
- The entity graph is populated: retain records a co-occurrence for every pair
  of entities a single memory mentions, which is what `GET .../entities/graph`
  reads for its edges (it was always empty before, so a graph view drew nothing).
- Replacing a document prunes the entities only the replaced text named, so an
  entity with no surviving mention leaves the listing rather than lingering with
  a stale identity; the mention count already tracked the surviving links.
- The recall trace reports the graph arm's results. The arm was run and its
  candidates fused, but its hits were never appended to the retrieval trace, so
  the `graph` entry always came back empty even when it reached a memory.
- Entity `first_seen`/`last_seen` keep their sub-second precision (RFC3339Nano),
  as the reference's own `isoformat` does; a second-resolution format reported
  `first_seen == last_seen` for an entity seen twice within one second.
- A validation item carries exactly the keys the reference's framework emits.
  The reference builds its items with `include_url=False`, and the contract
  document's `ValidationError` declares `loc`/`msg`/`type`/`input`/`ctx` only,
  so the `url` documentation link is no longer attached; a null `input` (an
  absent body) is rendered rather than dropped by the null-field shaping.
  The wrong-type entries are the framework's own: a scalar sent where a list or
  a dictionary belongs is `list_type` ("Input should be a valid array") or
  `dict_type` ("Input should be an object"), the `_parsing` wording includes
  what would not parse, and a non-string sent for a scalar names the `*_type`
  entry (or `int_from_float` for a fractional number) rather than a parsing
  failure. A body that arrives empty is the framework's `missing` item, and the
  item names the value the decode failed on for the recall and reflection routes
  too.

### Changed

- The repository's own identity copy now says `wenyuan` everywhere: the LICENSE
  copyright holder is `wenyuan-go contributors`, and the docs, comments, log
  messages, `--help` text and page titles refer to "the reference
  implementation" (「参考实现」/「上游」 in Chinese) where they used to name the
  product this service replaces. Client-visible strings that changed with it:
  `bankctl`'s `--archive` usage text, the Swagger/ReDoc page titles, the bank
  template version error (`Please upgrade Wenyuan.`), and the worker metrics
  snapshot directory (`$TMPDIR/wenyuan-metrics-<pid>`); the graph helper
  `convertHindsightGraphData` became `convertGraphPayload`. Strings that stay as
  they were are listed with their reasons in `AGENTS.md` under *Naming*:
  environment-variable and cookie names, the document served at
  `/openapi.json`, Prometheus metric names, the `X-Hindsight-*` webhook headers,
  the MCP `serverInfo.name`, the OTEL default service name, the daemon log
  default, the extension class value, our packages named after the reference's
  export format (`internal/hindsightarchive`, `internal/migration/hindsight`,
  `bankctl --from-hindsight`), citations of the reference's own paths, and the
  vendored parity suite.
- `make install` builds all four binaries with the `pgx` tag and adds
  `bin/worker` to the set. The previous untagged build produced a server and a
  bankctl without a PostgreSQL driver, which refuse to start against a
  configured `database_url`; `make run` now carries the tag for the same
  reason. The compose stack is dependency services only, so the README no
  longer describes `docker compose up --build` as API + worker.
- Recall scoring matches the reference after reranking: the normalized
  cross-encoder score is the base with RRF dropped from it, the combined boost
  multiplies the base, and the strategy boost is an absolute add. Reranker
  scores in `[0,1]` pass through, logits are squashed by a sigmoid.
- The reflection loop's tool schemas require `reason`; `recall` carries
  `max_chunk_tokens`, `read_mental_models` uses `mental_model_ids`, and `expand`
  carries `depth`. A `done` before any evidence is ignored, a tool failure fails
  the run rather than returning "nothing found", and model-supplied token
  budgets are clamped.
- Delete routes acknowledge with the reference's body and status: mental
  models, directives, knowledge nodes and webhooks return `200`, a document
  delete returns `200` with `{success, message, document_id,
  memory_units_deleted}`, and `storage.DeleteDocument` reports the cascade
  count.
- The directive create returns `200` (was `201`); the directive list is the
  `{items,total,limit,offset}` envelope with `active_only`/`tags`/`tags_match`
  filtering.
- Recall side channels use the reference field names (`chunk_index`,
  `entity_id`), and `source_facts` values are full result objects rather than
  bare text.
- Mental models expose `trigger`, `last_memory_seen_at`, `last_refresh_failed_at`
  and `is_stale`; the list is the `{items,total,limit,offset}` envelope with
  `tags`/`tags_match`/`detail`/paging; `detail` (`metadata`|`content`|`full`)
  selects how much content a read returns; and create returns
  `200 {mental_model_id, operation_id}`, queueing generation.
- The knowledge tree returns a `roots` array and its nodes use the reference's
  `name` and `mental_model_id` fields; a page read returns name/type/tags/
  timestamp/body/markdown, a page create accepts `name` and returns
  `{page_id, mental_model_id}`, export returns a `{files:[{path, content}]}`
  markdown bundle, and search returns `{results:[...], total}`.
- Async operations carry `operation_id`, `items_count`, `document_id`,
  `filename` and `mental_model_id`; the single read accepts `include_payload`
  and the list defaults to a page of 20.
- The document list accepts `tags`/`tags_match`/`time_field`/`start_date`/
  `end_date`, a document read reports `nodes_by_fact_type` and `attachments`,
  `/tags` honours `q` and paging, an entity read reports its observations, and
  the memory graph uses the reference's node/edge keys (`label`, `date`,
  `context`, `entities`, `source`, `target`, `linkType`, `weight`,
  `entityName`, `lineStyle`) with an entity graph node carrying `mentionCount`.
- `/stats` reports `links_by_fact_type` and `links_breakdown`; the memory
  timeseries bucket carries `world`/`experience`/`observation` counts.
- `/health/llm` probes the provider and reports
  `{operation, ok, status, latency_ms}` across retain/consolidation/reflect.
- `/v1/bank-template-schema` returns the manifest JSON Schema, and a retain
  response's `usage` is null for an asynchronous write.
- `/memories/list` accepts the reference's filters (`q`, `document_id`,
  `entity_id`, `tags`, `tags_match`, `state`/`consolidation_state`,
  `time_field`, `start_date`, `end_date`), and a memory item reports its
  `date`, `proof_count`, `consolidated_at`, `consolidation_failed_at`, `state`,
  `updated_at`, `edited_at` and `source_memory_ids`.
- `/files/retain` returns `{operation_ids}` and accepts the optional `request`
  form field (`parser`, `files_metadata`) with a parser allowlist and per-file
  `document_id`/`context`/`metadata`/`tags`/`strategy`/`timestamp`.
- `/memories/dry-run-extract` accepts `content`/`context`/`strategy` and returns
  `{facts, chunks, usage}`; `/prompts/preview` accepts `{operation, strategy}`
  and returns `{messages, strategy, strategies, run_settings, response_schema,
  skipped_reason}`; an observation scope item is `{tags, count}`.
- Unknown JSON body fields are reported on every JSON route (not only retain);
  open-row responses (graph, entity graph) keep explicit null fields; a webhook
  carries `http_config`; recall applies per-channel token budgets and marks
  truncated chunks; consolidation-strategy preview is driven by the request.

### Changed — BREAKING

- **The PostgreSQL schema is now the reference schema, exactly.** The migration
  series is generated from the upstream database's own catalogue by
  `scripts/reference_schema_migrations.py` and consists of `0001_extensions`,
  `0002_reference_tables`, `0003_reference_constraints`,
  `0004_reference_indexes` and `0005_reference_functions` — 0001..0027 are
  superseded. A database built from it is indistinguishable from one built by
  upstream: `docs/SCHEMA-ALIGNMENT.md` reports no missing, extra or different
  object, and a `pg_dump --schema-only` of each side diffs clean.
- Three tables the reference does not have are gone, along with the code that
  used them: `knowledge_nodes` (the knowledge tree is `knowledge_pages`, whose
  pages are backed by a mental model that holds the body), `webhook_deliveries`
  (a delivery is an `async_operations` row of type `webhook_delivery`) and
  `observation_sources` (an observation carries its evidence in
  `memory_units.source_memory_ids`).
- `banks` no longer stores `last_write_at` / `last_document_at`. The collection
  view derives both from the newest document and the newest memory unit, and
  orders as the reference orders.
- `mental_models` no longer stores `history`; the change log is the
  `mental_model_history` table, and its key is an identity column.
- `memory_units.search_vector` is a plain column the application maintains, not
  a generated one, and every write path sets it.
- Object definitions that had the reference's name but not its shape are
  corrected: the attachment short-id and knowledge-page-name unique indexes, the
  tag and link array widths, the two history identity keys, two check
  constraints and the `bank_aliases` column order. The four maintenance
  discovery routines are the reference's bodies; the previous
  `banks_needing_consolidation` referenced columns that do not exist and, with
  its own exception handler swallowing the error, silently found no work.
- Installing an extension is serialized with the same advisory lock the series
  takes, and each migration sets its own search path, so a deployment whose
  connection pool hands out fresh connections still resolves the right schema.

## [0.1.0] - 2026-10-13

First tagged release. The public HTTP surface and the PostgreSQL schema are the
compatibility contract; this release covers the full route set the port targets.

### Added

- Bank lifecycle API: create, list, replace, update, delete, aliases, and
  per-bank configuration.
- Retain pipeline: synchronous and asynchronous memory writes, inline file
  uploads with content-addressed attachment persistence, chunk and entity
  persistence, and async operation tracking.
- Recall API: multi-arm retrieval, fused re-ranking, token-budget selection,
  tag filters, temporal windows, and real vector similarity over a pgvector
  HNSW index.
- Reflect API: a bounded, model-driven agentic loop with cited evidence,
  execution traces, structured output, and directives.
- Consolidation engine: a queue-driven pass that folds raw facts into
  observations with evidence links and marks the facts it consumed.
- Model provider boundary with an OpenAI-compatible adapter and a no-model
  selection for offline operation.
- Background queue consumer (`cmd/worker`) with atomic claim, lease and retry.
- Export / import / clone / transfer and attachment-download routes over the
  archive pipeline.
- Mental models, directives, knowledge base, observations, webhooks, audit
  logs, and the read surface.
- An MCP namespace answering JSON-RPC `initialize`, `tools/list` and
  `tools/call` over the same store the HTTP surface uses.
- Monitoring surface: health, liveness, readiness, version, and metrics.
- PostgreSQL storage layer with embedded, versioned migrations (recorded in
  `schema_migrations`) and a cross-process bank-statistics cache.
- `bankctl` administrative CLI: bank rename and archive export/import.

[Unreleased]: https://github.com/studyzy/wenyuan/compare/v0.1.0...main
[0.1.0]: https://github.com/studyzy/wenyuan/releases/tag/v0.1.0