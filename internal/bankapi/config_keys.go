package bankapi

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/studyzy/wenyuan/internal/entitylabels"
	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/mentalmodels"
	"github.com/studyzy/wenyuan/internal/storage"
)

// configValueKind is the value type a configurable field accepts.
type configValueKind int

const (
	kindBool configValueKind = iota
	kindInt
	kindFloat
	kindString
	kindList
	kindObject
	// kindListOrObject is the widened shape entity_labels accepts: a bare list
	// of label groups or the {"attributes": [...]} envelope.
	kindListOrObject
)

// configurableFields maps every field the reference lets a bank override to the
// value type it accepts, in internal field form. A key that is not here is not
// configurable, so the write is refused the way the reference refuses an
// unknown field.
var configurableFields = map[string]configValueKind{
	"mcp_enabled_tools":                                     kindList,
	"audit_log_enabled":                                     kindBool,
	"store_document_text":                                   kindBool,
	"retain_chunk_size":                                     kindInt,
	"retain_structured_chunk_size":                          kindInt,
	"retain_extraction_mode":                                kindString,
	"retain_mission":                                        kindString,
	"retain_custom_instructions":                            kindString,
	"retain_default_strategy":                               kindString,
	"retain_strategies":                                     kindObject,
	"retain_chunk_batch_size":                               kindInt,
	"retain_max_attachments_per_chunk":                      kindInt,
	"entity_labels":                                         kindListOrObject,
	"entities_allow_free_form":                              kindBool,
	"enable_text_search":                                    kindBool,
	"enable_temporal_retrieval":                             kindBool,
	"enable_graph_retrieval":                                kindBool,
	"enable_reranking":                                      kindBool,
	"enable_observations":                                   kindBool,
	"enable_auto_consolidation":                             kindBool,
	"consolidation_llm_batch_size":                          kindInt,
	"consolidation_llm_parallelism":                         kindInt,
	"consolidation_max_memories_per_round":                  kindInt,
	"consolidation_source_facts_max_tokens":                 kindInt,
	"consolidation_source_facts_max_tokens_per_observation": kindInt,
	"observations_mission":                                  kindString,
	"max_observations_per_scope":                            kindInt,
	"observation_scope_limits":                              kindList,
	"consolidation_strategies":                              kindList,
	"mental_model_min_refresh_interval_seconds":             kindInt,
	"knowledge_page_default_trigger":                        kindObject,
	"reflect_mission":                                       kindString,
	"reflect_source_facts_max_tokens":                       kindInt,
	"reflect_default_options":                               kindObject,
	"recall_include_chunks":                                 kindBool,
	"recall_max_tokens":                                     kindInt,
	"recall_chunks_max_tokens":                              kindInt,
	"recall_budget_function":                                kindString,
	"recall_budget_fixed_low":                               kindInt,
	"recall_budget_fixed_mid":                               kindInt,
	"recall_budget_fixed_high":                              kindInt,
	"recall_budget_adaptive_low":                            kindFloat,
	"recall_budget_adaptive_mid":                            kindFloat,
	"recall_budget_adaptive_high":                           kindFloat,
	"recall_budget_min":                                     kindInt,
	"recall_budget_max":                                     kindInt,
	"disposition_skepticism":                                kindInt,
	"disposition_literalism":                                kindInt,
	"disposition_empathy":                                   kindInt,
	"llm_gemini_safety_settings":                            kindList,
	"memory_defense":                                        kindObject,
}

// dispositionFieldKeys maps the public config field names to the storage trait
// keys. The config route stores these fields as overrides in the bank's config
// column, the same as every other accepted key; the mapping is the one the
// profile/list overlay uses to read a trait out of the resolved config.
var dispositionFieldKeys = map[string]string{
	"disposition_skepticism": storage.DispositionKeySkepticism,
	"disposition_literalism": storage.DispositionKeyLiteralism,
	"disposition_empathy":    storage.DispositionKeyEmpathy,
}

// positiveIntFields are the size-like fields where a non-positive value would
// floor every chunk or disallow every attachment, so the reference rejects it
// at write time rather than storing a bank that cannot retain.
var positiveIntFields = map[string]struct{}{
	"retain_chunk_size":                {},
	"retain_structured_chunk_size":     {},
	"retain_max_attachments_per_chunk": {},
}

