package mentalmodels

import (
	"bytes"
	"encoding/json"
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

// TriggerMember reports whether name is a member of the reference's trigger
// model, for callers outside this package that refuse an unknown member by
// name (a bank's knowledge_page_default_trigger is one).
func TriggerMember(name string) bool { return triggerKnownMember(name) }

// TriggerErrors returns every validation item a trigger object's members
// violate, for callers outside this package that report the whole set at once
// (a bank's knowledge_page_default_trigger is one). The items' locations begin
// with the request path the mental-model routes use.
func TriggerErrors(object map[string]any) []httpx.ValidationError {
	failure := validateTrigger(object)
	if failure == nil {
		return nil
	}
	return failure.Errors
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
	decoded, failure := decodeTriggerValue(raw)
	if failure != nil {
		return nil, failure
	}
	object, isObject := decoded.(map[string]any)
	if !isObject {
		// The framework extracts the trigger's members from an object; any other
		// value is its model-shape failure, reported with the value as input.
		return nil, triggerFailure(httpx.TypeMismatch("model_attributes_type", []string{"body", "trigger"}, decoded))
	}
	supplied, failure := validTriggerMembers(object)
	if failure != nil {
		return nil, failure
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

// decodeTriggerValue decodes the trigger member, with JSON numbers as float64 —
// the form every trigger check reads. A member that will not decode at all is
// the same shape failure an object-only decode reports.
func decodeTriggerValue(raw json.RawMessage) (any, *httpx.ValidationFailure) {
	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return nil, triggerFailure(httpx.TypeMismatch("model_attributes_type", []string{"body", "trigger"}, nil))
	}
	return decodeTriggerNumbers(decoded), nil
}

// decodeTriggerNumbers renders the decoder's json.Number values as float64.
func decodeTriggerNumbers(value any) any {
	switch typed := value.(type) {
	case json.Number:
		if number, err := typed.Float64(); err == nil {
			return number
		}
		return typed.String()
	case []any:
		for index, entry := range typed {
			typed[index] = decodeTriggerNumbers(entry)
		}
	case map[string]any:
		for key, entry := range typed {
			typed[key] = decodeTriggerNumbers(entry)
		}
	}
	return value
}

// validateTrigger checks each member against the reference's accepted values.
func validateTrigger(object map[string]any) *httpx.ValidationFailure {
	_, failure := validTriggerMembers(object)
	return failure
}

// validTriggerMembers validates the members a client supplied and returns the
// declared ones, projected through the model: a member the framework coerces (a
// boolean word, a numeric string) is stored as the value it coerced to, an
// unknown member is dropped, and every declared member is kept — including one
// the client set to null, which clears it on a patch.
//
// The items come in the reference's own order, which is not the order the client
// sent the members in: the field checks run in the model's declaration order and
// the model-level validators run after them. Within one field a type or bound
// failure replaces the value, so that field's own validator never sees the
// refused value and contributes no `Value error` item of its own.
func validTriggerMembers(object map[string]any) (map[string]any, *httpx.ValidationFailure) {
	var items []httpx.ValidationError
	rejected := make(map[string]bool, len(triggerMemberOrder))
	// report names the item a field check refused the member with and records
	// that the member never reaches its custom validator.
	report := func(member string, item httpx.ValidationError) {
		rejected[member] = true
		items = append(items, item)
	}
	// The reference's non-nullable members reject an explicit null the way any
	// wrong-typed value is rejected: mode is a Literal, the rest are bools. A
	// boolean takes the framework's lax coercion, so "yes" and 1 are booleans —
	// but a nullable one (include_chunks, the entity flag) may be cleared.
	for _, member := range triggerBoolMembers {
		raw, ok := object[member]
		if !ok || (raw == nil && contains(triggerNullableBoolMembers, member)) {
			continue
		}
		if _, coerced := httpx.LaxBoolean(raw); !coerced {
			// A value the coercion cannot interpret is always a parsing failure —
			// including a number outside 0/1 — because the framework tries the
			// declared bool first and only then the lax conversion.
			report(member, httpx.TypeMismatch("bool_parsing", triggerLoc(member), raw))
		}
	}
	if raw, ok := object["budget"]; ok && raw != nil {
		budget, isString := raw.(string)
		if !isString || !contains(triggerBudgets, budget) {
			report("budget", httpx.Enum(triggerLoc("budget"), raw, triggerBudgets))
		}
	}
	if raw, ok := object["mode"]; ok {
		if mode, isString := raw.(string); !isString || !contains(triggerModes, mode) {
			report("mode", httpx.Literal(triggerLoc("mode"), raw, triggerModes))
		}
	}
	if raw, ok := object["refresh_cron"]; ok && raw != nil {
		expression, isString := raw.(string)
		trimmed := strings.TrimSpace(expression)
		switch {
		case !isString:
			report("refresh_cron", httpx.TypeMismatch("string_type", triggerLoc("refresh_cron"), raw))
		case trimmed != "" && !validCron(trimmed):
			// The reference interpolates repr(v), which single-quotes the text.
			report("refresh_cron", httpx.ValueError(triggerLoc("refresh_cron"), expression,
				"refresh_cron is not a valid cron expression: '"+trimmed+"'"))
		}
	}
	for _, member := range triggerIntMembers {
		raw, ok := object[member]
		if !ok || raw == nil {
			continue
		}
		if item, refused := triggerIntItem(member, raw); refused {
			report(member, item)
		}
	}
	if raw, ok := object["fact_types"]; ok && raw != nil {
		if factTypes, isList := raw.([]any); !isList {
			report("fact_types", httpx.TypeMismatch("list_type", triggerLoc("fact_types"), raw))
		} else if len(factTypes) == 0 {
			report("fact_types", httpx.ValueError(triggerLoc("fact_types"), raw,
				"fact_types must not be empty. Use null to include all fact types."))
		} else {
			for index, entry := range factTypes {
				name, isString := entry.(string)
				if !isString || !contains(triggerFactTypes, name) {
					loc := append(triggerLoc("fact_types"), strconv.Itoa(index))
					report("fact_types", httpx.Literal(loc, entry, triggerFactTypes))
				}
			}
		}
	}
	if raw, ok := object["exclude_mental_model_ids"]; ok && raw != nil {
		ids, isList := raw.([]any)
		if !isList {
			report("exclude_mental_model_ids",
				httpx.TypeMismatch("list_type", triggerLoc("exclude_mental_model_ids"), raw))
		} else {
			for index, entry := range ids {
				if _, isString := entry.(string); !isString {
					loc := append(triggerLoc("exclude_mental_model_ids"), strconv.Itoa(index))
					report("exclude_mental_model_ids", httpx.TypeMismatch("string_type", loc, entry))
				}
			}
		}
	}
	if raw, ok := object["tags_match"]; ok && raw != nil {
		match, isString := raw.(string)
		if !isString || !contains(triggerTagsMatch, match) {
			report("tags_match", httpx.Literal(triggerLoc("tags_match"), raw, triggerTagsMatch))
		}
	}
	if raw, ok := object["tag_groups"]; ok && raw != nil {
		groups, isList := raw.([]any)
		if !isList {
			report("tag_groups", httpx.TypeMismatch("list_type", triggerLoc("tag_groups"), raw))
		} else {
			for index, entry := range groups {
				loc := append(triggerLoc("tag_groups"), strconv.Itoa(index))
				collectTagGroupItems(entry, loc, &items)
			}
		}
	}
	if raw, ok := object["response_schema"]; ok && raw != nil {
		if _, isObject := raw.(map[string]any); !isObject {
			report("response_schema", httpx.TypeMismatch("dict_type", triggerLoc("response_schema"), raw))
		}
	}
	// The model's own validators, in declaration order. Each one that refuses
	// contributes its item and the rest still run: the framework files one item
	// per failing validator, so a trigger can carry several — but only while the
	// member's own field-level checks passed, since a refused value never reaches
	// the validator that reads it.
	for _, custom := range triggerCustomValidators {
		raw, ok := object[custom.member]
		if custom.member != "" && (!ok || rejected[custom.member]) {
			continue
		}
		if item, refused := custom.validate(object, raw); refused {
			items = append(items, item)
		}
	}
	// The framework skips a model-level validator once any field-level check has
	// failed the model, so the exclusivity rule is reported only on an otherwise
	// valid trigger.
	if len(items) == 0 {
		if item, refused := triggerExclusivityValidator(object, nil); refused {
			items = append(items, item)
		}
	}
	if len(items) > 0 {
		return nil, triggerFailure(items...)
	}
	supplied := make(map[string]any, len(object))
	for key, value := range object {
		if triggerKnownMember(key) {
			supplied[key] = coerceTriggerMember(key, value)
		}
	}
	return supplied, nil
}

// validCron reports whether an expression is one the reference's cron library
// accepts.
func validCron(expression string) bool {
	_, err := cron.ParseStandard(expression)
	return err == nil
}

// triggerIntItem validates one integer-bound trigger member: a value of the
// wrong kind or a number with a fraction is typed the way the framework types
// it, and only a whole number reaches the member's inclusive lower bound.
func triggerIntItem(member string, raw any) (httpx.ValidationError, bool) {
	loc := triggerLoc(member)
	value, ok := httpx.LaxFloat(raw)
	if !ok {
		return httpx.ScalarTypeMismatch("int", loc, raw), true
	}
	bound := 1
	if member == "min_refresh_interval_seconds" {
		bound = 0
	}
	if value < bound {
		return httpx.GreaterThanEqual(loc, raw, bound), true
	}
	return httpx.ValidationError{}, false
}

// coerceTriggerMember renders a validated member as its model holds it: a member
// declared as an integer stores the number a numeric string spelled, and one
// declared as a boolean stores the value a boolean word or 0/1 spelled. Every
// other member is stored exactly as the client sent it.
func coerceTriggerMember(member string, value any) any {
	if contains(triggerIntMembers, member) {
		if number, ok := httpx.LaxFloat(value); ok {
			return number
		}
		return value
	}
	if contains(triggerBoolMembers, member) {
		if value == nil {
			return nil
		}
		if flag, ok := httpx.LaxBoolean(value); ok {
			return flag
		}
		return value
	}
	return value
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
