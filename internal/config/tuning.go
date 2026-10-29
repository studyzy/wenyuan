package config

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// The reference's tuning keys.
//
// These are resolved into Config so a deployment that exported its reference
// .env starts this build with the same behaviour. Each one is consumed — by the
// process default a bank's configuration read layers its overrides over, or by
// the engine path the key names — which is why a key whose behaviour is not
// implemented is still refused rather than accepted (see reference.go).

// resolveProcessDefaults reads the per-bank configurable surface's process
// layer. The defaults are the reference's own, so an unset environment resolves
// exactly as it does there.
func resolveProcessDefaults(lookup func(string) (string, bool)) ProcessDefaults {
	return ProcessDefaults{
		MCPEnabledTools:               mcpConfigToolsFromEnv(lookup),
		EnableTextSearch:              boolFromEnv(lookup, EnvEnableTextSearch, true),
		EnableTemporalRetrieval:       boolFromEnv(lookup, EnvEnableTemporalRetrieval, true),
		EnableGraphRetrieval:          boolFromEnv(lookup, EnvEnableGraphRetrieval, true),
		EnableReranking:               boolFromEnv(lookup, EnvEnableReranking, true),
		RetainChunkSize:               intFromEnv(lookup, EnvRetainChunkSize, DefaultChunkSize),
		RetainStructuredChunkSize:     optionalIntValue(lookup, EnvRetainStructuredChunkSize),
		RetainMission:                 optionalStringValue(lookup, EnvRetainMission),
		RetainCustomInstructions:      optionalStringValue(lookup, EnvRetainCustomInstructions),
		RetainDefaultStrategy:         optionalStringValue(lookup, EnvRetainDefaultStrategy),
		RetainChunkBatchSize:          intFromEnv(lookup, EnvRetainChunkBatchSize, DefaultRetainChunkBatchSize),
		RetainMaxAttachments:          intFromEnv(lookup, EnvRetainMaxAttachmentsPerChunk, DefaultRetainMaxAttachmentsPerChunk),
		ConsolidationLLMBatchSize:     intFromEnv(lookup, EnvConsolidationLLMBatchSize, DefaultConsolidationLLMBatchSize),
		ConsolidationLLMParallelism:   intFromEnv(lookup, EnvConsolidationLLMParallelism, DefaultConsolidationLLMParallelism),
		ConsolidationMaxPerRound:      intFromEnv(lookup, EnvConsolidationMaxMemoriesPerRound, DefaultConsolidationMaxMemoriesPerRound),
		ConsolidationSourceFactTokens: intFromEnv(lookup, EnvConsolidationSourceFactsMaxTokens, DefaultConsolidationSourceFactsMaxTokens),
		ConsolidationSourceFactTokensPerObservation: intFromEnv(lookup, EnvConsolidationSourceFactsMaxTokensPerObservation,
			DefaultConsolidationSourceFactsMaxTokensPerObservation),
		ObservationsMission:          optionalStringValue(lookup, EnvObservationsMission),
		MaxObservationsPerScope:      intFromEnvAllowNegative(lookup, EnvMaxObservationsPerScope, -1),
		ObservationScopeLimits:       jsonOnceFromEnv(lookup, EnvObservationScopeLimits),
		ConsolidationStrategies:      jsonOnceFromEnv(lookup, EnvConsolidationStrategies),
		MentalModelMinRefreshSeconds: intFromEnv(lookup, EnvMentalModelMinRefreshIntervalSeconds, 0),
		KnowledgePageDefaultTrigger:  jsonOnceFromEnv(lookup, EnvKnowledgePageDefaultTrigger),
		LLMGeminiSafetySettings:      jsonValueFromEnv(lookup, EnvLLMGeminiSafetySettings),
		ReflectMission:               optionalStringValue(lookup, EnvReflectMission),
		ReflectSourceFactsMaxTokens:  intFromEnvAllowNegative(lookup, EnvReflectSourceFactsMaxTokens, DefaultReflectSourceFactsMaxTokens),
		ReflectDefaultOptions:        jsonObjectFromEnv(lookup, EnvReflectDefaultOptions),
		RecallIncludeChunks:          boolFromEnv(lookup, EnvRecallIncludeChunks, true),
		RecallPreferObservations:     boolFromEnv(lookup, EnvRecallPreferObservations, false),
		DefaultObservationScopes:     observationScopesFromEnv(lookup),
		RecallMaxTokens:              intFromEnv(lookup, EnvRecallMaxTokens, DefaultRecallMaxTokens),
		RecallChunksMaxTokens:        intFromEnv(lookup, EnvRecallChunksMaxTokens, DefaultRecallChunksMaxTokens),
		RecallBudgetFunction:         budgetFunction(lookup),
		RecallBudgetFixedLow:         intFromEnv(lookup, EnvRecallBudgetFixedLow, DefaultRecallBudgetFixedLow),
		RecallBudgetFixedMid:         intFromEnv(lookup, EnvRecallBudgetFixedMid, DefaultRecallBudgetFixedMid),
		RecallBudgetFixedHigh:        intFromEnv(lookup, EnvRecallBudgetFixedHigh, DefaultRecallBudgetFixedHigh),
		RecallBudgetAdaptiveLow:      floatFromEnv(lookup, EnvRecallBudgetAdaptiveLow, DefaultRecallBudgetAdaptiveLow),
		RecallBudgetAdaptiveMid:      floatFromEnv(lookup, EnvRecallBudgetAdaptiveMid, DefaultRecallBudgetAdaptiveMid),
		RecallBudgetAdaptiveHigh:     floatFromEnv(lookup, EnvRecallBudgetAdaptiveHigh, DefaultRecallBudgetAdaptiveHigh),
		RecallBudgetMin:              intFromEnv(lookup, EnvRecallBudgetMin, DefaultRecallBudgetMin),
		RecallBudgetMax:              intFromEnv(lookup, EnvRecallBudgetMax, DefaultRecallBudgetMax),
		DispositionSkepticism:        optionalIntValue(lookup, EnvDispositionSkepticism),
		DispositionLiteralism:        optionalIntValue(lookup, EnvDispositionLiteralism),
		DispositionEmpathy:           optionalIntValue(lookup, EnvDispositionEmpathy),
	}
}