// configPatch converts a configuration override map into a storage patch. Keys
// may be given in field form or environment-variable form; only configurable
// fields may be overridden. It returns the reference's client error when a key
// is unknown or carries the wrong shape: the reference raises ValueError for
// these and answers 400, not 422.
//
// Every accepted key — the disposition traits and reflect_mission included —
// is an override in the bank's config column, which is the only place the
// reference's config endpoint writes and the only place it reads. The legacy
// disposition and mission columns belong to the profile paths.
func configPatch(updates map[string]any) (storage.BankPatch, *httpx.HTTPError) {
	// The policy check runs before everything else, matching the reference: an
	// invalid memory_defense is a 422 regardless of what else the update names.
	// It keys on the raw field name, as the reference's engine does: an
	// environment-variable-spelled key (HINDSIGHT_API_MEMORY_DEFENSE) skips the
	// policy check and is stored verbatim after only the base type check.
	for key, raw := range updates {
		if key == "memory_defense" && raw != nil {
			if err := validateMemoryDefense(raw); err != nil {
				return storage.BankPatch{}, err
			}
		}
	}

	patch := storage.BankPatch{}
	configValues := map[string]any{}

	// Normalize every key once: the credential and configurability checks work
	// on the whole update set, not the first offending key.
	normalizedKeys := make([]string, 0, len(updates))
	for key := range updates {
		normalizedKeys = append(normalizedKeys, normalizeConfigKey(key))
	}
	// Credentials (API keys, base URLs, service-account keys) are rejected first
	// with their own message, over every offending key at once, exactly as the
	// reference does before it checks configurability.
	if attempts := sortedIntersection(normalizedKeys, credentialConfigFields); len(attempts) > 0 {
		return storage.BankPatch{}, httpx.NewHTTPError(httpx.KindBadRequest,
			"Cannot set credential fields via API: "+pyStringList(attempts)+
				". Credentials (API keys, base URLs) must be set at server level only.")
	}
	invalid := sortedDifference(normalizedKeys, configurableFields)
	// A key that is a real server-level field has its own message, distinct from
	// an unknown one: "static" is a field the deployment knows but which cannot
	// vary per bank.
	if staticFields := sortedIntersection(invalid, staticConfigFields); len(staticFields) > 0 {
		return storage.BankPatch{}, httpx.NewHTTPError(httpx.KindBadRequest,
			"Cannot override static (server-level) fields: "+pyStringList(staticFields)+
				". Only configurable fields can be overridden per-bank. Configurable fields include: "+
				configFieldSample()+"... (total: "+fmt.Sprintf("%d", len(configurableFields))+" fields)")
	}
	if len(invalid) > 0 {
		return storage.BankPatch{}, httpx.NewHTTPError(httpx.KindBadRequest,
			"Unknown configuration fields: "+pyStringList(invalid)+
				". Valid configurable fields: "+configFieldSample()+"...")
	}

	keys := make([]string, 0, len(updates))
	for key := range updates {
		keys = append(keys, key)
	}
	// A deterministic order means a request with several bad keys reports the
	// same one every time, instead of whichever map iteration reached first.
	sort.Strings(keys)

	for _, key := range keys {
		normalized := normalizeConfigKey(key)
		raw := updates[key]
		kind := configurableFields[normalized]
		if raw == nil {
			// JSON null is the reference's "server default" tombstone: the value
			// is dropped at read, so the override is cleared. The store applies
			// the same clear by deleting the key.
			configValues[normalized] = nil
			continue
		}
		if _, isTrait := dispositionFieldKeys[normalized]; isTrait {
			value, ok := asInt(raw)
			if !ok || value < storage.DispositionTraitMin || value > storage.DispositionTraitMax {
				return storage.BankPatch{}, httpx.NewHTTPError(httpx.KindBadRequest,
					fmt.Sprintf("%s must be an integer between 1 and 5, got %s", normalized, describeValue(raw)))
			}
			configValues[normalized] = value
			continue
		}
		if !kindMatches(kind, raw) {
			return storage.BankPatch{}, httpx.NewHTTPError(httpx.KindBadRequest,
				fmt.Sprintf("%s must be %s, got %s", normalized, describeKind(kind), pythonTypeName(raw)))
		}
		if err := validateConfigValue(normalized, raw); err != nil {
			return storage.BankPatch{}, err
		}
		configValues[normalized] = raw
	}

	if err := validateRecallBudgetBounds(updates); err != nil {
		return storage.BankPatch{}, err
	}
	if len(configValues) > 0 {
		patch.Config = &configValues
	}
	return patch, nil
}

