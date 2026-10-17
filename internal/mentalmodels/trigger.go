package mentalmodels

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"github.com/robfig/cron/v3"

	"github.com/studyzy/wenyuan/internal/httpx"
)

// The trigger members the reference accepts, spelled out so validation rejects
// exactly the values the reference's Literals reject.
var (
	triggerBudgets   = []string{"low", "mid", "high"}
	triggerModes     = []string{"full", "delta"}
	triggerTagsMatch = []string{"any", "all", "any_strict", "all_strict", "exact"}
	triggerFactTypes = []string{"world", "experience", "observation"}
)

// triggerDefaultMap is the reference's MentalModelTrigger at its defaults: the
// four non-nullable members carry their declared default, the rest null.
var triggerDefaultMap = map[string]any{
	"budget":                                 nil,
	"mode":                                   "full",
	"refresh_after_consolidation":            false,
	"refresh_cron":                           nil,
	"min_refresh_interval_seconds":           nil,
	"fact_types":                             nil,
	"exclude_mental_models":                  false,
	"exclude_mental_model_ids":               nil,
	"tags_match":                             nil,
	"tag_groups":                             nil,
	"include_chunks":                         nil,
	"recall_max_tokens":                      nil,
	"recall_chunks_max_tokens":               nil,
	"reflect_search_observations_max_tokens": nil,
	"reflect_search_observations_include_entities": nil,
	"response_schema": nil,
	"keep_trace":      false,
}

// triggerKnownMember reports whether a member is part of the reference's
// trigger model, so a projection keeps exactly that set.
func triggerKnownMember(name string) bool {
	_, ok := triggerDefaultMap[name]
	return ok
}

// NormalizeTrigger projects a stored trigger through the reference's
// MentalModelTrigger model for callers outside this package (the knowledge-base
// routes project the same stored trigger). It fills every unset member with its
// default, drops members the model does not declare, and leaves nil nil.
func NormalizeTrigger(stored map[string]any) map[string]any { return normalizeTrigger(stored) }

// normalizeTrigger projects a stored trigger through the reference's model: it
// fills every unset member with its default and drops any member the model does
// not declare. A nil stored trigger stays nil (the reference's null).
func normalizeTrigger(stored map[string]any) map[string]any {
	if stored == nil {
		return nil
	}
	out := cloneTrigger(triggerDefaultMap)
	for key, value := range stored {
		if triggerKnownMember(key) {
			out[key] = value
		}
	}
	return out
}

// triggerFailure wraps one or more structured items for a trigger member.
func triggerFailure(items ...httpx.ValidationError) *httpx.ValidationFailure {
	return &httpx.ValidationFailure{Errors: items}
}

// triggerLoc is the field path of one trigger member.
func triggerLoc(member string) []string { return []string{"body", "trigger", member} }

// parseTrigger decodes and validates a trigger body. It returns the normalized
// map to persist. When create is true the absent members are filled with the
// reference's defaults; when false (a patch) only the members the client sent
// are returned, so the caller can overlay them on the model's current trigger.
//
// A nil raw (member absent) yields the full default trigger for a create and
// nil for a patch, which leaves the model's current trigger untouched.
func parseTrigger(raw json.RawMessage, create bool) (map[string]any, *httpx.ValidationFailure) {
	if len(raw) == 0 || string(raw) == "null" {
		if create {
			return cloneTrigger(triggerDefaultMap), nil
		}
		return nil, nil
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, triggerFailure(httpx.TypeMismatch("dict_type", []string{"body", "trigger"}, nil))
	}
	if failure := validateTrigger(object); failure != nil {
		return nil, failure
	}
	// The reference validates the member set through its MentalModelTrigger
	// model, so unknown members are dropped and every declared member is kept —
	// including one the client set to null, which clears it on a patch.
	supplied := make(map[string]any, len(object))
	for key, value := range object {
		if triggerKnownMember(key) {
			supplied[key] = value
		}
	}
	if !create {
		return supplied, nil
	}
	merged := cloneTrigger(triggerDefaultMap)
	for key, value := range supplied {
		merged[key] = value
	}
	return merged, nil
}

