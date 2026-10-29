package storage

import (
	"context"
	"database/sql"
	"sort"
	"time"
)

// Observation scoping for consolidation.
//
// A retain item may ask for its facts to be consolidated at a scope other than
// "all of my tags together" (see RetainItem.ObservationScopes). When it does,
// consolidation folds the fact at each resolved scope in its own pass and the
// observation it produces carries that scope. This file owns the storage side
// of that: reading only the observations of one scope, and writing a produced
// observation at its scope.
//
// The scope is stored as the observation's own tags, which is the reference's
// representation and what recall already filters on. Nothing else carries it:
// the reference's observation insert names only id, bank_id, text, fact_type,
// embedding, proof_count, source_memory_ids, tags and the temporal columns
// (engine/memories/pg/consolidation.py:268-352), so a produced observation's
// metadata is the column default `{}` and the scope listing reads the tags.

// ConsolidationScopeStore is the optional capability a store provides to
// support observation scoping. The base Store interface's consolidation methods
// are scope-less for compatibility; a store that implements this interface lets
// a consolidation pass read the observations of one scope and stamp a produced
// observation with its scope.
type ConsolidationScopeStore interface {
	// ConsolidationObservationsInScope returns the bank's observations whose
	// tag scope equals scope, newest first.
	ConsolidationObservationsInScope(ctx context.Context, bankID string, scope []string, limit int) ([]ConsolidationObservation, error)
	// CreateObservationInScope records a new observation at the given scope.
	CreateObservationInScope(ctx context.Context, bankID, text string, sourceIDs, scope []string) (string, error)
}

// canonicalScope returns a scope's tags sorted and deduplicated, the canonical
// form two scopes are compared in. An untagged scope stays nil, so no scope and
// an empty one compare equal.
func canonicalScope(scope []string) []string {
	if len(scope) == 0 {
		return nil
	}
	out := append([]string(nil), scope...)
	sort.Strings(out)
	kept := out[:1]
	for _, tag := range out[1:] {
		if tag != kept[len(kept)-1] {
			kept = append(kept, tag)
		}
	}
	return kept
}

