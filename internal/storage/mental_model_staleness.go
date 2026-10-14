package storage

import (
	"context"
	"strings"
	"time"
)

// Refresh tag-match defaults. A tag-scoped model defaults to all_strict so a
// refresh reads the narrowest set its author scoped it to; an untagged model
// has no scope to narrow and matches any memory in the bank.
const (
	defaultScopedTagsMatch   = TagsMatchAllStrict
	defaultUnscopedTagsMatch = TagsMatchAny
)

// MentalModelScope is the memory window a model's refresh reads, and therefore
// the window its staleness is judged over.
//
// It is resolved once from the model's own tags and its trigger, because the
// two must agree: judging staleness over a wider window than the refresh reads
// would flag the model stale forever on writes it can never see.
type MentalModelScope struct {
	// Since is the watermark the model last observed. Zero means never.
	Since time.Time
	// FactTypes narrows the scope to these memory types, empty for all.
	FactTypes []string
	// Tags and TagsMatch express flat tag scoping.
	Tags      []string
	TagsMatch string
	// TagGroups expresses compound scoping, which overrides flat tags.
	TagGroups []TagGroup
}

// ScopeFromModel resolves the window a model refreshes over.
//
// The priority is the trigger's: explicit tag groups win outright, then a
// declared match mode, then a default that depends on whether the model is
// scoped at all.
func ScopeFromModel(model MentalModel) MentalModelScope {
	scope := MentalModelScope{
		Since:     model.LastMemorySeenAtTime(),
		FactTypes: stringSliceFromTrigger(model.Trigger, "fact_types"),
	}

	if raw, ok := model.Trigger["tag_groups"]; ok {
		if groups := tagGroupsFromTrigger(raw); len(groups) > 0 {
			scope.TagGroups = groups
			scope.TagsMatch = TagsMatchAny
			return scope
		}
	}

	scope.Tags = model.Tags
	if declared := stringFromTrigger(model.Trigger, "tags_match"); declared != "" {
		scope.TagsMatch = declared
	} else if len(model.Tags) > 0 {
		scope.TagsMatch = defaultScopedTagsMatch
	} else {
		scope.TagsMatch = defaultUnscopedTagsMatch
	}
	return scope
}

// LastMemorySeenAtTime returns the watermark the model last observed, falling
// back to its refresh time.
//
// The fallback exists for a model refreshed before the watermark column
// existed. It is not equivalent — a refresh that found nothing new still moves
// the refresh time — but it is strictly better than treating the model as
// having seen nothing, which would refresh every model in a deployment at once.
func (m MentalModel) LastMemorySeenAtTime() time.Time {
	if m.LastMemorySeenAt != nil {
		return *m.LastMemorySeenAt
	}
	return m.LastRefreshedAt
}

// IsStale reports whether a write inside the model's scope has landed since the
// model last looked.
//
// It asks the bank watermark first: a model cannot be stale if nothing has been
// written to its bank since it last read, so the scoped query is skipped in the
// common case where a bank is quiet. That short-circuit is what keeps a cron
// tick over many models from paying for a scan each.
func (s *MemoryStore) IsStale(ctx context.Context, bankID string, model MentalModel) bool {
	scope := ScopeFromModel(model)
	if scope.Since.IsZero() {
		// Never stamped with a watermark: nothing to be current with.
		return true
	}
	if bankWatermark := s.BankWriteWatermark(ctx, bankID); !bankWatermark.After(scope.Since) {
		return false
	}
	return s.anyMemoryInScopeSince(ctx, bankID, scope)
}

// anyMemoryInScopeSince reports whether any memory matching the scope was
// written after the watermark.
func (s *MemoryStore) anyMemoryInScopeSince(ctx context.Context, bankID string, scope MentalModelScope) bool {
	if err := ctx.Err(); err != nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, unit := range s.units {
		if unit.BankID != bankID {
			continue
		}
		written := unit.MentionedAt
		if written == nil {
			written = unit.OccurredStart
		}
		if written == nil || !written.After(scope.Since) {
			continue
		}
		if !scope.includesUnit(unit) {
			continue
		}
		return true
	}
	return false
}