// validateTrigger checks each member against the reference's accepted values.
func validateTrigger(object map[string]any) *httpx.ValidationFailure {
	// The reference's non-nullable members reject an explicit null the way any
	// wrong-typed value is rejected: mode is a Literal, the rest are bools.
	for _, member := range []string{"refresh_after_consolidation", "exclude_mental_models", "keep_trace"} {
		if raw, ok := object[member]; ok {
			if _, isBool := raw.(bool); !isBool {
				return triggerFailure(httpx.ScalarTypeMismatch("bool", triggerLoc(member), raw))
			}
		}
	}
	if raw, ok := object["mode"]; ok {
		if mode, isString := raw.(string); !isString || !contains(triggerModes, mode) {
			return triggerFailure(httpx.Literal(triggerLoc("mode"), raw, triggerModes))
		}
	}
	if raw, ok := object["budget"]; ok && raw != nil {
		budget, ok := raw.(string)
		if !ok || !contains(triggerBudgets, budget) {
			return triggerFailure(httpx.Enum(triggerLoc("budget"), raw, triggerBudgets))
		}
	}
	if raw, ok := object["tags_match"]; ok && raw != nil {
		match, ok := raw.(string)
		if !ok || !contains(triggerTagsMatch, match) {
			return triggerFailure(httpx.Literal(triggerLoc("tags_match"), raw, triggerTagsMatch))
		}
	}
	if raw, ok := object["fact_types"]; ok && raw != nil {
		factTypes, ok := raw.([]any)
		if !ok {
			return triggerFailure(httpx.TypeMismatch("list_type", triggerLoc("fact_types"), raw))
		}
		if len(factTypes) == 0 {
			return triggerFailure(httpx.ValueError(triggerLoc("fact_types"), raw,
				"fact_types must not be empty. Use null to include all fact types."))
		}
		for index, entry := range factTypes {
			name, ok := entry.(string)
			if !ok || !contains(triggerFactTypes, name) {
				loc := append(triggerLoc("fact_types"), strconv.Itoa(index))
				return triggerFailure(httpx.Literal(loc, entry, triggerFactTypes))
			}
		}
	}
	if raw, ok := object["min_refresh_interval_seconds"]; ok && raw != nil {
		if failure := triggerNonNegativeInt("min_refresh_interval_seconds", raw); failure != nil {
			return failure
		}
	}
	if raw, ok := object["reflect_search_observations_max_tokens"]; ok && raw != nil {
		loc := triggerLoc("reflect_search_observations_max_tokens")
		value, ok := raw.(float64)
		if !ok || value != math.Trunc(value) {
			return triggerFailure(httpx.ScalarTypeMismatch("int", loc, raw))
		}
		if value < 1 {
			return triggerFailure(httpx.GreaterThanEqual(loc, raw, 1))
		}
	}
	if raw, ok := object["refresh_cron"]; ok && raw != nil {
		expression, ok := raw.(string)
		if !ok {
			return triggerFailure(httpx.TypeMismatch("string_type", triggerLoc("refresh_cron"), raw))
		}
		if trimmed := strings.TrimSpace(expression); trimmed != "" {
			if _, err := cron.ParseStandard(trimmed); err != nil {
				// The reference interpolates repr(v), which single-quotes a string.
				return triggerFailure(httpx.ValueError(triggerLoc("refresh_cron"), expression,
					"refresh_cron is not a valid cron expression: '"+trimmed+"'"))
			}
		}
	}
	if refreshAfter, ok := object["refresh_after_consolidation"].(bool); ok && refreshAfter {
		if expression, ok := object["refresh_cron"].(string); ok && strings.TrimSpace(expression) != "" {
			// The framework's model-level validator reports the whole trigger
			// object as the item's input.
			return triggerFailure(httpx.ValueError([]string{"body", "trigger"}, object,
				"refresh_after_consolidation and refresh_cron are mutually exclusive: "+
					"a mental model refreshes either after consolidation or on a cron schedule, not both."))
		}
	}
	if raw, ok := object["tag_groups"]; ok && raw != nil {
		if triggerHasFuzzy(raw) {
			return triggerFailure(httpx.ValueError(triggerLoc("tag_groups"), raw,
				"resolve='fuzzy' is not supported in a trigger's tag_groups. "+
					"A stored scope must be exact; use it on a recall or reflect request instead."))
		}
	}
	return nil
}