// budgetFunction reads the budget strategy. Only the fixed function is
// implemented, and the reference's adaptive one is refused by the key table, so
// a value read here is always the fixed one.
func budgetFunction(lookup func(string) (string, bool)) string {
	value := strings.ToLower(strings.TrimSpace(stringFromEnv(lookup, EnvRecallBudgetFunction)))
	if value == "" {
		return DefaultRecallBudgetFunction
	}
	return value
}

// resolveRecallTuning reads the retrieval pipeline's process tuning.
func resolveRecallTuning(lookup func(string) (string, bool)) RecallTuning {
	return RecallTuning{
		GraphRetriever:                graphRetriever(lookup),
		GraphSeedMinSimilarity:        floatFromEnv(lookup, EnvGraphSeedMinSimilarity, DefaultGraphSeedMinSimilarity),
		SemanticMinSimilarity:         floatFromEnv(lookup, EnvSemanticMinSimilarity, DefaultSemanticMinSimilarity),
		TemporalSemanticMinSimilarity: floatFromEnv(lookup, EnvTemporalSemanticMinSimilarity, DefaultTemporalSemanticMinSimilarity),
		SemanticLinkMinSimilarity:     floatFromEnv(lookup, EnvSemanticLinkMinSimilarity, DefaultSemanticLinkMinSimilarity),
		BM25MinScore:                  floatFromEnv(lookup, EnvBM25MinScore, DefaultBM25MinScore),
		BM25MaxQueryTerms:             intFromEnv(lookup, EnvBM25MaxQueryTerms, DefaultBM25MaxQueryTerms),
		BM25SelectiveTerms:            boolFromEnv(lookup, EnvBM25SelectiveTerms, true),
		LinkExpansionPerEntityLimit:   intFromEnv(lookup, EnvLinkExpansionPerEntityLimit, DefaultLinkExpansionPerEntityLimit),
		LinkExpansionTimeout:          secondsDurationFromEnv(lookup, EnvLinkExpansionTimeout, DefaultLinkExpansionTimeoutS),
		DiagnosticPhases:              boolFromEnv(lookup, EnvRecallDiagnosticPhases, true),
		PhaseSampleEvery:              intFromEnv(lookup, EnvRecallPhaseSampleEvery, 1),
		MaxQueryTokens:                intFromEnv(lookup, EnvRecallMaxQueryTokens, DefaultRecallMaxQueryTokens),
		MaxConcurrent:                 intFromEnv(lookup, EnvRecallMaxConcurrent, DefaultRecallMaxConcurrent),
		ConnectionBudget:              intFromEnv(lookup, EnvRecallConnectionBudget, DefaultRecallConnectionBudget),
	}
}