// validateConfigValue applies the field-specific checks the reference runs
// after the base type check: a positive segment size, and a structural check
// for the two fields whose shape decides whether the bank can ever extract.
func validateConfigValue(key string, raw any) *httpx.HTTPError {
	if strings.HasPrefix(key, "recall_budget_") {
		if err := validateRecallBudget(key, raw); err != nil {
			return err
		}
	}
	if _, ok := positiveIntFields[key]; ok {
		if value, ok := asInt(raw); !ok || value < 1 {
			return httpx.NewHTTPError(httpx.KindBadRequest,
				fmt.Sprintf("%s must be >= 1, got %s", key, numberLiteral(value, raw)))
		}
	}
	switch key {
	case "entity_labels":
		if _, err := entitylabels.Parse(raw); err != nil {
			return httpx.NewHTTPError(httpx.KindBadRequest, "Invalid entity_labels format: "+err.Error())
		}
	case "retain_strategies":
		if err := validateRetainStrategies(raw); err != nil {
			return err
		}
	case "consolidation_strategies":
		if err := validateConsolidationStrategies(raw); err != nil {
			return err
		}
	case "reflect_default_options":
		if err := validateReflectDefaultOptions(raw); err != nil {
			return err
		}
	case "knowledge_page_default_trigger":
		if err := validateKnowledgePageDefaultTrigger(raw); err != nil {
			return err
		}
	}
	return nil
}

// validateKnowledgePageDefaultTrigger rejects a bank default whose shape or
// members the page layer could not apply. It is checked at the write so a bad
// value cannot break every later page creation in the bank.
func validateKnowledgePageDefaultTrigger(raw any) *httpx.HTTPError {
	object, ok := raw.(map[string]any)
	if !ok {
		return httpx.NewHTTPError(httpx.KindBadRequest,
			fmt.Sprintf("knowledge_page_default_trigger must be an object, got %s", describeValue(raw)))
	}
	if failure := mentalmodels.ValidateTriggerObject(object); failure != nil {
		return httpx.NewHTTPError(httpx.KindBadRequest,
			"knowledge_page_default_trigger is invalid: "+failure.Error())
	}
	return nil
}

// memoryDefenseActions are the actions a memory_defense rule accepts.
var memoryDefenseActions = []string{"allow", "block", "redact"}

// validateMemoryDefense reproduces the reference's parse_policy failure as a
// 422, so a malformed policy (a missing/empty `on`, or an unknown action) is
// rejected with the same status and detail the reference reports.
func validateMemoryDefense(raw any) *httpx.HTTPError {
	object, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	rules, _ := object["rules"].([]any)
	for _, entry := range rules {
		item, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		on, isString := item["on"].(string)
		if !isString || on == "" {
			return httpx.NewHTTPError(httpx.KindUnprocessable,
				"invalid memory_defense policy: invalid on "+pyRepr(item["on"])+"; must be a non-empty string")
		}
		action, isString := item["action"].(string)
		if !isString || !containsString(memoryDefenseActions, action) {
			return httpx.NewHTTPError(httpx.KindUnprocessable,
				"invalid memory_defense policy: invalid action "+pyRepr(item["action"])+"; must be one of "+pyList(memoryDefenseActions))
		}
	}
	return nil
}

// pyRepr renders a JSON value the way Python's repr does, which the reference's
// error messages interpolate: a string is single-quoted, null is None.
func pyRepr(value any) string {
	switch typed := value.(type) {
	case nil:
		return "None"
	case string:
		return "'" + typed + "'"
	case bool:
		if typed {
			return "True"
		}
		return "False"
	case float64:
		if typed == float64(int(typed)) {
			return fmt.Sprintf("%d", int(typed))
		}
		return fmt.Sprintf("%v", typed)
	default:
		return fmt.Sprintf("%v", value)
	}
}

// numberLiteral renders a rejected numeric value the way the reference's
// f-string does: the integer as-is when it parsed, otherwise the raw JSON value.
func numberLiteral(value int, raw any) string {
	if _, ok := asInt(raw); ok {
		return fmt.Sprintf("%d", value)
	}
	return describeValue(raw)
}

// validateRecallBudgetBounds reproduces the reference's cross-field recall
// budget check, which only fires when the update names both bounds.
func validateRecallBudgetBounds(updates map[string]any) *httpx.HTTPError {
	normalized := make(map[string]any, len(updates))
	for key, value := range updates {
		normalized[normalizeConfigKey(key)] = value
	}
	rawMin, hasMin := normalized["recall_budget_min"]
	rawMax, hasMax := normalized["recall_budget_max"]
	if !hasMin || !hasMax || rawMin == nil || rawMax == nil {
		return nil
	}
	min, okMin := asIntStrict(rawMin)
	max, okMax := asIntStrict(rawMax)
	if okMin && okMax && min > max {
		return httpx.NewHTTPError(httpx.KindBadRequest,
			fmt.Sprintf("recall_budget_min (%d) must be <= recall_budget_max (%d)", min, max))
	}
	return nil
}

