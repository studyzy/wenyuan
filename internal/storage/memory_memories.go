package storage

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"time"
)

// unitWithinWindow reports whether a unit's selected timestamp falls inside the
// query's optional bounds.
//
// Naming a time axis excludes the rows that have no value on it, with or without
// bounds: "order by when it was mentioned" cannot place a memory that was never
// dated, so it says nothing rather than falling back to another axis. A query
// that named no axis keeps its historical meaning.
func unitWithinWindow(unit MemoryUnit, query MemoryQuery) bool {
	windowed := query.TimeField != "" || query.TimeStart != nil || query.TimeEnd != nil
	if !windowed {
		return true
	}
	// The window names a single axis; when only dates are given it defaults to
	// created_at, and rows with no value on it are excluded.
	column := query.TimeField
	if column == "" {
		column = "created_at"
	}
	instant := memoryTimeInstant(unit, column)
	if instant == nil {
		return false
	}
	if query.TimeStart != nil && instant.Before(*query.TimeStart) {
		return false
	}
	// Half-open [start, end): the upper bound is exclusive.
	if query.TimeEnd != nil && !instant.Before(*query.TimeEnd) {
		return false
	}
	return true
}

// memoryTimeInstant returns the timestamp a query's time_field selects, falling
// back to mentioned_at (the historical default) when no axis was named.
func memoryTimeInstant(unit MemoryUnit, timeField string) *time.Time {
	switch timeField {
	case "created_at":
		return unit.CreatedAt
	case "updated_at":
		return unit.UpdatedAt
	case "occurred_start":
		return unit.OccurredStart
	case "occurred_end":
		return unit.OccurredEnd
	default:
		return unit.MentionedAt
	}
}

// listTagsMatch applies the memory-list tag semantics to one unit, mirroring the
// reference's build_tags_where_clause: `any`/`all` also admit the untagged
// scope, `any_strict`/`all_strict` exclude it, and `exact` matches the tag set
// (an empty scope matching only untagged units).
func listTagsMatch(unitTags, wanted []string, mode string) bool {
	switch mode {
	case TagsMatchAll, TagsMatchAllStrict:
		if mode == TagsMatchAll && len(unitTags) == 0 {
			return true
		}
		for _, want := range wanted {
			if !containsTag(unitTags, want) {
				return false
			}
		}
		return len(wanted) > 0
	case TagsMatchExact:
		return sameTags(unitTags, wanted, false)
	default: // any, any_strict
		if mode == TagsMatchAny && len(unitTags) == 0 {
			return true
		}
		for _, want := range wanted {
			if containsTag(unitTags, want) {
				return true
			}
		}
		return false
	}
}

// unitMatchesConsolidationState applies the failed|pending|done filter, which
// only bears on source facts.
func unitMatchesConsolidationState(unit MemoryUnit, state string) bool {
	if state == "" {
		return true
	}
	// Only world/experience facts are consolidated; observations derive from them.
	if unit.FactType != TypeWorld && unit.FactType != TypeExperience {
		return false
	}
	switch state {
	case "failed":
		return unit.ConsolidationFailedAt != nil
	case "pending":
		return unit.ConsolidationFailedAt == nil && unit.ConsolidatedAt == nil
	case "done":
		return unit.ConsolidatedAt != nil
	default:
		return false
	}
}

