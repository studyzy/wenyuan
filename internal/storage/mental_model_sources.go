package storage

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"time"
)

// ScopeSourceChecker is the capability a backend implements to answer whether a
// model's refresh scope has anything to read at all.
//
// Having nothing to read is not a cheap reflect — it is the most expensive one
// there is: every forced retrieval turn comes back empty, the evidence
// guardrail then refuses every `done`, and the loop runs to its iteration limit
// and pays a forced synthesis on top. A bank whose default knowledge pages each
// enqueue a refresh the moment they are created would spend its whole model
// budget on worst-case reflects over an empty graph. So the caller asks first.
type ScopeSourceChecker interface {
	// ScopeHasSources reports whether any memory inside the model's scope,
	// written after `since` (zero means no lower bound), exists.
	ScopeHasSources(ctx context.Context, bankID string, model MentalModel, since time.Time) bool
}

// ScopeHasSources reports whether any in-scope memory exists.
func (s *MemoryStore) ScopeHasSources(ctx context.Context, bankID string, model MentalModel, since time.Time) bool {
	if err := ctx.Err(); err != nil {
		return false
	}
	scope := ScopeFromModel(model)
	scope.Since = since
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, unit := range s.units {
		if unit.BankID != bankID {
			continue
		}
		if !since.IsZero() {
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
			if written == nil || !written.After(since) {
				continue
			}
		}
		if scope.includesUnit(unit) {
			return true
		}
	}
	// The recall projection is the other half of a memory in this backend: a
	// seeded or derived match that has no unit row still counts as a source. Its
	// timestamps may be unset (a seeded match is timeless), so the window only
	// excludes a match that actually carries a time behind it.
	for _, match := range s.memories[bankID] {
		if !since.IsZero() {
			written := match.MentionedAt
			if written == nil {
				written = match.OccurredStart
			}
			if written != nil && !written.After(since) {
				continue
			}
		}
		if scope.includesUnit(MemoryUnit{FactType: MemoryUnitType(match.Type), Tags: match.Tags}) {
			return true
		}
	}
	return false
}

// MentalModelRefreshSnapshot is the database-time snapshot a refresh bounds its
// reads with, read through the model row: a model that no longer exists has no
// snapshot, which the caller reads as the zero time.
//
// It is the row-scoped reading the reference calls
// `_mental_model_refresh_cutoff`, and it is deliberately not the bank write
// watermark: a snapshot says "everything committed now", a watermark says "the
// newest memory in the bank", and a dry run reports both — the first as the
// window's upper bound, the second as the watermark a refresh would persist.
func (s *MemoryStore) MentalModelRefreshSnapshot(ctx context.Context, bankID, modelID string) time.Time {
	if err := ctx.Err(); err != nil {
		return time.Time{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.mentalModelRecords[mentalModelKey(bankID, modelID)]; !ok {
		return time.Time{}
	}
	return time.Now().UTC()
}

// MentalModelRefreshSnapshot reads the database clock through the model row.
func (s *PostgresStore) MentalModelRefreshSnapshot(ctx context.Context, bankID, modelID string) time.Time {
	var snapshot *time.Time
	query := `SELECT now() FROM ` + quoteIdent(s.schema) + `.mental_models
	          WHERE bank_id = $1 AND id = $2`
	if err := s.db.QueryRowContext(ctx, query, bankID, modelID).Scan(&snapshot); err != nil || snapshot == nil {
		return time.Time{}
	}
	return *snapshot
}

// MentalModelScopeWatermark is the newest in-scope memory visible at the
// snapshot: the value a successful refresh persists as last_memory_seen_at. The
// zero time means the scope held no memory at all.
//
// The scope is the resolved one a refresh reads with — tag_groups override flat
// tags, tags default to all_strict when present, fact_types narrows further —
// because a watermark taken over the whole bank would claim the document had
// seen memories it never had in scope.
func (s *MemoryStore) MentalModelScopeWatermark(ctx context.Context, bankID string, model MentalModel, cutoff time.Time) time.Time {
	if err := ctx.Err(); err != nil || cutoff.IsZero() {
		return time.Time{}
	}
	return s.BankWriteWatermark(ctx, bankID)
}

// MentalModelScopeWatermark asks the in-scope memories for their newest write.
func (s *PostgresStore) MentalModelScopeWatermark(ctx context.Context, bankID string, model MentalModel, cutoff time.Time) time.Time {
	if err := ctx.Err(); err != nil || cutoff.IsZero() {
		return time.Time{}
	}
	scope := ScopeFromModel(model)
	clauses := []string{"bank_id = $1"}
	args := []any{bankID}
	args = append(args, cutoff)
	clauses = append(clauses, "COALESCE(updated_at, mentioned_at, occurred_start) <= $"+itoa(len(args))+"::timestamptz")
	if len(scope.FactTypes) > 0 {
		args = append(args, scope.FactTypes)
		clauses = append(clauses, "fact_type = ANY($"+itoa(len(args))+"::text[])")
	}
	if predicate, predicateArgs := tagPredicate(scope, len(args)); predicate != "" {
		args = append(args, predicateArgs...)
		clauses = append(clauses, predicate)
	}
	query := `SELECT MAX(COALESCE(updated_at, mentioned_at, occurred_start)) FROM ` + quoteIdent(s.schema) + `.memory_units
	          WHERE ` + strings.Join(clauses, " AND ")
	var newest *time.Time
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&newest); err != nil || newest == nil {
		return time.Time{}
	}
	return *newest
}

// ScopeHasSources asks the database whether any in-scope memory exists.
func (s *PostgresStore) ScopeHasSources(ctx context.Context, bankID string, model MentalModel, since time.Time) bool {
	if err := ctx.Err(); err != nil {
		return false
	}
	scope := ScopeFromModel(model)
	clauses := []string{"bank_id = $1"}
	args := []any{bankID}
	if !since.IsZero() {
		args = append(args, since)
		clauses = append(clauses, "COALESCE(updated_at, mentioned_at, occurred_start) > $"+itoa(len(args)))
	}
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
	query := `SELECT 1 FROM ` + quoteIdent(s.schema) + `.memory_units
	          WHERE ` + strings.Join(clauses, " AND ") + `
	          LIMIT 1`
	var found int
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&found)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		// An existence check cannot report an error, and its caller reads a
		// failure as "nothing in scope" - which refuses the refresh. Log it, so
		// a broken statement is not indistinguishable from an empty bank.
		slog.Default().Warn("could not check whether a mental model's scope has sources",
			"bank_id", bankID, "mental_model_id", model.ID, "error", err)
	}
	return err == nil
}