// includesUnit reports whether a memory falls inside the scope.
func (scope MentalModelScope) includesUnit(unit MemoryUnit) bool {
	if len(scope.FactTypes) > 0 {
		matched := false
		for _, factType := range scope.FactTypes {
			if string(unit.Type) == factType {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if len(scope.TagGroups) > 0 {
		return matchTagGroups(unit.Tags, scope.TagGroups)
	}
	if len(scope.Tags) == 0 {
		// An unscoped model's scope is the whole bank, so any write counts.
		return true
	}
	return matchFlatTags(unit.Tags, scope.Tags, scope.TagsMatch)
}

// matchFlatTags applies one flat tag predicate to a unit's tags.
//
// A tag-scoped model is stale only when a write lands *in* its tags: the
// refresh under any/all may still read untagged memories, but an untagged write
// is not about this model, and counting it would flag every scoped model stale
// after nearly every write to a bank that mixes the two.
func matchFlatTags(unitTags, wanted []string, mode string) bool {
	strict := strings.HasSuffix(mode, "_strict")
	switch mode {
	case TagsMatchAll, TagsMatchAllStrict:
		for _, want := range wanted {
			if !containsTag(unitTags, want) {
				return false
			}
		}
		return len(wanted) > 0
	case TagsMatchExact:
		return sameTags(unitTags, wanted, strict)
	default: // any, any_strict
		for _, want := range wanted {
			if containsTag(unitTags, want) {
				return true
			}
		}
		return false
	}
}

// matchTagGroups applies a compound predicate tree.
func matchTagGroups(unitTags []string, groups []TagGroup) bool {
	for _, group := range groups {
		if !matchTagGroup(unitTags, group) {
			return false
		}
	}
	return len(groups) > 0
}

// matchTagGroup applies one compound node.
func matchTagGroup(unitTags []string, group TagGroup) bool {
	switch {
	case len(group.And) > 0:
		for _, child := range group.And {
			if !matchTagGroup(unitTags, child) {
				return false
			}
		}
		return true
	case len(group.Or) > 0:
		for _, child := range group.Or {
			if matchTagGroup(unitTags, child) {
				return true
			}
		}
		return false
	case len(group.Not) > 0:
		for _, child := range group.Not {
			if matchTagGroup(unitTags, child) {
				return false
			}
		}
		return true
	default:
		wanted := group.Tags
		if group.Tag != "" {
			wanted = append([]string{group.Tag}, wanted...)
		}
		return matchFlatTags(unitTags, wanted, group.Match)
	}
}

// containsTag reports whether a tag set holds a wanted tag.
func containsTag(tags []string, want string) bool {
	for _, tag := range tags {
		if tag == want {
			return true
		}
	}
	return false
}

// sameTags reports whether two tag sets are equal. A strict comparison also
// requires the count to match, so a scope of one tag does not match a unit
// carrying that tag among others.
func sameTags(unitTags, wanted []string, strict bool) bool {
	for _, want := range wanted {
		if !containsTag(unitTags, want) {
			return false
		}
	}
	if strict && len(unitTags) != len(wanted) {
		return false
	}
	for _, tag := range unitTags {
		if !containsTag(wanted, tag) {
			return false
		}
	}
	return true
}

// stringSliceFromTrigger reads a list-of-strings field out of the trigger bag.
func stringSliceFromTrigger(trigger map[string]any, key string) []string {
	raw, ok := trigger[key]
	if !ok {
		return nil
	}
	items, ok := raw.([]any)
	if !ok {
		if typed, ok := raw.([]string); ok {
			return typed
		}
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok && text != "" {
			out = append(out, text)
		}
	}
	return out
}

// stringFromTrigger reads a string field out of the trigger bag.
func stringFromTrigger(trigger map[string]any, key string) string {
	if trigger == nil {
		return ""
	}
	text, _ := trigger[key].(string)
	return text
}

// tagGroupsFromTrigger decodes a compound tag filter from the trigger bag,
// which is stored as plain JSON with no schema guarantee.
func tagGroupsFromTrigger(raw any) []TagGroup {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]TagGroup, 0, len(items))
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, tagGroupFromMap(object))
	}
	return out
}

// tagGroupFromMap decodes one compound node.
func tagGroupFromMap(object map[string]any) TagGroup {
	group := TagGroup{
		Tags:    stringSliceFromTrigger(object, "tags"),
		Resolve: stringFromTrigger(object, "resolve"),
		Match:   stringFromTrigger(object, "match"),
	}
	group.Tag = stringFromTrigger(object, "tag")
	group.And = tagGroupsFromTrigger(object["and"])
	group.Or = tagGroupsFromTrigger(object["or"])
	group.Not = tagGroupsFromTrigger(object["not"])
	if group.Match == "" {
		group.Match = TagsMatchAny
	}
	return group
}

// IsStale reports whether a write inside the model's scope has landed since the
// model last looked.
//
// The bank watermark is asked first, in its own cheap query: a model cannot be
// stale if nothing has been written to its bank since it last read, so the
// scoped scan is skipped in the common case where a bank is quiet. That
// short-circuit is what keeps a cron tick over many models proportionate.
func (s *PostgresStore) IsStale(ctx context.Context, bankID string, model MentalModel) bool {
	scope := ScopeFromModel(model)
	if scope.Since.IsZero() {
		return true
	}
	if watermark := s.BankWriteWatermark(ctx, bankID); !watermark.After(scope.Since) {
		return false
	}
	return s.anyMemoryInScopeSince(ctx, bankID, scope)
}