// ListMemories returns one page of a bank's memory units, newest first.
func (s *MemoryStore) ListMemories(ctx context.Context, query MemoryQuery) (MemoryPage, error) {
	if err := ctx.Err(); err != nil {
		return MemoryPage{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.readErr != nil {
		return MemoryPage{}, s.readErr
	}
	source := s.units
	if query.State == "invalidated" {
		source = s.invalidated
	}
	matched := make([]MemoryUnit, 0, len(source))
	for _, unit := range source {
		if unit.BankID != query.BankID {
			continue
		}
		if query.Type != "" && string(unit.FactType) != query.Type {
			continue
		}
		if term := strings.ToLower(strings.TrimSpace(query.Q)); term != "" &&
			!strings.Contains(strings.ToLower(unit.Text), term) &&
			!strings.Contains(strings.ToLower(unit.Context), term) {
			continue
		}
		if query.DocumentID != "" && unit.DocumentID != query.DocumentID {
			continue
		}
		if query.EntityID != "" && !containsString(s.unitEntities[unit.ID], query.EntityID) {
			continue
		}
		if len(query.TagGroups) > 0 {
			// The caller's resolved read scope arrives as a compound tree and
			// replaces the flat tag filter, narrowing the listing to what the
			// scope admits.
			if !matchTagGroups(unit.Tags, query.TagGroups) {
				continue
			}
		} else if len(query.Tags) > 0 || query.TagsMatch == TagsMatchExact {
			mode := query.TagsMatch
			if mode == "" {
				mode = TagsMatchAny
			}
			if !listTagsMatch(unit.Tags, query.Tags, mode) {
				continue
			}
		}
		if !unitMatchesConsolidationState(unit, query.ConsolidationState) {
			continue
		}
		if !unitWithinWindow(unit, query) {
			continue
		}
		matched = append(matched, cloneMemoryUnit(unit))
	}
	windowed := query.TimeField != "" || query.TimeStart != nil || query.TimeEnd != nil
	column := query.TimeField
	if windowed && column == "" {
		column = "created_at"
	}
	sort.SliceStable(matched, func(i, j int) bool {
		if windowed {
			left, right := memoryTimeInstant(matched[i], column), memoryTimeInstant(matched[j], column)
			switch {
			case left == nil && right == nil:
				return matched[i].ID < matched[j].ID
			case left == nil:
				return false
			case right == nil:
				return true
			case !left.Equal(*right):
				return left.After(*right)
			default:
				return matched[i].ID < matched[j].ID
			}
		}
		// Default ordering: mentioned_at DESC, NULLS LAST, then created_at DESC.
		lm, rm := matched[i].MentionedAt, matched[j].MentionedAt
		switch {
		case lm == nil && rm == nil:
			// fall through to created_at
		case lm == nil:
			return false
		case rm == nil:
			return true
		default:
			if !lm.Equal(*rm) {
				return lm.After(*rm)
			}
		}
		lc, rc := matched[i].CreatedAt, matched[j].CreatedAt
		switch {
		case lc == nil && rc == nil:
			return matched[i].ID < matched[j].ID
		case lc == nil:
			return false
		case rc == nil:
			return true
		case !lc.Equal(*rc):
			return lc.After(*rc)
		}
		return matched[i].ID < matched[j].ID
	})
	total := len(matched)
	start, end := pageBounds(total, query.Offset, query.Limit)
	return MemoryPage{Memories: matched[start:end], Total: total,
		Limit: query.Limit, Offset: query.Offset}, nil
}

// MemoryAttachments returns each unit's attachments, keyed by unit id, for the
// memory-detail view. An observation with no attachment ids of its own inherits
// its source facts'.
func (s *MemoryStore) MemoryAttachments(ctx context.Context, bankID string, unitIDs []string) (map[string][]Attachment, error) {
	if len(unitIDs) == 0 {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	byIDs := map[string][]string{}
	var all []string
	seen := map[string]bool{}
	s.mu.RLock()
	for _, unitID := range unitIDs {
		unit, ok := s.units[unitID]
		if !ok || unit.BankID != bankID {
			continue
		}
		ids := unit.AttachmentIDs
		if len(ids) == 0 {
			ids = s.sourceAttachmentIDs(unitID)
		}
		unique := make([]string, 0, len(ids))
		local := map[string]bool{}
		for _, id := range ids {
			if local[id] {
				continue
			}
			local[id] = true
			unique = append(unique, id)
			if !seen[id] {
				seen[id] = true
				all = append(all, id)
			}
		}
		byIDs[unitID] = unique
	}
	s.mu.RUnlock()
	resolved, err := s.AttachmentsByShortIDs(ctx, bankID, all)
	if err != nil {
		return nil, err
	}
	out := map[string][]Attachment{}
	for unitID, ids := range byIDs {
		list := make([]Attachment, 0, len(ids))
		for _, id := range ids {
			if attachment, ok := resolved[id]; ok {
				list = append(list, attachment)
			}
		}
		if len(list) > 0 {
			out[unitID] = list
		}
	}
	return out, nil
}

// SourceMemories returns an observation's source facts straight from the live
// units map — never the invalidation archive — ordered by mention (newest first,
// nulls last). The in-process unit model keeps text and context as plain
// strings, so an empty value is reported as null (the closest it can get to the
// column's NULL).
func (s *MemoryStore) SourceMemories(ctx context.Context, bankID string, ids []string) ([]SourceMemoryRow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]SourceMemoryRow, 0, len(ids))
	for _, id := range ids {
		unit, ok := s.units[id]
		if !ok || unit.BankID != bankID {
			continue
		}
		row := SourceMemoryRow{ID: unit.ID, FactType: string(unit.FactType)}
		if unit.Text != "" {
			value := unit.Text
			row.Text = &value
		}
		if unit.Context != "" {
			value := unit.Context
			row.Context = &value
		}
		row.OccurredStart = unit.OccurredStart
		row.MentionedAt = unit.MentionedAt
		out = append(out, row)
	}
	sort.SliceStable(out, func(i, j int) bool {
		left, right := out[i].MentionedAt, out[j].MentionedAt
		switch {
		case left == nil && right == nil:
			return out[i].ID < out[j].ID
		case left == nil:
			return false
		case right == nil:
			return true
		default:
			return left.After(*right)
		}
	})
	return out, nil
}

// GetMemory returns one memory unit by identifier.
func (s *MemoryStore) GetMemory(ctx context.Context, bankID, memoryID string) (MemoryUnit, error) {
	if err := ctx.Err(); err != nil {
		return MemoryUnit{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	unit, ok := s.units[memoryID]
	if !ok {
		unit, ok = s.invalidated[memoryID]
	}
	if !ok || unit.BankID != bankID {
		return MemoryUnit{}, ErrMemoryNotFound
	}
	// An observation's evidence is kept as its own postings, so a unit read back
	// carries the source ids it was built from.
	if unit.FactType == TypeObservation {
		if sources, ok := s.observationSources[memoryID]; ok {
			unit.SourceMemoryIDs = append([]string(nil), sources...)
		}
	}
	return cloneMemoryUnit(unit), nil
}

// UpdateMemory applies a partial update to one memory unit.
//
// The unit's own content is written under the lock, but a content edit re-embeds
// the unit off it — an embedding provider is a network call and must not pin the
// store — and the observation sweep plus the follow-up submissions run after the
// lock is released, because each takes the lock again.
func (s *MemoryStore) UpdateMemory(ctx context.Context, bankID, memoryID string, patch MemoryPatch) (MemoryUnit, error) {
	if err := ctx.Err(); err != nil {
		return MemoryUnit{}, err
	}
	s.mu.Lock()
	unit, action, err := s.updateMemoryLocked(bankID, memoryID, patch)
	s.mu.Unlock()
	if err != nil {
		return MemoryUnit{}, err
	}
	switch action {
	case curationEdit:
		if embedding := s.applyEditEmbedding(ctx, bankID, memoryID, unit); len(embedding) > 0 {
			unit.Embedding = embedding
		}
		if _, err := s.DeleteStaleObservations(ctx, bankID, []string{memoryID}); err != nil {
			return MemoryUnit{}, err
		}
		s.submitEditFollowUps(ctx, bankID)
	case curationInvalidate:
		// The row left the live view, so the observations it was evidence for
		// are stale now.
		if _, err := s.DeleteStaleObservations(ctx, bankID, []string{memoryID}); err != nil {
			return MemoryUnit{}, err
		}
		s.submitEditFollowUps(ctx, bankID)
	case curationRevert:
		s.submitEditFollowUps(ctx, bankID)
	}
	return cloneMemoryUnit(unit), nil
}

// curationAction names what one curation write did, so the caller knows which
// background work to submit once the lock is released.
type curationAction int

const (
	curationNone curationAction = iota
	curationEdit
	curationInvalidate
	curationRevert
)

// updateMemoryLocked is the body of UpdateMemory that runs under the write
// lock. It reports the curation action so the caller can do the off-lock
// embedding and the follow-up submissions afterwards.
func (s *MemoryStore) updateMemoryLocked(bankID, memoryID string, patch MemoryPatch) (MemoryUnit, curationAction, error) {
	if s.writeErr != nil {
		return MemoryUnit{}, curationNone, s.writeErr
	}
	unit, ok := s.units[memoryID]
	archived := false
	if !ok {
		unit, ok = s.invalidated[memoryID]
		archived = true
	}
	if !ok || unit.BankID != bankID {
		return MemoryUnit{}, curationNone, ErrMemoryNotFound
	}
	if patch.Text != nil {
		unit.Text = *patch.Text
	}
	if patch.Tags != nil {
		unit.Tags = append([]string(nil), (*patch.Tags)...)
	}
	if patch.Metadata != nil {
		unit.Metadata = copyAnyMap(*patch.Metadata)
	}
	if patch.Context != nil {
		unit.Context = *patch.Context
	}
	if patch.FactType != nil {
		unit.FactType = MemoryUnitType(*patch.FactType)
	}
	if patch.ClearOccurredStart {
		unit.OccurredStart = nil
	}
	unit.OccurredStart = applyDatePatch(unit.OccurredStart, patch.OccurredStart)
	if patch.ClearOccurredEnd {
		unit.OccurredEnd = nil
	}
	unit.OccurredEnd = applyDatePatch(unit.OccurredEnd, patch.OccurredEnd)
	// An explicit occurred_start edit — including a clear — re-derives the
	// legacy single date with retain's own rule (occurred_start or
	// mentioned_at), as the reference does.
	if patch.ClearOccurredStart || patch.OccurredStart != nil {
		if unit.OccurredStart != nil {
			unit.EventDate = unit.OccurredStart
		} else {
			unit.EventDate = unit.MentionedAt
		}
	}
	if patch.Entities != nil {
		s.setUnitEntitiesLocked(bankID, unit.ID, *patch.Entities, patch.ResolveEntities == nil || *patch.ResolveEntities)
	}

	now := time.Now().UTC()
	action := curationNone
	switch {
	case patch.State == nil:
		if editTouchesContent(patch) {
			// A content edit resets the consolidation markers, so the fact is
			// folded again with its new text, exactly as the reference's
			// apply_edit does.
			unit.ConsolidatedAt = nil
			unit.ConsolidationFailedAt = nil
			delete(s.consolidated, memoryID)
			// The edit also drops the derived links, so a stale temporal or
			// semantic edge does not survive it; graph maintenance rebuilds the
			// ones that still hold, and the causal edges stay (#2864). The
			// survivors that pointed here, and the edited unit itself, are
			// queued for that rebuild before the rows go.
			s.enqueueRelinkVictimsLocked(bankID, []string{memoryID}, true)
			s.links = dropNonCausalLinksFor(s.links, memoryID)
			action = curationEdit
		}
		s.putUnitLocked(memoryID, unit)
	case *patch.State == "invalidated":
		if archived {
			// Already retired: a new reason is the only thing the repeat
			// changes; the invalidation time stands.
			if patch.Reason != nil {
				unit.InvalidationReason = patch.Reason
			}
			s.invalidated[memoryID] = unit
			break
		}
		unit.InvalidatedAt = &now
		unit.InvalidationReason = patch.Reason
		delete(s.units, memoryID)
		// Invalidating unlinks the unit, so its postings go with it and the
		// entities they named get their mention counts back — the same unlink
		// the postgres archive move performs.
		s.releaseUnitPostingsLocked(memoryID)
		delete(s.unitEntities, memoryID)
		s.invalidated[memoryID] = unit
		action = curationInvalidate
	case *patch.State == "valid":
		unit.InvalidatedAt = nil
		unit.InvalidationReason = nil
		delete(s.invalidated, memoryID)
		s.units[memoryID] = unit
		action = curationRevert
	default:
		s.putUnitLocked(memoryID, unit)
	}
	return cloneMemoryUnit(unit), action, nil
}

// applyEditEmbedding recomputes one edited unit's embedding and stores it: the
// same date+entity augmentation retain uses, over the unit's post-edit text and
// entity names. It runs off the write lock, and a failed or empty embed leaves
// the unit's existing vector in place rather than clearing it.
func (s *MemoryStore) applyEditEmbedding(ctx context.Context, bankID, memoryID string, unit MemoryUnit) []float32 {
	embedder := s.observationEmbedderRef()
	if embedder == nil {
		return nil
	}
	names := []string(nil)
	if byUnit, err := s.UnitEntityNames(ctx, bankID, []string{memoryID}); err == nil {
		names = byUnit[memoryID]
	}
	embedding := embedUnitText(ctx, embedder, unit, names)
	if len(embedding) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if stored, ok := s.units[memoryID]; ok {
		stored.Embedding = embedding
		s.units[memoryID] = stored
	}
	return embedding
}

// submitEditFollowUps queues the background work a curation write leaves behind:
// the consolidation pass that re-folds the fact (only when automatic
// consolidation is on for the bank) and a graph-maintenance sweep (unconditional,
// as the reference submits it with force_sweep=True). A failure to submit never
// fails the write; the periodic sweeps pick the work up.
func (s *MemoryStore) submitEditFollowUps(ctx context.Context, bankID string) {
	if s.autoConsolidationFor(ctx, bankID) {
		if _, _, err := s.EnqueueConsolidation(ctx, bankID); err != nil {
			slog.Default().Warn("could not submit consolidation after curating a memory",
				"bank_id", bankID, "error", err)
		}
	}
	if _, _, err := s.EnqueueGraphMaintenance(ctx, bankID); err != nil {
		slog.Default().Warn("could not submit graph maintenance after curating a memory",
			"bank_id", bankID, "error", err)
	}
}

// autoConsolidationFor resolves whether a curation write submits a consolidation
// pass: the bank's own enable_auto_consolidation override when set, the
// deployment default otherwise.
func (s *MemoryStore) autoConsolidationFor(ctx context.Context, bankID string) bool {
	if bank, err := s.GetBank(ctx, bankID); err == nil {
		if value, ok := bank.Config["enable_auto_consolidation"].(bool); ok {
			return value
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.autoConsolidation
}

// putUnitLocked stores a unit in whichever view currently holds it.
func (s *MemoryStore) putUnitLocked(memoryID string, unit MemoryUnit) {
	if _, ok := s.invalidated[memoryID]; ok {
		s.invalidated[memoryID] = unit
		return
	}
	s.units[memoryID] = unit
}

// SetUnitEntities replaces the entities one memory unit mentions.
//
// Names resolve through the same path retain uses, so a name that matches an
// existing entity is linked to it and a name that matches nothing creates one;
// the entities the caller leaves in place keep their link.
func (s *MemoryStore) SetUnitEntities(ctx context.Context, bankID, unitID string, names []string, resolve bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return s.writeErr
	}
	unit, ok := s.units[unitID]
	if !ok {
		unit, ok = s.invalidated[unitID]
	}
	if !ok || unit.BankID != bankID {
		return ErrMemoryNotFound
	}
	s.setUnitEntitiesLocked(bankID, unitID, names, resolve)
	return nil
}

// setUnitEntitiesLocked rewrites a unit's entity links. It expects s.mu held.
func (s *MemoryStore) setUnitEntitiesLocked(bankID, unitID string, names []string, resolve bool) {
	wanted := make([]string, 0, len(names))
	for _, name := range names {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		entity := s.resolveEntityOfKindLocked(bankID, trimmed, EntityKindRegular, resolve)
		if entity.ID == "" || slices.Contains(wanted, entity.ID) {
			continue
		}
		wanted = append(wanted, entity.ID)
	}
	current := s.unitEntities[unitID]
	// An edit that rewrites the entity set unlinks the entities the old set
	// named and links the ones the new set names, so each dropped posting gives
	// its mention back and each added one records a new mention — the count
	// tracks the set rather than drifting.
	now := time.Now().UTC()
	for _, entityID := range current {
		if slices.Contains(wanted, entityID) {
			continue
		}
		s.decrementEntityMentionLocked(entityID)
	}
	for _, entityID := range wanted {
		if slices.Contains(current, entityID) {
			continue
		}
		s.incrementEntityMentionLocked(entityID, now)
	}
	s.unitEntities[unitID] = wanted
}

// applyDatePatch applies a date/context patch: an absent value leaves the
// current one, an empty string clears it, and a valid RFC 3339 value replaces
// it.
func applyDatePatch(current *time.Time, raw *string) *time.Time {
	if raw == nil {
		return current
	}
	if *raw == "" {
		return nil
	}
	parsed, err := parseISO8601(*raw)
	if err != nil {
		return current
	}
	value := parsed.UTC()
	return &value
}

// parseISO8601 parses the ISO-8601 date/datetime forms the reference's
// datetime.fromisoformat accepts for an edit date: a date-only value, an
// offset-less or fractional datetime (read as UTC), and a trailing "Z" or
// numeric offset.
func parseISO8601(value string) (time.Time, error) {
	normalized := value
	if strings.HasSuffix(normalized, "Z") {
		normalized = normalized[:len(normalized)-1] + "+00:00"
	}
	layouts := []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
		"2006-01-02T15:04",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, normalized); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, errInvalidISO8601
}

// errInvalidISO8601 is returned for a date string in no accepted form.
var errInvalidISO8601 = errors.New("invalid ISO-8601 datetime")

// MemoryHistory returns an observation's recorded changes, oldest first, from
// the snapshots its consolidation rewrites wrote. Curation writes nothing here:
// only world/experience facts can be curated, and an observation's history is
// the record of how the conclusion was rewritten.
func (s *MemoryStore) MemoryHistory(ctx context.Context, bankID, memoryID string) ([]MemoryHistoryEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	unit, ok := s.units[memoryID]
	if !ok {
		unit, ok = s.invalidated[memoryID]
	}
	if !ok || unit.BankID != bankID {
		return nil, ErrMemoryNotFound
	}
	entries := append([]MemoryHistoryEntry(nil), s.memoryHistory[memoryID]...)
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].ChangedAt.Before(entries[j].ChangedAt)
	})
	return entries, nil
}