// graphRetriever reads the graph arm's implementation selector. Only link
// expansion is implemented, and the key table refuses the others, so a value
// read here always names it.
func graphRetriever(lookup func(string) (string, bool)) string {
	value := strings.TrimSpace(stringFromEnv(lookup, EnvGraphRetriever))
	if value == "" {
		return DefaultGraphRetriever
	}
	return value
}

// resolveRetainTuning reads the write path's process tuning.
func resolveRetainTuning(lookup func(string) (string, bool)) RetainTuning {
	return RetainTuning{
		MaxCompletionTokens:        intFromEnv(lookup, EnvRetainMaxCompletionTokens, DefaultRetainMaxCompletionTokens),
		ExtractCausalLinks:         boolFromEnv(lookup, EnvRetainExtractCausalLinks, true),
		SubBatchTokens:             intFromEnv(lookup, EnvRetainBatchTokens, DefaultRetainSubBatchTokens),
		EntityLookup:               entityLookup(lookup),
		EntityResolutionBatchSize:  intFromEnv(lookup, EnvRetainEntityResolutionBatchSize, DefaultEntityResolutionBatchSize),
		EntityResolutionCandidates: intFromEnv(lookup, EnvRetainEntityResolutionMaxCandidates, DefaultEntityResolutionMaxCandidates),
		MaxConcurrent:              intFromEnv(lookup, EnvRetainMaxConcurrent, DefaultRetainMaxConcurrent),
		SubBatchConcurrency:        intFromEnv(lookup, EnvRetainSubbatchConcurrency, DefaultRetainSubBatchConcurrency),
		StoreMaxConcurrent:         intFromEnv(lookup, EnvRetainStoreMaxConcurrent, DefaultRetainStoreMaxConcurrent),
		MemoryBudgetMB:             intFromEnv(lookup, EnvRetainMemoryBudgetMB, DefaultRetainMemoryBudgetMB),
		BatchDocumentWrites:        boolFromEnv(lookup, EnvRetainBatchDocumentWrites, false),
		FailOnExtractionErrors:     boolFromEnv(lookup, EnvFailOnExtractionErrors, false),
	}
}

// entityLookup reads the entity-lookup mode. Only the trigram mode is
// implemented, and the key table refuses the others, so a value read here
// always names it; an unset key resolves to the reference's default.
func entityLookup(lookup func(string) (string, bool)) string {
	value := strings.TrimSpace(stringFromEnv(lookup, EnvRetainEntityLookup))
	if value == "" {
		return DefaultRetainEntityLookup
	}
	return value
}

// resolveReflectTuning reads the reflection loop's process tuning.
func resolveReflectTuning(lookup func(string) (string, bool)) ReflectTuning {
	return ReflectTuning{
		MaxIterations:       intFromEnv(lookup, EnvReflectMaxIterations, DefaultReflectMaxIterations),
		PromptCacheEnabled:  boolFromEnv(lookup, EnvReflectPromptCacheEnabled, true),
		MaxContextTokens:    intFromEnv(lookup, EnvReflectMaxContextTokens, DefaultReflectMaxContextTokens),
		MaxCompletionTokens: optionalIntValue(lookup, EnvReflectMaxCompletionTokens),
	}
}

