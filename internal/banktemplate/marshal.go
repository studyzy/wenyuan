package banktemplate

import (
	"bytes"
	"encoding/json"
	"sort"
)

// templateBankFieldOrder is the order the reference's BankTemplateConfig
// declares its fields in. A Go map serialises in sorted key order, so a manifest
// whose bank section is a plain map renders `audit_log_enabled` first where the
// reference renders `reflect_mission` first: same values, different bytes. The
// contract is the rendered document, so the export emits the declaration order.
//
// The list mirrors BankTemplateConfig (api/http.py:3833-4070); a field it omits
// still serialises, after the known ones, so a stored key this list has not
// learned about is never silently dropped from the export.
var templateBankFieldOrder = []string{
	"reflect_mission",
	"retain_mission",
	"retain_extraction_mode",
	"retain_custom_instructions",
	"retain_chunk_size",
	"retain_structured_chunk_size",
	"enable_observations",
	"observations_mission",
	"enable_text_search",
	"enable_temporal_retrieval",
	"enable_graph_retrieval",
	"enable_reranking",
	"disposition_skepticism",
	"disposition_literalism",
	"disposition_empathy",
	"entity_labels",
	"entities_allow_free_form",
	"retain_default_strategy",
	"retain_strategies",
	"retain_chunk_batch_size",
	"retain_max_attachments_per_chunk",
	"mcp_enabled_tools",
	"consolidation_llm_batch_size",
	"consolidation_source_facts_max_tokens",
	"consolidation_source_facts_max_tokens_per_observation",
	"max_observations_per_scope",
	"observation_scope_limits",
	"consolidation_strategies",
	"reflect_source_facts_max_tokens",
	"knowledge_page_default_trigger",
	"reflect_default_options",
	"mental_model_min_refresh_interval_seconds",
	"llm_gemini_safety_settings",
	"recall_budget_function",
	"recall_budget_fixed_low",
	"recall_budget_fixed_mid",
	"recall_budget_fixed_high",
	"recall_budget_adaptive_low",
	"recall_budget_adaptive_mid",
	"recall_budget_adaptive_high",
	"recall_budget_min",
	"recall_budget_max",
	"audit_log_enabled",
	"store_document_text",
	"enable_auto_consolidation",
	"consolidation_max_memories_per_round",
	"consolidation_llm_parallelism",
	"recall_include_chunks",
	"recall_max_tokens",
	"recall_chunks_max_tokens",
	"memory_defense",
}

// MarshalJSON renders the manifest with the bank section in the reference's
// field order. Every other member keeps its own declaration order from the
// struct tags; only the map needs help, because encoding/json sorts map keys.
func (m Manifest) MarshalJSON() ([]byte, error) {
	var bank json.RawMessage
	if m.Bank != nil {
		encoded, err := orderedObject(m.Bank, templateBankFieldOrder)
		if err != nil {
			return nil, err
		}
		bank = encoded
	} else {
		bank = json.RawMessage("null")
	}
	type inner struct {
		Version      string          `json:"version"`
		Bank         json.RawMessage `json:"bank"`
		MentalModels []MentalModel   `json:"mental_models"`
		Directives   []Directive     `json:"directives"`
	}
	return json.Marshal(inner{
		Version: m.Version, Bank: bank,
		MentalModels: m.MentalModels, Directives: m.Directives,
	})
}

// orderedObject renders a decoded JSON object with the named keys first, in the
// given order, then any remaining keys sorted so the output is deterministic.
func orderedObject(values map[string]any, order []string) (json.RawMessage, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	written := make(map[string]struct{}, len(values))
	first := true
	write := func(key string) error {
		value, present := values[key]
		if !present {
			return nil
		}
		if !first {
			buf.WriteByte(',')
		}
		first = false
		encodedKey, err := json.Marshal(key)
		if err != nil {
			return err
		}
		buf.Write(encodedKey)
		buf.WriteByte(':')
		encodedValue, err := json.Marshal(value)
		if err != nil {
			return err
		}
		buf.Write(encodedValue)
		written[key] = struct{}{}
		return nil
	}
	for _, key := range order {
		if err := write(key); err != nil {
			return nil, err
		}
	}
	rest := make([]string, 0, len(values))
	for key := range values {
		if _, done := written[key]; !done {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	for _, key := range rest {
		if err := write(key); err != nil {
			return nil, err
		}
	}
	buf.WriteByte('}')
	return json.RawMessage(buf.Bytes()), nil
}
