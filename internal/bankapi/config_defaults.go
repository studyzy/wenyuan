package bankapi

import "github.com/studyzy/wenyuan/internal/config"

// PublicConfigDefaults returns the process-level values a bank's configuration
// read layers its overrides on top of.
//
// The reference resolves a bank's configuration from the process environment,
// then the tenant overrides, then the bank's own overrides, and returns every
// configurable field — a bank that overrides nothing still reads the process
// value for each one, and only the disposition traits and the reflect mission
// default to null. This build keeps the same shape: the map names every public
// configurable field, so a caller reading a field it did not set gets the
// effective value rather than a missing key.
//
// The handful of fields this build honours from its own process configuration
// are read from cfg; the rest carry the reference's shipped defaults, which is
// what an unset environment resolves to on both sides.
func PublicConfigDefaults(cfg config.Config) map[string]any {
	retainExtractionMode := cfg.RetainExtractionMode
	if retainExtractionMode == "" {
		retainExtractionMode = config.DefaultRetainExtractionMode
	}
	// Every value below is the process layer of a bank's configuration: a bank
	// that overrides nothing reads the deployment's resolved value, which is
	// what the reference does with a copied .env. The pointer-typed entries stay
	// null when the deployment set nothing, because null is a distinct answer
	// from the empty string.
	p := cfg.ProcessDefaults
	return map[string]any{
		// MCP and audit.
		"mcp_enabled_tools": p.MCPEnabledTools,
		"audit_log_enabled": cfg.AuditLogEnabled,
		// Persisting raw source text.
		"store_document_text": cfg.StoreDocumentText,
		// Retention and extraction.
		"retain_chunk_size":                                     p.RetainChunkSize,
		"retain_structured_chunk_size":                          optionalInt(p.RetainStructuredChunkSize),
		"retain_extraction_mode":                                retainExtractionMode,
		"retain_mission":                                        optionalString(p.RetainMission),
		"retain_custom_instructions":                            optionalString(p.RetainCustomInstructions),
		"retain_default_strategy":                               optionalString(p.RetainDefaultStrategy),
		"retain_strategies":                                     optionalString(p.RetainStrategies),
		"retain_chunk_batch_size":                               p.RetainChunkBatchSize,
		"retain_max_attachments_per_chunk":                      p.RetainMaxAttachments,
		"entity_labels":                                         nil,
		"entities_allow_free_form":                              true,
		"enable_text_search":                                    p.EnableTextSearch,
		"enable_temporal_retrieval":                             p.EnableTemporalRetrieval,
		"enable_graph_retrieval":                                p.EnableGraphRetrieval,
		"enable_reranking":                                      p.EnableReranking,
		"enable_observations":                                   cfg.EnableObservations,
		"enable_auto_consolidation":                             cfg.EnableAutoConsolidation,
		"consolidation_llm_batch_size":                          p.ConsolidationLLMBatchSize,
		"consolidation_llm_parallelism":                         p.ConsolidationLLMParallelism,
		"consolidation_max_memories_per_round":                  p.ConsolidationMaxPerRound,
		"consolidation_source_facts_max_tokens":                 p.ConsolidationSourceFactTokens,
		"consolidation_source_facts_max_tokens_per_observation": p.ConsolidationSourceFactTokensPerObservation,
		"observations_mission":                                  optionalString(p.ObservationsMission),
		"max_observations_per_scope":                            p.MaxObservationsPerScope,
		"observation_scope_limits":                              p.ObservationScopeLimits,
		"consolidation_strategies":                              p.ConsolidationStrategies,
		"mental_model_min_refresh_interval_seconds":             p.MentalModelMinRefreshSeconds,
		"knowledge_page_default_trigger":                        p.KnowledgePageDefaultTrigger,
		"reflect_mission":                                       optionalString(p.ReflectMission),
		"reflect_source_facts_max_tokens":                       p.ReflectSourceFactsMaxTokens,
		"reflect_default_options":                               p.ReflectDefaultOptions,
		"recall_include_chunks":                                 p.RecallIncludeChunks,
		"recall_prefer_observations":                            p.RecallPreferObservations,
		"default_observation_scopes":                            p.DefaultObservationScopes,
		"recall_max_tokens":                                     p.RecallMaxTokens,
		"recall_chunks_max_tokens":                              p.RecallChunksMaxTokens,
		"recall_budget_function":                                p.RecallBudgetFunction,
		"recall_budget_fixed_low":                               p.RecallBudgetFixedLow,
		"recall_budget_fixed_mid":                               p.RecallBudgetFixedMid,
		"recall_budget_fixed_high":                              p.RecallBudgetFixedHigh,
		"recall_budget_adaptive_low":                            p.RecallBudgetAdaptiveLow,
		"recall_budget_adaptive_mid":                            p.RecallBudgetAdaptiveMid,
		"recall_budget_adaptive_high":                           p.RecallBudgetAdaptiveHigh,
		"recall_budget_min":                                     p.RecallBudgetMin,
		"recall_budget_max":                                     p.RecallBudgetMax,
		"disposition_skepticism":                                optionalInt(p.DispositionSkepticism),
		"disposition_literalism":                                optionalInt(p.DispositionLiteralism),
		"disposition_empathy":                                   optionalInt(p.DispositionEmpathy),
		"llm_gemini_safety_settings":                            p.LLMGeminiSafetySettings,
		"memory_defense":                                        nil,
	}
}

// optionalString renders a nullable string the way a bank's configuration read
// reports it: the value when the deployment set one, and null when it did not.
func optionalString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

// optionalInt renders a nullable integer the same way.
func optionalInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}