// resolveRecallBudget reads the consolidation recall budget level. Unset means
// the reference's default "low", not an empty level: the reference resolves the
// same key to "low" when the environment does not name one.
func resolveRecallBudget(lookup func(string) (string, bool)) string {
	budget := strings.ToLower(strings.TrimSpace(stringFromEnv(lookup, EnvConsolidationRecallBudget)))
	if budget == "" {
		return DefaultConsolidationRecallBudget
	}
	return budget
}

// resolveConsolidationTuning reads the consolidation subsystem's process
// tuning.
func resolveConsolidationTuning(lookup func(string) (string, bool)) ConsolidationTuning {
	return ConsolidationTuning{
		MemoryBatchSize:           intFromEnv(lookup, EnvConsolidationMemoryBatchSize, DefaultConsolidationMemoryBatchSize),
		MaxCompletionTokens:       optionalIntValue(lookup, EnvConsolidationMaxCompletionTokens),
		DedupThreshold:            floatFromEnv(lookup, EnvConsolidationDedupThreshold, DefaultConsolidationDedupThreshold),
		MaxAttempts:               intFromEnv(lookup, EnvConsolidationMaxAttempts, DefaultConsolidationMaxAttempts),
		MaxTokens:                 intFromEnv(lookup, EnvConsolidationMaxTokens, DefaultConsolidationMaxTokens),
		RecallBudget:              resolveRecallBudget(lookup),
		HistoryMaxEntries:         intFromEnv(lookup, EnvObservationHistoryMaxEntries, DefaultHistoryMaxEntries),
		BankInfoCacheMaxEntries:   intFromEnv(lookup, EnvBankInfoCacheMaxEntries, DefaultBankInfoCacheMaxEntries),
		BankStatsCacheMaxEntries:  intFromEnv(lookup, EnvBankStatsCacheMaxEntries, DefaultBankStatsCacheMaxEntries),
		MentalModelHistoryEntries: intFromEnv(lookup, EnvMentalModelHistoryMaxEntries, DefaultHistoryMaxEntries),
	}
}

// resolveWorkerTuning reads the queue scheduler's process tuning. The per-type
// slot reservations are resolved with the rest of the worker configuration (see
// resolveReservedSlots), because the consumer reads them from there.
func resolveWorkerTuning(lookup func(string) (string, bool)) WorkerTuning {
	return WorkerTuning{
		ConsolidationByBank: stringFromEnv(lookup, EnvWorkerConsolidationBankPriority),
		BackpressureDefer:   secondsDurationFromEnv(lookup, EnvBackpressureDeferSeconds, DefaultBackpressureDeferS),
		// The same key ProcessDefaults.MentalModelMinRefreshSeconds resolves:
		// the consumer cannot read the whole Config, so the worker-facing tuning
		// bundle carries the floor its defer check needs.
		MentalModelMinRefreshSeconds: intFromEnv(lookup, EnvMentalModelMinRefreshIntervalSeconds, 0),
	}
}

