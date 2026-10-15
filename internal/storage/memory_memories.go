package storage

import (
	"context"
	"encoding/json"
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
	instant := memoryTimeInstant(unit, query.TimeField)
	if query.TimeField != "" && instant == nil {
		return false
	}
	if query.TimeStart == nil && query.TimeEnd == nil {
		return true
	}
	if instant == nil {
		return false
	}
	if query.TimeStart != nil && instant.Before(*query.TimeStart) {
		return false
	}
	if query.TimeEnd != nil && instant.After(*query.TimeEnd) {
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

// unitMatchesConsolidationState applies the failed|pending|done filter, which
// only bears on source facts.
func unitMatchesConsolidationState(unit MemoryUnit, state string) bool {
	switch state {
	case "failed":
		return unit.ConsolidationFailedAt != nil
	case "pending":
		return unit.ConsolidationFailedAt == nil && unit.ConsolidatedAt == nil
	case "done":
		return unit.ConsolidatedAt != nil
	default:
		return true
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
			!strings.Contains(strings.ToLower(unit.Text), term) {
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
		} else if len(query.Tags) > 0 {
			mode := query.TagsMatch
			if mode == "" {
				mode = TagsMatchAny
			}
			if !matchFlatTags(unit.Tags, query.Tags, mode) {
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
	sort.SliceStable(matched, func(i, j int) bool {
		left, right := memoryTimeInstant(matched[i], query.TimeField), memoryTimeInstant(matched[j], query.TimeField)
		switch {
		case left == nil && right == nil:
			return matched[i].ID < matched[j].ID
		case left == nil:
			return false
		case right == nil:
			return true
		default:
			if !left.Equal(*right) {
				return left.After(*right)
			}
			return matched[i].ID < matched[j].ID
		}
	})
	total := len(matched)
	start, end := pageBounds(total, query.Offset, query.Limit)
	return MemoryPage{Memories: matched[start:end], Total: total,
		Limit: query.Limit, Offset: query.Offset}, nil
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
	return cloneMemoryUnit(unit), nil
}

// UpdateMemory applies a partial update to one memory unit.
func (s *MemoryStore) UpdateMemory(ctx context.Context, bankID, memoryID string, patch MemoryPatch) (MemoryUnit, error) {
	if err := ctx.Err(); err != nil {
		return MemoryUnit{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return MemoryUnit{}, s.writeErr
	}
	unit, ok := s.units[memoryID]
	if !ok {
		unit, ok = s.invalidated[memoryID]
	}
	if !ok || unit.BankID != bankID {
		return MemoryUnit{}, ErrMemoryNotFound
	}
	entry := MemoryHistoryEntry{
		ChangedAt:             time.Now().UTC(),
		Reason:                historyReason(patch),
		Text:                  unit.Text,
		Tags:                  append([]string(nil), unit.Tags...),
		PreviousText:          stringPtr(unit.Text),
		PreviousTags:          append([]string(nil), unit.Tags...),
		PreviousOccurredStart: unit.OccurredStart,
		PreviousOccurredEnd:   unit.OccurredEnd,
		PreviousMentionedAt:   unit.MentionedAt,
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
	unit.OccurredStart = applyDatePatch(unit.OccurredStart, patch.OccurredStart)
	unit.OccurredEnd = applyDatePatch(unit.OccurredEnd, patch.OccurredEnd)
	if patch.Entities != nil {
		s.setUnitEntitiesLocked(bankID, unit.ID, *patch.Entities, patch.ResolveEntities == nil || *patch.ResolveEntities)
	}

	now := time.Now().UTC()
	switch {
	case patch.State == nil:
		s.putUnitLocked(memoryID, unit)
	case *patch.State == "invalidated":
		unit.InvalidatedAt = &now
		unit.InvalidationReason = patch.Reason
		delete(s.units, memoryID)
		s.invalidated[memoryID] = unit
	case *patch.State == "valid":
		unit.InvalidatedAt = nil
		unit.InvalidationReason = nil
		delete(s.invalidated, memoryID)
		s.units[memoryID] = unit
	default:
		s.putUnitLocked(memoryID, unit)
	}

	// Every change is recorded, so a memory's history explains its current
	// state rather than only its creation.
	if s.memoryHistory == nil {
		s.memoryHistory = map[string][]MemoryHistoryEntry{}
	}
	entry.Text = unit.Text
	entry.Tags = append([]string(nil), unit.Tags...)
	s.memoryHistory[memoryID] = append(s.memoryHistory[memoryID], entry)
	return cloneMemoryUnit(unit), nil
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
	parsed, err := time.Parse(time.RFC3339, *raw)
	if err != nil {
		return current
	}
	value := parsed.UTC()
	return &value
}

// historyReason is the label recorded on a history entry, preferring the
// caller's invalidation reason.
func historyReason(patch MemoryPatch) string {
	if patch.Reason != nil && *patch.Reason != "" {
		return *patch.Reason
	}
	return "update"
}

// stringPtr returns a pointer to a copy of value.
func stringPtr(value string) *string { return &value }

// MemoryHistory returns the recorded changes of one memory unit, newest first.
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
		return entries[i].ChangedAt.After(entries[j].ChangedAt)
	})
	return entries, nil
}

// ClearBankMemories removes a bank's memory units and the rows hanging off them
// — links and entity associations — while leaving the bank, its entities and
// its documents in place. An empty factType clears every type.
func (s *MemoryStore) ClearBankMemories(ctx context.Context, bankID, factType string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return 0, s.writeErr
	}
	removed := 0
	doomed := map[string]struct{}{}
	for id, unit := range s.units {
		if unit.BankID != bankID {
			continue
		}
		if factType != "" && string(unit.FactType) != factType {
			continue
		}
		doomed[id] = struct{}{}
		delete(s.units, id)
		delete(s.unitEntities, id)
		delete(s.observationSources, id)
		removed++
	}
	// Links and the derived recall projection reference units by id, so both
	// have to drop the identifiers that just went away.
	if len(doomed) > 0 {
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
	}
	s.invalidateBankLocked(bankID)
	if factType == "" {
		// A cleared bank goes on existing, so nothing cascades from the bank
		// row: an image that outlived the clear would stay downloadable from a
		// bank with no documents.
		for key, attachment := range s.attachments {
			if attachment.BankID == bankID {
				delete(s.attachments, key)
			}
		}
	}
	return removed, nil
}

// DeleteObservations removes the observations of a bank, or of one memory.
func (s *MemoryStore) DeleteObservations(ctx context.Context, bankID, memoryID string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return 0, s.writeErr
	}
	removed := 0
	for id, unit := range s.units {
		if unit.BankID != bankID || unit.FactType != TypeObservation {
			continue
		}
		if memoryID != "" {
			sources := s.observationSources[id]
			if !containsString(sources, memoryID) {
				continue
			}
		}
		delete(s.units, id)
		delete(s.unitEntities, id)
		delete(s.observationSources, id)
		removed++
	}
	return removed, nil
}

// EnqueueConsolidation records a consolidation pass for a bank.
func (s *MemoryStore) EnqueueConsolidation(ctx context.Context, bankID string) (Operation, bool, error) {
	if err := ctx.Err(); err != nil {
		return Operation{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return Operation{}, false, s.writeErr
	}
	// A second consolidation for the same bank while one is outstanding would
	// do the same work twice, so the request is deduplicated.
	for _, op := range s.operations {
		if op.BankID == bankID && op.OperationType == OperationTypeConsolidate && !op.Status.Terminal() {
			return op, true, nil
		}
	}
	now := time.Now().UTC()
	op := Operation{
		OperationID: newID(), BankID: bankID, OperationType: OperationTypeConsolidate,
		Status: StatusPending, CreatedAt: now, UpdatedAt: now,
		SerializationKey: ConsolidationSerializationKey(bankID),
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
