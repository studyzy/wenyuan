package config

// This file records the reference service's runtime (non-provider) environment
// keys and what this build does with each one.
//
// The provider families (LLM, embeddings, reranker, vision) are recorded
// separately, next to the provider boundary. Everything else the reference
// reads from its environment is here: the worker and queue, the caches, the
// maintenance and consolidation cadences, the observation and retrieval
// feature flags, the entity-resolution tuning, the database pool, and the
// observability / file / webhook / audit remainder.
//
// Each key is either resolved — this build derives a value from it and acts on
// it — or refused. A refused key that is set fails startup naming the key,
// which is the whole point: a copied reference .env must never be silently
// half-honoured. There is no third outcome.
//
// referenceKey documents the reference default and a non-default sample so the
// table-driven test can prove each resolved key actually moves the resolved
// configuration.

// referenceKey is one entry of the reference runtime key table.
type referenceKey struct {
	// Name is the environment key, spelled as the reference spells it.
	Name string
	// Default is the reference's default value, recorded for the reader.
	Default string
	// Resolve returns the resolved value this build derives from the key. It is
	// nil when the key is refused rather than honoured.
	Resolve func(Config) any
	// Reason is a short note for a refused key, naming the subsystem it belongs
	// to so the operator knows what they asked for.
	Reason string
	// Divergent, when non-empty, explains why this build's resolved default
	// intentionally differs from the reference's Default. It is the only way a
	// resolved key may skip the table test's default check, and it must say why.
	Divergent string
	// Delegated, when non-empty, names the package that resolves this key
	// outside Config. Such a key is acknowledged rather than refused — refusing
	// it would stop startup for a feature another package implements — but it is
	// neither in the resolved set here nor silently ignored.
	Delegated string
}

