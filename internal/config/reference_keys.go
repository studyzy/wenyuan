package config

// Runtime (non-provider) environment keys, spelled as the reference spells
// them. The provider families (LLM, embeddings, reranker, vision) live in
// provider_env_keys.go; these are every other key the reference reads from its
// environment. reference.go decides, for each, whether this build resolves it
// or refuses it.
const (
	// Process, server and logging.
	EnvLogLevel                = "HINDSIGHT_API_LOG_LEVEL"
	EnvAccessLog               = "HINDSIGHT_API_ACCESS_LOG"
	EnvWorkers                 = "HINDSIGHT_API_WORKERS"
	EnvLogFormat               = "HINDSIGHT_API_LOG_FORMAT"
	EnvLogJSONFields           = "HINDSIGHT_API_LOG_JSON_FIELDS"
	EnvDaemonLog               = "HINDSIGHT_API_DAEMON_LOG"
	EnvMCPAuthToken            = "HINDSIGHT_API_MCP_AUTH_TOKEN"
	EnvMCPStateless            = "HINDSIGHT_API_MCP_STATELESS"
	EnvMCPEnabledTools         = "HINDSIGHT_API_MCP_ENABLED_TOOLS"
	EnvMCPInstructions         = "HINDSIGHT_API_MCP_INSTRUCTIONS"
	EnvEnableDryRunExtract     = "HINDSIGHT_API_ENABLE_DRY_RUN_EXTRACT"
	EnvDefaultBankTemplate     = "HINDSIGHT_API_DEFAULT_BANK_TEMPLATE"
	EnvExtensionPassThroughHdr = "HINDSIGHT_API_EXTENSION_PASSTHROUGH_HEADERS"

	// Database pool and migrations.
	EnvDatabaseBackend            = "HINDSIGHT_API_DATABASE_BACKEND"
	EnvReadDatabaseURL            = "HINDSIGHT_API_READ_DATABASE_URL"
	EnvReadDBPoolMinSize          = "HINDSIGHT_API_READ_DB_POOL_MIN_SIZE"
	EnvReadDBPoolMaxSize          = "HINDSIGHT_API_READ_DB_POOL_MAX_SIZE"
	EnvMigrationDatabaseURL       = "HINDSIGHT_API_MIGRATION_DATABASE_URL"
	EnvDBCommandTimeout           = "HINDSIGHT_API_DB_COMMAND_TIMEOUT"
	EnvDBAcquireTimeout           = "HINDSIGHT_API_DB_ACQUIRE_TIMEOUT"
	EnvDBPoolSlowAcquireThreshold = "HINDSIGHT_API_DB_POOL_SLOW_ACQUIRE_THRESHOLD_SECONDS"
	EnvDBStatementTimeout         = "HINDSIGHT_API_DB_STATEMENT_TIMEOUT"
	EnvDBMaxParallelWorkersGather = "HINDSIGHT_API_DB_MAX_PARALLEL_WORKERS_PER_GATHER"
	EnvDBSessionSetupOnAcquire    = "HINDSIGHT_API_DB_SESSION_SETUP_ON_ACQUIRE"
	EnvDBAcquireWarnThresholdMS   = "HINDSIGHT_API_DB_ACQUIRE_WARN_THRESHOLD_MS"
	EnvMigrationIsolation         = "HINDSIGHT_API_MIGRATION_ISOLATION"
	EnvMigrationConcurrency       = "HINDSIGHT_API_MIGRATION_CONCURRENCY"

	// Feature flags for consolidation and history.
	EnvEnableAutoConsolidation  = "HINDSIGHT_API_ENABLE_AUTO_CONSOLIDATION"
	EnvEnableObservationHistory = "HINDSIGHT_API_ENABLE_OBSERVATION_HISTORY"
	EnvEnableMentalModelHistory = "HINDSIGHT_API_ENABLE_MENTAL_MODEL_HISTORY"

	// Retrieval arms and their tuning.
	EnvEnableTextSearch              = "HINDSIGHT_API_ENABLE_TEXT_SEARCH"
	EnvEnableTemporalRetrieval       = "HINDSIGHT_API_ENABLE_TEMPORAL_RETRIEVAL"
	EnvEnableGraphRetrieval          = "HINDSIGHT_API_ENABLE_GRAPH_RETRIEVAL"
	EnvEnableReranking               = "HINDSIGHT_API_ENABLE_RERANKING"
	EnvGraphRetriever                = "HINDSIGHT_API_GRAPH_RETRIEVER"
	EnvGraphSeedMinSimilarity        = "HINDSIGHT_API_GRAPH_SEED_MIN_SIMILARITY"
	EnvSemanticMinSimilarity         = "HINDSIGHT_API_SEMANTIC_MIN_SIMILARITY"
	EnvTemporalSemanticMinSimilarity = "HINDSIGHT_API_TEMPORAL_SEMANTIC_MIN_SIMILARITY"
	EnvSemanticLinkMinSimilarity     = "HINDSIGHT_API_SEMANTIC_LINK_MIN_SIMILARITY"
	EnvBM25MinScore                  = "HINDSIGHT_API_BM25_MIN_SCORE"
	EnvBM25MaxQueryTerms             = "HINDSIGHT_API_BM25_MAX_QUERY_TERMS"
	EnvBM25SelectiveTerms            = "HINDSIGHT_API_BM25_SELECTIVE_TERMS"
	EnvLinkExpansionPerEntityLimit   = "HINDSIGHT_API_LINK_EXPANSION_PER_ENTITY_LIMIT"
	EnvLinkExpansionTimeout          = "HINDSIGHT_API_LINK_EXPANSION_TIMEOUT"
	EnvRecallConnectionBudget        = "HINDSIGHT_API_RECALL_CONNECTION_BUDGET"
	EnvRecallMaxQueryTokens          = "HINDSIGHT_API_RECALL_MAX_QUERY_TOKENS"
	EnvRecallDiagnosticPhases        = "HINDSIGHT_API_RECALL_DIAGNOSTIC_PHASES"
	EnvRecallPhaseSampleEvery        = "HINDSIGHT_API_RECALL_PHASE_SAMPLE_EVERY"
	EnvRecallMaxConcurrent           = "HINDSIGHT_API_RECALL_MAX_CONCURRENT"
	EnvRecallIncludeChunks           = "HINDSIGHT_API_RECALL_INCLUDE_CHUNKS"
	EnvRecallMaxTokens               = "HINDSIGHT_API_RECALL_MAX_TOKENS"
	EnvRecallChunksMaxTokens         = "HINDSIGHT_API_RECALL_CHUNKS_MAX_TOKENS"
	EnvBackpressureDeferSeconds      = "HINDSIGHT_API_BACKPRESSURE_DEFER_SECONDS"
	EnvRecallBudgetFunction          = "HINDSIGHT_API_RECALL_BUDGET_FUNCTION"
	EnvRecallBudgetFixedLow          = "HINDSIGHT_API_RECALL_BUDGET_FIXED_LOW"
	EnvRecallBudgetFixedMid          = "HINDSIGHT_API_RECALL_BUDGET_FIXED_MID"
	EnvRecallBudgetFixedHigh         = "HINDSIGHT_API_RECALL_BUDGET_FIXED_HIGH"
	EnvRecallBudgetAdaptiveLow       = "HINDSIGHT_API_RECALL_BUDGET_ADAPTIVE_LOW"
	EnvRecallBudgetAdaptiveMid       = "HINDSIGHT_API_RECALL_BUDGET_ADAPTIVE_MID"
	EnvRecallBudgetAdaptiveHigh      = "HINDSIGHT_API_RECALL_BUDGET_ADAPTIVE_HIGH"
	EnvRecallBudgetMin               = "HINDSIGHT_API_RECALL_BUDGET_MIN"
	EnvRecallBudgetMax               = "HINDSIGHT_API_RECALL_BUDGET_MAX"

	// Retain tuning.
	EnvRetainMaxCompletionTokens           = "HINDSIGHT_API_RETAIN_MAX_COMPLETION_TOKENS"
	EnvRetainChunkSize                     = "HINDSIGHT_API_RETAIN_CHUNK_SIZE"
	EnvRetainStructuredChunkSize           = "HINDSIGHT_API_RETAIN_STRUCTURED_CHUNK_SIZE"
	EnvRetainExtractCausalLinks            = "HINDSIGHT_API_RETAIN_EXTRACT_CAUSAL_LINKS"
	EnvRetainOptionalFactDimensions        = "HINDSIGHT_API_RETAIN_OPTIONAL_FACT_DIMENSIONS"
	EnvRetainExtractionMode                = "HINDSIGHT_API_RETAIN_EXTRACTION_MODE"
	EnvRetainMission                       = "HINDSIGHT_API_RETAIN_MISSION"
	EnvRetainCustomInstructions            = "HINDSIGHT_API_RETAIN_CUSTOM_INSTRUCTIONS"
	EnvRetainDefaultStrategy               = "HINDSIGHT_API_RETAIN_DEFAULT_STRATEGY"
	EnvRetainBatchTokens                   = "HINDSIGHT_API_RETAIN_BATCH_TOKENS"
	EnvRetainEntityLookup                  = "HINDSIGHT_API_RETAIN_ENTITY_LOOKUP"
	EnvRetainEntityResolutionBatchSize     = "HINDSIGHT_API_RETAIN_ENTITY_RESOLUTION_BATCH_SIZE"
	EnvRetainEntityResolutionMaxCandidates = "HINDSIGHT_API_RETAIN_ENTITY_RESOLUTION_MAX_CANDIDATES"
	EnvRetainBatchPollIntervalSeconds      = "HINDSIGHT_API_RETAIN_BATCH_POLL_INTERVAL_SECONDS"
	EnvRetainChunkBatchSize                = "HINDSIGHT_API_RETAIN_CHUNK_BATCH_SIZE"
	EnvRetainMemoryBudgetMB                = "HINDSIGHT_API_RETAIN_MEMORY_BUDGET_MB"
	EnvRetainMaxAttachmentsPerChunk        = "HINDSIGHT_API_RETAIN_MAX_ATTACHMENTS_PER_CHUNK"
	EnvRetainBatchDocumentWrites           = "HINDSIGHT_API_RETAIN_BATCH_DOCUMENT_WRITES"
	EnvRetainMaxConcurrent                 = "HINDSIGHT_API_RETAIN_MAX_CONCURRENT"
	EnvRetainSubbatchConcurrency           = "HINDSIGHT_API_RETAIN_SUBBATCH_CONCURRENCY"
	EnvRetainStoreMaxConcurrent            = "HINDSIGHT_API_RETAIN_STORE_MAX_CONCURRENT"
	EnvFailOnExtractionErrors              = "HINDSIGHT_API_FAIL_ON_EXTRACTION_ERRORS"

	// Reflect tuning.
	EnvReflectMaxIterations        = "HINDSIGHT_API_REFLECT_MAX_ITERATIONS"
	EnvReflectPromptCacheEnabled   = "HINDSIGHT_API_REFLECT_PROMPT_CACHE_ENABLED"
	EnvReflectMaxContextTokens     = "HINDSIGHT_API_REFLECT_MAX_CONTEXT_TOKENS"
	EnvReflectMission              = "HINDSIGHT_API_REFLECT_MISSION"
	EnvReflectSourceFactsMaxTokens = "HINDSIGHT_API_REFLECT_SOURCE_FACTS_MAX_TOKENS"
	EnvReflectDefaultOptions       = "HINDSIGHT_API_REFLECT_DEFAULT_OPTIONS"
	EnvReflectMaxCompletionTokens  = "HINDSIGHT_API_REFLECT_MAX_COMPLETION_TOKENS"

	// Observations and consolidation behaviour.
	EnvMaxObservationsPerScope                         = "HINDSIGHT_API_MAX_OBSERVATIONS_PER_SCOPE"
	EnvObservationScopeLimits                          = "HINDSIGHT_API_OBSERVATION_SCOPE_LIMITS"
	EnvObservationsMission                             = "HINDSIGHT_API_OBSERVATIONS_MISSION"
	EnvConsolidationMemoryBatchSize                    = "HINDSIGHT_API_CONSOLIDATION_BATCH_SIZE"
	EnvConsolidationMaxMemoriesPerRound                = "HINDSIGHT_API_CONSOLIDATION_MAX_MEMORIES_PER_ROUND"
	EnvConsolidationDedupThreshold                     = "HINDSIGHT_API_CONSOLIDATION_DEDUP_THRESHOLD"
	EnvConsolidationMaxTokens                          = "HINDSIGHT_API_CONSOLIDATION_MAX_TOKENS"
	EnvConsolidationMaxCompletionTokens                = "HINDSIGHT_API_CONSOLIDATION_MAX_COMPLETION_TOKENS"
	EnvConsolidationSourceFactsMaxTokens               = "HINDSIGHT_API_CONSOLIDATION_SOURCE_FACTS_MAX_TOKENS"
	EnvConsolidationSourceFactsMaxTokensPerObservation = "HINDSIGHT_API_CONSOLIDATION_SOURCE_FACTS_MAX_TOKENS_PER_OBSERVATION"
	EnvConsolidationRecallBudget                       = "HINDSIGHT_API_CONSOLIDATION_RECALL_BUDGET"
	EnvConsolidationMaxAttempts                        = "HINDSIGHT_API_CONSOLIDATION_MAX_ATTEMPTS"
	EnvConsolidationStrategies                         = "HINDSIGHT_API_CONSOLIDATION_STRATEGIES"
	EnvConsolidationLLMBatchSize                       = "HINDSIGHT_API_CONSOLIDATION_LLM_BATCH_SIZE"
	EnvConsolidationLLMParallelism                     = "HINDSIGHT_API_CONSOLIDATION_LLM_PARALLELISM"

	// Per-type wall ceilings, the reference's spelling.
	EnvRetainWallTimeout        = "HINDSIGHT_API_RETAIN_WALL_TIMEOUT"
	EnvConsolidationWallTimeout = "HINDSIGHT_API_CONSOLIDATION_WALL_TIMEOUT"
	EnvReflectWallTimeout       = "HINDSIGHT_API_REFLECT_WALL_TIMEOUT"

	// Maintenance cadences beyond the shared ones.
	EnvMentalModelHistoryMaxEntries         = "HINDSIGHT_API_MENTAL_MODEL_HISTORY_MAX_ENTRIES"
	EnvMentalModelMinRefreshIntervalSeconds = "HINDSIGHT_API_MENTAL_MODEL_MIN_REFRESH_INTERVAL_SECONDS"
	EnvObservationHistoryMaxEntries         = "HINDSIGHT_API_OBSERVATION_HISTORY_MAX_ENTRIES"
	EnvKnowledgePageDefaultTrigger          = "HINDSIGHT_API_KNOWLEDGE_PAGE_DEFAULT_TRIGGER"

	// Vector index and text search.
	EnvANNIterativeScan                  = "HINDSIGHT_API_ANN_ITERATIVE_SCAN"
	EnvANNMaxScanTuples                  = "HINDSIGHT_API_ANN_MAX_SCAN_TUPLES"
	EnvTextSearchExtensionNativeLanguage = "HINDSIGHT_API_TEXT_SEARCH_EXTENSION_NATIVE_LANGUAGE"
	EnvTextSearchPGFunctionSchema        = "HINDSIGHT_API_TEXT_SEARCH_EXTENSION_PG_SEARCH_FUNCTION_SCHEMA"
	EnvTextSearchPGTokenizer             = "HINDSIGHT_API_TEXT_SEARCH_EXTENSION_PG_SEARCH_TOKENIZER"
	EnvTokenizerEncoding                 = "HINDSIGHT_API_TOKENIZER_ENCODING"
	EnvQueryAnalyzerLanguages            = "HINDSIGHT_API_QUERY_ANALYZER_LANGUAGES"

	// Caches.
	EnvBankInfoCacheTTLSeconds  = "HINDSIGHT_API_BANK_INFO_CACHE_TTL_SECONDS"
	EnvBankInfoCacheMaxEntries  = "HINDSIGHT_API_BANK_INFO_CACHE_MAX_ENTRIES"
	EnvBankAliasCacheTTLSeconds = "HINDSIGHT_API_BANK_ALIAS_CACHE_TTL_SECONDS"
	EnvBankAliasCacheMaxEntries = "HINDSIGHT_API_BANK_ALIAS_CACHE_MAX_ENTRIES"
	EnvBankStatsCacheTTLSeconds = "HINDSIGHT_API_BANK_STATS_CACHE_TTL_SECONDS"
	EnvBankStatsCacheMaxEntries = "HINDSIGHT_API_BANK_STATS_CACHE_MAX_ENTRIES"

	// Entity resolution.
	EnvEntityTRGMSimilarityThreshold   = "HINDSIGHT_API_ENTITY_TRGM_SIMILARITY_THRESHOLD"
	EnvEntityIntrabatchMergeSimilarity = "HINDSIGHT_API_ENTITY_INTRABATCH_MERGE_SIMILARITY"
	EnvEntityMergeMinSimilarity        = "HINDSIGHT_API_ENTITY_MERGE_MIN_SIMILARITY"

	// Worker slot reservations and priority.
	EnvWorkerConsolidationBankPriority       = "HINDSIGHT_API_WORKER_CONSOLIDATION_BANK_PRIORITY"
	EnvWorkerRetainReservedSlots             = "HINDSIGHT_API_WORKER_RETAIN_RESERVED_SLOTS"
	EnvWorkerFileConvertRetainReservedSlots  = "HINDSIGHT_API_WORKER_FILE_CONVERT_RETAIN_RESERVED_SLOTS"
	EnvWorkerRefreshMentalModelReservedSlots = "HINDSIGHT_API_WORKER_REFRESH_MENTAL_MODEL_RESERVED_SLOTS"
	EnvWorkerGraphMaintenanceReservedSlots   = "HINDSIGHT_API_WORKER_GRAPH_MAINTENANCE_RESERVED_SLOTS"
	EnvWorkerVectorIndexMaintReservedSlots   = "HINDSIGHT_API_WORKER_VECTOR_INDEX_MAINTENANCE_RESERVED_SLOTS"
	EnvWorkerImportDocumentsReservedSlots    = "HINDSIGHT_API_WORKER_IMPORT_DOCUMENTS_RESERVED_SLOTS"
	EnvWorkerExportDocumentsReservedSlots    = "HINDSIGHT_API_WORKER_EXPORT_DOCUMENTS_RESERVED_SLOTS"
	EnvWorkerImportBankReservedSlots         = "HINDSIGHT_API_WORKER_IMPORT_BANK_RESERVED_SLOTS"
	EnvWorkerExportBankReservedSlots         = "HINDSIGHT_API_WORKER_EXPORT_BANK_RESERVED_SLOTS"
	EnvWorkerCloneBankReservedSlots          = "HINDSIGHT_API_WORKER_CLONE_BANK_RESERVED_SLOTS"

	// Admission, the reference's in-flight and wait ceilings.
	EnvAdmissionRecallMaxInFlight  = "HINDSIGHT_API_ADMISSION_RECALL_MAX_IN_FLIGHT"
	EnvAdmissionReflectMaxInFlight = "HINDSIGHT_API_ADMISSION_REFLECT_MAX_IN_FLIGHT"
	EnvAdmissionRetainMaxInFlight  = "HINDSIGHT_API_ADMISSION_RETAIN_MAX_IN_FLIGHT"
	EnvAdmissionRecallMaxWaitMS    = "HINDSIGHT_API_ADMISSION_RECALL_MAX_WAIT_MS"
	EnvAdmissionReflectMaxWaitMS   = "HINDSIGHT_API_ADMISSION_REFLECT_MAX_WAIT_MS"
	EnvAdmissionRetainMaxWaitMS    = "HINDSIGHT_API_ADMISSION_RETAIN_MAX_WAIT_MS"

	// Audit log.
	EnvAuditLogActions = "HINDSIGHT_API_AUDIT_LOG_ACTIONS"

	// Metrics and loop watchdog.
	EnvMetricsIncludeBankID         = "HINDSIGHT_API_METRICS_INCLUDE_BANK_ID"
	EnvMetricsBacklogEnabled        = "HINDSIGHT_API_METRICS_BACKLOG_ENABLED"
	EnvMetricsIncludeTenant         = "HINDSIGHT_API_METRICS_INCLUDE_TENANT"
	EnvMetricsWorkerLabel           = "HINDSIGHT_API_METRICS_WORKER_LABEL"
	EnvLoopLagMetric                = "HINDSIGHT_API_LOOP_LAG_METRIC"
	EnvLoopWatchdogEnabled          = "HINDSIGHT_API_LOOP_WATCHDOG_ENABLED"
	EnvLoopWatchdogStallThresholdMS = "HINDSIGHT_API_LOOP_WATCHDOG_STALL_THRESHOLD_MS"
	EnvLoopWatchdogPollIntervalMS   = "HINDSIGHT_API_LOOP_WATCHDOG_POLL_INTERVAL_MS"

	// File storage, parser and conversion.
	EnvFileStorageExtension            = "HINDSIGHT_API_FILE_STORAGE_EXTENSION"
	EnvFileStorageGCSBucket            = "HINDSIGHT_API_FILE_STORAGE_GCS_BUCKET"
	EnvFileStorageGCSServiceAccountKey = "HINDSIGHT_API_FILE_STORAGE_GCS_SERVICE_ACCOUNT_KEY"
	EnvFileStorageAzureContainer       = "HINDSIGHT_API_FILE_STORAGE_AZURE_CONTAINER"
	EnvFileStorageAzureAccountName     = "HINDSIGHT_API_FILE_STORAGE_AZURE_ACCOUNT_NAME"
	EnvFileStorageAzureAccountKey      = "HINDSIGHT_API_FILE_STORAGE_AZURE_ACCOUNT_KEY"
	EnvFileParser                      = "HINDSIGHT_API_FILE_PARSER"
	EnvFileParserAllowlist             = "HINDSIGHT_API_FILE_PARSER_ALLOWLIST"
	EnvFileParserMarkitdownOCREnabled  = "HINDSIGHT_API_FILE_PARSER_MARKITDOWN_OCR_ENABLED"
	EnvFileParserMarkitdownOCRAPIKey   = "HINDSIGHT_API_FILE_PARSER_MARKITDOWN_OCR_API_KEY"
	EnvFileParserMarkitdownOCRBaseURL  = "HINDSIGHT_API_FILE_PARSER_MARKITDOWN_OCR_BASE_URL"
	EnvFileParserMarkitdownOCRModel    = "HINDSIGHT_API_FILE_PARSER_MARKITDOWN_OCR_MODEL"
	EnvFileParserMarkitdownOCRPrompt   = "HINDSIGHT_API_FILE_PARSER_MARKITDOWN_OCR_PROMPT"
	EnvFileParserMarkitdownOCRHeaders  = "HINDSIGHT_API_FILE_PARSER_MARKITDOWN_OCR_DEFAULT_HEADERS"
	EnvFileParserIrisToken             = "HINDSIGHT_API_FILE_PARSER_IRIS_TOKEN"
	EnvFileParserIrisOrgID             = "HINDSIGHT_API_FILE_PARSER_IRIS_ORG_ID"
	EnvFileParserLlamaParseAPIKey      = "HINDSIGHT_API_FILE_PARSER_LLAMA_PARSE_API_KEY"
	EnvFileConversionMaxBatchSizeMB    = "HINDSIGHT_API_FILE_CONVERSION_MAX_BATCH_SIZE_MB"
	EnvFileConversionMaxBatchSize      = "HINDSIGHT_API_FILE_CONVERSION_MAX_BATCH_SIZE"
	EnvFileDeleteAfterRetain           = "HINDSIGHT_API_FILE_DELETE_AFTER_RETAIN"

	// Webhooks.
	EnvWebhookURL                = "HINDSIGHT_API_WEBHOOK_URL"
	EnvWebhookSecret             = "HINDSIGHT_API_WEBHOOK_SECRET"
	EnvWebhookEventTypes         = "HINDSIGHT_API_WEBHOOK_EVENT_TYPES"
	EnvWebhookExposeResponseBody = "HINDSIGHT_API_WEBHOOK_EXPOSE_RESPONSE_BODY"

	// Disposition seeds.
	EnvDispositionSkepticism = "HINDSIGHT_API_DISPOSITION_SKEPTICISM"
	EnvDispositionLiteralism = "HINDSIGHT_API_DISPOSITION_LITERALISM"
	EnvDispositionEmpathy    = "HINDSIGHT_API_DISPOSITION_EMPATHY"

	// Model initialization.
	EnvModelInitTimeout = "HINDSIGHT_API_MODEL_INIT_TIMEOUT"
)
