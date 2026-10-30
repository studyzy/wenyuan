package storage

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
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
//
// It is the *staleness* rule, matching the reference's (`since =
// last_memory_seen_at or last_refreshed_at`). A refresh's delta window must not
// use it: there the bound is the watermark alone, because a model carries a
// refresh stamp from the moment it is created, and reading that as "already
// seen everything" would make a new document's first refresh read nothing.
func (m MentalModel) LastMemorySeenAtTime() time.Time {
	if m.LastMemorySeenAt != nil {
		return *m.LastMemorySeenAt
	}
	return m.LastRefreshedAt
}

// IsStale reports whether the model is out of date in the full sense: a write
// inside its scope landed since it last looked, or evidence it cites has gone.
//
// The retraction half is what the watermark cannot see, and it is the question
// the refresh triggers ask. Display surfaces that read a whole set of models at
// once ask IsStaleBatch instead — the reference's batch staleness deliberately
// leaves the retraction check to the single-model path.
func (s *MemoryStore) IsStale(ctx context.Context, bankID string, model MentalModel) bool {
	if s.IsStaleBatch(ctx, bankID, model) {
		return true
	}
	return groundingRetracted(ctx, s, bankID, model)
}

// IsStaleBatch reports whether a write inside the model's scope has landed since
// the model last looked, with no retraction check.
//
// This is the question a list of models is answered with: asking each one
// whether its grounding is intact costs a lookup per model, and the refresh is
// queued by the single-model path regardless, so a page can read "up to date"
// in a listing while its refresh is already scheduled.
//
// It asks the bank watermark first: a model cannot be stale if nothing has been
// written to its bank since it last read, so the scoped query is skipped in the
// common case where a bank is quiet. That short-circuit is what keeps a cron
// tick over many models from paying for a scan each.
func (s *MemoryStore) IsStaleBatch(ctx context.Context, bankID string, model MentalModel) bool {
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
		// The written time is the unit's record timestamp — the same column the
		// bank watermark reads — so a write that moves the watermark also makes
		// the model stale. The mention and occurrence times are fallbacks for a
		// unit whose record time is unset, which is the shape a seeded store can
		// have; against a real row the record time is always the one to use.
		written := unit.UpdatedAt
		if written == nil {
			written = unit.MentionedAt
		}
		if written == nil {
			written = unit.OccurredStart
		}
		if written == nil {
			written = unit.CreatedAt
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
			if string(unit.FactType) == factType {
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
	case group.Not != nil:
		return !matchTagGroup(unitTags, *group.Not)
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
// tagGroupFromTrigger reads a single group node, which is how the reference
// stores a NOT's child (`filter: TagGroup`, engine/search/tags.py:333-337).
func tagGroupFromTrigger(raw any) *TagGroup {
	object, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	group := tagGroupFromMap(object)
	return &group
}

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
	group.Not = tagGroupFromTrigger(object["not"])
	if group.Match == "" {
		group.Match = TagsMatchAny
	}
	return group
}

// IsStale reports whether the model is out of date in the full sense: a write
// inside its scope landed since it last looked, or evidence it cites has gone.
//
// The retraction half costs a lookup, so it is asked here and not in
// IsStaleBatch — the same split the reference draws between its single-model and
// batch staleness questions.
func (s *PostgresStore) IsStale(ctx context.Context, bankID string, model MentalModel) bool {
	if s.IsStaleBatch(ctx, bankID, model) {
		return true
	}
	return groundingRetracted(ctx, s, bankID, model)
}

// IsStaleBatch reports whether a write inside the model's scope has landed since
// the model last looked, with no retraction check: the batch question a listing
// of models is answered with.
//
// The bank watermark is asked first, in its own cheap query: a model cannot be
// stale if nothing has been written to its bank since it last read, so the
// scoped scan is skipped in the common case where a bank is quiet. That
// short-circuit is what keeps a cron tick over many models proportionate.
func (s *PostgresStore) IsStaleBatch(ctx context.Context, bankID string, model MentalModel) bool {
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
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return false
	}
	clauses := []string{"bank_id = $1"}
	args := []any{bankID, scope.Since}
	// The written time is the unit's record timestamp, the same column the bank
	// watermark reads (MAX(updated_at)): a write must move both, or a fresh
	// memory raises the watermark while the scoped check misses it and the
	// model stays wrongly "current". Mention and occurrence times are fallbacks
	// for a row whose record time is unset; a real row always carries one.
	clauses = append(clauses, "COALESCE(updated_at, mentioned_at, occurred_start) > $2")

	if len(scope.FactTypes) > 0 {
		args = append(args, scope.FactTypes)
		// The column is fact_type, not type: memory_units carries the fact type
		// under its own name (the reference's schema), and naming the wrong one
		// made the query fail - which the caller below turned into "no source",
		// so a page whose scope names fact types silently never refreshed.
		clauses = append(clauses, "fact_type = ANY($"+itoa(len(args))+"::text[])")
	}
	if predicate, predicateArgs := tagPredicate(scope, len(args)); predicate != "" {
		args = append(args, predicateArgs...)
		clauses = append(clauses, predicate)
	}

	query := `SELECT 1 FROM ` + quoteIdent(schema) + `.memory_units
	          WHERE ` + strings.Join(clauses, " AND ") + `
	          LIMIT 1`
	var found int
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&found)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		// Reading a failure as "not stale" parks the model, so the failure is
		// logged rather than swallowed.
		slog.Default().Warn("could not check whether a mental model's scope has newer memories",
			"bank_id", bankID, "scope_since", scope.Since, "error", err)
	}
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
		return "tags::text[] @> " + placeholder + "::text[]", []any{scope.Tags}
	case TagsMatchExact:
		// The set must be equal, not merely contain.
		return "tags::text[] <@ " + placeholder + "::text[] AND tags::text[] @> " + placeholder + "::text[]", []any{scope.Tags}
	default: // any, any_strict
		return "tags::text[] && " + placeholder + "::text[]", []any{scope.Tags}
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
	case group.Not != nil:
		inner, args := renderTagGroup(*group.Not, used)
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
			// A leaf naming no tag still filters in `exact` mode: set equality
			// against the empty set is the "global" (untagged) scope, the same
			// clause an ungrouped `tags_match=exact` with no tags renders
			// (flatTagClause) and the same reading the in-memory matcher takes.
			// Returning "" here would turn the request into no filter at all,
			// so a caller asking for the untagged graph would get the bank.
			if group.Match == TagsMatchExact {
				return "(tags IS NULL OR tags = '{}')", nil
			}
			return "", nil
		}
		placeholder := "$" + itoa(used+1)
		switch group.Match {
		case TagsMatchAll, TagsMatchAllStrict:
			return "tags::text[] @> " + placeholder + "::text[]", []any{wanted}
		case TagsMatchExact:
			// Set equality, matching recall's `exact` (flatTagClause) and the
			// in-memory matcher, so a compound `exact` leaf means the same thing
			// on both backends.
			return "tags::text[] <@ " + placeholder + "::text[] AND tags::text[] @> " + placeholder + "::text[]", []any{wanted}
		default:
			return "tags::text[] && " + placeholder + "::text[]", []any{wanted}
		}
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

