package mentalmodels

import (
	"bytes"
	"encoding/json"
	"sort"
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

// triggerDefaults is the reference's MentalModelTrigger at its defaults, in the
// model's declaration order: the four non-nullable members carry their declared
// default, the rest null.
//
// It is a list rather than a map because its order is observable: the reference
// hands its response model to the framework, which serializes the fields in
// declaration order, so a trigger always reaches the wire —
// `{"budget":…,"mode":…,"refresh_after_consolidation":…,…,"keep_trace":…}` — and
// a Go map would sort the eighteen keys alphabetically instead.
var triggerDefaults = []OrderedTriggerMember{
	{"budget", nil},
	{"mode", "full"},
	{"refresh_after_consolidation", false},
	{"refresh_cron", nil},
	// full_refresh_cron is the second, independent schedule: on it the model is
	// refreshed in full mode whatever its mode says. It sits beside refresh_cron
	// because that is where the reference's model declares it.
	{"full_refresh_cron", nil},
	{"min_refresh_interval_seconds", nil},
	{"fact_types", nil},
	{"exclude_mental_models", false},
	{"exclude_mental_model_ids", nil},
	{"tags_match", nil},
	{"tag_groups", nil},
	{"include_chunks", nil},
	{"recall_max_tokens", nil},
	{"recall_chunks_max_tokens", nil},
	{"reflect_search_observations_max_tokens", nil},
	{"reflect_search_observations_include_entities", nil},
	{"response_schema", nil},
	{"keep_trace", false},
}

// triggerDefaultMap is triggerDefaults keyed by member, for the lookups that
// only care about the member set and each member's default value.
var triggerDefaultMap = func() map[string]any {
	out := make(map[string]any, len(triggerDefaults))
	for _, member := range triggerDefaults {
		out[member.Key] = member.Value
	}
	return out
}()

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

// Trigger is a projected trigger: the reference's MentalModelTrigger members in
// declaration order, so the eighteen keys reach the wire in the order the model
// declares them rather than in a Go map's sorted one.
type Trigger []OrderedTriggerMember

// OrderedTriggerMember is one member of a projected Trigger.
type OrderedTriggerMember struct {
	Key   string
	Value any
}

// UnmarshalJSON reads a trigger object back into its ordered form, keeping the
// declared members in the model's order and any undeclared one after them (in
// document order, so nothing is lost when a stored trigger carries a member this
// build does not know). A JSON null yields the empty projection, which is the
// model's `None`.
func (t *Trigger) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		*t = nil
		return nil
	}
	var object map[string]any
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	if err := decoder.Decode(&object); err != nil {
		return err
	}
	seen := make(map[string]bool, len(object))
	out := make(Trigger, 0, len(object))
	for _, member := range triggerDefaults {
		value, present := object[member.Key]
		if !present {
			continue
		}
		seen[member.Key] = true
		out = append(out, OrderedTriggerMember{Key: member.Key, Value: decodeTriggerNumbers(value)})
	}
	for _, member := range documentMemberOrder(trimmed) {
		if seen[member] {
			continue
		}
		out = append(out, OrderedTriggerMember{Key: member, Value: decodeTriggerNumbers(object[member])})
	}
	*t = out
	return nil
}

// documentMemberOrder returns an object's members in the order the document
// spells them, so an undeclared member keeps the position it was written in.
func documentMemberOrder(data []byte) []string {
	decoder := json.NewDecoder(bytes.NewReader(data))
	// The opening brace is already known to be there: the caller decoded this.
	if _, err := decoder.Token(); err != nil {
		return nil
	}
	var members []string
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return members
		}
		name, ok := key.(string)
		if !ok {
			return members
		}
		members = append(members, name)
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return members
		}
	}
	return members
}

// Value returns one projected member, and whether the projection carries it.
// It is the read side of the ordered form, for callers that ask a trigger for a
// member by name rather than ranging over its declaration order.
func (t Trigger) Value(member string) (any, bool) {
	for _, entry := range t {
		if entry.Key == member {
			return entry.Value, true
		}
	}
	return nil, false
}

// MarshalJSON writes the members in the order the model declares them. An empty
// Trigger is the reference's null, not an empty object: a nil slice marshals as
// `[]` through a custom encoder, and the detail level that drops the trigger
// (metadata) reports null there.
func (t Trigger) MarshalJSON() ([]byte, error) {
	if t == nil {
		return []byte("null"), nil
	}
	var buffer bytes.Buffer
	buffer.WriteByte('{')
	for index, member := range t {
		if index > 0 {
			buffer.WriteByte(',')
		}
		key, err := json.Marshal(member.Key)
		if err != nil {
			return nil, err
		}
		buffer.Write(key)
		buffer.WriteByte(':')
		value, err := json.Marshal(member.Value)
		if err != nil {
			return nil, err
		}
		buffer.Write(value)
	}
	buffer.WriteByte('}')
	return buffer.Bytes(), nil
}

