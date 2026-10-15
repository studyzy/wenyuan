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
	return map[string]any{
		// MCP and audit.
		"mcp_enabled_tools": nil,
		"audit_log_enabled": cfg.AuditLogEnabled,
		// Persisting raw source text.
		"store_document_text": cfg.StoreDocumentText,
		// Retention and extraction.
		"retain_chunk_size":                                     3000,
		"retain_structured_chunk_size":                          nil,
		"retain_extraction_mode":                                retainExtractionMode,
		"retain_mission":                                        nil,
		"retain_custom_instructions":                            nil,
		"retain_default_strategy":                               nil,
		"retain_strategies":                                     nil,
		"retain_chunk_batch_size":                               100,
		"retain_max_attachments_per_chunk":                      8,
		"entity_labels":                                         nil,
		"entities_allow_free_form":                              true,
		"enable_text_search":                                    true,
		"enable_temporal_retrieval":                             true,
		"enable_graph_retrieval":                                true,
		"enable_reranking":                                      true,
		"enable_observations":                                   cfg.EnableObservations,
		"enable_auto_consolidation":                             cfg.EnableAutoConsolidation,
		"consolidation_llm_batch_size":                          8,
		"consolidation_llm_parallelism":                         4,
		"consolidation_max_memories_per_round":                  100,
		"consolidation_source_facts_max_tokens":                 4096,
		"consolidation_source_facts_max_tokens_per_observation": 256,
		"observations_mission":                                  nil,
		"max_observations_per_scope":                            -1,
		"observation_scope_limits":                              nil,
		"consolidation_strategies":                              nil,
		"mental_model_min_refresh_interval_seconds":             0,
		"knowledge_page_default_trigger":                        nil,
		"reflect_mission":                                       nil,
		"reflect_source_facts_max_tokens":                       -1,
		"reflect_default_options":                               nil,
		"recall_include_chunks":                                 true,
		"recall_max_tokens":                                     2048,
		"recall_chunks_max_tokens":                              1000,
		"recall_budget_function":                                "fixed",
		"recall_budget_fixed_low":                               100,
		"recall_budget_fixed_mid":                               300,
		"recall_budget_fixed_high":                              1000,
		"recall_budget_adaptive_low":                            0.025,
		"recall_budget_adaptive_mid":                            0.075,
		"recall_budget_adaptive_high":                           0.25,
		"recall_budget_min":                                     20,
		"recall_budget_max":                                     2000,
		"disposition_skepticism":                                nil,
		"disposition_literalism":                                nil,
		"disposition_empathy":                                   nil,
		"llm_gemini_safety_settings":                            nil,
		"memory_defense":                                        nil,
	}
}