// RefreshModePayloadKey is the payload key carrying a one-run refresh-mode
// override ("full") for a mental model refresh: a "full refresh now" or a
// full_refresh_cron tick. The worker reads it to run the refresh in full mode
// whatever the stored trigger says, and it is the second payload key a fold has
// to reconcile (see PromoteQueuedRefreshToFull).
const RefreshModePayloadKey = "_mode"

// AutomaticRefreshPayloadKey marks a queued refresh an automatic trigger
// submitted — a consolidation flush or a cron tick — rather than an explicit
// API/MCP call. Only an automatic refresh parks behind the model's
// min_refresh_interval_seconds (memory_engine.py:24183, _REFRESH_AUTOMATIC_KEY).
const AutomaticRefreshPayloadKey = "_automatic"

// LifecycleLockedStore is a store able to mutate a queued operation's payload
// inside the same critical section that decides whether to queue one. It is a
// narrow capability rather than part of Store, like the staleness readers: only
// the mental-model refresh submit needs it.
type LifecycleLockedStore interface {
	// WithOperationSubjectLock runs fn under the lock a deduplicated enqueue of
	// this bank and operation type takes, so a submit that finds an existing
	// queued operation may amend it without racing a concurrent submit.
	WithOperationSubjectLock(ctx context.Context, bankID, operationType string, fn func() error) error
}

// PromoteQueuedRefreshToFull makes a queued refresh run in full mode.
//
// Only a row that is still pending is touched. A fold into a *processing*
// refresh changes nothing: that run has already chosen its mode, and the
// full_refresh_cron scan simply finds the model due again on its next tick,
// because only a full run moves last_full_refreshed_at. Returns whether the row
// was amended; a row that is gone, no longer pending, or already full is left
// alone. A store that cannot amend a queued payload reports false rather than
// failing the submit: the caller's refresh is still queued, just in its own mode.
func PromoteQueuedRefreshToFull(ctx context.Context, store Store, bankID, operationID string) (bool, error) {
	amender, ok := store.(OperationPayloadAmender)
	if !ok {
		return false, nil
	}
	var promoted bool
	locker, locked := store.(LifecycleLockedStore)
	if !locked {
		return amender.PromoteOperationPayload(ctx, bankID, operationID,
			RefreshModePayloadKey, "full", StatusPending)
	}
	err := locker.WithOperationSubjectLock(ctx, bankID, OperationTypeRefreshMentalModel, func() error {
		changed, err := amender.PromoteOperationPayload(ctx, bankID, operationID,
			RefreshModePayloadKey, "full", StatusPending)
		promoted = changed
		return err
	})
	return promoted, err
}

// RefreshParkCleaner is the optional capability a store provides when it can
// release a queued refresh's min-interval park: drop the automatic marker and
// clear next_retry_at, so "refresh now" folds into a queued automatic refresh
// without inheriting its wait (memory_engine.py:_clear_refresh_park).
type RefreshParkCleaner interface {
	// ClearOperationRefreshPark unparks the pending operation: the automatic
	// marker leaves its payload and next_retry_at is cleared in the same
	// write. It reports whether a row changed; a row that is gone or not
	// pending is left alone.
	ClearOperationRefreshPark(ctx context.Context, bankID, operationID string) (bool, error)
}

// ClearQueuedRefreshPark releases a queued refresh's min-interval park. A store
// that cannot amend a queued payload reports false rather than failing the
// submit: the caller's refresh is still queued, just still parked.
func ClearQueuedRefreshPark(ctx context.Context, store Store, bankID, operationID string) (bool, error) {
	cleaner, ok := store.(RefreshParkCleaner)
	if !ok {
		return false, nil
	}
	return cleaner.ClearOperationRefreshPark(ctx, bankID, operationID)
}

// OperationPayloadAmender sets one payload member on an operation that is still
// in the given status, so a queued refresh can be upgraded without changing
// what it works on.
type OperationPayloadAmender interface {
	// PromoteOperationPayload writes key=value into the operation's payload
	// JSON, only while the operation still carries the expected status, and
	// reports whether a row changed. It is a no-op when the member already
	// holds the value.
	PromoteOperationPayload(ctx context.Context, bankID, operationID, key string, value any, onlyStatus OperationStatus) (bool, error)
}