// normalizeTrigger projects a stored trigger through the reference's model: it
// emits every declared member in declaration order, filling the unset ones with
// their default and dropping any member the model does not declare. A nil stored
// trigger projects to the model's defaults — the reference reaches that object
// by validating the absent member into a `MentalModelTrigger`, so a row whose
// column holds `{}` or NULL still answers with the full eighteen-key object
// rather than null.
//
// Normalization is not just a fill: the reference's own field validators rewrite
// the value they accept, so `refresh_cron` is stored and read back stripped (a
// whitespace-only expression becomes null), and every `tag_groups` leaf is
// expanded with the `match`/`resolve` defaults it declares. Both are applied
// here, because this is the projection that decides what a client sees.
func normalizeTrigger(stored map[string]any) map[string]any {
	out := make(map[string]any, len(triggerDefaults))
	for _, member := range triggerDefaults {
		out[member.Key] = member.Value
	}
	for key, value := range stored {
		if value == nil {
			// The reference fills a null member with its default rather than
			// carrying the null: `mode: null` reads back as "full", not null.
			continue
		}
		if triggerKnownMember(key) {
			out[key] = value
		}
	}
	// The two cron members share one validator in the reference, so both are
	// stored and read back stripped, and a whitespace-only expression is null.
	for _, member := range triggerCronMembers {
		if expression, ok := out[member].(string); ok {
			if trimmed := strings.TrimSpace(expression); trimmed != "" {
				out[member] = trimmed
			} else {
				out[member] = nil
			}
		}
	}
	if groups, ok := out["tag_groups"].([]any); ok {
		out["tag_groups"] = normalizeTagGroups(groups)
	}
	return out
}

// OrderedObject is a JSON object whose members render in the order it holds
// them. Reordering a Go map cannot do that — the encoder sorts a map's keys
// whatever order it was built in — so a value that has to reach the wire in a
// particular order is carried as this type, whose MarshalJSON writes the members
// as it finds them.
type OrderedObject []OrderedTriggerMember

// MarshalJSON writes the members in the order the object holds them.
func (o OrderedObject) MarshalJSON() ([]byte, error) {
	var buffer bytes.Buffer
	buffer.WriteByte('{')
	for index, member := range o {
		if index > 0 {
			buffer.WriteByte(',')
		}
		key, err := json.Marshal(member.Key)
		if err != nil {
			return nil, err
		}
		buffer.Write(key)
		buffer.WriteByte(':')
		value, err := json.Marshal(member.Value)
		if err != nil {
			return nil, err
		}
		buffer.Write(value)
	}
	buffer.WriteByte('}')
	return buffer.Bytes(), nil
}