// ClearBankMemories removes a bank's memories. An empty factType is the full
// clear: the bank row survives, but every unit, document, chunk, entity,
// archived unit, observation snapshot and attachment it holds goes. A non-empty
// factType removes only that type's live and archived units, leaving the
// bank's documents, entities and other types in place.
func (s *MemoryStore) ClearBankMemories(ctx context.Context, bankID, factType string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return 0, s.writeErr
	}
	if factType == "" {
		return s.clearBankContentsLocked(bankID), nil
	}
	return s.clearBankMemoriesOfTypeLocked(bankID, factType), nil
}

// clearBankContentsLocked purges every domain row a bank holds, leaving the
// bank row itself. It is DeleteBank's purge without the bank row removal.
func (s *MemoryStore) clearBankContentsLocked(bankID string) int {
	removed := 0
	for id, unit := range s.units {
		if unit.BankID != bankID {
			continue
		}
		delete(s.units, id)
		delete(s.unitEntities, id)
		delete(s.observationSources, id)
		delete(s.memoryHistory, id)
		removed++
	}
	for id, unit := range s.invalidated {
		if unit.BankID == bankID {
			delete(s.invalidated, id)
		}
	}
	for id, document := range s.documents {
		if document.BankID == bankID {
			delete(s.documents, id)
		}
	}
	for id, entity := range s.entities {
		if entity.BankID == bankID {
			delete(s.entities, id)
		}
	}
	for key, attachment := range s.attachments {
		if attachment.BankID == bankID {
			delete(s.attachments, key)
		}
	}
	chunks := s.chunks[:0]
	for _, chunk := range s.chunks {
		if chunk.BankID == bankID {
			continue
		}
		chunks = append(chunks, chunk)
	}
	s.chunks = chunks
	links := s.links[:0]
	for _, link := range s.links {
		if link.BankID == bankID {
			continue
		}
		links = append(links, link)
	}
	s.links = links
	for key := range s.cooccurrences {
		if strings.HasPrefix(key, bankID+"\x00") {
			delete(s.cooccurrences, key)
		}
	}
	delete(s.memories, bankID)
	s.invalidateBankLocked(bankID)
	return removed
}