// triggerNonNegativeInt reports a trigger's non-negative integer bound: a value
// of the wrong kind, or a number with a fraction, is named the way the framework
// names it, and only a whole number reaches the inclusive bound.
func triggerNonNegativeInt(member string, raw any) *httpx.ValidationFailure {
	loc := triggerLoc(member)
	value, ok := raw.(float64)
	// A value of the wrong kind and a number with a fraction are both typed the
	// way the framework types them: only a whole number reaches the bound.
	if !ok || value != math.Trunc(value) {
		return triggerFailure(httpx.ScalarTypeMismatch("int", loc, raw))
	}
	if value < 0 {
		return triggerFailure(httpx.GreaterThanEqual(loc, raw, 0))
	}
	return nil
}

// triggerHasFuzzy reports whether a tag-group tree carries a fuzzy resolution,
// which a stored trigger scope may not.
func triggerHasFuzzy(value any) bool {
	switch node := value.(type) {
	case []any:
		for _, entry := range node {
			if triggerHasFuzzy(entry) {
				return true
			}
		}
	case map[string]any:
		if resolve, ok := node["resolve"].(string); ok && strings.EqualFold(resolve, "fuzzy") {
			return true
		}
		for _, key := range []string{"and", "or", "not"} {
			if triggerHasFuzzy(node[key]) {
				return true
			}
		}
	}
	return false
}

// KnowledgePageDefaultTrigger is the built-in refresh policy a knowledge page
// gets when its create names none: a curated summary that follows
// consolidation, updating incrementally over the observation layer rather than
// re-reading every raw fact. A page that fell back to a bare mental model's
// defaults would be a full re-read of the whole bank on every refresh.
//
// It is the base a bank's `knowledge_page_default_trigger` overrides, and that
// in turn is the base a create request's trigger overrides.
var KnowledgePageDefaultTrigger = map[string]any{
	"mode":                        "delta",
	"fact_types":                  []any{"observation"},
	"exclude_mental_models":       true,
	"refresh_after_consolidation": true,
}

// MergeTrigger layers the members a caller actually set over a base, so a
// trigger patches rather than replaces. An absent base means the built-in
// knowledge-page default; otherwise it is whatever the previous layer resolved.
//
// The two refresh-scheduling members are exclusive: naming one drops the other,
// so a page moved onto a cron schedule stops refreshing after consolidation and
// vice versa. A caller that names neither keeps the base's.
func MergeTrigger(supplied, base map[string]any) map[string]any {
	if base == nil {
		base = KnowledgePageDefaultTrigger
	}
	merged := cloneTrigger(base)
	if merged == nil {
		merged = map[string]any{}
	}
	for key, value := range supplied {
		if value == nil {
			continue
		}
		merged[key] = value
	}
	_, namesRefresh := supplied["refresh_after_consolidation"]
	_, namesCron := supplied["refresh_cron"]
	if refreshAfter, _ := supplied["refresh_after_consolidation"].(bool); refreshAfter && !namesCron {
		delete(merged, "refresh_cron")
	}
	if namesCron && !namesRefresh {
		delete(merged, "refresh_after_consolidation")
	}
	return merged
}

// ValidateTriggerObject checks a stored or configured trigger map against the
// members the reference accepts, for callers outside this package (a bank's
// `knowledge_page_default_trigger` is one).
func ValidateTriggerObject(object map[string]any) *httpx.ValidationFailure {
	return validateTrigger(object)
}

// cloneTrigger copies a trigger map so callers cannot mutate shared defaults.
func cloneTrigger(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

// mergeTrigger overlays a patch's set members onto an existing trigger, matching
// the reference's _merge_trigger: an explicit null in the patch clears the
// stored member, and the two refresh-scheduling members stay mutually exclusive.
// Naming one without the other drops the other, so moving a model onto a cron
// schedule clears the auto-refresh it had and vice versa.
func mergeTrigger(current map[string]any, patch map[string]any) map[string]any {
	out := cloneTrigger(current)
	if out == nil {
		out = map[string]any{}
	}
	for key, value := range patch {
		out[key] = value
	}
	_, namesConsolidation := patch["refresh_after_consolidation"]
	_, namesCron := patch["refresh_cron"]
	if triggerTruthy(patch["refresh_cron"]) && !namesConsolidation {
		delete(out, "refresh_after_consolidation")
	}
	if triggerTruthy(patch["refresh_after_consolidation"]) && !namesCron {
		delete(out, "refresh_cron")
	}
	return out
}

// triggerTruthy reports whether a scheduling member was stated as enabled: a
// false bool, an empty string and null all read as "not stated".
func triggerTruthy(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return typed != ""
	}
	return false
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