// resolveDBTuning reads the database session's process tuning.
func resolveDBTuning(lookup func(string) (string, bool)) DBTuning {
	return DBTuning{
		StatementTimeout:         secondsDurationFromEnv(lookup, EnvDBStatementTimeout, DefaultDBStatementTimeoutS),
		CommandTimeout:           secondsDurationFromEnv(lookup, EnvDBCommandTimeout, DefaultDBCommandTimeoutS),
		MaxParallelWorkersGather: optionalIntValue(lookup, EnvDBMaxParallelWorkersGather),
		SessionSetupOnAcquire:    boolFromEnv(lookup, EnvDBSessionSetupOnAcquire, true),
		MigrationDatabaseURL:     stringFromEnv(lookup, EnvMigrationDatabaseURL),
		AcquireTimeout:           secondsDurationFromEnv(lookup, EnvDBAcquireTimeout, DefaultDBAcquireTimeoutS),
		PoolSlowAcquireThreshold: floatSecondsFromEnv(lookup, EnvDBPoolSlowAcquireThreshold, DefaultDBPoolSlowAcquireS),
		AcquireWarnThreshold:     millisFromEnv(lookup, EnvDBAcquireWarnThresholdMS, DefaultDBAcquireWarn),
		PGFunctionSchema:         pgFunctionSchema(lookup),
		PGTokenizer:              stringFromEnv(lookup, EnvTextSearchPGTokenizer),
		ANNIterativeScan:         boolFromEnv(lookup, EnvANNIterativeScan, true),
		ANNMaxScanTuples:         intFromEnv(lookup, EnvANNMaxScanTuples, DefaultANNMaxScanTuples),
	}
}

// resolveMetricsTuning reads the observability surface's process tuning.
func resolveMetricsTuning(lookup func(string) (string, bool)) MetricsTuning {
	return MetricsTuning{
		IncludeBankID:     boolFromEnv(lookup, EnvMetricsIncludeBankID, false),
		BacklogEnabled:    boolFromEnv(lookup, EnvMetricsBacklogEnabled, false),
		IncludeTenant:     boolFromEnv(lookup, EnvMetricsIncludeTenant, false),
		WorkerLabel:       boolFromEnv(lookup, EnvMetricsWorkerLabel, false),
		LoopLagMetric:     boolFromEnv(lookup, EnvLoopLagMetric, false),
		LoopWatchdog:      boolFromEnv(lookup, EnvLoopWatchdogEnabled, true),
		LoopWatchdogStall: millisFromEnv(lookup, EnvLoopWatchdogStallThresholdMS, DefaultLoopWatchdogStall),
		LoopWatchdogPoll:  millisFromEnv(lookup, EnvLoopWatchdogPollIntervalMS, DefaultLoopWatchdogPoll),
	}
}

// resolveEntityTuning reads the entity resolver's process tuning.
func resolveEntityTuning(lookup func(string) (string, bool)) EntityTuning {
	return EntityTuning{
		TRGMSimilarityThreshold: floatFromEnv(lookup, EnvEntityTRGMSimilarityThreshold, DefaultEntityTRGMSimilarityThreshold),
		IntrabatchMerge:         floatFromEnv(lookup, EnvEntityIntrabatchMergeSimilarity, DefaultEntityIntrabatchMergeSimilarity),
		MergeMinSimilarity:      floatFromEnv(lookup, EnvEntityMergeMinSimilarity, DefaultEntityMergeMinSimilarity),
	}
}