// clearBankMemoriesOfTypeLocked removes one bank's live and archived units of a
// single fact type, plus the links and associations hanging off them. The
// derived recall projection and the observation snapshots of the removed units
// go with them.
func (s *MemoryStore) clearBankMemoriesOfTypeLocked(bankID, factType string) int {
	removed := 0
	doomed := map[string]struct{}{}
	for id, unit := range s.units {
		if unit.BankID != bankID || string(unit.FactType) != factType {
			continue
		}
		doomed[id] = struct{}{}
		s.releaseUnitPostingsLocked(id)
		delete(s.units, id)
		delete(s.unitEntities, id)
		delete(s.observationSources, id)
		delete(s.memoryHistory, id)
		removed++
	}
	for id, unit := range s.invalidated {
		if unit.BankID == bankID && string(unit.FactType) == factType {
			delete(s.invalidated, id)
		}
	}
	// Links and the derived recall projection reference units by id, so both
	// have to drop the identifiers that just went away. The survivors that
	// pointed at them lose their outgoing edges with them, so they are queued
	// for a relink while the links are still readable.
	if len(doomed) > 0 {
		s.enqueueRelinkVictimsLocked(bankID, keysOf(doomed), false)
		kept := s.links[:0]
		for _, link := range s.links {
			if link.BankID != bankID {
				kept = append(kept, link)
				continue
			}
			if _, gone := doomed[link.FromUnitID]; gone {
				continue
			}
			if _, gone := doomed[link.ToUnitID]; gone {
				continue
			}
			kept = append(kept, link)
		}
		s.links = kept

		matches := s.memories[bankID]
		live := matches[:0]
		for _, match := range matches {
			if _, gone := doomed[match.ID]; gone {
				continue
			}
			live = append(live, match)
		}
		s.memories[bankID] = live

		// Every observation resting on one of the removed units is stale, so it
		// is dropped and its surviving co-sources are reset for re-consolidation,
		// mirroring the reference's stale-observation sweep on the typed clear.
		_, _ = s.deleteStaleObservationsLocked(bankID, keysOf(doomed))
	}
	s.invalidateBankLocked(bankID)
	return removed
}