// containsString reports whether values holds want.
func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// pyList renders a string list the way Python's repr does.
func pyList(values []string) string {
	parts := make([]string, len(values))
	for i, value := range values {
		parts[i] = "'" + value + "'"
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// validateRecallBudget reproduces the reference's recall-budget checks, raising
// the same 400 messages. Only the fields the update names are checked.
func validateRecallBudget(key string, raw any) *httpx.HTTPError {
	switch key {
	case "recall_budget_function":
		value, ok := raw.(string)
		if !ok || !containsString([]string{"adaptive", "fixed"}, strings.ToLower(value)) {
			return httpx.NewHTTPError(httpx.KindBadRequest,
				"recall_budget_function must be one of ['adaptive', 'fixed'], got "+pyRepr(raw))
		}
	case "recall_budget_fixed_low", "recall_budget_fixed_mid", "recall_budget_fixed_high",
		"recall_budget_min", "recall_budget_max":
		value, ok := asIntStrict(raw)
		if !ok || value < 1 {
			return httpx.NewHTTPError(httpx.KindBadRequest,
				key+" must be a positive integer, got "+pyRepr(raw))
		}
	case "recall_budget_adaptive_low", "recall_budget_adaptive_mid", "recall_budget_adaptive_high":
		value, ok := asFloat(raw)
		if !ok || value <= 0 {
			return httpx.NewHTTPError(httpx.KindBadRequest,
				key+" must be a positive number, got "+pyRepr(raw))
		}
	}
	return nil
}

// asIntStrict reads an integer, rejecting a bool (which JSON decodes as bool)
// and a fractional number.
func asIntStrict(raw any) (int, bool) {
	if _, isBool := raw.(bool); isBool {
		return 0, false
	}
	value, ok := asInt(raw)
	if !ok {
		return 0, false
	}
	if number, isFloat := raw.(float64); isFloat && number != float64(int(number)) {
		return 0, false
	}
	return value, true
}

// asFloat reads a number, rejecting a bool.
func asFloat(raw any) (float64, bool) {
	if _, isBool := raw.(bool); isBool {
		return 0, false
	}
	switch typed := raw.(type) {
	case int:
		return float64(typed), true
	case float64:
		return typed, true
	case json.Number:
		value, err := typed.Float64()
		return value, err == nil
	}
	return 0, false
}

// validateRetainStrategies reproduces the reference's retain_strategies checks:
// an empty strategy name, a non-object strategy, or a wrong-shaped value inside
// one is a 400 naming the offending location.
func validateRetainStrategies(raw any) *httpx.HTTPError {
	object, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	for name, overrides := range object {
		if strings.TrimSpace(name) == "" {
			return httpx.NewHTTPError(httpx.KindBadRequest,
				"Strategy names must not be empty strings. Remove entries with empty names before saving.")
		}
		inner, ok := overrides.(map[string]any)
		if !ok {
			return httpx.NewHTTPError(httpx.KindBadRequest,
				"Invalid retain strategy "+pyRepr(name)+": must be an object")
		}
		for innerKey, innerValue := range inner {
			normalized := normalizeConfigKey(innerKey)
			kind, known := configurableFields[normalized]
			if !known {
				continue
			}
			if innerValue != nil && !kindMatches(kind, innerValue) {
				return httpx.NewHTTPError(httpx.KindBadRequest,
					"Invalid retain strategy "+pyRepr(name)+": "+normalized+" must be "+describeKind(kind)+", got "+pythonTypeName(innerValue))
			}
		}
	}
	return nil
}

// consolidationStrategyKeys are the fields a consolidation-strategy entry may
// carry. A key outside the set is the "scope" for "scopes" typo that would
// otherwise be stored and silently ignored for the life of the bank.
var consolidationStrategyKeys = map[string]struct{}{
	"scopes":                                {},
	"observations_mission":                  {},
	"max_observations_per_scope":            {},
	"consolidation_source_facts_max_tokens": {},
	"consolidation_source_facts_max_tokens_per_observation": {},
}

// consolidationScopeRuleKeys are the fields one scope rule may carry.
var consolidationScopeRuleKeys = map[string]struct{}{"tags": {}, "tags_match": {}}

// consolidationTagsMatchValues are the accepted tags_match modes.
var consolidationTagsMatchValues = map[string]struct{}{"all": {}, "exact": {}}

// validateConsolidationStrategies rejects a value whose shape cannot work.
//
// Shape only: an incomplete draft — a rule with no tags yet, a strategy with no
// settings — is accepted, because the control plane saves strategies as typed
// and consolidation ignores the unusable ones. What is rejected is what could
// only be a mistake: an entry that is not an object, an unknown key, a tag list
// that is not a list of strings, an unknown tags_match.
func validateConsolidationStrategies(raw any) *httpx.HTTPError {
	list, ok := raw.([]any)
	if !ok {
		return httpx.NewHTTPError(httpx.KindBadRequest,
			fmt.Sprintf("consolidation_strategies must be a list, got %s", describeValue(raw)))
	}
	for index, entry := range list {
		object, ok := entry.(map[string]any)
		if !ok {
			return invalidStrategy(index, "entry", "Input should be a valid dictionary")
		}
		for key := range object {
			if _, known := consolidationStrategyKeys[key]; !known {
				return invalidStrategy(index, key, "Extra inputs are not permitted")
			}
		}
		scopesRaw, present := object["scopes"]
		if !present || scopesRaw == nil {
			continue
		}
		rules, ok := scopesRaw.([]any)
		if !ok {
			return invalidStrategy(index, "scopes", "Input should be a valid list")
		}
		for ruleIndex, ruleRaw := range rules {
			rule, ok := ruleRaw.(map[string]any)
			if !ok {
				return invalidStrategy(index, fmt.Sprintf("scopes.%d", ruleIndex), "Input should be a valid dictionary")
			}
			for key := range rule {
				if _, known := consolidationScopeRuleKeys[key]; !known {
					return invalidStrategy(index, fmt.Sprintf("scopes.%d.%s", ruleIndex, key), "Extra inputs are not permitted")
				}
			}
			if tagsRaw, present := rule["tags"]; present && tagsRaw != nil {
				tags, ok := tagsRaw.([]any)
				if !ok || !allStrings(tags) {
					return invalidStrategy(index, fmt.Sprintf("scopes.%d.tags", ruleIndex), "Input should be a valid list")
				}
			}
			if mode, present := rule["tags_match"]; present && mode != nil {
				text, ok := mode.(string)
				if !ok {
					return invalidStrategy(index, fmt.Sprintf("scopes.%d.tags_match", ruleIndex), "Input should be a valid string")
				}
				if _, known := consolidationTagsMatchValues[text]; !known {
					return invalidStrategy(index, fmt.Sprintf("scopes.%d.tags_match", ruleIndex), "must be one of all, exact")
				}
			}
		}
	}
	return nil
}

// invalidStrategy renders the reference's consolidation-strategy validation
// message, which names the offending location and reason.
func invalidStrategy(index int, location, reason string) *httpx.HTTPError {
	return httpx.NewHTTPError(httpx.KindBadRequest,
		fmt.Sprintf("Invalid consolidation strategy at index %d: %s: %s", index, location, reason))
}

// reflectDefaultOptionKeys are the fields reflect_default_options may carry,
// matching the reflect request model the value is validated against.
var reflectDefaultOptionKeys = map[string]struct{}{
	"reflect_search_observations_max_tokens":       {},
	"reflect_search_observations_include_entities": {},
}

// validateReflectDefaultOptions rejects unknown option fields and values the
// reflect request model itself would refuse. A typo'd key stored silently would
// quietly do nothing on every reflect afterwards, with nothing to explain why.
func validateReflectDefaultOptions(raw any) *httpx.HTTPError {
	object, ok := raw.(map[string]any)
	if !ok {
		return httpx.NewHTTPError(httpx.KindBadRequest, "reflect_default_options must be an object")
	}
	unknown := make([]string, 0, len(object))
	for key := range object {
		if _, known := reflectDefaultOptionKeys[key]; !known {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return httpx.NewHTTPError(httpx.KindBadRequest,
			"reflect_default_options has unknown fields: "+strings.Join(unknown, ", "))
	}
	if value, present := object["reflect_search_observations_max_tokens"]; present && value != nil {
		parsed, ok := asInt(value)
		if !ok || parsed < 1 {
			return httpx.NewHTTPError(httpx.KindBadRequest,
				"Invalid reflect_default_options: reflect_search_observations_max_tokens: Input should be greater than or equal to 1")
		}
	}
	if value, present := object["reflect_search_observations_include_entities"]; present && value != nil {
		if _, ok := value.(bool); !ok {
			return httpx.NewHTTPError(httpx.KindBadRequest,
				"Invalid reflect_default_options: reflect_search_observations_include_entities: Input should be a valid boolean")
		}
	}
	return nil
}

// allStrings reports whether every element of a decoded list is a string.
func allStrings(values []any) bool {
	for _, value := range values {
		if _, ok := value.(string); !ok {
			return false
		}
	}
	return true
}

// kindMatches reports whether a JSON-decoded value satisfies a field's declared
// type contract.
func kindMatches(kind configValueKind, raw any) bool {
	switch kind {
	case kindBool:
		_, ok := raw.(bool)
		return ok
	case kindInt:
		_, ok := asInt(raw)
		return ok
	case kindFloat:
		switch raw.(type) {
		case float64, int, json.Number:
			return true
		default:
			return false
		}
	case kindString:
		_, ok := raw.(string)
		return ok
	case kindList:
		_, ok := raw.([]any)
		return ok
	case kindObject:
		_, ok := raw.(map[string]any)
		return ok
	case kindListOrObject:
		if _, ok := raw.([]any); ok {
			return true
		}
		_, ok := raw.(map[string]any)
		return ok
	default:
		return false
	}
}

// normalizeConfigKey converts an environment-variable-form key to its internal
// field form, leaving an already-internal key unchanged. It mirrors the
// reference: strip the HINDSIGHT_API_ prefix, then lowercase.
func normalizeConfigKey(key string) string {
	return strings.ToLower(strings.TrimPrefix(key, "HINDSIGHT_API_"))
}

// configFieldSample renders a sample of the configurable fields the way the
// reference does: the first ten, sorted, as a Python list repr. The reference
// takes the sample from an unordered set, so its particular ten vary per
// process; the wording and quoting are what the message contract pins.
func configFieldSample() string {
	sample := make([]string, 0, len(configurableFields))
	for name := range configurableFields {
		sample = append(sample, name)
	}
	sort.Strings(sample)
	if len(sample) > 10 {
		sample = sample[:10]
	}
	return pyStringList(sample)
}

// pyStringList renders a string slice the way Python's repr does: single-quoted
// members, comma-separated, wrapped in brackets.
func pyStringList(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = "'" + value + "'"
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// sortedIntersection returns the sorted members of names that are in set.
func sortedIntersection(names []string, set map[string]struct{}) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, name := range names {
		if _, ok := set[name]; !ok {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// sortedDifference returns the sorted distinct members of names that are not
// keys of fields.
func sortedDifference(names []string, fields map[string]configValueKind) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, name := range names {
		if _, ok := fields[name]; ok {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// describeKind names a value kind the way the JSON payload reads.
func describeKind(kind configValueKind) string {
	switch kind {
	case kindBool:
		return "a boolean"
	case kindInt:
		return "an integer"
	case kindFloat:
		return "a number"
	case kindString:
		return "a string"
	case kindList:
		return "a list"
	case kindObject:
		return "an object"
	case kindListOrObject:
		return "a list or an object"
	default:
		return "the declared type"
	}
}

// pythonTypeName names a JSON value's type the way Python's type(value).__name__
// does, which the reference interpolates into a wrong-typed-value message.
func pythonTypeName(raw any) string {
	switch raw.(type) {
	case bool:
		return "bool"
	case float64:
		return "float"
	case json.Number:
		return "int"
	case int, int64:
		return "int"
	case string:
		return "str"
	case []any:
		return "list"
	case map[string]any:
		return "dict"
	case nil:
		return "NoneType"
	default:
		return fmt.Sprintf("%T", raw)
	}
}

// describeValue names a JSON value's runtime type.
func describeValue(raw any) string {
	switch raw.(type) {
	case bool:
		return "bool"
	case float64, int, json.Number:
		return "number"
	case string:
		return "string"
	case []any:
		return "list"
	case map[string]any:
		return "object"
	case nil:
		return "null"
	default:
		return fmt.Sprintf("%T", raw)
	}
}

func asInt(raw any) (int, bool) {
	switch value := raw.(type) {
	case int:
		return value, true
	case float64:
		return int(value), float64(int(value)) == value
	case json.Number:
		parsed, err := value.Int64()
		if err != nil {
			return 0, false
		}
		return int(parsed), true
	default:
		return 0, false
	}
}