// stringMapFromEnv reads a key whose value is a JSON object of strings, which is
// how the reference carries a header map through the environment.
func stringMapFromEnv(lookup func(string) (string, bool), key string) map[string]string {
	parsed := jsonObjectFromEnv(lookup, key)
	if parsed == nil {
		return nil
	}
	out := make(map[string]string, len(parsed))
	for name, value := range parsed {
		if text, ok := value.(string); ok {
			out[name] = text
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// jsonObjectFromEnv reads a key whose value is a JSON object. A value that is
// not a JSON object is refused by the key's own predicate rather than silently
// ignored, so the resolver only ever sees a well-formed object or nothing.
func jsonObjectFromEnv(lookup func(string) (string, bool), key string) map[string]any {
	raw, ok := lookup(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil || parsed == nil {
		return nil
	}
	return parsed
}

// validJSONObject reports whether a configured value is a JSON object, which is
// the shape the reference parses these keys as.
func validJSONObject(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return true
	}
	var parsed map[string]any
	return json.Unmarshal([]byte(trimmed), &parsed) == nil && parsed != nil
}

// pgFunctionSchema reads the ParadeDB function schema. The reference's default
// is the extension's own schema name, so an unset key resolves to it rather
// than to the empty string.
func pgFunctionSchema(lookup func(string) (string, bool)) string {
	value := strings.TrimSpace(stringFromEnv(lookup, EnvTextSearchPGFunctionSchema))
	if value == "" {
		return DefaultTextSearchPGFunctionSchema
	}
	return value
}

// databaseBackend reads the storage dialect. An unset key is the reference's
// PostgreSQL default.
func databaseBackend(lookup func(string) (string, bool)) string {
	value := strings.TrimSpace(stringFromEnv(lookup, EnvDatabaseBackend))
	if value == "" {
		return DefaultDatabaseBackend
	}
	return value
}

// resolveWebhookGlobal reads the server-level webhook target.
func resolveWebhookGlobal(lookup func(string) (string, bool)) WebhookGlobal {
	target := WebhookGlobal{
		URL:                strings.TrimSpace(stringFromEnv(lookup, EnvWebhookURL)),
		Secret:             stringFromEnv(lookup, EnvWebhookSecret),
		EventTypes:         stringListFromEnv(lookup, EnvWebhookEventTypes),
		ExposeResponseBody: boolFromEnv(lookup, EnvWebhookExposeResponseBody, false),
	}
	if len(target.EventTypes) == 0 {
		target.EventTypes = []string{DefaultWebhookEventTypes}
	}
	return target
}

// mcpEnabledToolsFromEnv reads the server-level MCP tool allowlist. An unset
// key means every tool; the reference's own spelling for "everything" is an
// unset key, and the literal "all" is accepted here as the same thing because
// the key's documented default is written that way.
func mcpEnabledToolsFromEnv(lookup func(string) (string, bool)) []string {
	raw, ok := lookup(EnvMCPEnabledTools)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(raw), "all") {
		return nil
	}
	return stringListFromEnv(lookup, EnvMCPEnabledTools)
}

// mcpConfigToolsFromEnv reads the MCP tool allowlist as the bank configuration
// surface reports it: the comma-separated list when the deployment set the key,
// and null when it did not. Unlike the server-level allowlist, the literal
// "all" is not folded away, because the reference's own configurable field
// holds the split list verbatim ("all" becomes ["all"]).
func mcpConfigToolsFromEnv(lookup func(string) (string, bool)) []string {
	raw, ok := lookup(EnvMCPEnabledTools)
	// The reference tests the raw value for truthiness: an unset or empty key
	// resolves to null, while a whitespace-only value yields an empty list.
	if !ok || raw == "" {
		return nil
	}
	tools := []string{}
	for _, tool := range strings.Split(raw, ",") {
		if tool = strings.TrimSpace(tool); tool != "" {
			tools = append(tools, tool)
		}
	}
	return tools
}

// jsonValueFromEnv parses a JSON-valued key, mirroring the reference's
// `json.loads(os.getenv(key, "null"))`: an unset key resolves to null, and a
// value that is not valid JSON also resolves to null rather than faulting the
// read.
func jsonValueFromEnv(lookup func(string) (string, bool), key string) any {
	raw, ok := lookup(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil
	}
	var parsed any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil
	}
	return parsed
}

// observationScopesFromEnv parses the default observation_scopes: a mode name
// ("combined", "per_tag", "all_combinations", "shared") as written, or a JSON
// scope list such as [["topic:*"]]. An unset, blank or unparseable value is null,
// which the reference resolves as the combined default.
func observationScopesFromEnv(lookup func(string) (string, bool)) any {
	raw, ok := lookup(EnvDefaultObservationScopes)
	if !ok {
		return nil
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if !strings.HasPrefix(raw, "[") {
		return raw
	}
	var parsed any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil
	}
	return parsed
}

// observationScopesModes are the mode names an observation_scopes value accepts
// (config.py OBSERVATION_SCOPES_MODES).
var observationScopesModes = []string{"combined", "per_tag", "all_combinations", "shared"}

// observationScopesEnvRefused reports whether the reference's environment
// parser refuses the value: the upstream parser validates at config
// construction, so an invalid mode, malformed JSON or a scope list whose
// members are not tag lists stops start-up instead of failing every later
// consolidation (_parse_observation_scopes_env + validate_observation_scopes,
// config.py:361-393). An unset-equivalent blank is not refused.
func observationScopesEnvRefused(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	var value any = raw
	if strings.HasPrefix(raw, "[") {
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			return true
		}
	}
	return !observationScopesValueValid(value)
}