// referenceRuntimeKeys enumerates the reference's non-provider runtime keys.
// It is the single source of truth for both refusal-at-startup and the
// table-driven contract test.
var referenceRuntimeKeys = []referenceKey{
	// --- Server, logging and process shape ---
	{Name: EnvHost, Default: DefaultHost, Resolve: func(c Config) any { return c.Host }},
	{Name: EnvPort, Default: "8888", Resolve: func(c Config) any { return c.Port }},
	{Name: EnvLogLevel, Default: DefaultLogLevel, Resolve: func(c Config) any { return c.LogLevel }},
	{Name: EnvAccessLog, Default: "false", Resolve: func(c Config) any { return c.AccessLog }},
	{Name: EnvWorkers, Default: "1", Reason: "this build serves from a single process, so the worker-process count is not honoured"},
	{Name: EnvBasePath, Default: "", Resolve: func(c Config) any { return c.BasePath }},
	{Name: EnvGzipMinSize, Default: "1024", Resolve: func(c Config) any { return c.GzipMinSize }},
	{Name: EnvProfile, Default: "", Resolve: func(c Config) any { return c.Profile }},
	{Name: EnvRunMigrationsOnStartup, Default: "true", Resolve: func(c Config) any { return c.RunMigrationsOnStartup }},
	{Name: EnvStoreDocumentText, Default: "true", Resolve: func(c Config) any { return c.StoreDocumentText }},
	{Name: EnvLogFormat, Default: DefaultLogFormat, Resolve: func(c Config) any { return c.LogFormat }},
	{Name: EnvLogJSONFields, Default: "", Resolve: func(c Config) any { return c.LogJSONFields }},
	{Name: EnvDaemonLog, Default: "", Reason: "the reference's daemon log file is not implemented"},
	{Name: EnvMCPAuthToken, Default: "", Reason: "the MCP authentication token is not implemented"},
	{Name: EnvMCPStateless, Default: "false", Reason: "the reference's stateless MCP transport is not implemented"},
	{Name: EnvMCPEnabledTools, Default: "all", Reason: "the MCP tool allowlist is not implemented"},
	{Name: EnvMCPInstructions, Default: "", Reason: "the MCP server instructions are not implemented"},
	{Name: EnvEnableDryRunExtract, Default: "true", Reason: "the dry-run extraction endpoint toggle is not implemented"},
	{Name: EnvDefaultBankTemplate, Default: "", Reason: "the default bank template is not implemented"},
	{Name: EnvExtensionPassThroughHdr, Default: "", Reason: "extension header passthrough is not implemented"},

	// --- Database connection pool ---
	{Name: EnvDatabaseURL, Default: "pg0", Resolve: func(c Config) any { return c.DatabaseURL },
		Divergent: "the reference's default is an embedded pg0 instance this build cannot manage; an unset URL refuses startup instead"},
	{Name: EnvDatabaseSchema, Default: "public", Resolve: func(c Config) any { return c.DatabaseSchema }},
	{Name: EnvDBPoolMinSize, Default: "5", Resolve: func(c Config) any { return c.DBPoolMinSize }},
	{Name: EnvDBPoolMaxSize, Default: "100", Resolve: func(c Config) any { return c.DBPoolMaxSize }},
	{Name: EnvDatabaseBackend, Default: "postgresql", Reason: "only the PostgreSQL backend is implemented"},
	{Name: EnvReadDatabaseURL, Default: "", Reason: "the read-replica pool is not implemented"},
	{Name: EnvReadDBPoolMinSize, Default: "5", Reason: "the read-replica pool is not implemented"},
	{Name: EnvReadDBPoolMaxSize, Default: "100", Reason: "the read-replica pool is not implemented"},
	{Name: EnvMigrationDatabaseURL, Default: "", Reason: "a separate migration database URL is not implemented"},
	{Name: EnvDBCommandTimeout, Default: "60", Reason: "the per-command database timeout is not implemented"},
	{Name: EnvDBAcquireTimeout, Default: "30", Reason: "the connection-acquire timeout is not implemented"},
	{Name: EnvDBPoolSlowAcquireThreshold, Default: "0.05", Reason: "the slow-acquire warning threshold is not implemented"},
	{Name: EnvDBStatementTimeout, Default: "600", Reason: "the session statement_timeout is not implemented"},
	{Name: EnvDBMaxParallelWorkersGather, Default: "", Reason: "the per-gather parallel-worker cap is not implemented"},
	{Name: EnvDBSessionSetupOnAcquire, Default: "true", Reason: "per-acquire session setup is not implemented"},
	{Name: EnvDBAcquireWarnThresholdMS, Default: "1000", Reason: "the pool-acquire warning threshold is not implemented"},
	{Name: EnvMigrationIsolation, Default: "false", Reason: "migration isolation is not implemented"},
	{Name: EnvMigrationConcurrency, Default: "1", Reason: "concurrent migrations are not implemented"},

	// --- Feature flags ---
	{Name: EnvMCPEnabled, Default: "true", Resolve: func(c Config) any { return c.MCPEnabled }},
	{Name: EnvEnableBankConfigAPI, Default: "true", Resolve: func(c Config) any { return c.EnableBankConfigAPI }},
	{Name: EnvEnableBankLLMHealth, Default: "false", Resolve: func(c Config) any { return c.EnableBankLLMHealth }},
	{Name: EnvEnableFileUploadAPI, Default: "true", Resolve: func(c Config) any { return c.EnableFileUploadAPI }},
	{Name: EnvEnableDocumentExportAPI, Default: "true", Resolve: func(c Config) any { return c.EnableDocumentExportAPI }},
	{Name: EnvEnableDocumentImportAPI, Default: "true", Resolve: func(c Config) any { return c.EnableDocumentImportAPI }},
	{Name: EnvEnableObservations, Default: "true", Resolve: func(c Config) any { return c.EnableObservations }},
	{Name: EnvEnableAutoConsolidation, Default: "true", Resolve: func(c Config) any { return c.EnableAutoConsolidation }},
	{Name: EnvEnableObservationHistory, Default: "true", Reason: "observation history is not implemented"},
	{Name: EnvEnableMentalModelHistory, Default: "true", Reason: "mental-model history is not implemented"},

	// --- Retrieval arms ---
	{Name: EnvEnableTextSearch, Default: "true", Reason: "the retrieval-arm feature flags are not implemented; arms are always on"},
	{Name: EnvEnableTemporalRetrieval, Default: "true", Reason: "the retrieval-arm feature flags are not implemented; arms are always on"},
	{Name: EnvEnableGraphRetrieval, Default: "true", Reason: "the retrieval-arm feature flags are not implemented; arms are always on"},
	{Name: EnvEnableReranking, Default: "true", Reason: "the cross-encoder reranker is not implemented"},
	{Name: EnvGraphRetriever, Default: "", Reason: "the graph retriever selection is not implemented"},
	{Name: EnvGraphSeedMinSimilarity, Default: "0.3", Reason: "graph-seed similarity tuning is not implemented"},
	{Name: EnvSemanticMinSimilarity, Default: "0.3", Reason: "per-arm semantic similarity tuning is not implemented"},
	{Name: EnvTemporalSemanticMinSimilarity, Default: "0.1", Reason: "per-arm semantic similarity tuning is not implemented"},
	{Name: EnvSemanticLinkMinSimilarity, Default: "0.7", Reason: "semantic-link similarity tuning is not implemented"},
	{Name: EnvBM25MinScore, Default: "0.0", Reason: "BM25 score tuning is not implemented"},
	{Name: EnvBM25MaxQueryTerms, Default: "16", Reason: "BM25 query-term tuning is not implemented"},
	{Name: EnvBM25SelectiveTerms, Default: "true", Reason: "BM25 selective-term tuning is not implemented"},
	{Name: EnvLinkExpansionPerEntityLimit, Default: "200", Reason: "graph link-expansion tuning is not implemented"},
	{Name: EnvLinkExpansionTimeout, Default: "10", Reason: "graph link-expansion tuning is not implemented"},
	{Name: EnvRecallConnectionBudget, Default: "4", Reason: "the per-recall connection budget is not implemented"},
	{Name: EnvRecallMaxQueryTokens, Default: "500", Reason: "the recall query token cap is not implemented"},
	{Name: EnvRecallDiagnosticPhases, Default: "true", Reason: "recall phase diagnostics are not implemented"},
	{Name: EnvRecallPhaseSampleEvery, Default: "1", Reason: "recall phase sampling is not implemented"},
	{Name: EnvRecallMaxConcurrent, Default: "32", Reason: "the recall concurrency cap is not implemented"},
	{Name: EnvRecallIncludeChunks, Default: "false", Reason: "the per-process chunk-inclusion default is not implemented"},
	{Name: EnvRecallMaxTokens, Default: "", Reason: "the recall token budget is not implemented"},
	{Name: EnvRecallChunksMaxTokens, Default: "", Reason: "the chunk token budget is not implemented"},
	{Name: EnvBackpressureDeferSeconds, Default: "", Reason: "backpressure deferral is not implemented"},

	// --- Recall budget function ---
	{Name: EnvRecallBudgetFunction, Default: "fixed", Reason: "the recall budget function is not implemented"},
	{Name: EnvRecallBudgetFixedLow, Default: "", Reason: "recall budget tuning is not implemented"},
	{Name: EnvRecallBudgetFixedMid, Default: "", Reason: "recall budget tuning is not implemented"},
	{Name: EnvRecallBudgetFixedHigh, Default: "", Reason: "recall budget tuning is not implemented"},
	{Name: EnvRecallBudgetAdaptiveLow, Default: "", Reason: "recall budget tuning is not implemented"},
	{Name: EnvRecallBudgetAdaptiveMid, Default: "", Reason: "recall budget tuning is not implemented"},
	{Name: EnvRecallBudgetAdaptiveHigh, Default: "", Reason: "recall budget tuning is not implemented"},
	{Name: EnvRecallBudgetMin, Default: "", Reason: "recall budget tuning is not implemented"},
	{Name: EnvRecallBudgetMax, Default: "", Reason: "recall budget tuning is not implemented"},

	// --- Recall tuning this build does honour ---
	{Name: EnvRecallStrategyBoosts, Default: "", Resolve: func(c Config) any { return c.RecallStrategyBoosts }},
	{Name: EnvRecallMaxCandidatesPerSource, Default: "0", Resolve: func(c Config) any { return c.Worker.RecallMaxCandidatesPerSource }},
	{Name: EnvRecencyDecayFunction, Default: DefaultRecencyDecayFunction, Resolve: func(c Config) any { return c.Recency.Function }},
	{Name: EnvRecencyDecayLinearWindowDays, Default: "365", Resolve: func(c Config) any { return c.Recency.LinearWindowDays }},
	{Name: EnvRecencyDecayHalflifeDays, Default: "90", Resolve: func(c Config) any { return c.Recency.HalflifeDays }},

	// --- Retain tuning ---
	{Name: EnvRetainAttachmentMaxSizeMB, Default: "20", Resolve: func(c Config) any { return c.RetainAttachmentMaxSizeMB }},
	{Name: EnvRetainAttachmentMaxCount, Default: "50", Resolve: func(c Config) any { return c.RetainAttachmentMaxCount }},
	{Name: EnvRetainBatchEnabled, Default: "false", Resolve: func(c Config) any { return c.RetainBatchEnabled }},
	{Name: EnvRetainMaxCompletionTokens, Default: "64000", Reason: "the retain completion-token budget is not implemented"},
	{Name: EnvRetainChunkSize, Default: "3000", Reason: "the retain chunk size is not implemented"},
	{Name: EnvRetainStructuredChunkSize, Default: "", Reason: "the structured chunk size is not implemented"},
	{Name: EnvRetainExtractCausalLinks, Default: "true", Reason: "causal-link extraction tuning is not implemented"},
	{Name: EnvRetainOptionalFactDimensions, Default: "false", Resolve: func(c Config) any { return c.RetainOptionalFactDimensions }},
	{Name: EnvRetainExtractionMode, Default: DefaultRetainExtractionMode, Resolve: func(c Config) any { return c.RetainExtractionMode }},
	{Name: EnvRetainMission, Default: "", Reason: "the retain mission is not implemented"},
	{Name: EnvRetainCustomInstructions, Default: "", Reason: "custom retain instructions are not implemented"},
	{Name: EnvRetainDefaultStrategy, Default: "", Reason: "named retain strategies are not implemented"},
	{Name: EnvRetainBatchTokens, Default: "10000", Reason: "the retain sub-batch token budget is not implemented"},
	{Name: EnvRetainEntityLookup, Default: "trigram", Reason: "the entity-lookup mode selection is not implemented"},
	{Name: EnvRetainEntityResolutionBatchSize, Default: "100", Reason: "entity-resolution tuning is not implemented"},
	{Name: EnvRetainEntityResolutionMaxCandidates, Default: "200", Reason: "entity-resolution tuning is not implemented"},
	{Name: EnvRetainBatchPollIntervalSeconds, Default: "60", Reason: "the LLM Batch API poll interval is not implemented"},
	{Name: EnvRetainChunkBatchSize, Default: "", Reason: "the retain chunk batch size is not implemented"},
	{Name: EnvRetainMemoryBudgetMB, Default: "128", Reason: "the retain memory budget is not implemented"},
	{Name: EnvRetainMaxAttachmentsPerChunk, Default: "8", Reason: "the per-chunk attachment cap is not implemented"},
	{Name: EnvRetainBatchDocumentWrites, Default: "false", Reason: "batched document writes are not implemented"},
	{Name: EnvRetainMaxConcurrent, Default: "4", Reason: "the retain concurrency cap is not implemented"},
	{Name: EnvRetainSubbatchConcurrency, Default: "1", Reason: "retain sub-batch concurrency is not implemented"},
	{Name: EnvRetainStoreMaxConcurrent, Default: "", Reason: "the retain store concurrency cap is not implemented"},
	{Name: EnvFailOnExtractionErrors, Default: "false", Reason: "failing retain on extraction errors is not implemented"},

	// --- Reflect tuning ---
	{Name: EnvReflectMaxIterations, Default: "", Reason: "reflect loop tuning is not implemented"},
	{Name: EnvReflectPromptCacheEnabled, Default: "", Reason: "the reflect prompt cache is not implemented"},
	{Name: EnvReflectMaxContextTokens, Default: "", Reason: "the reflect context budget is not implemented"},
	{Name: EnvReflectMission, Default: "", Reason: "the reflect mission is not implemented"},
	{Name: EnvReflectSourceFactsMaxTokens, Default: "", Reason: "the reflect source-facts budget is not implemented"},
	{Name: EnvReflectDefaultOptions, Default: "", Reason: "the reflect default options are not implemented"},
	{Name: EnvReflectMaxCompletionTokens, Default: "", Reason: "the reflect completion-token budget is not implemented"},

	// --- Observations and consolidation ---
	{Name: EnvMaxObservationsPerScope, Default: "-1", Reason: "the per-scope observation cap is not implemented"},
	{Name: EnvObservationScopeLimits, Default: "", Reason: "per-scope observation limits are not implemented"},
	{Name: EnvObservationsMission, Default: "", Reason: "the observations mission is not implemented"},
	{Name: EnvConsolidationMemoryBatchSize, Default: "50", Reason: "the consolidation batch size is not implemented"},
	{Name: EnvConsolidationMaxMemoriesPerRound, Default: "", Reason: "the consolidation round cap is not implemented"},
	{Name: EnvConsolidationDedupThreshold, Default: "0.97", Reason: "the consolidation dedup threshold is not implemented"},
	{Name: EnvConsolidationMaxTokens, Default: "512", Reason: "the consolidation recall token budget is not implemented"},
	{Name: EnvConsolidationMaxCompletionTokens, Default: "", Reason: "the consolidation completion-token budget is not implemented"},
	{Name: EnvConsolidationSourceFactsMaxTokens, Default: "", Reason: "the consolidation source-facts budget is not implemented"},
	{Name: EnvConsolidationSourceFactsMaxTokensPerObservation, Default: "", Reason: "the consolidation source-facts budget is not implemented"},
	{Name: EnvConsolidationRecallBudget, Default: "low", Reason: "the consolidation recall budget is not implemented"},
	{Name: EnvConsolidationMaxAttempts, Default: "3", Reason: "the consolidation attempt budget is not implemented"},
	{Name: EnvConsolidationStrategies, Default: "", Reason: "named consolidation strategies are not implemented"},
	{Name: EnvConsolidationWallTimeout, Default: "7200", Resolve: func(c Config) any { return c.Worker.WallTimeouts["consolidate"] }},
	{Name: EnvRetainWallTimeout, Default: "3600", Resolve: func(c Config) any { return c.Worker.WallTimeouts["retain"] }},
	{Name: EnvReflectWallTimeout, Default: "300", Resolve: func(c Config) any { return c.Worker.WallTimeouts["reflect"] }},

	// --- Maintenance cadences ---
	{Name: EnvRetentionSweepIntervalSeconds, Default: "3600", Resolve: func(c Config) any { return c.Maintenance.RetentionSweepInterval }},
	{Name: EnvOperationCleanupIntervalSeconds, Default: "900", Resolve: func(c Config) any { return c.Maintenance.OperationCleanupInterval }},
	{Name: EnvOperationCleanupBatchSize, Default: "1000", Resolve: func(c Config) any { return c.Maintenance.OperationCleanupBatchSize }},
	{Name: EnvOperationRetentionDays, Default: "0", Resolve: func(c Config) any { return c.Maintenance.OperationRetentionDays }},
	{Name: EnvConsolidationReconcileIntervalSeconds, Default: "300", Resolve: func(c Config) any { return c.Maintenance.ConsolidationReconcileInterval }},
	{Name: EnvMentalModelRefreshTickSeconds, Default: "300", Resolve: func(c Config) any { return c.Maintenance.MentalModelRefreshTick }},
	{Name: EnvMaintenanceStartJitterSeconds, Default: "60", Resolve: func(c Config) any { return c.Maintenance.StartJitter }},
	{Name: EnvMentalModelHistoryMaxEntries, Default: "50", Reason: "mental-model history is not implemented"},
	{Name: EnvMentalModelMinRefreshIntervalSeconds, Default: "0", Reason: "the minimum mental-model refresh interval is not implemented"},
	{Name: EnvObservationHistoryMaxEntries, Default: "50", Reason: "observation history is not implemented"},
	{Name: EnvKnowledgePageDefaultTrigger, Default: "", Reason: "the knowledge-page default trigger is not implemented"},

	// --- Vector index maintenance ---
	{Name: EnvVectorIndexMinRows, Default: "0", Resolve: func(c Config) any { return c.VectorIndex.MinRows }},
	{Name: EnvVectorIndexReconcileInterval, Default: "900", Resolve: func(c Config) any { return c.VectorIndex.ReconcileInterval }},
	{Name: EnvVectorExtension, Default: "pgvector", Resolve: func(c Config) any { return c.VectorExtension }},
	{Name: EnvANNIterativeScan, Default: "true", Reason: "iterative ANN scanning is not implemented"},
	{Name: EnvANNMaxScanTuples, Default: "4000", Reason: "the ANN scan-tuple cap is not implemented"},

	// --- Text search ---
	{Name: EnvTextSearchExtension, Default: "native", Resolve: func(c Config) any { return c.TextSearchExtension }},
	{Name: EnvTextSearchExtensionNativeLanguage, Default: "english", Reason: "the native text-search dictionary is not implemented"},
	{Name: EnvTextSearchPGFunctionSchema, Default: "paradedb", Reason: "the ParadeDB pg_search schema is not implemented"},
	{Name: EnvTextSearchPGTokenizer, Default: "", Reason: "the ParadeDB pg_search tokenizer is not implemented"},
	{Name: EnvTokenizerEncoding, Default: DefaultTokenizerEncoding, Resolve: func(c Config) any { return c.TokenizerEncoding }},
	{Name: EnvQueryAnalyzerLanguages, Default: "", Reason: "dateparser language restriction is not implemented"},

	// --- Caches ---
	{Name: EnvBankInfoCacheTTLSeconds, Default: "30", Resolve: func(c Config) any { return c.BankInfoCacheTTL }},
	{Name: EnvBankInfoCacheMaxEntries, Default: "2048", Reason: "the bank-info cache entry bound is not implemented"},
	{Name: EnvBankAliasCacheTTLSeconds, Default: "10", Reason: "the bank-alias cache is not implemented"},
	{Name: EnvBankAliasCacheMaxEntries, Default: "2048", Reason: "the bank-alias cache is not implemented"},
	{Name: EnvBankStatsCacheTTLSeconds, Default: "60", Resolve: func(c Config) any { return c.BankStatsCacheTTL }},
	{Name: EnvBankStatsCacheMaxEntries, Default: "1024", Reason: "the bank-stats cache is not implemented"},

	// --- Entity resolution ---
	{Name: EnvEntityTRGMSimilarityThreshold, Default: "0.3", Reason: "the entity trigram threshold is not implemented"},
	{Name: EnvEntityIntrabatchMergeSimilarity, Default: "0.5", Reason: "intra-batch entity merging is not implemented"},
	{Name: EnvEntityMergeMinSimilarity, Default: "0.3", Reason: "the entity merge similarity is not implemented"},

	// --- Worker and queue ---
	{Name: EnvWorkerEnabled, Default: "true", Resolve: func(c Config) any { return c.WorkerEnabled }},
	{Name: EnvWorkerID, Default: "", Resolve: func(c Config) any { return c.Worker.ID }},
	{Name: EnvWorkerMaxRetries, Default: "3", Resolve: func(c Config) any { return c.Worker.MaxRetries }},
	{Name: EnvWorkerMaxSlots, Default: "10", Resolve: func(c Config) any { return c.Worker.Concurrency }},
	{Name: EnvWorkerPollIntervalMS, Default: "500", Resolve: func(c Config) any { return c.Worker.PollInterval }},
	{Name: EnvWorkerTaskRetryBackoffSecs, Default: "60", Resolve: func(c Config) any { return c.Worker.InitialBackoff }},
	{Name: EnvWorkerHTTPPort, Default: "8889", Resolve: func(c Config) any { return c.Worker.HTTPPort }},
	{Name: EnvWorkerConsolidationReservedSlots, Default: "2", Resolve: func(c Config) any { return c.Worker.ReservedSlots["consolidate"] }},
	{Name: EnvWorkerConsolidationBankPriority, Default: "", Reason: "consolidation bank priority is not implemented"},
	{Name: EnvWorkerRetainReservedSlots, Default: "0", Reason: "per-type worker slot reservations beyond consolidation are not implemented"},
	{Name: EnvWorkerFileConvertRetainReservedSlots, Default: "0", Reason: "per-type worker slot reservations beyond consolidation are not implemented"},
	{Name: EnvWorkerRefreshMentalModelReservedSlots, Default: "0", Reason: "per-type worker slot reservations beyond consolidation are not implemented"},
	{Name: EnvWorkerGraphMaintenanceReservedSlots, Default: "0", Reason: "per-type worker slot reservations beyond consolidation are not implemented"},
	{Name: EnvWorkerVectorIndexMaintReservedSlots, Default: "0", Reason: "per-type worker slot reservations beyond consolidation are not implemented"},
	{Name: EnvWorkerImportDocumentsReservedSlots, Default: "0", Reason: "per-type worker slot reservations beyond consolidation are not implemented"},
	{Name: EnvWorkerExportDocumentsReservedSlots, Default: "0", Reason: "per-type worker slot reservations beyond consolidation are not implemented"},
	{Name: EnvWorkerImportBankReservedSlots, Default: "0", Reason: "per-type worker slot reservations beyond consolidation are not implemented"},
	{Name: EnvWorkerExportBankReservedSlots, Default: "0", Reason: "per-type worker slot reservations beyond consolidation are not implemented"},
	{Name: EnvWorkerCloneBankReservedSlots, Default: "0", Reason: "per-type worker slot reservations beyond consolidation are not implemented"},

	// --- Admission ---
	{Name: EnvAdmissionRecallMaxInFlight, Default: "0", Resolve: func(c Config) any { return c.AdmissionRecallLimit },
		Divergent: "the reference derives the ceiling from the core count; this build has no per-core formula and keeps a fixed default"},
	{Name: EnvAdmissionReflectMaxInFlight, Default: "0", Resolve: func(c Config) any { return c.AdmissionReflectLimit },
		Divergent: "the reference derives the ceiling from the core count; this build has no per-core formula and keeps a fixed default"},
	{Name: EnvAdmissionRetainMaxInFlight, Default: "0", Resolve: func(c Config) any { return c.AdmissionRetainLimit },
		Divergent: "the reference derives the ceiling from the core count; this build has no per-core formula and keeps a fixed default"},
	{Name: EnvAdmissionRecallMaxWaitMS, Default: "30000", Reason: "per-lane admission waits are not implemented"},
	{Name: EnvAdmissionReflectMaxWaitMS, Default: "5000", Reason: "per-lane admission waits are not implemented"},
	{Name: EnvAdmissionRetainMaxWaitMS, Default: "2000", Reason: "per-lane admission waits are not implemented"},

	// --- Audit log ---
	{Name: EnvAuditLogEnabled, Default: "false", Resolve: func(c Config) any { return c.AuditLogEnabled }},
	{Name: EnvAuditLogRetentionDays, Default: "-1", Resolve: func(c Config) any { return c.Maintenance.AuditLogRetentionDays }},
	{Name: EnvAuditLogActions, Default: "", Resolve: func(c Config) any { return c.AuditLogActions }},

	// --- Model-request tracing beyond the exporter ---

	// --- OpenTelemetry ---
	{Name: EnvOTELTracesEnabled, Default: "false", Resolve: func(c Config) any { return c.Tracing.Enabled }},
	{Name: EnvOTELExporterOTLPEndpoint, Default: "", Resolve: func(c Config) any { return c.Tracing.Endpoint }},
	{Name: EnvOTELExporterOTLPHeaders, Default: "", Resolve: func(c Config) any { return c.Tracing.Headers }},
	{Name: EnvOTELServiceName, Default: DefaultTracingServiceName, Resolve: func(c Config) any { return c.Tracing.ServiceName }},
	{Name: EnvOTELDeploymentEnvironment, Default: DefaultTracingEnvironment, Resolve: func(c Config) any { return c.Tracing.Environment }},
	{Name: EnvMetricsIncludeBankID, Default: "false", Reason: "the metrics bank-id label is not implemented"},
	{Name: EnvMetricsBacklogEnabled, Default: "false", Reason: "backlog metrics are not implemented"},
	{Name: EnvMetricsIncludeTenant, Default: "false", Reason: "the metrics tenant label is not implemented"},
	{Name: EnvMetricsWorkerLabel, Default: "false", Reason: "the per-worker metrics label is not implemented"},
	{Name: EnvLoopLagMetric, Default: "false", Reason: "the event-loop lag histogram is not implemented"},
	{Name: EnvLoopLagReportSeconds, Default: "0", Resolve: func(c Config) any { return c.LoopLagReport }},
	{Name: EnvLoopWatchdogEnabled, Default: "true", Reason: "the loop watchdog is not implemented"},
	{Name: EnvLoopWatchdogStallThresholdMS, Default: "", Reason: "the loop watchdog is not implemented"},
	{Name: EnvLoopWatchdogPollIntervalMS, Default: "", Reason: "the loop watchdog is not implemented"},

	// --- File storage and parsing ---
	{Name: EnvFileStorageType, Default: "native", Resolve: func(c Config) any { return c.FileStorage.Type },
		Divergent: "this build names its in-database backend postgresql; native and postgresql both select it"},
	{Name: EnvFileStorageS3Bucket, Default: "", Resolve: func(c Config) any { return c.FileStorage.S3.Bucket }},
	{Name: EnvFileStorageS3Region, Default: "", Resolve: func(c Config) any { return c.FileStorage.S3.Region }},
	{Name: EnvFileStorageS3Endpoint, Default: "", Resolve: func(c Config) any { return c.FileStorage.S3.Endpoint }},
	{Name: EnvFileStorageS3AccessKeyID, Default: "", Resolve: func(c Config) any { return c.FileStorage.S3.AccessKeyID }},
	{Name: EnvFileStorageS3SecretAccessKey, Default: "", Resolve: func(c Config) any { return c.FileStorage.S3.SecretAccessKey }},
	{Name: EnvFileStorageExtension, Default: "", Reason: "external file-storage extensions are not implemented"},
	{Name: EnvFileStorageGCSBucket, Default: "", Resolve: func(c Config) any { return c.FileStorage.GCS.Bucket }},
	{Name: EnvFileStorageGCSServiceAccountKey, Default: "", Resolve: func(c Config) any { return c.FileStorage.GCS.ServiceAccountKey }},
	{Name: EnvFileStorageAzureContainer, Default: "", Resolve: func(c Config) any { return c.FileStorage.Azure.Container }},
	{Name: EnvFileStorageAzureAccountName, Default: "", Resolve: func(c Config) any { return c.FileStorage.Azure.AccountName }},
	{Name: EnvFileStorageAzureAccountKey, Default: "", Resolve: func(c Config) any { return c.FileStorage.Azure.AccountKey }},
	{Name: EnvFileParser, Default: "markitdown", Delegated: "resolved by internal/fileparse"},
	{Name: EnvFileParserAllowlist, Default: "", Delegated: "resolved by internal/fileparse"},
	{Name: EnvFileParserMarkitdownOCREnabled, Default: "false", Reason: "the document-parser OCR chain is not implemented"},
	{Name: EnvFileParserMarkitdownOCRAPIKey, Default: "", Reason: "the document-parser OCR chain is not implemented"},
	{Name: EnvFileParserMarkitdownOCRBaseURL, Default: "", Reason: "the document-parser OCR chain is not implemented"},
	{Name: EnvFileParserMarkitdownOCRModel, Default: "", Reason: "the document-parser OCR chain is not implemented"},
	{Name: EnvFileParserMarkitdownOCRPrompt, Default: "", Reason: "the document-parser OCR chain is not implemented"},
	{Name: EnvFileParserMarkitdownOCRHeaders, Default: "", Reason: "the document-parser OCR chain is not implemented"},
	{Name: EnvFileParserIrisToken, Default: "", Delegated: "resolved by internal/fileparse"},
	{Name: EnvFileParserIrisOrgID, Default: "", Delegated: "resolved by internal/fileparse"},
	{Name: EnvFileParserLlamaParseAPIKey, Default: "", Delegated: "resolved by internal/fileparse"},
	{Name: EnvFileConversionMaxBatchSizeMB, Default: "100", Delegated: "resolved by internal/fileparse"},
	{Name: EnvFileConversionMaxBatchSize, Default: "10", Delegated: "resolved by internal/fileparse"},
	{Name: EnvFileDeleteAfterRetain, Default: "true", Delegated: "resolved by internal/fileparse"},

	// --- Webhooks and notifications ---
	{Name: EnvWebhookAllowedHosts, Default: "", Resolve: func(c Config) any { return c.WebhookAllowedHosts }},
	{Name: EnvWebhookURL, Default: "", Reason: "the global webhook target is not implemented"},
	{Name: EnvWebhookSecret, Default: "", Reason: "the global webhook secret is not implemented"},
	{Name: EnvWebhookEventTypes, Default: "consolidation.completed", Reason: "the global webhook event filter is not implemented"},
	{Name: EnvWebhookExposeResponseBody, Default: "false", Reason: "exposing webhook response bodies is not implemented"},

	// --- Disposition seeds ---
	{Name: EnvModelInitTimeout, Default: "300", Reason: "the model-init timeout is not implemented"},
	{Name: EnvDispositionSkepticism, Default: "", Reason: "disposition seeding is not implemented"},
	{Name: EnvDispositionLiteralism, Default: "", Reason: "disposition seeding is not implemented"},
	{Name: EnvDispositionEmpathy, Default: "", Reason: "disposition seeding is not implemented"},

	// --- Model init ---
}
