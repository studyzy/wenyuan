package config

// Runtime (non-provider) environment keys, spelled as the reference spells
// them. The provider families (LLM, embeddings, reranker, vision) live in
// provider_env_keys.go; these are every other key the reference reads from its
// environment. reference.go decides, for each, whether this build resolves it
// or refuses it.
const (
	// Process, server and logging.
	EnvLogLevel                = "WENYUAN_API_LOG_LEVEL"
	EnvAccessLog               = "WENYUAN_API_ACCESS_LOG"
	EnvWorkers                 = "WENYUAN_API_WORKERS"
	EnvLogFormat               = "WENYUAN_API_LOG_FORMAT"
	EnvLogJSONFields           = "WENYUAN_API_LOG_JSON_FIELDS"
	EnvDaemonLog               = "WENYUAN_API_DAEMON_LOG"
	EnvMCPAuthToken            = "WENYUAN_API_MCP_AUTH_TOKEN"
	EnvMCPStateless            = "WENYUAN_API_MCP_STATELESS"
	EnvMCPEnabledTools         = "WENYUAN_API_MCP_ENABLED_TOOLS"
	EnvMCPInstructions         = "WENYUAN_API_MCP_INSTRUCTIONS"
	EnvEnableDryRunExtract     = "WENYUAN_API_ENABLE_DRY_RUN_EXTRACT"
	EnvDefaultBankTemplate     = "WENYUAN_API_DEFAULT_BANK_TEMPLATE"
	EnvExtensionPassThroughHdr = "WENYUAN_API_EXTENSION_PASSTHROUGH_HEADERS"

	// Database pool and migrations.
	EnvDatabaseBackend            = "WENYUAN_API_DATABASE_BACKEND"
	EnvReadDatabaseURL            = "WENYUAN_API_READ_DATABASE_URL"
	EnvReadDBPoolMinSize          = "WENYUAN_API_READ_DB_POOL_MIN_SIZE"
	EnvReadDBPoolMaxSize          = "WENYUAN_API_READ_DB_POOL_MAX_SIZE"
	EnvMigrationDatabaseURL       = "WENYUAN_API_MIGRATION_DATABASE_URL"
	EnvDBCommandTimeout           = "WENYUAN_API_DB_COMMAND_TIMEOUT"
	EnvDBAcquireTimeout           = "WENYUAN_API_DB_ACQUIRE_TIMEOUT"
	EnvDBPoolSlowAcquireThreshold = "WENYUAN_API_DB_POOL_SLOW_ACQUIRE_THRESHOLD_SECONDS"
	EnvDBStatementTimeout         = "WENYUAN_API_DB_STATEMENT_TIMEOUT"
	EnvDBMaxParallelWorkersGather = "WENYUAN_API_DB_MAX_PARALLEL_WORKERS_PER_GATHER"
	EnvDBSessionSetupOnAcquire    = "WENYUAN_API_DB_SESSION_SETUP_ON_ACQUIRE"
	EnvDBAcquireWarnThresholdMS   = "WENYUAN_API_DB_ACQUIRE_WARN_THRESHOLD_MS"
	EnvMigrationIsolation         = "WENYUAN_API_MIGRATION_ISOLATION"
	EnvMigrationConcurrency       = "WENYUAN_API_MIGRATION_CONCURRENCY"

	// Feature flags for consolidation and history.
	EnvEnableAutoConsolidation  = "WENYUAN_API_ENABLE_AUTO_CONSOLIDATION"
	EnvEnableObservationHistory = "WENYUAN_API_ENABLE_OBSERVATION_HISTORY"
	EnvEnableMentalModelHistory = "WENYUAN_API_ENABLE_MENTAL_MODEL_HISTORY"

	// Retrieval arms and their tuning.
	EnvEnableTextSearch              = "WENYUAN_API_ENABLE_TEXT_SEARCH"
	EnvEnableTemporalRetrieval       = "WENYUAN_API_ENABLE_TEMPORAL_RETRIEVAL"
	EnvEnableGraphRetrieval          = "WENYUAN_API_ENABLE_GRAPH_RETRIEVAL"
	EnvEnableReranking               = "WENYUAN_API_ENABLE_RERANKING"
	EnvGraphRetriever                = "WENYUAN_API_GRAPH_RETRIEVER"
	EnvGraphSeedMinSimilarity        = "WENYUAN_API_GRAPH_SEED_MIN_SIMILARITY"
	EnvSemanticMinSimilarity         = "WENYUAN_API_SEMANTIC_MIN_SIMILARITY"
	EnvTemporalSemanticMinSimilarity = "WENYUAN_API_TEMPORAL_SEMANTIC_MIN_SIMILARITY"
	EnvSemanticLinkMinSimilarity     = "WENYUAN_API_SEMANTIC_LINK_MIN_SIMILARITY"
	EnvBM25MinScore                  = "WENYUAN_API_BM25_MIN_SCORE"
	EnvBM25MaxQueryTerms             = "WENYUAN_API_BM25_MAX_QUERY_TERMS"
	EnvBM25SelectiveTerms            = "WENYUAN_API_BM25_SELECTIVE_TERMS"
	EnvLinkExpansionPerEntityLimit   = "WENYUAN_API_LINK_EXPANSION_PER_ENTITY_LIMIT"
	EnvLinkExpansionTimeout          = "WENYUAN_API_LINK_EXPANSION_TIMEOUT"
	EnvRecallConnectionBudget        = "WENYUAN_API_RECALL_CONNECTION_BUDGET"
	EnvRecallMaxQueryTokens          = "WENYUAN_API_RECALL_MAX_QUERY_TOKENS"
	EnvRecallDiagnosticPhases        = "WENYUAN_API_RECALL_DIAGNOSTIC_PHASES"
	EnvRecallPhaseSampleEvery        = "WENYUAN_API_RECALL_PHASE_SAMPLE_EVERY"
	EnvRecallMaxConcurrent           = "WENYUAN_API_RECALL_MAX_CONCURRENT"
	EnvRecallIncludeChunks           = "WENYUAN_API_RECALL_INCLUDE_CHUNKS"
	// EnvRecallPreferObservations is recall's prefer_observations value when a
	// request leaves it out (the reference made the request field nullable so a
	// deployment can turn it on for clients that never pass it).
	EnvRecallPreferObservations = "WENYUAN_API_RECALL_PREFER_OBSERVATIONS"
	// EnvDefaultObservationScopes is the observation_scopes retain applies to
	// items that send none: a mode name, or a JSON list of tag lists (globs
	// allowed), e.g. [["topic:*"]].
	EnvDefaultObservationScopes = "WENYUAN_API_DEFAULT_OBSERVATION_SCOPES"
	EnvRecallMaxTokens          = "WENYUAN_API_RECALL_MAX_TOKENS"
	EnvRecallChunksMaxTokens    = "WENYUAN_API_RECALL_CHUNKS_MAX_TOKENS"
	EnvBackpressureDeferSeconds = "WENYUAN_API_BACKPRESSURE_DEFER_SECONDS"
	EnvRecallBudgetFunction     = "WENYUAN_API_RECALL_BUDGET_FUNCTION"
	EnvRecallBudgetFixedLow     = "WENYUAN_API_RECALL_BUDGET_FIXED_LOW"
	EnvRecallBudgetFixedMid     = "WENYUAN_API_RECALL_BUDGET_FIXED_MID"
	EnvRecallBudgetFixedHigh    = "WENYUAN_API_RECALL_BUDGET_FIXED_HIGH"
	EnvRecallBudgetAdaptiveLow  = "WENYUAN_API_RECALL_BUDGET_ADAPTIVE_LOW"
	EnvRecallBudgetAdaptiveMid  = "WENYUAN_API_RECALL_BUDGET_ADAPTIVE_MID"
	EnvRecallBudgetAdaptiveHigh = "WENYUAN_API_RECALL_BUDGET_ADAPTIVE_HIGH"
	EnvRecallBudgetMin          = "WENYUAN_API_RECALL_BUDGET_MIN"
	EnvRecallBudgetMax          = "WENYUAN_API_RECALL_BUDGET_MAX"

	// Retain tuning.
	EnvRetainMaxCompletionTokens           = "WENYUAN_API_RETAIN_MAX_COMPLETION_TOKENS"
	EnvRetainChunkSize                     = "WENYUAN_API_RETAIN_CHUNK_SIZE"
	EnvRetainStructuredChunkSize           = "WENYUAN_API_RETAIN_STRUCTURED_CHUNK_SIZE"
	EnvRetainExtractCausalLinks            = "WENYUAN_API_RETAIN_EXTRACT_CAUSAL_LINKS"
	EnvRetainOptionalFactDimensions        = "WENYUAN_API_RETAIN_OPTIONAL_FACT_DIMENSIONS"
	EnvRetainExtractionMode                = "WENYUAN_API_RETAIN_EXTRACTION_MODE"
	EnvRetainMission                       = "WENYUAN_API_RETAIN_MISSION"
	EnvRetainCustomInstructions            = "WENYUAN_API_RETAIN_CUSTOM_INSTRUCTIONS"
	EnvRetainDefaultStrategy               = "WENYUAN_API_RETAIN_DEFAULT_STRATEGY"
	EnvRetainBatchTokens                   = "WENYUAN_API_RETAIN_BATCH_TOKENS"
	EnvRetainEntityLookup                  = "WENYUAN_API_RETAIN_ENTITY_LOOKUP"
	EnvRetainEntityResolutionBatchSize     = "WENYUAN_API_RETAIN_ENTITY_RESOLUTION_BATCH_SIZE"
	EnvRetainEntityResolutionMaxCandidates = "WENYUAN_API_RETAIN_ENTITY_RESOLUTION_MAX_CANDIDATES"
	EnvRetainBatchPollIntervalSeconds      = "WENYUAN_API_RETAIN_BATCH_POLL_INTERVAL_SECONDS"
	EnvRetainChunkBatchSize                = "WENYUAN_API_RETAIN_CHUNK_BATCH_SIZE"
	EnvRetainMemoryBudgetMB                = "WENYUAN_API_RETAIN_MEMORY_BUDGET_MB"
	EnvRetainMaxAttachmentsPerChunk        = "WENYUAN_API_RETAIN_MAX_ATTACHMENTS_PER_CHUNK"
	EnvRetainBatchDocumentWrites           = "WENYUAN_API_RETAIN_BATCH_DOCUMENT_WRITES"
	EnvRetainMaxConcurrent                 = "WENYUAN_API_RETAIN_MAX_CONCURRENT"
	EnvRetainSubbatchConcurrency           = "WENYUAN_API_RETAIN_SUBBATCH_CONCURRENCY"
	EnvRetainStoreMaxConcurrent            = "WENYUAN_API_RETAIN_STORE_MAX_CONCURRENT"
	EnvFailOnExtractionErrors              = "WENYUAN_API_FAIL_ON_EXTRACTION_ERRORS"

	// Reflect tuning.
	EnvReflectMaxIterations        = "WENYUAN_API_REFLECT_MAX_ITERATIONS"
	EnvReflectPromptCacheEnabled   = "WENYUAN_API_REFLECT_PROMPT_CACHE_ENABLED"
	EnvReflectMaxContextTokens     = "WENYUAN_API_REFLECT_MAX_CONTEXT_TOKENS"
	EnvReflectMission              = "WENYUAN_API_REFLECT_MISSION"
	EnvReflectSourceFactsMaxTokens = "WENYUAN_API_REFLECT_SOURCE_FACTS_MAX_TOKENS"
	EnvReflectDefaultOptions       = "WENYUAN_API_REFLECT_DEFAULT_OPTIONS"
	EnvReflectMaxCompletionTokens  = "WENYUAN_API_REFLECT_MAX_COMPLETION_TOKENS"

	// Observations and consolidation behaviour.
	EnvMaxObservationsPerScope                         = "WENYUAN_API_MAX_OBSERVATIONS_PER_SCOPE"
	EnvObservationScopeLimits                          = "WENYUAN_API_OBSERVATION_SCOPE_LIMITS"
	EnvObservationsMission                             = "WENYUAN_API_OBSERVATIONS_MISSION"
	EnvConsolidationMemoryBatchSize                    = "WENYUAN_API_CONSOLIDATION_BATCH_SIZE"
	EnvConsolidationMaxMemoriesPerRound                = "WENYUAN_API_CONSOLIDATION_MAX_MEMORIES_PER_ROUND"
	EnvConsolidationDedupThreshold                     = "WENYUAN_API_CONSOLIDATION_DEDUP_THRESHOLD"
	EnvConsolidationMaxTokens                          = "WENYUAN_API_CONSOLIDATION_MAX_TOKENS"
	EnvConsolidationMaxCompletionTokens                = "WENYUAN_API_CONSOLIDATION_MAX_COMPLETION_TOKENS"
	EnvConsolidationSourceFactsMaxTokens               = "WENYUAN_API_CONSOLIDATION_SOURCE_FACTS_MAX_TOKENS"
	EnvConsolidationSourceFactsMaxTokensPerObservation = "WENYUAN_API_CONSOLIDATION_SOURCE_FACTS_MAX_TOKENS_PER_OBSERVATION"
	EnvConsolidationRecallBudget                       = "WENYUAN_API_CONSOLIDATION_RECALL_BUDGET"
	EnvConsolidationMaxAttempts                        = "WENYUAN_API_CONSOLIDATION_MAX_ATTEMPTS"
	EnvConsolidationStrategies                         = "WENYUAN_API_CONSOLIDATION_STRATEGIES"
	EnvConsolidationLLMBatchSize                       = "WENYUAN_API_CONSOLIDATION_LLM_BATCH_SIZE"
	EnvConsolidationLLMParallelism                     = "WENYUAN_API_CONSOLIDATION_LLM_PARALLELISM"

	// Per-type wall ceilings, the reference's spelling.
	EnvRetainWallTimeout        = "WENYUAN_API_RETAIN_WALL_TIMEOUT"
	EnvConsolidationWallTimeout = "WENYUAN_API_CONSOLIDATION_WALL_TIMEOUT"
	EnvReflectWallTimeout       = "WENYUAN_API_REFLECT_WALL_TIMEOUT"

	// Maintenance cadences beyond the shared ones.
	EnvMentalModelHistoryMaxEntries         = "WENYUAN_API_MENTAL_MODEL_HISTORY_MAX_ENTRIES"
	EnvMentalModelMinRefreshIntervalSeconds = "WENYUAN_API_MENTAL_MODEL_MIN_REFRESH_INTERVAL_SECONDS"
	EnvObservationHistoryMaxEntries         = "WENYUAN_API_OBSERVATION_HISTORY_MAX_ENTRIES"
	EnvKnowledgePageDefaultTrigger          = "WENYUAN_API_KNOWLEDGE_PAGE_DEFAULT_TRIGGER"

	// Vector index and text search.
	EnvANNIterativeScan                  = "WENYUAN_API_ANN_ITERATIVE_SCAN"
	EnvANNMaxScanTuples                  = "WENYUAN_API_ANN_MAX_SCAN_TUPLES"
	EnvTextSearchExtensionNativeLanguage = "WENYUAN_API_TEXT_SEARCH_EXTENSION_NATIVE_LANGUAGE"
	EnvTextSearchPGFunctionSchema        = "WENYUAN_API_TEXT_SEARCH_EXTENSION_PG_SEARCH_FUNCTION_SCHEMA"
	EnvTextSearchPGTokenizer             = "WENYUAN_API_TEXT_SEARCH_EXTENSION_PG_SEARCH_TOKENIZER"
	EnvTokenizerEncoding                 = "WENYUAN_API_TOKENIZER_ENCODING"
	EnvQueryAnalyzerLanguages            = "WENYUAN_API_QUERY_ANALYZER_LANGUAGES"

	// Caches.
	EnvBankInfoCacheTTLSeconds  = "WENYUAN_API_BANK_INFO_CACHE_TTL_SECONDS"
	EnvBankInfoCacheMaxEntries  = "WENYUAN_API_BANK_INFO_CACHE_MAX_ENTRIES"
	EnvBankAliasCacheTTLSeconds = "WENYUAN_API_BANK_ALIAS_CACHE_TTL_SECONDS"
	EnvBankAliasCacheMaxEntries = "WENYUAN_API_BANK_ALIAS_CACHE_MAX_ENTRIES"
	EnvBankStatsCacheTTLSeconds = "WENYUAN_API_BANK_STATS_CACHE_TTL_SECONDS"
	EnvBankStatsCacheMaxEntries = "WENYUAN_API_BANK_STATS_CACHE_MAX_ENTRIES"

	// Entity resolution.
	EnvEntityTRGMSimilarityThreshold   = "WENYUAN_API_ENTITY_TRGM_SIMILARITY_THRESHOLD"
	EnvEntityIntrabatchMergeSimilarity = "WENYUAN_API_ENTITY_INTRABATCH_MERGE_SIMILARITY"
	EnvEntityMergeMinSimilarity        = "WENYUAN_API_ENTITY_MERGE_MIN_SIMILARITY"

	// Worker slot reservations and priority.
	EnvWorkerConsolidationBankPriority       = "WENYUAN_API_WORKER_CONSOLIDATION_BANK_PRIORITY"
	EnvWorkerRetainReservedSlots             = "WENYUAN_API_WORKER_RETAIN_RESERVED_SLOTS"
	EnvWorkerFileConvertRetainReservedSlots  = "WENYUAN_API_WORKER_FILE_CONVERT_RETAIN_RESERVED_SLOTS"
	EnvWorkerRefreshMentalModelReservedSlots = "WENYUAN_API_WORKER_REFRESH_MENTAL_MODEL_RESERVED_SLOTS"
	EnvWorkerGraphMaintenanceReservedSlots   = "WENYUAN_API_WORKER_GRAPH_MAINTENANCE_RESERVED_SLOTS"
	EnvWorkerVectorIndexMaintReservedSlots   = "WENYUAN_API_WORKER_VECTOR_INDEX_MAINTENANCE_RESERVED_SLOTS"
	EnvWorkerImportDocumentsReservedSlots    = "WENYUAN_API_WORKER_IMPORT_DOCUMENTS_RESERVED_SLOTS"
	EnvWorkerExportDocumentsReservedSlots    = "WENYUAN_API_WORKER_EXPORT_DOCUMENTS_RESERVED_SLOTS"
	EnvWorkerImportBankReservedSlots         = "WENYUAN_API_WORKER_IMPORT_BANK_RESERVED_SLOTS"
	EnvWorkerExportBankReservedSlots         = "WENYUAN_API_WORKER_EXPORT_BANK_RESERVED_SLOTS"
	EnvWorkerCloneBankReservedSlots          = "WENYUAN_API_WORKER_CLONE_BANK_RESERVED_SLOTS"

	// Admission, the reference's in-flight and wait ceilings.
	EnvAdmissionRecallMaxInFlight  = "WENYUAN_API_ADMISSION_RECALL_MAX_IN_FLIGHT"
	EnvAdmissionReflectMaxInFlight = "WENYUAN_API_ADMISSION_REFLECT_MAX_IN_FLIGHT"
	EnvAdmissionRetainMaxInFlight  = "WENYUAN_API_ADMISSION_RETAIN_MAX_IN_FLIGHT"
	EnvAdmissionRecallMaxWaitMS    = "WENYUAN_API_ADMISSION_RECALL_MAX_WAIT_MS"
	EnvAdmissionReflectMaxWaitMS   = "WENYUAN_API_ADMISSION_REFLECT_MAX_WAIT_MS"
	EnvAdmissionRetainMaxWaitMS    = "WENYUAN_API_ADMISSION_RETAIN_MAX_WAIT_MS"

	// Audit log.
	EnvAuditLogActions = "WENYUAN_API_AUDIT_LOG_ACTIONS"

	// Metrics and loop watchdog.
	EnvMetricsIncludeBankID         = "WENYUAN_API_METRICS_INCLUDE_BANK_ID"
	EnvMetricsBacklogEnabled        = "WENYUAN_API_METRICS_BACKLOG_ENABLED"
	EnvMetricsIncludeTenant         = "WENYUAN_API_METRICS_INCLUDE_TENANT"
	EnvMetricsWorkerLabel           = "WENYUAN_API_METRICS_WORKER_LABEL"
	EnvLoopLagMetric                = "WENYUAN_API_LOOP_LAG_METRIC"
	EnvLoopWatchdogEnabled          = "WENYUAN_API_LOOP_WATCHDOG_ENABLED"
	EnvLoopWatchdogStallThresholdMS = "WENYUAN_API_LOOP_WATCHDOG_STALL_THRESHOLD_MS"
	EnvLoopWatchdogPollIntervalMS   = "WENYUAN_API_LOOP_WATCHDOG_POLL_INTERVAL_MS"

	// File storage, parser and conversion.
	EnvFileStorageExtension            = "WENYUAN_API_FILE_STORAGE_EXTENSION"
	EnvFileStorageGCSBucket            = "WENYUAN_API_FILE_STORAGE_GCS_BUCKET"
	EnvFileStorageGCSServiceAccountKey = "WENYUAN_API_FILE_STORAGE_GCS_SERVICE_ACCOUNT_KEY"
	EnvFileStorageAzureContainer       = "WENYUAN_API_FILE_STORAGE_AZURE_CONTAINER"
	EnvFileStorageAzureAccountName     = "WENYUAN_API_FILE_STORAGE_AZURE_ACCOUNT_NAME"
	EnvFileStorageAzureAccountKey      = "WENYUAN_API_FILE_STORAGE_AZURE_ACCOUNT_KEY"
	EnvFileParser                      = "WENYUAN_API_FILE_PARSER"
	EnvFileParserAllowlist             = "WENYUAN_API_FILE_PARSER_ALLOWLIST"
	EnvFileParserMarkitdownOCREnabled  = "WENYUAN_API_FILE_PARSER_MARKITDOWN_OCR_ENABLED"
	EnvFileParserMarkitdownOCRAPIKey   = "WENYUAN_API_FILE_PARSER_MARKITDOWN_OCR_API_KEY"
	EnvFileParserMarkitdownOCRBaseURL  = "WENYUAN_API_FILE_PARSER_MARKITDOWN_OCR_BASE_URL"
	EnvFileParserMarkitdownOCRModel    = "WENYUAN_API_FILE_PARSER_MARKITDOWN_OCR_MODEL"
	EnvFileParserMarkitdownOCRPrompt   = "WENYUAN_API_FILE_PARSER_MARKITDOWN_OCR_PROMPT"
	EnvFileParserMarkitdownOCRHeaders  = "WENYUAN_API_FILE_PARSER_MARKITDOWN_OCR_DEFAULT_HEADERS"
	EnvFileParserIrisToken             = "WENYUAN_API_FILE_PARSER_IRIS_TOKEN"
	EnvFileParserIrisOrgID             = "WENYUAN_API_FILE_PARSER_IRIS_ORG_ID"
	EnvFileParserLlamaParseAPIKey      = "WENYUAN_API_FILE_PARSER_LLAMA_PARSE_API_KEY"
	EnvFileConversionMaxBatchSizeMB    = "WENYUAN_API_FILE_CONVERSION_MAX_BATCH_SIZE_MB"
	EnvFileConversionMaxBatchSize      = "WENYUAN_API_FILE_CONVERSION_MAX_BATCH_SIZE"
	EnvFileDeleteAfterRetain           = "WENYUAN_API_FILE_DELETE_AFTER_RETAIN"

	// Webhooks.
	EnvWebhookURL                = "WENYUAN_API_WEBHOOK_URL"
	EnvWebhookSecret             = "WENYUAN_API_WEBHOOK_SECRET"
	EnvWebhookEventTypes         = "WENYUAN_API_WEBHOOK_EVENT_TYPES"
	EnvWebhookExposeResponseBody = "WENYUAN_API_WEBHOOK_EXPOSE_RESPONSE_BODY"

	// Disposition seeds.
	EnvDispositionSkepticism = "WENYUAN_API_DISPOSITION_SKEPTICISM"
	EnvDispositionLiteralism = "WENYUAN_API_DISPOSITION_LITERALISM"
	EnvDispositionEmpathy    = "WENYUAN_API_DISPOSITION_EMPATHY"

	// Model initialization.
	EnvModelInitTimeout = "WENYUAN_API_MODEL_INIT_TIMEOUT"
)