// orderedObject renders a map with its named members first, in the given order,
// then any remaining key in its own sorted order.
func orderedObject(order []string, stored map[string]any) OrderedObject {
	members := make(OrderedObject, 0, len(stored))
	named := make(map[string]bool, len(order))
	for _, key := range order {
		named[key] = true
		if value, present := stored[key]; present {
			members = append(members, OrderedTriggerMember{Key: key, Value: value})
		}
	}
	rest := make([]string, 0, len(stored))
	for key := range stored {
		if !named[key] {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	for _, key := range rest {
		members = append(members, OrderedTriggerMember{Key: key, Value: stored[key]})
	}
	return members
}

// reflectResponseOrder is the member order a mental model's stored
// reflect_response reaches the wire in. The reference's payload is an
// insertion-ordered dict — text, outcome, based_on, then mental_models, with
// reflect_skipped landing between them when the refresh read nothing — and the
// jsonb column it is stored in carries no order back, so the read path restores
// it here. Any member this list does not name follows, in the stored map's own
// (sorted) order, so nothing is dropped.
var reflectResponseOrder = []string{
	"text", "outcome", "based_on", "reflect_skipped", "mental_models",
}

// basedOnOrder is the order the fact types inside based_on are listed in: the
// order the reference's own `based_on` dict is built in.
var basedOnOrder = []string{
	"world", "opinion", "directives", "experience", "observation", "mental-models",
}

// ReorderReflectResponse renders a stored reflect_response as the ordered object
// the reference serializes, so a reader sees its members in the order the
// reference's model declares rather than a jsonb round trip's sorted one. The
// result is an OrderedObject, not a map: only a type of its own can pin the
// member order on the wire.
func ReorderReflectResponse(stored map[string]any) any {
	if stored == nil {
		return nil
	}
	out := make(map[string]any, len(stored))
	for key, value := range stored {
		if key == "based_on" {
			if based, isMap := value.(map[string]any); isMap {
				out[key] = orderedObject(basedOnOrder, based)
				continue
			}
		}
		out[key] = value
	}
	return orderedObject(reflectResponseOrder, out)
}

// projectTrigger renders a stored trigger as the ordered wire value: its
// eighteen declared members in declaration order.
func projectTrigger(stored map[string]any) Trigger {
	normalized := normalizeTrigger(stored)
	out := make(Trigger, 0, len(triggerDefaults))
	for _, member := range triggerDefaults {
		out = append(out, OrderedTriggerMember{Key: member.Key, Value: normalized[member.Key]})
	}
	return out
}

// normalizeTagGroups projects each stored tag-group node through the union's
// models, which fill the leaf's `match`/`resolve` defaults and serialize their
// members in declaration order. A node the model does not recognise is kept as
// stored, so a projection never drops a scope it cannot read.
func normalizeTagGroups(groups []any) []any {
	out := make([]any, len(groups))
	for index, group := range groups {
		out[index] = normalizeTagGroup(group)
	}
	return out
}

// normalizeTagGroup projects one tag-group node.
func normalizeTagGroup(node any) any {
	object, ok := node.(map[string]any)
	if !ok {
		return node
	}
	if raw, present := object["tags"]; present {
		// A leaf: `tags`, then the two optional members with their declared
		// defaults (TagsMatch "any_strict", TagResolution "exact").
		leaf := make(map[string]any, 3)
		leaf["tags"] = raw
		leaf["match"] = tagLeafMemberDefault(object, "match", "any_strict")
		leaf["resolve"] = tagLeafMemberDefault(object, "resolve", "exact")
		return leaf
	}
	for _, member := range []struct{ alias, name string }{{"and", "filters"}, {"or", "filters"}, {"not", "filter"}} {
		raw, present := tagGroupMember(object, member.alias, member.name)
		if !present {
			continue
		}
		// The compound groups serialize by alias, so the wire key is the one the
		// client wrote either way.
		if member.name == "filters" {
			list, isList := raw.([]any)
			if !isList {
				return node
			}
			return map[string]any{member.alias: normalizeTagGroups(list)}
		}
		return map[string]any{member.alias: normalizeTagGroup(raw)}
	}
	return node
}

// tagLeafMemberDefault reads one optional leaf member, substituting the model's
// default when the member is absent or null.
func tagLeafMemberDefault(object map[string]any, name, fallback string) any {
	value, present := object[name]
	if !present || value == nil {
		return fallback
	}
	return value
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
// nil for a patch, which leaves the model's current trigger untouched. A raw
// that holds JSON null is not an absence: the framework reads a supplied null
// as a value it cannot extract members from, so it is the same model-shape
// failure as any other non-object.
func parseTrigger(raw json.RawMessage, create bool) (map[string]any, *httpx.ValidationFailure) {
	if len(raw) == 0 {
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
		// A trigger member left absent takes the model's default — that is what
		// `parseTrigger` handles above — but one the client sent as a JSON null is
		// not an absence: the framework tries to read the members out of the null
		// and files this same failure, so an explicit null is a 422 on both the
		// create and the patch route.
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
			// A value the coercion cannot interpret: a string is a *parsing* failure,
			// while a null, an object, a list or a fractional number is simply the
			// wrong type — the framework reaches its lax string conversion only for
			// a value of that kind, and files a bare mismatch for every other.
			report(member, triggerBoolItem(member, raw))
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
	// One field validator serves both cron members in the reference
	// (`@field_validator("refresh_cron", "full_refresh_cron")`), and its message
	// names the member `info.field_name` resolved to, so a violation of either is
	// the same item under its own name.
	for _, member := range triggerCronMembers {
		raw, ok := object[member]
		if !ok || raw == nil {
			continue
		}
		expression, isString := raw.(string)
		trimmed := strings.TrimSpace(expression)
		switch {
		case !isString:
			report(member, httpx.TypeMismatch("string_type", triggerLoc(member), raw))
		case trimmed != "" && !validCron(trimmed):
			// The reference interpolates repr(v), which single-quotes the text.
			report(member, httpx.ValueError(triggerLoc(member), expression,
				member+" is not a valid cron expression: '"+trimmed+"'"))
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
// accepts: robfig's standard grammar first, then the croniter extensions the
// reference's validator reads (see croniter.go for the accepted forms).
func validCron(expression string) bool {
	if _, err := cron.ParseStandard(expression); err == nil {
		return true
	}
	return croniterValid(expression)
}

// triggerIntItem validates one integer-bound trigger member: a value of the
// wrong kind or a number with a fraction is typed the way the framework types
// it, and only a whole number reaches the member's inclusive lower bound.
func triggerIntItem(member string, raw any) (httpx.ValidationError, bool) {
	loc := triggerLoc(member)
	value, ok := triggerIntValue(raw)
	if !ok {
		return httpx.ScalarTypeMismatch("int", loc, raw), true
	}
	if value < triggerIntLowerBound(member) {
		return httpx.GreaterThanEqual(loc, raw, triggerIntLowerBound(member)), true
	}
	return httpx.ValidationError{}, false
}

// triggerIntValue reads a trigger integer the way the reference's model declares
// it: a plain `int` field, not a lax-converted one. The difference is the site:
// the model is declared without strict mode, so a bare `int` annotation takes a
// JSON number, a numeric string and a JSON boolean (true is 1, false is 0) —
// which is why `min_refresh_interval_seconds: true` is accepted as 1, and why a
// `bool` was the one kind the shared lax reader refused and the reference did
// not. A fractional number is the framework's int_from_float and anything else
// is a parse/type failure, both reported by ScalarTypeMismatch.
func triggerIntValue(raw any) (int, bool) {
	if flag, isBool := raw.(bool); isBool {
		if flag {
			return 1, true
		}
		return 0, true
	}
	return httpx.LaxFloat(raw)
}

// triggerIntLowerBound is the inclusive floor each integer member declares
// (`ge=0` on the refresh interval, `ge=1` on the token budgets).
func triggerIntLowerBound(member string) int {
	if member == "min_refresh_interval_seconds" {
		return 0
	}
	return 1
}

// coerceTriggerMember renders a validated member as its model holds it: a member
// declared as an integer stores the number a numeric string spelled, and one
// declared as a boolean stores the value a boolean word or 0/1 spelled. Every
// other member is stored exactly as the client sent it.
func coerceTriggerMember(member string, value any) any {
	if contains(triggerIntMembers, member) {
		if number, ok := triggerIntValue(value); ok {
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
// The two refresh-scheduling members are exclusive, and the reference's rule is
// directional rather than symmetric: a supplied `refresh_cron` drops the base's
// `refresh_after_consolidation`, while a supplied `refresh_after_consolidation`
// alone is a plain overlay — the base's `refresh_cron` survives. The create route
// never sends both, so it is not what this protects; a *patch* is, where the
// stored trigger is the base and a client turning auto-refresh on must not have
// the schedule it never mentioned silently preserved beside it.
func MergeTrigger(supplied, base map[string]any) map[string]any {
	if base == nil {
		base = KnowledgePageDefaultTrigger
	}
	merged := cloneTrigger(base)
	if merged == nil {
		merged = map[string]any{}
	}
	// Every supplied member overwrites, and an explicit null overwrites with a
	// null: the reference merges `{**base, **supplied}`, so stating a member as
	// null is how a client clears it.
	for key, value := range supplied {
		merged[key] = value
	}
	// The two schedule members are exclusive, and a stated one wins: a supplied
	// refresh_cron clears a stale refresh_after_consolidation, and enabling
	// consolidation clears a configured cron. The reference tests `supplied`
	// rather than the merged map, so only a member the patch actually stated can
	// clear the other — a patch that names neither leaves both as they were.
	//
	// A member stated as null does not count as stated here: null is how the
	// client clears a member, not a request to turn the other one off.
	if _, statesFlag := supplied["refresh_after_consolidation"]; !statesFlag {
		if supplied["refresh_cron"] != nil {
			delete(merged, "refresh_after_consolidation")
		}
	} else if after, _ := supplied["refresh_after_consolidation"].(bool); after {
		if _, statesCron := supplied["refresh_cron"]; !statesCron {
			delete(merged, "refresh_cron")
		}
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

// mergeTrigger overlays a patch's set members onto an existing trigger by
// delegating to MergeTrigger, so the patch route and the knowledge-page routes
// share one implementation: an explicit null in the patch clears the stored
// member, and the two refresh-scheduling members stay mutually exclusive.
func mergeTrigger(current map[string]any, patch map[string]any) map[string]any {
	return MergeTrigger(patch, current)
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