// anyMemoryInScopeSince asks the database whether any in-scope memory is newer
// than the watermark.
//
// The query stops at the first match rather than counting, because the caller
// only needs to know whether one exists — a model with ten thousand newer
// memories is no more stale than one with a single newer memory, and counting
// them all would turn a cheap existence check into a scan.
func (s *PostgresStore) anyMemoryInScopeSince(ctx context.Context, bankID string, scope MentalModelScope) bool {
	clauses := []string{"bank_id = $1"}
	args := []any{bankID, scope.Since}
	// The written time is the mention time, falling back to the occurred
	// start: a memory with no mention time still has a time the model can be
	// judged against.
	clauses = append(clauses, "COALESCE(mentioned_at, occurred_start) > $2")

	if len(scope.FactTypes) > 0 {
		args = append(args, scope.FactTypes)
		clauses = append(clauses, "type = ANY($"+itoa(len(args))+"::text[])")
	}
	if predicate, predicateArgs := tagPredicate(scope, len(args)); predicate != "" {
		args = append(args, predicateArgs...)
		clauses = append(clauses, predicate)
	}

	query := `SELECT 1 FROM ` + quoteIdent(s.schema) + `.memory_units
	          WHERE ` + strings.Join(clauses, " AND ") + `
	          LIMIT 1`
	var found int
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&found)
	return err == nil
}

// tagPredicate renders the scope's tag filter as a SQL predicate over the tags
// array, numbering its placeholders from the position already used.
func tagPredicate(scope MentalModelScope, used int) (string, []any) {
	if len(scope.TagGroups) > 0 {
		return renderTagGroups(scope.TagGroups, used)
	}
	if len(scope.Tags) == 0 {
		// An unscoped model's scope is the whole bank, so any write counts.
		return "", nil
	}
	placeholder := "$" + itoa(used+1)
	switch scope.TagsMatch {
	case TagsMatchAll, TagsMatchAllStrict:
		// Every wanted tag must be present.
		return "tags @> " + placeholder + "::text[]", []any{scope.Tags}
	case TagsMatchExact:
		// The set must be equal, not merely contain.
		return "tags <@ " + placeholder + "::text[] AND tags @> " + placeholder + "::text[]", []any{scope.Tags}
	default: // any, any_strict
		return "tags && " + placeholder + "::text[]", []any{scope.Tags}
	}
}

// renderTagGroups renders a compound filter, ANDing the top-level groups.
func renderTagGroups(groups []TagGroup, used int) (string, []any) {
	var (
		parts []string
		args  []any
	)
	for _, group := range groups {
		predicate, predicateArgs := renderTagGroup(group, used+len(args))
		if predicate == "" {
			continue
		}
		parts = append(parts, "("+predicate+")")
		args = append(args, predicateArgs...)
	}
	if len(parts) == 0 {
		return "", nil
	}
	return strings.Join(parts, " AND "), args
}

// renderTagGroup renders one compound node.
func renderTagGroup(group TagGroup, used int) (string, []any) {
	switch {
	case len(group.And) > 0:
		return joinTagChildren(group.And, " AND ", used)
	case len(group.Or) > 0:
		return joinTagChildren(group.Or, " OR ", used)
	case len(group.Not) > 0:
		inner, args := joinTagChildren(group.Not, " OR ", used)
		if inner == "" {
			return "", nil
		}
		return "NOT (" + inner + ")", args
	default:
		wanted := group.Tags
		if group.Tag != "" {
			wanted = append([]string{group.Tag}, wanted...)
		}
		if len(wanted) == 0 {
			return "", nil
		}
		placeholder := "$" + itoa(used+1)
		if group.Match == TagsMatchAll || group.Match == TagsMatchAllStrict {
			return "tags @> " + placeholder + "::text[]", []any{wanted}
		}
		return "tags && " + placeholder + "::text[]", []any{wanted}
	}
}

// joinTagChildren renders a boolean node's children.
func joinTagChildren(children []TagGroup, operator string, used int) (string, []any) {
	var (
		parts []string
		args  []any
	)
	for _, child := range children {
		predicate, predicateArgs := renderTagGroup(child, used+len(args))
		if predicate == "" {
			continue
		}
		parts = append(parts, "("+predicate+")")
		args = append(args, predicateArgs...)
	}
	if len(parts) == 0 {
		return "", nil
	}
	return strings.Join(parts, operator), args
}

// itoa renders a parameter index. It is a local helper so this file carries no
// formatting import for a single use.
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}