// DeleteObservations removes the observations of a bank — or of one memory —
// and requeues the source facts behind them so the next consolidation pass
// regenerates them, matching the reference's clear_observations /
// clear_observations_for_memory.
func (s *MemoryStore) DeleteObservations(ctx context.Context, bankID, memoryID string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return 0, s.writeErr
	}
	if memoryID == "" {
		removed := 0
		for id, unit := range s.units {
			if unit.BankID != bankID || unit.FactType != TypeObservation {
				continue
			}
			delete(s.units, id)
			delete(s.unitEntities, id)
			delete(s.observationSources, id)
			delete(s.memoryHistory, id)
			removed++
		}
		s.requeueBankSourcesLocked(bankID)
		return removed, nil
	}
	removed, err := s.deleteStaleObservationsLocked(bankID, []string{memoryID})
	if err != nil {
		return 0, err
	}
	// The memory was a source of the deleted observations, so clear its own
	// consolidated marker too — it needs fresh observations.
	if removed > 0 {
		s.requeueSourceLocked(bankID, memoryID)
	}
	return removed, nil
}

// requeueBankSourcesLocked clears the consolidated marker on every world /
// experience fact in a bank. The caller holds the write lock.
func (s *MemoryStore) requeueBankSourcesLocked(bankID string) {
	for id, unit := range s.units {
		if unit.BankID != bankID || (unit.FactType != TypeWorld && unit.FactType != TypeExperience) {
			continue
		}
		unit.ConsolidatedAt = nil
		s.units[id] = unit
		delete(s.consolidated, id)
	}
}