// observationScopesValueValid reports whether the value is a mode name or a
// list of tag lists — the shapes validate_observation_scopes accepts.
func observationScopesValueValid(value any) bool {
	if mode, isString := value.(string); isString {
		for _, candidate := range observationScopesModes {
			if mode == candidate {
				return true
			}
		}
		return false
	}
	scopes, isList := value.([]any)
	if !isList {
		return false
	}
	for _, scope := range scopes {
		tags, scopeIsList := scope.([]any)
		if !scopeIsList {
			return false
		}
		for _, tag := range tags {
			if _, isString := tag.(string); !isString {
				return false
			}
		}
	}
	return true
}

// jsonOnceFromEnv parses a JSON-valued key whose reference resolution is
// `json.loads(env or "null") or DEFAULT` with a null default: the parsed
// literal, or null when the key is unset, unparseable, or falsey (0, false,
// "", [], {}) — the `or` fallback the reference applies to the parsed value.
func jsonOnceFromEnv(lookup func(string) (string, bool), key string) any {
	value := jsonValueFromEnv(lookup, key)
	if !jsonTruthy(value) {
		return nil
	}
	return value
}

// jsonTruthy reports whether a parsed JSON value is true in the sense the
// reference's `parsed or default` fallback tests.
func jsonTruthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case float64:
		return typed != 0
	case string:
		return typed != ""
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	}
	return true
}

// floatSecondsFromEnv reads a duration expressed in fractional seconds, which
// is how the reference spells a sub-second threshold (0.05).
func floatSecondsFromEnv(lookup func(string) (string, bool), key string, fallback time.Duration) time.Duration {
	raw, ok := lookup(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || value < 0 {
		return fallback
	}
	return time.Duration(value * float64(time.Second))
}

// millisFromEnv reads a duration expressed in milliseconds.
func millisFromEnv(lookup func(string) (string, bool), key string, fallback time.Duration) time.Duration {
	raw, ok := lookup(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 0 {
		return fallback
	}
	return time.Duration(value) * time.Millisecond
}

// optionalStringValue reads a string key that the reference leaves null when it
// is unset. The distinction matters: a bank's configuration read reports null
// rather than the empty string.
func optionalStringValue(lookup func(string) (string, bool), key string) *string {
	raw, ok := lookup(key)
	if !ok {
		return nil
	}
	// A key set to the empty string is still set, and reads as the empty string
	// rather than as null.
	value := strings.TrimSpace(raw)
	return &value
}

// optionalIntValue reads an integer key that the reference leaves null when it
// is unset.
func optionalIntValue(lookup func(string) (string, bool), key string) *int {
	raw, ok := lookup(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil
	}
	value := intFromString(raw)
	return &value
}

// intFromEnvAllowNegative reads an integer key whose negative values are
// meaningful: -1 means "unbounded" for the reference's observation and
// source-fact budgets, so it must survive resolution.
func intFromEnvAllowNegative(lookup func(string) (string, bool), key string, fallback int) int {
	raw, ok := lookup(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	return value
}

// floatFromEnv reads a decimal key, falling back on a blank or unparseable
// value.
func floatFromEnv(lookup func(string) (string, bool), key string, fallback float64) float64 {
	raw, ok := lookup(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return fallback
	}
	return value
}

// secondsDurationFromEnv reads a duration expressed in whole seconds, accepting
// zero (which several of the reference's keys use to mean "no limit").
func secondsDurationFromEnv(lookup func(string) (string, bool), key string, fallback time.Duration) time.Duration {
	raw, ok := lookup(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 0 {
		return fallback
	}
	return time.Duration(value) * time.Second
}