// sameScope reports whether two scopes name the same canonical tag set.
func sameScope(a, b []string) bool {
	left, right := canonicalScope(a), canonicalScope(b)
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// ConsolidationObservationsInScope returns the observations whose tag scope
// equals the given scope, newest first.
func (s *MemoryStore) ConsolidationObservationsInScope(ctx context.Context, bankID string, scope []string, limit int) ([]ConsolidationObservation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.readErr != nil {
		return nil, s.readErr
	}
	var candidates []ConsolidationObservation
	for _, unit := range s.units {
		if unit.BankID != bankID || unit.FactType != TypeObservation {
			continue
		}
		if !sameScope(unit.Tags, scope) {
			continue
		}
		sources := append([]string(nil), s.observationSources[unit.ID]...)
		candidates = append(candidates, ConsolidationObservation{
			ID: unit.ID, Text: unit.Text, SourceIDs: sources,
			SourceTexts: s.sourceTextsLocked(sources),
			ProofCount:  len(sources), MentionedAt: unit.MentionedAt,
			Scope: canonicalScope(unit.Tags),
		})
	}
	sortNewestFirst(candidates, func(o ConsolidationObservation) *time.Time { return o.MentionedAt },
		func(a, b ConsolidationObservation) bool { return a.ID < b.ID })
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates, nil
}

// CountObservationsInScope reports how many observations the bank holds at the
// scope, with no limit: the scope's observation cap is measured against the
// true count, not the newest slice the candidate pool reads (the reference's
// count-to-the-end fix, engine/memories/base.py:3838-3857).
func (s *MemoryStore) CountObservationsInScope(ctx context.Context, bankID string, scope []string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.readErr != nil {
		return 0, s.readErr
	}
	count := 0
	for _, unit := range s.units {
		if unit.BankID == bankID && unit.FactType == TypeObservation && sameScope(unit.Tags, scope) {
			count++
		}
	}
	return count, nil
}

// sourceTextsLocked returns the texts of the given units in the order given,
// skipping an id the store no longer holds. The caller holds the read lock.
func (s *MemoryStore) sourceTextsLocked(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	byID := make(map[string]string, len(ids))
	for _, unit := range s.units {
		byID[unit.ID] = unit.Text
	}
	texts := make([]string, 0, len(ids))
	for _, id := range ids {
		if text, ok := byID[id]; ok {
			texts = append(texts, text)
		}
	}
	return texts
}

// CreateObservationInScope records a new observation at the given scope.
func (s *MemoryStore) CreateObservationInScope(ctx context.Context, bankID, text string, sourceIDs, scope []string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	scope = canonicalScope(scope)
	observation := MemoryUnit{
		BankID: bankID, Text: text, FactType: TypeObservation,
		Tags: scope,
		// An observation has no context column value of its own; see
		// CreateObservation.
		ContextAbsent: true,
	}
	s.sourceObservationBoundsLocked(bankID, sourceIDs).ApplyTo(&observation)
	observation.Embedding = embedObservation(ctx, s.observationEmbedderRef(), text)
	unit, err := s.UpsertUnit(ctx, observation)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sourceID := range sourceIDs {
		if !containsString(s.observationSources[unit.ID], sourceID) {
			s.observationSources[unit.ID] = append(s.observationSources[unit.ID], sourceID)
		}
	}
	return unit.ID, nil
}

// ConsolidationObservationsInScope returns the observations whose tag scope
// equals the given scope, newest first. Scope equality is an exact tag-set
// match: an untagged scope returns only untagged observations, so a pass never
// offers a differently-scoped observation as a merge candidate.
func (s *PostgresStore) ConsolidationObservationsInScope(ctx context.Context, bankID string, scope []string, limit int) ([]ConsolidationObservation, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return nil, schemaErr
	}
	if limit <= 0 {
		limit = 100
	}
	scope = canonicalScope(scope)
	// An empty scope must reach the driver as an empty array, not NULL: a NULL
	// would make the containment test below NULL and match no row at all.
	scopeArg := scope
	if scopeArg == nil {
		scopeArg = []string{}
	}
	query := `SELECT u.id, u.text, u.mentioned_at,
	                 COALESCE(u.source_memory_ids, '{}'::uuid[])::text,
	                 COALESCE(u.tags, '{}'::text[])::text,
	                 COALESCE(src.texts, ARRAY[]::text[])::text
	          FROM ` + quoteIdent(schema) + `.memory_units u
	          LEFT JOIN LATERAL (
	              SELECT array_agg(m.text ORDER BY source.ord) AS texts
	              FROM unnest(u.source_memory_ids) WITH ORDINALITY AS source(sid, ord)
	              JOIN ` + quoteIdent(schema) + `.memory_units m ON m.id = source.sid
	          ) src ON true
	          WHERE u.bank_id = $1 AND u.fact_type = $2
	            AND cardinality(COALESCE(u.tags, '{}'::text[])) = $3
	            AND COALESCE(u.tags, '{}'::text[])::text[] @> $4::text[]
	          ORDER BY u.created_at DESC
	          LIMIT $5`
	rows, err := s.db.QueryContext(ctx, query, bankID, string(TypeObservation), len(scope), scopeArg, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	observations := []ConsolidationObservation{}
	for rows.Next() {
		var observation ConsolidationObservation
		var mentioned sql.NullTime
		var sources, tags, sourceTexts string
		if err := rows.Scan(&observation.ID, &observation.Text, &mentioned, &sources, &tags, &sourceTexts); err != nil {
			return nil, err
		}
		observation.MentionedAt = nullTimePtr(mentioned)
		observation.SourceIDs = decodeTextArray(sources)
		observation.SourceTexts = decodeTextArray(sourceTexts)
		observation.ProofCount = len(observation.SourceIDs)
		observation.Scope = canonicalScope(decodeTextArray(tags))
		observations = append(observations, observation)
	}
	return observations, rows.Err()
}

// CountObservationsInScope reports how many observations the bank holds at the
// scope, with no limit: the scope's observation cap is measured against the
// true count, not the newest slice the candidate pool reads (the reference's
// count-to-the-end fix, engine/memories/base.py:3838-3857).
func (s *PostgresStore) CountObservationsInScope(ctx context.Context, bankID string, scope []string) (int, error) {
	schema, err := s.schemaFor(ctx)
	if err != nil {
		return 0, err
	}
	scope = canonicalScope(scope)
	scopeArg := scope
	if scopeArg == nil {
		scopeArg = []string{}
	}
	var count int
	err = s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM `+quoteIdent(schema)+`.memory_units u
		  WHERE u.bank_id = $1 AND u.fact_type = $2
		    AND cardinality(COALESCE(u.tags, '{}'::text[])) = $3
		    AND COALESCE(u.tags, '{}'::text[])::text[] @> $4::text[]`,
		bankID, string(TypeObservation), len(scope), scopeArg).Scan(&count)
	return count, err
}

// CreateObservationInScope records a new observation at the given scope.
func (s *PostgresStore) CreateObservationInScope(ctx context.Context, bankID, text string, sourceIDs, scope []string) (string, error) {
	scope = canonicalScope(scope)
	unit := MemoryUnit{
		BankID: bankID, Text: text, FactType: TypeObservation,
		Tags: scope,
		// An observation has no context column value of its own; see
		// CreateObservation.
		ContextAbsent: true,
	}
	bounds, err := s.sourceObservationBounds(ctx, bankID, sourceIDs)
	if err != nil {
		return "", err
	}
	bounds.ApplyTo(&unit)
	unit.Embedding = embedObservation(ctx, s.observationEmbedder, text)
	stored, err := s.UpsertUnit(ctx, unit)
	if err != nil {
		return "", err
	}
	if err := s.linkObservationSources(ctx, stored.ID, sourceIDs); err != nil {
		return "", err
	}
	return stored.ID, nil
}

// EnqueueConsolidationScoped records a consolidation pass restricted to the
// given observation scopes, never deduplicating it against a pending full-bank
// pass. It exists for the one submit EnqueueConsolidation's variadic cannot
// express: an explicit but empty scope list, which the reference submits as a
// scoped (hence non-deduplicated) run matching no memory rather than folding
// into a full-bank sweep. The scopes are carried in the task payload as sent.
func (s *PostgresStore) EnqueueConsolidationScoped(ctx context.Context, bankID string, observationScopes [][]string) (Operation, bool, error) {
	if observationScopes == nil {
		observationScopes = [][]string{}
	}
	op := Operation{
		OperationID: newID(), BankID: bankID, OperationType: OperationTypeConsolidate,
		Status: StatusPending, SerializationKey: ConsolidationSerializationKey(bankID),
		// The scoped submit is the route's own, and the route's chain keeps
		// draining past the auto-consolidation gate (api/http.py:9992).
		Payload: map[string]any{"observation_scopes": observationScopes, "caller_requested": true},
	}
	op, err := s.PutOperation(ctx, op)
	if err != nil {
		return Operation{}, false, err
	}
	return op, false, nil
}

// EnqueueConsolidationScoped is the in-process counterpart of the Postgres
// implementation: a scoped submit, including an empty scope list, that never
// deduplicates against an outstanding pass for the bank.
func (s *MemoryStore) EnqueueConsolidationScoped(ctx context.Context, bankID string, observationScopes [][]string) (Operation, bool, error) {
	if err := ctx.Err(); err != nil {
		return Operation{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return Operation{}, false, s.writeErr
	}
	if observationScopes == nil {
		observationScopes = [][]string{}
	}
	now := time.Now().UTC()
	op := Operation{
		OperationID: newID(), BankID: bankID, OperationType: OperationTypeConsolidate,
		Status: StatusPending, CreatedAt: now, UpdatedAt: now,
		SerializationKey: ConsolidationSerializationKey(bankID),
		// The scoped submit is the route's own, and the route's chain keeps
		// draining past the auto-consolidation gate (api/http.py:9992).
		Payload: map[string]any{"observation_scopes": observationScopes, "caller_requested": true},
	}
	s.operations[op.OperationID] = op
	return op, false, nil
}