// requeueSourceLocked clears one source fact's consolidated marker when it is a
// world / experience fact. The caller holds the write lock.
func (s *MemoryStore) requeueSourceLocked(bankID, unitID string) {
	unit, ok := s.units[unitID]
	if !ok || unit.BankID != bankID || (unit.FactType != TypeWorld && unit.FactType != TypeExperience) {
		return
	}
	unit.ConsolidatedAt = nil
	s.units[unitID] = unit
	delete(s.consolidated, unitID)
}

// EnqueueConsolidation records a consolidation pass for a bank. A scoped submit
// (observationScopes non-nil) never deduplicates against a pending full-bank
// pass — the caller wants a targeted run — and records the scopes in the
// payload; an unscoped submit reuses an outstanding pass for the bank.
func (s *MemoryStore) EnqueueConsolidation(ctx context.Context, bankID string, observationScopes ...[]string) (Operation, bool, error) {
	if err := ctx.Err(); err != nil {
		return Operation{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return Operation{}, false, s.writeErr
	}
	if len(observationScopes) == 0 {
		// A second consolidation for the same bank while one is outstanding
		// would do the same work twice, so the request is deduplicated.
		for _, op := range s.operations {
			if op.BankID == bankID && op.OperationType == OperationTypeConsolidate && !op.Status.Terminal() {
				return op, true, nil
			}
		}
	}
	now := time.Now().UTC()
	op := Operation{
		OperationID: newID(), BankID: bankID, OperationType: OperationTypeConsolidate,
		Status: StatusPending, CreatedAt: now, UpdatedAt: now,
		SerializationKey: ConsolidationSerializationKey(bankID),
	}
	if len(observationScopes) > 0 {
		op.Payload = map[string]any{"observation_scopes": observationScopes}
	}
	s.operations[op.OperationID] = op
	return op, false, nil
}

// EnqueueGraphMaintenance records a graph-maintenance pass for a bank, reusing
// one already pending or running for the bank: a sweep drains the bank's queues
// to empty, so a running one already covers work queued after it started.
func (s *MemoryStore) EnqueueGraphMaintenance(ctx context.Context, bankID string) (Operation, bool, error) {
	if err := ctx.Err(); err != nil {
		return Operation{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return Operation{}, false, s.writeErr
	}
	for _, op := range s.operations {
		if op.BankID == bankID && op.OperationType == OperationTypeGraphMaintenance && !op.Status.Terminal() {
			return op, true, nil
		}
	}
	now := time.Now().UTC()
	op := Operation{
		OperationID: newID(), BankID: bankID, OperationType: OperationTypeGraphMaintenance,
		Status: StatusPending, CreatedAt: now, UpdatedAt: now,
		SerializationKey: GraphMaintenanceSerializationKey(bankID),
	}
	s.operations[op.OperationID] = op
	return op, false, nil
}

// RecoverConsolidations returns a bank's permanently-failed sources to the
// not-yet-consolidated state and reports how many, then queues a pass so the
// recovery does not wait for the next sweep.
func (s *MemoryStore) RecoverConsolidations(ctx context.Context, bankID string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	retried := 0
	func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.writeErr != nil {
			return
		}
		for id, unit := range s.units {
			if unit.BankID != bankID || unit.ConsolidationFailedAt == nil {
				continue
			}
			unit.ConsolidationFailedAt = nil
			unit.ConsolidatedAt = nil
			s.units[id] = unit
			delete(s.consolidated, id)
			retried++
		}
	}()
	if retried == 0 {
		return 0, nil
	}
	if _, _, err := s.EnqueueConsolidation(ctx, bankID); err != nil {
		return retried, err
	}
	return retried, nil
}

// cloneMemoryUnit copies the mutable fields of a memory unit.
func cloneMemoryUnit(unit MemoryUnit) MemoryUnit {
	out := unit
	out.Tags = append([]string(nil), unit.Tags...)
	out.Embedding = append([]float32(nil), unit.Embedding...)
	out.Metadata = copyAnyMap(unit.Metadata)
	return out
}

// ListArchivedMemories returns a bank's curation archive as rows, oldest first.
func (s *MemoryStore) ListArchivedMemories(ctx context.Context, bankID string) ([]map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	units := make([]MemoryUnit, 0)
	for _, unit := range s.invalidated {
		if unit.BankID == bankID {
			units = append(units, cloneMemoryUnit(unit))
		}
	}
	sort.SliceStable(units, func(i, j int) bool { return archivedBefore(units[i], units[j]) })
	rows := make([]map[string]any, 0, len(units))
	for _, unit := range units {
		rows = append(rows, archivedMemoryRow(unit))
	}
	return rows, nil
}

// PutArchivedMemory inserts one archived row, leaving an id already archived
// alone.
func (s *MemoryStore) PutArchivedMemory(ctx context.Context, row map[string]any) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	id, _ := row["id"].(string)
	if id == "" {
		return false, ErrInvalidMemoryID
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.invalidated[id]; ok {
		return false, nil
	}
	if s.invalidated == nil {
		s.invalidated = map[string]MemoryUnit{}
	}
	s.invalidated[id] = archivedMemoryFromRow(row)
	return true, nil
}

// archivedBefore orders two archived units by invalidation time then id, which
// is the order the PostgreSQL read uses.
func archivedBefore(left, right MemoryUnit) bool {
	leftAt := timeOrZero(left.InvalidatedAt)
	rightAt := timeOrZero(right.InvalidatedAt)
	if !leftAt.Equal(rightAt) {
		return leftAt.Before(rightAt)
	}
	return left.ID < right.ID
}

func timeOrZero(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

// archivedMemoryRow renders one archived unit under the table's column names.
func archivedMemoryRow(unit MemoryUnit) map[string]any {
	row := map[string]any{
		"id":                      unit.ID,
		"bank_id":                 unit.BankID,
		"document_id":             unit.DocumentID,
		"text":                    unit.Text,
		"context":                 unit.Context,
		"event_date":              archivedTime(unit.EventDate),
		"occurred_start":          archivedTime(unit.OccurredStart),
		"occurred_end":            archivedTime(unit.OccurredEnd),
		"mentioned_at":            archivedTime(unit.MentionedAt),
		"fact_type":               string(unit.FactType),
		"metadata":                unit.Metadata,
		"created_at":              archivedTime(unit.CreatedAt),
		"updated_at":              archivedTime(unit.UpdatedAt),
		"chunk_id":                unit.ChunkID,
		"tags":                    nonNilStrings(unit.Tags),
		"proof_count":             unit.ProofCount,
		"source_memory_ids":       nonNilStrings(unit.SourceMemoryIDs),
		"consolidated_at":         archivedTime(unit.ConsolidatedAt),
		"observation_scopes":      unit.ObservationScopes,
		"consolidation_failed_at": archivedTime(unit.ConsolidationFailedAt),
		"edited_at":               archivedTime(unit.EditedAt),
		"invalidation_reason":     archivedString(unit.InvalidationReason),
		"invalidated_at":          archivedTime(unit.InvalidatedAt),
		"attachment_ids":          nonNilStrings(unit.AttachmentIDs),
	}
	return row
}

// archivedTime renders an optional timestamp the way the JSON a row is
// serialized to would: nil when absent, the value otherwise. A row built in
// process therefore has the same shape as one decoded from the archive.
func archivedTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return *value
}

// archivedString renders an optional string the way the JSON would.
func archivedString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

// archivedMemoryFromRow rebuilds the projection the in-memory archive keeps
// from a row. Columns the model does not hold are dropped, which is the
// documented asymmetry between the two backends.
func archivedMemoryFromRow(row map[string]any) MemoryUnit {
	unit := MemoryUnit{
		ID:                    rowStringValue(row, "id"),
		BankID:                rowStringValue(row, "bank_id"),
		DocumentID:            rowStringValue(row, "document_id"),
		Text:                  rowStringValue(row, "text"),
		Context:               rowStringValue(row, "context"),
		FactType:              MemoryUnitType(rowStringValue(row, "fact_type")),
		ChunkID:               rowStringValue(row, "chunk_id"),
		Tags:                  rowStringList(row, "tags"),
		SourceMemoryIDs:       rowStringList(row, "source_memory_ids"),
		AttachmentIDs:         rowStringList(row, "attachment_ids"),
		EventDate:             rowTimeValue(row, "event_date"),
		OccurredStart:         rowTimeValue(row, "occurred_start"),
		OccurredEnd:           rowTimeValue(row, "occurred_end"),
		MentionedAt:           rowTimeValue(row, "mentioned_at"),
		ConsolidatedAt:        rowTimeValue(row, "consolidated_at"),
		ConsolidationFailedAt: rowTimeValue(row, "consolidation_failed_at"),
		EditedAt:              rowTimeValue(row, "edited_at"),
		InvalidatedAt:         rowTimeValue(row, "invalidated_at"),
		CreatedAt:             rowTimeValue(row, "created_at"),
		UpdatedAt:             rowTimeValue(row, "updated_at"),
	}
	if value, ok := row["invalidation_reason"].(string); ok {
		unit.InvalidationReason = &value
	}
	if value, ok := row["metadata"].(map[string]any); ok {
		unit.Metadata = value
	}
	if number, ok := rowIntValue(row, "proof_count"); ok {
		unit.ProofCount = number
	}
	return unit
}

// rowStringValue reads a string field from a decoded row.
func rowStringValue(row map[string]any, key string) string {
	text, _ := row[key].(string)
	return text
}

// rowStringList reads a string array field from a decoded row.
func rowStringList(row map[string]any, key string) []string {
	switch value := row[key].(type) {
	case []string:
		return value
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out
	}
	return nil
}

// rowTimeValue reads a timestamp field from a decoded row. A row that travelled
// through the archive holds an RFC 3339 string; one built in process holds the
// time itself.
func rowTimeValue(row map[string]any, key string) *time.Time {
	switch value := row[key].(type) {
	case time.Time:
		return &value
	case string:
		if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
			return &parsed
		}
	}
	return nil
}

// rowIntValue reads an integer field from a decoded row.
func rowIntValue(row map[string]any, key string) (int, bool) {
	switch value := row[key].(type) {
	case int:
		return value, true
	case int64:
		return int(value), true
	case float64:
		return int(value), true
	case json.Number:
		parsed, err := value.Int64()
		if err != nil {
			return 0, false
		}
		return int(parsed), true
	}
	return 0, false
}

// nonNilStrings returns a non-nil copy of a string slice, because the archive
// columns are not null.
func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return append([]string(nil), values...)
}
