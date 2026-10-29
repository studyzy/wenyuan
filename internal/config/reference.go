package config

import (
	"encoding/json"
	"strings"
)

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
	// UnsupportedWhen refuses the key for the values it accepts rather than for
	// being set at all. It is for a key this build honours *partially*: the
	// reference switches behaviour on the value, so refusing every setting would
	// reject the value this build does implement, and honouring every setting
	// would serve behaviour the operator did not ask for.
	UnsupportedWhen func(value string) bool
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
	{Name: EnvPort, Default: "18888", Resolve: func(c Config) any { return c.Port }},
	{Name: EnvLogLevel, Default: DefaultLogLevel, Resolve: func(c Config) any { return c.LogLevel }},
	{Name: EnvAccessLog, Default: "false", Resolve: func(c Config) any { return c.AccessLog }},
	{Name: EnvWorkers, Default: "1", Resolve: func(c Config) any { return c.Workers }},
	{Name: EnvBasePath, Default: "", Resolve: func(c Config) any { return c.BasePath }},
	{Name: EnvGzipMinSize, Default: "1024", Resolve: func(c Config) any { return c.GzipMinSize }},
	{Name: EnvProfile, Default: "", Resolve: func(c Config) any { return c.Profile }},
	{Name: EnvRunMigrationsOnStartup, Default: "true", Resolve: func(c Config) any { return c.RunMigrationsOnStartup }},
	{Name: EnvStoreDocumentText, Default: "true", Resolve: func(c Config) any { return c.StoreDocumentText }},
	{Name: EnvLogFormat, Default: DefaultLogFormat, Resolve: func(c Config) any { return c.LogFormat }},
	{Name: EnvLogJSONFields, Default: "", Resolve: func(c Config) any { return c.LogJSONFields }},
	{Name: EnvDaemonLog, Default: "", Resolve: func(c Config) any { return c.DaemonLog }},
	{Name: EnvMCPAuthToken, Default: "", Resolve: func(c Config) any { return c.MCPAuthToken }},
	{Name: EnvMCPStateless, Default: "false", Resolve: func(c Config) any { return false },
		UnsupportedWhen: func(value string) bool {
			return !strings.EqualFold(strings.TrimSpace(value), "false")
		},
		Reason: "the stateful streamable-HTTP transport is the only one implemented"},
	{Name: EnvMCPEnabledTools, Default: "all", Resolve: func(c Config) any { return c.MCPEnabledTools }},
	{Name: EnvMCPInstructions, Default: "", Resolve: func(c Config) any { return c.MCPInstructions }},
	{Name: EnvEnableDryRunExtract, Default: "true", Resolve: func(c Config) any { return c.EnableDryRunExtract }},
	{Name: EnvDefaultBankTemplate, Default: "", Resolve: func(c Config) any { return c.DefaultBankTemplate },
		UnsupportedWhen: func(value string) bool { return !validJSONObject(value) },
		Reason:          "the value must be a JSON object"},
	{Name: EnvExtensionPassThroughHdr, Default: "", Resolve: func(c Config) any { return c.ExtensionPassThroughHeaders }},

	// --- Database connection pool ---
	{Name: EnvDatabaseURL, Default: "pg0", Resolve: func(c Config) any { return c.DatabaseURL },
		Divergent: "the reference's default is an embedded pg0 instance this build cannot manage; an unset URL refuses startup instead"},
	{Name: EnvDatabaseSchema, Default: "public", Resolve: func(c Config) any { return c.DatabaseSchema }},
	{Name: EnvDBPoolMinSize, Default: "5", Resolve: func(c Config) any { return c.DBPoolMinSize }},
	{Name: EnvDBPoolMaxSize, Default: "100", Resolve: func(c Config) any { return c.DBPoolMaxSize }},
	{Name: EnvDatabaseBackend, Default: "postgresql", Resolve: func(c Config) any { return c.DatabaseBackend },
		UnsupportedWhen: func(value string) bool {
			switch strings.ToLower(strings.TrimSpace(value)) {
			case "", "postgresql", "pgx", "memory":
				return false
			}
			return true
		},
		Reason: "only the PostgreSQL backend (and this build's in-memory one) is implemented; the reference's Oracle dialect is not"},
	{Name: EnvReadDatabaseURL, Default: "", Resolve: func(c Config) any { return c.ReadReplica.URL }},
	{Name: EnvReadDBPoolMinSize, Default: "5", Resolve: func(c Config) any { return c.ReadReplica.PoolMin }},
	{Name: EnvReadDBPoolMaxSize, Default: "100", Resolve: func(c Config) any { return c.ReadReplica.PoolMax }},
	{Name: EnvMigrationDatabaseURL, Default: "", Resolve: func(c Config) any { return c.DBTuning.MigrationDatabaseURL }},
	{Name: EnvDBCommandTimeout, Default: "60", Reason: "the per-command database timeout is not implemented"},
	{Name: EnvDBAcquireTimeout, Default: "30", Resolve: func(c Config) any { return c.DBTuning.AcquireTimeout }},
	{Name: EnvDBPoolSlowAcquireThreshold, Default: "0.05", Resolve: func(c Config) any { return c.DBTuning.PoolSlowAcquireThreshold }},
	{Name: EnvDBStatementTimeout, Default: "600", Resolve: func(c Config) any { return c.DBTuning.StatementTimeout }},
	{Name: EnvDBMaxParallelWorkersGather, Default: "", Resolve: func(c Config) any { return c.DBTuning.MaxParallelWorkersGather }},
	{Name: EnvDBSessionSetupOnAcquire, Default: "true", Resolve: func(c Config) any { return c.DBTuning.SessionSetupOnAcquire },
		UnsupportedWhen: func(value string) bool {
			return strings.EqualFold(strings.TrimSpace(value), "false")
		},
		Reason: "this build applies the session settings to every connection; turning per-acquire setup off is not implemented"},
	{Name: EnvDBAcquireWarnThresholdMS, Default: "1000", Resolve: func(c Config) any { return c.DBTuning.AcquireWarnThreshold }},
	{Name: EnvMigrationIsolation, Default: "false", Resolve: func(c Config) any { return "false" },
		UnsupportedWhen: func(value string) bool {
			return !strings.EqualFold(strings.TrimSpace(value), "false")
		},
		Reason: "this build migrates in-process; the reference's subprocess isolation is not implemented"},
	{Name: EnvMigrationConcurrency, Default: "1", Resolve: func(c Config) any { return 1 },
		UnsupportedWhen: func(value string) bool {
			return strings.TrimSpace(value) != "1"
		},
		Reason: "this build migrates one schema at a time; the reference's concurrent tenant sweep is not implemented"},

	// --- Feature flags ---
	{Name: EnvMCPEnabled, Default: "true", Resolve: func(c Config) any { return c.MCPEnabled }},
	{Name: EnvEnableBankConfigAPI, Default: "true", Resolve: func(c Config) any { return c.EnableBankConfigAPI }},
	{Name: EnvEnableBankLLMHealth, Default: "false", Resolve: func(c Config) any { return c.EnableBankLLMHealth }},
	{Name: EnvEnableFileUploadAPI, Default: "true", Resolve: func(c Config) any { return c.EnableFileUploadAPI }},
	{Name: EnvEnableDocumentExportAPI, Default: "true", Resolve: func(c Config) any { return c.EnableDocumentExportAPI }},
	{Name: EnvEnableDocumentImportAPI, Default: "true", Resolve: func(c Config) any { return c.EnableDocumentImportAPI }},
	{Name: EnvEnableObservations, Default: "true", Resolve: func(c Config) any { return c.EnableObservations }},
	{Name: EnvEnableAutoConsolidation, Default: "true", Resolve: func(c Config) any { return c.EnableAutoConsolidation }},
	{Name: EnvEnableObservationHistory, Default: "true", Resolve: func(c Config) any { return c.EnableObservationHistory }},
	{Name: EnvEnableMentalModelHistory, Default: "true", Resolve: func(c Config) any { return c.EnableMentalModelHistory }},

	// --- Retrieval arms ---
	{Name: EnvEnableTextSearch, Default: "true", Resolve: func(c Config) any { return c.ProcessDefaults.EnableTextSearch }},
	{Name: EnvEnableTemporalRetrieval, Default: "true", Resolve: func(c Config) any { return c.ProcessDefaults.EnableTemporalRetrieval }},
	{Name: EnvEnableGraphRetrieval, Default: "true", Resolve: func(c Config) any { return c.ProcessDefaults.EnableGraphRetrieval }},
	{Name: EnvEnableReranking, Default: "true", Resolve: func(c Config) any { return c.ProcessDefaults.EnableReranking }},
	{Name: EnvGraphRetriever, Default: "link_expansion", Resolve: func(c Config) any { return c.RecallTuning.GraphRetriever },
		UnsupportedWhen: func(value string) bool {
			return !strings.EqualFold(strings.TrimSpace(value), "link_expansion")
		},
		Reason: "only the link_expansion graph retriever is implemented"},
	{Name: EnvGraphSeedMinSimilarity, Default: "0.3", Resolve: func(c Config) any { return c.RecallTuning.GraphSeedMinSimilarity }},
	{Name: EnvSemanticMinSimilarity, Default: "0.3", Resolve: func(c Config) any { return c.RecallTuning.SemanticMinSimilarity }},
	{Name: EnvTemporalSemanticMinSimilarity, Default: "0.1", Resolve: func(c Config) any { return c.RecallTuning.TemporalSemanticMinSimilarity }},
	{Name: EnvSemanticLinkMinSimilarity, Default: "0.7", Resolve: func(c Config) any { return c.RecallTuning.SemanticLinkMinSimilarity }},
	{Name: EnvBM25MinScore, Default: "0.0", Resolve: func(c Config) any { return c.RecallTuning.BM25MinScore }},
	{Name: EnvBM25MaxQueryTerms, Default: "16", Resolve: func(c Config) any { return c.RecallTuning.BM25MaxQueryTerms }},
	{Name: EnvBM25SelectiveTerms, Default: "true", Resolve: func(c Config) any { return c.RecallTuning.BM25SelectiveTerms }},
	{Name: EnvLinkExpansionPerEntityLimit, Default: "200", Resolve: func(c Config) any { return c.RecallTuning.LinkExpansionPerEntityLimit }},
	{Name: EnvLinkExpansionTimeout, Default: "10", Resolve: func(c Config) any { return c.RecallTuning.LinkExpansionTimeout }},
	{Name: EnvRecallConnectionBudget, Default: "4", Resolve: func(c Config) any { return c.RecallTuning.ConnectionBudget }},
	{Name: EnvRecallMaxQueryTokens, Default: "500", Resolve: func(c Config) any { return c.RecallTuning.MaxQueryTokens }},
	{Name: EnvRecallDiagnosticPhases, Default: "true", Resolve: func(c Config) any { return c.RecallTuning.DiagnosticPhases }},
	{Name: EnvRecallPhaseSampleEvery, Default: "1", Resolve: func(c Config) any { return c.RecallTuning.PhaseSampleEvery }},
	{Name: EnvRecallMaxConcurrent, Default: "32", Resolve: func(c Config) any { return c.RecallTuning.MaxConcurrent }},
	{Name: EnvRecallIncludeChunks, Default: "true", Resolve: func(c Config) any { return c.ProcessDefaults.RecallIncludeChunks }},
	{Name: EnvRecallPreferObservations, Default: "false", Resolve: func(c Config) any {
		return c.ProcessDefaults.RecallPreferObservations
	}},
	{Name: EnvDefaultObservationScopes, Default: "combined", Resolve: func(c Config) any {
		// A mode name is the raw string; an explicit scope list is the raw JSON
		// text, exactly as the caller wrote it.
		if groups, ok := c.ProcessDefaults.DefaultObservationScopes.([]any); ok {
			if encoded, err := json.Marshal(groups); err == nil {
				return string(encoded)
			}
		}
		return c.ProcessDefaults.DefaultObservationScopes
	},
		// The environment parser validates the value at config construction, so a
		// mode it does not know, malformed JSON or a scope list with non-string
		// members stops start-up rather than failing every later consolidation
		// (config.py:_parse_observation_scopes_env). The shapes it accepts are
		// honoured.
		UnsupportedWhen: observationScopesEnvRefused,
		Reason:          "the value must be one of combined, per_tag, all_combinations, shared or a list of tag lists (e.g. [[\"topic:*\"]])"},
	{Name: EnvRecallMaxTokens, Default: "", Resolve: func(c Config) any { return c.ProcessDefaults.RecallMaxTokens }},
	{Name: EnvRecallChunksMaxTokens, Default: "", Resolve: func(c Config) any { return c.ProcessDefaults.RecallChunksMaxTokens }},
	{Name: EnvBackpressureDeferSeconds, Default: "", Resolve: func(c Config) any { return c.WorkerTuning.BackpressureDefer }},

	// --- Recall budget function ---
	{Name: EnvRecallBudgetFunction, Default: "fixed", Resolve: func(c Config) any { return c.ProcessDefaults.RecallBudgetFunction }},
	{Name: EnvRecallBudgetFixedLow, Default: "100", Resolve: func(c Config) any { return c.ProcessDefaults.RecallBudgetFixedLow }},
	{Name: EnvRecallBudgetFixedMid, Default: "300", Resolve: func(c Config) any { return c.ProcessDefaults.RecallBudgetFixedMid }},
	{Name: EnvRecallBudgetFixedHigh, Default: "1000", Resolve: func(c Config) any { return c.ProcessDefaults.RecallBudgetFixedHigh }},
	{Name: EnvRecallBudgetAdaptiveLow, Default: "", Resolve: func(c Config) any { return c.ProcessDefaults.RecallBudgetAdaptiveLow }},
	{Name: EnvRecallBudgetAdaptiveMid, Default: "", Resolve: func(c Config) any { return c.ProcessDefaults.RecallBudgetAdaptiveMid }},
	{Name: EnvRecallBudgetAdaptiveHigh, Default: "", Resolve: func(c Config) any { return c.ProcessDefaults.RecallBudgetAdaptiveHigh }},
	{Name: EnvRecallBudgetMin, Default: "", Resolve: func(c Config) any { return c.ProcessDefaults.RecallBudgetMin }},
	{Name: EnvRecallBudgetMax, Default: "", Resolve: func(c Config) any { return c.ProcessDefaults.RecallBudgetMax }},

	// --- Recall tuning this build does honour ---
	{Name: EnvRecallStrategyBoosts, Default: "", Resolve: func(c Config) any { return c.RecallStrategyBoosts }},
	{Name: EnvRecallMaxCandidatesPerSource, Default: "0", Resolve: func(c Config) any { return c.Worker.RecallMaxCandidatesPerSource }},
	{Name: EnvRecencyDecayFunction, Default: DefaultRecencyDecayFunction, Resolve: func(c Config) any { return c.Recency.Function }},
	{Name: EnvRecencyDecayLinearWindowDays, Default: "365", Resolve: func(c Config) any { return c.Recency.LinearWindowDays }},
	{Name: EnvRecencyDecayHalflifeDays, Default: "90", Resolve: func(c Config) any { return c.Recency.HalflifeDays }},

	// --- Retain tuning ---
	{Name: EnvRetainAttachmentMaxSizeMB, Default: "20", Resolve: func(c Config) any { return c.RetainAttachmentMaxSizeMB }},
	{Name: EnvRetainAttachmentMaxCount, Default: "50", Resolve: func(c Config) any { return c.RetainAttachmentMaxCount }},
	{Name: EnvRetainBatchEnabled, Default: "false", Resolve: func(c Config) any { return c.RetainBatchEnabled },
		UnsupportedWhen: func(value string) bool {
			return strings.EqualFold(strings.TrimSpace(value), "true")
		},
		Reason: "the reference routes retain extraction through the LLM Batch API when this is set; that path (submission, polling, crash recovery) is not implemented"},
	{Name: EnvRetainMaxCompletionTokens, Default: "64000", Resolve: func(c Config) any { return c.RetainTuning.MaxCompletionTokens }},
	{Name: EnvRetainChunkSize, Default: "3000", Resolve: func(c Config) any { return c.ProcessDefaults.RetainChunkSize }},
	{Name: EnvRetainStructuredChunkSize, Default: "", Resolve: func(c Config) any { return c.ProcessDefaults.RetainStructuredChunkSize }},
	{Name: EnvRetainExtractCausalLinks, Default: "true", Resolve: func(c Config) any { return c.RetainTuning.ExtractCausalLinks }},
	{Name: EnvRetainOptionalFactDimensions, Default: "false", Resolve: func(c Config) any { return c.RetainOptionalFactDimensions }},
	{Name: EnvRetainExtractionMode, Default: DefaultRetainExtractionMode, Resolve: func(c Config) any { return c.RetainExtractionMode }},
	{Name: EnvRetainMission, Default: "", Resolve: func(c Config) any { return c.ProcessDefaults.RetainMission }},
	{Name: EnvRetainCustomInstructions, Default: "", Resolve: func(c Config) any { return c.ProcessDefaults.RetainCustomInstructions }},
	{Name: EnvRetainDefaultStrategy, Default: "", Resolve: func(c Config) any { return c.ProcessDefaults.RetainDefaultStrategy }},
	{Name: EnvRetainBatchTokens, Default: "10000", Resolve: func(c Config) any { return c.RetainTuning.SubBatchTokens }},
	{Name: EnvRetainEntityLookup, Default: "trigram", Resolve: func(c Config) any { return c.RetainTuning.EntityLookup },
		UnsupportedWhen: func(value string) bool {
			return !strings.EqualFold(strings.TrimSpace(value), "trigram")
		},
		Reason: "only the trigram entity-lookup mode is implemented"},
	{Name: EnvRetainEntityResolutionBatchSize, Default: "100", Reason: "entity-resolution tuning is not implemented"},
	{Name: EnvRetainEntityResolutionMaxCandidates, Default: "200", Resolve: func(c Config) any { return c.RetainTuning.EntityResolutionCandidates }},
	{Name: EnvRetainBatchPollIntervalSeconds, Default: "60", Reason: "the LLM Batch API poll interval is not implemented"},
	{Name: EnvRetainChunkBatchSize, Default: "100", Resolve: func(c Config) any { return c.ProcessDefaults.RetainChunkBatchSize }},
	{Name: EnvRetainMemoryBudgetMB, Default: "128", Reason: "the retain memory budget is not implemented"},
	{Name: EnvRetainMaxAttachmentsPerChunk, Default: "8", Resolve: func(c Config) any { return c.ProcessDefaults.RetainMaxAttachments }},
	{Name: EnvRetainBatchDocumentWrites, Default: "false", Reason: "batched document writes are not implemented"},
	{Name: EnvRetainMaxConcurrent, Default: "4", Reason: "the retain concurrency cap is not implemented"},
	{Name: EnvRetainSubbatchConcurrency, Default: "1", Reason: "retain sub-batch concurrency is not implemented"},
	{Name: EnvRetainStoreMaxConcurrent, Default: "", Reason: "the retain store concurrency cap is not implemented"},
	{Name: EnvFailOnExtractionErrors, Default: "false", Reason: "the reference escalates batch-API extraction errors; this build has no batch path, and its streaming path already fails the operation on an extraction error"},

	// --- Reflect tuning ---
	{Name: EnvReflectMaxIterations, Default: "10", Resolve: func(c Config) any { return c.ReflectTuning.MaxIterations }},
	{Name: EnvReflectPromptCacheEnabled, Default: "", Reason: "the reference's incremental prompt cache is not implemented: it is a per-provider cache session with its own lifecycle, and this build's provider boundary has no equivalent"},
	{Name: EnvReflectMaxContextTokens, Default: "100000", Resolve: func(c Config) any { return c.ReflectTuning.MaxContextTokens }},
	{Name: EnvReflectMission, Default: "", Resolve: func(c Config) any { return c.ProcessDefaults.ReflectMission }},
	{Name: EnvReflectSourceFactsMaxTokens, Default: "-1", Resolve: func(c Config) any { return c.ProcessDefaults.ReflectSourceFactsMaxTokens }},
	{Name: EnvReflectDefaultOptions, Default: "", Resolve: func(c Config) any { return c.ProcessDefaults.ReflectDefaultOptions },
		UnsupportedWhen: func(value string) bool { return !validJSONObject(value) },
		Reason:          "the value must be a JSON object"},
	{Name: EnvReflectMaxCompletionTokens, Default: "", Resolve: func(c Config) any { return c.ReflectTuning.MaxCompletionTokens }},

	// --- Observations and consolidation ---
	{Name: EnvMaxObservationsPerScope, Default: "-1", Resolve: func(c Config) any { return c.ProcessDefaults.MaxObservationsPerScope }},
	{Name: EnvObservationScopeLimits, Default: "", Resolve: func(c Config) any { return c.ProcessDefaults.ObservationScopeLimits }},
	{Name: EnvObservationsMission, Default: "", Resolve: func(c Config) any { return c.ProcessDefaults.ObservationsMission }},
	{Name: EnvConsolidationMemoryBatchSize, Default: "50", Resolve: func(c Config) any { return c.ConsolidationTuning.MemoryBatchSize }},
	{Name: EnvConsolidationMaxMemoriesPerRound, Default: "100", Resolve: func(c Config) any { return c.ProcessDefaults.ConsolidationMaxPerRound }},
	{Name: EnvConsolidationDedupThreshold, Default: "0.97", Resolve: func(c Config) any { return c.ConsolidationTuning.DedupThreshold }},
	{Name: EnvConsolidationMaxTokens, Default: "512", Resolve: func(c Config) any { return c.ConsolidationTuning.MaxTokens }},
	{Name: EnvConsolidationMaxCompletionTokens, Default: "", Resolve: func(c Config) any { return c.ConsolidationTuning.MaxCompletionTokens }},
	{Name: EnvConsolidationSourceFactsMaxTokens, Default: "4096", Resolve: func(c Config) any { return c.ProcessDefaults.ConsolidationSourceFactTokens }},
	{Name: EnvConsolidationSourceFactsMaxTokensPerObservation, Default: "256", Resolve: func(c Config) any { return c.ProcessDefaults.ConsolidationSourceFactTokensPerObservation }},
	{Name: EnvConsolidationRecallBudget, Default: "low", Resolve: func(c Config) any { return c.ConsolidationTuning.RecallBudget }},
	{Name: EnvConsolidationMaxAttempts, Default: "3", Resolve: func(c Config) any { return c.ConsolidationTuning.MaxAttempts }},
	{Name: EnvConsolidationStrategies, Default: "", Resolve: func(c Config) any { return c.ProcessDefaults.ConsolidationStrategies }},
	{Name: EnvConsolidationLLMBatchSize, Default: "8", Resolve: func(c Config) any { return c.ProcessDefaults.ConsolidationLLMBatchSize }},
	{Name: EnvConsolidationLLMParallelism, Default: "4", Resolve: func(c Config) any { return c.ProcessDefaults.ConsolidationLLMParallelism }},
	{Name: EnvConsolidationWallTimeout, Default: "7200", Resolve: func(c Config) any { return c.Worker.WallTimeouts["consolidation"] }},
	{Name: EnvRetainWallTimeout, Default: "3600", Resolve: func(c Config) any { return c.Worker.WallTimeouts["retain"] }},
	// The reference key names the *budget*, and the ceiling it sets belongs to the
	// operation that spends it: a refresh is one reflect plus its write, so the
	// map is keyed by the operation type and this key resolves through it.
	{Name: EnvReflectWallTimeout, Default: "300", Resolve: func(c Config) any { return c.Worker.WallTimeouts["refresh_mental_model"] }},

	// --- Maintenance cadences ---
	{Name: EnvRetentionSweepIntervalSeconds, Default: "3600", Resolve: func(c Config) any { return c.Maintenance.RetentionSweepInterval }},
	{Name: EnvOperationCleanupIntervalSeconds, Default: "900", Resolve: func(c Config) any { return c.Maintenance.OperationCleanupInterval }},
	{Name: EnvOperationCleanupBatchSize, Default: "1000", Resolve: func(c Config) any { return c.Maintenance.OperationCleanupBatchSize }},
	{Name: EnvOperationRetentionDays, Default: "0", Resolve: func(c Config) any { return c.Maintenance.OperationRetentionDays }},
	{Name: EnvConsolidationReconcileIntervalSeconds, Default: "300", Resolve: func(c Config) any { return c.Maintenance.ConsolidationReconcileInterval }},
	{Name: EnvMentalModelRefreshTickSeconds, Default: "300", Resolve: func(c Config) any { return c.Maintenance.MentalModelRefreshTick }},
	{Name: EnvMaintenanceStartJitterSeconds, Default: "60", Resolve: func(c Config) any { return c.Maintenance.StartJitter }},
	{Name: EnvMentalModelHistoryMaxEntries, Default: "50", Resolve: func(c Config) any { return c.ConsolidationTuning.MentalModelHistoryEntries }},
	{Name: EnvMentalModelMinRefreshIntervalSeconds, Default: "0", Resolve: func(c Config) any { return c.ProcessDefaults.MentalModelMinRefreshSeconds }},
	{Name: EnvObservationHistoryMaxEntries, Default: "50", Resolve: func(c Config) any { return c.ConsolidationTuning.HistoryMaxEntries }},
	{Name: EnvKnowledgePageDefaultTrigger, Default: "", Resolve: func(c Config) any { return c.ProcessDefaults.KnowledgePageDefaultTrigger },
		UnsupportedWhen: func(value string) bool { return !validJSONObject(value) },
		Reason:          "the value must be a JSON object"},

	// --- Vector index maintenance ---
	{Name: EnvVectorIndexMinRows, Default: "0", Resolve: func(c Config) any { return c.VectorIndex.MinRows }},
	{Name: EnvVectorIndexReconcileInterval, Default: "900", Resolve: func(c Config) any { return c.VectorIndex.ReconcileInterval }},
	{Name: EnvVectorExtension, Default: "pgvector", Resolve: func(c Config) any { return c.VectorExtension }},
	{Name: EnvANNIterativeScan, Default: "true", Resolve: func(c Config) any { return c.DBTuning.ANNIterativeScan }},
	{Name: EnvANNMaxScanTuples, Default: "4000", Resolve: func(c Config) any { return c.DBTuning.ANNMaxScanTuples }},

	// --- Text search ---
	{Name: EnvTextSearchExtension, Default: "native",
		Resolve: func(c Config) any { return c.TextSearchExtension },
		// The reference switches its whole text-search implementation on this
		// key (native tsvector, vchord, ParadeDB pg_search, pgroonga,
		// pg_textsearch) and rebuilds the vectors and their indexes accordingly.
		// This build serves the native backend only, so any other value would be
		// accepted and then ignored - the one outcome the unsupported-key
		// mechanism exists to prevent.
		UnsupportedWhen: func(value string) bool {
			return !strings.EqualFold(strings.TrimSpace(value), DefaultTextSearchExtension)
		},
		Reason: "only the native tsvector backend is implemented"},
	{Name: EnvTextSearchExtensionNativeLanguage, Default: "english",
		Resolve: func(c Config) any { return c.TextSearchExtensionNativeLanguage },
		// The dictionary is embedded in the vector expression this build writes
		// and in every query it makes, and the reference's generated columns
		// carry it too, so a dictionary is a schema-level choice rather than a
		// query-level one. The reference lowercases the value and validates it as
		// a PostgreSQL identifier; that check runs where the expression is built,
		// and a well-formed name of a dictionary the server does not have is
		// accepted here and surfaces as the server's own error at query time.
	},
	{Name: EnvTextSearchPGFunctionSchema, Default: "paradedb", Resolve: func(c Config) any { return c.DBTuning.PGFunctionSchema }},
	{Name: EnvTextSearchPGTokenizer, Default: "", Resolve: func(c Config) any { return c.DBTuning.PGTokenizer }},
	{Name: EnvTokenizerEncoding, Default: DefaultTokenizerEncoding, Resolve: func(c Config) any { return c.TokenizerEncoding }},
	{Name: EnvQueryAnalyzerLanguages, Default: "", Reason: "dateparser language restriction is not implemented"},

	// --- Caches ---
	{Name: EnvBankInfoCacheTTLSeconds, Default: "30", Resolve: func(c Config) any { return c.BankInfoCacheTTL }},
	{Name: EnvBankInfoCacheMaxEntries, Default: "2048", Resolve: func(c Config) any { return c.ConsolidationTuning.BankInfoCacheMaxEntries }},
	{Name: EnvBankAliasCacheTTLSeconds, Default: "10", Resolve: func(c Config) any { return c.BankAliasCacheTTL }},
	{Name: EnvBankAliasCacheMaxEntries, Default: "2048", Resolve: func(c Config) any { return c.BankAliasCacheMaxEntries }},
	{Name: EnvBankStatsCacheTTLSeconds, Default: "60", Resolve: func(c Config) any { return c.BankStatsCacheTTL }},
	{Name: EnvBankStatsCacheMaxEntries, Default: "1024", Resolve: func(c Config) any { return c.ConsolidationTuning.BankStatsCacheMaxEntries }},

	// --- Entity resolution ---
	{Name: EnvEntityTRGMSimilarityThreshold, Default: "0.3", Resolve: func(c Config) any { return c.EntityTuning.TRGMSimilarityThreshold }},
	{Name: EnvEntityIntrabatchMergeSimilarity, Default: "0.5", Resolve: func(c Config) any { return c.EntityTuning.IntrabatchMerge }},
	{Name: EnvEntityMergeMinSimilarity, Default: "0.3", Resolve: func(c Config) any { return c.EntityTuning.MergeMinSimilarity }},

	// --- Worker and queue ---
	{Name: EnvWorkerEnabled, Default: "true", Resolve: func(c Config) any { return c.WorkerEnabled }},
	{Name: EnvWorkerID, Default: "", Resolve: func(c Config) any { return c.Worker.ID }},
	{Name: EnvWorkerMaxRetries, Default: "3", Resolve: func(c Config) any { return c.Worker.MaxRetries }},
	{Name: EnvWorkerMaxSlots, Default: "10", Resolve: func(c Config) any { return c.Worker.Concurrency }},
	{Name: EnvWorkerPollIntervalMS, Default: "500", Resolve: func(c Config) any { return c.Worker.PollInterval }},
	{Name: EnvWorkerTaskRetryBackoffSecs, Default: "60", Resolve: func(c Config) any { return c.Worker.InitialBackoff }},
	{Name: EnvWorkerHTTPPort, Default: "18889", Resolve: func(c Config) any { return c.Worker.HTTPPort }},
	{Name: EnvWorkerConsolidationReservedSlots, Default: "2", Resolve: func(c Config) any { return c.Worker.ReservedSlots["consolidation"] }},
	{Name: EnvWorkerConsolidationBankPriority, Default: "", Resolve: func(c Config) any { return c.WorkerTuning.ConsolidationByBank }},
	{Name: EnvWorkerRetainReservedSlots, Default: "0", Resolve: func(c Config) any { return c.Worker.ReservedSlots["retain"] }},
	{Name: EnvWorkerFileConvertRetainReservedSlots, Default: "0", Resolve: func(c Config) any { return c.Worker.ReservedSlots["file_convert_retain"] }},
	{Name: EnvWorkerRefreshMentalModelReservedSlots, Default: "0", Resolve: func(c Config) any { return c.Worker.ReservedSlots["refresh_mental_model"] }},
	{Name: EnvWorkerGraphMaintenanceReservedSlots, Default: "0", Resolve: func(c Config) any { return c.Worker.ReservedSlots["graph_maintenance"] }},
	{Name: EnvWorkerVectorIndexMaintReservedSlots, Default: "0", Resolve: func(c Config) any { return c.Worker.ReservedSlots["vector_index_maintenance"] }},
	{Name: EnvWorkerImportDocumentsReservedSlots, Default: "0", Resolve: func(c Config) any { return c.Worker.ReservedSlots["import_documents"] }},
	{Name: EnvWorkerExportDocumentsReservedSlots, Default: "0", Resolve: func(c Config) any { return c.Worker.ReservedSlots["export_documents"] }},
	{Name: EnvWorkerImportBankReservedSlots, Default: "0", Resolve: func(c Config) any { return c.Worker.ReservedSlots["import_bank"] }},
	{Name: EnvWorkerExportBankReservedSlots, Default: "0", Resolve: func(c Config) any { return c.Worker.ReservedSlots["export_bank"] }},
	{Name: EnvWorkerCloneBankReservedSlots, Default: "0", Resolve: func(c Config) any { return c.Worker.ReservedSlots["clone_bank"] }},

	// --- Admission ---
	{Name: EnvAdmissionRecallMaxInFlight, Default: "0", Resolve: func(c Config) any { return c.AdmissionRecallLimit },
		Divergent: "the reference derives the ceiling from the core count; this build has no per-core formula and keeps a fixed default"},
	{Name: EnvAdmissionReflectMaxInFlight, Default: "0", Resolve: func(c Config) any { return c.AdmissionReflectLimit },
		Divergent: "the reference derives the ceiling from the core count; this build has no per-core formula and keeps a fixed default"},
	{Name: EnvAdmissionRetainMaxInFlight, Default: "0", Resolve: func(c Config) any { return c.AdmissionRetainLimit },
		Divergent: "the reference derives the ceiling from the core count; this build has no per-core formula and keeps a fixed default"},
	{Name: EnvAdmissionRecallMaxWaitMS, Default: "30000", Resolve: func(c Config) any { return c.AdmissionRecallMaxWait }},
	{Name: EnvAdmissionReflectMaxWaitMS, Default: "5000", Resolve: func(c Config) any { return c.AdmissionReflectMaxWait }},
	{Name: EnvAdmissionRetainMaxWaitMS, Default: "2000", Resolve: func(c Config) any { return c.AdmissionRetainMaxWait }},

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
	{Name: EnvMetricsIncludeBankID, Default: "false", Resolve: func(c Config) any { return c.MetricsTuning.IncludeBankID }},
	{Name: EnvMetricsBacklogEnabled, Default: "false", Resolve: func(c Config) any { return c.MetricsTuning.BacklogEnabled }},
	{Name: EnvMetricsIncludeTenant, Default: "false", Resolve: func(c Config) any { return c.MetricsTuning.IncludeTenant }},
	{Name: EnvMetricsWorkerLabel, Default: "false", Resolve: func(c Config) any { return c.MetricsTuning.WorkerLabel }},
	{Name: EnvLoopLagMetric, Default: "false", Resolve: func(c Config) any { return c.MetricsTuning.LoopLagMetric }},
	{Name: EnvLoopLagReportSeconds, Default: "0", Resolve: func(c Config) any { return c.LoopLagReport }},
	{Name: EnvLoopWatchdogEnabled, Default: "true", Resolve: func(c Config) any { return c.MetricsTuning.LoopWatchdog }},
	{Name: EnvLoopWatchdogStallThresholdMS, Default: "1000", Resolve: func(c Config) any { return c.MetricsTuning.LoopWatchdogStall }},
	{Name: EnvLoopWatchdogPollIntervalMS, Default: "250", Resolve: func(c Config) any { return c.MetricsTuning.LoopWatchdogPoll }},

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
	{Name: EnvFileParserMarkitdownOCREnabled, Default: "false", Delegated: "resolved by internal/fileparse"},
	{Name: EnvFileParserMarkitdownOCRAPIKey, Default: "", Delegated: "resolved by internal/fileparse"},
	{Name: EnvFileParserMarkitdownOCRBaseURL, Default: "", Delegated: "resolved by internal/fileparse"},
	{Name: EnvFileParserMarkitdownOCRModel, Default: "", Delegated: "resolved by internal/fileparse"},
	{Name: EnvFileParserMarkitdownOCRPrompt, Default: "", Delegated: "resolved by internal/fileparse"},
	{Name: EnvFileParserMarkitdownOCRHeaders, Default: "", Delegated: "resolved by internal/fileparse"},
	{Name: EnvFileParserIrisToken, Default: "", Delegated: "resolved by internal/fileparse"},
	{Name: EnvFileParserIrisOrgID, Default: "", Delegated: "resolved by internal/fileparse"},
	{Name: EnvFileParserLlamaParseAPIKey, Default: "", Delegated: "resolved by internal/fileparse"},
	{Name: EnvFileConversionMaxBatchSizeMB, Default: "100", Delegated: "resolved by internal/fileparse"},
	{Name: EnvFileConversionMaxBatchSize, Default: "10", Delegated: "resolved by internal/fileparse"},
	{Name: EnvFileDeleteAfterRetain, Default: "true", Delegated: "resolved by internal/fileparse"},

	// --- Webhooks and notifications ---
	{Name: EnvWebhookAllowedHosts, Default: "", Resolve: func(c Config) any { return c.WebhookAllowedHosts }},
	{Name: EnvWebhookURL, Default: "", Resolve: func(c Config) any { return c.WebhookGlobal.URL }},
	{Name: EnvWebhookSecret, Default: "", Resolve: func(c Config) any { return c.WebhookGlobal.Secret }},
	{Name: EnvWebhookEventTypes, Default: "consolidation.completed", Resolve: func(c Config) any { return c.WebhookGlobal.EventTypes }},
	{Name: EnvWebhookExposeResponseBody, Default: "false", Resolve: func(c Config) any { return c.WebhookGlobal.ExposeResponseBody }},

	// --- Disposition seeds ---
	{Name: EnvModelInitTimeout, Default: "300", Resolve: func(c Config) any { return c.ModelInitTimeout }},
	{Name: EnvDispositionSkepticism, Default: "", Resolve: func(c Config) any { return c.ProcessDefaults.DispositionSkepticism }},
	{Name: EnvDispositionLiteralism, Default: "", Resolve: func(c Config) any { return c.ProcessDefaults.DispositionLiteralism }},
	{Name: EnvDispositionEmpathy, Default: "", Resolve: func(c Config) any { return c.ProcessDefaults.DispositionEmpathy }},

	// --- Model init ---
}
