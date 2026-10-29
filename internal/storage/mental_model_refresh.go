package storage

import (
	"context"
	"time"
)

// BankWriteWatermark returns the time of the newest write to a bank, or the
// zero time when the bank has never been written.
//
// A scoped staleness check asks this first: a model cannot be stale if nothing
// has been written to its bank since it last looked, so the expensive scoped
// query is skipped outright in the common case where a bank is quiet. That
// short-circuit is what keeps a cron tick over many models from paying for a
// scan each.
func (s *MemoryStore) BankWriteWatermark(ctx context.Context, bankID string) time.Time {
	if err := ctx.Err(); err != nil {
		return time.Time{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	bank, ok := s.banks[bankID]
	if !ok || bank.LastWriteAt == nil {
		return time.Time{}
	}
	return *bank.LastWriteAt
}

// MarkMentalModelSeen stamps the write watermark a refresh actually observed.
//
// It is what makes a refresh retryable: the stamp is written only after the new
// content is stored, so a refresh that failed leaves the model looking stale
// and the next tick tries again rather than waiting for new memories to arrive.
func (s *MemoryStore) MarkMentalModelSeen(ctx context.Context, bankID, id string, seenAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return s.writeErr
	}
	model, ok := s.mentalModelRecords[mentalModelKey(bankID, id)]
	if !ok {
		return ErrMentalModelNotFound
	}
	if !seenAt.IsZero() {
		model.LastMemorySeenAt = &seenAt
	}
	model.LastRefreshedAt = time.Now().UTC()
	// A successful refresh clears any park: the next failure is what parks it
	// again, so the counter does not survive a recovery.
	model.LastRefreshFailedAt = nil
	s.mentalModelRecords[mentalModelKey(bankID, id)] = model
	return nil
}

// RecordMentalModelRefreshFailure stamps a failed refresh.
//
// The watermark is deliberately not advanced: the window that failed is the
// window the next attempt must re-read. The failure time is what parks an
// automatic refresh, so a schedule that cannot succeed stops spending a model
// call on every tick.
func (s *MemoryStore) RecordMentalModelRefreshFailure(ctx context.Context, bankID, id string, failedAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return s.writeErr
	}
	model, ok := s.mentalModelRecords[mentalModelKey(bankID, id)]
	if !ok {
		return ErrMentalModelNotFound
	}
	model.LastRefreshFailedAt = &failedAt
	s.mentalModelRecords[mentalModelKey(bankID, id)] = model
	return nil
}

// BankWriteWatermark reads the newest memory write in the bank, which is what a
// mental model's staleness is measured against. A bank with no memories
// reports the zero time. The value is not stored: it is the newest
// memory_units.updated_at, which is where the reference reads it from.
func (s *PostgresStore) BankWriteWatermark(ctx context.Context, bankID string) time.Time {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return time.Time{}
	}
	var last *time.Time
	query := `SELECT MAX(updated_at) FROM ` +
		quoteIdent(schema) + `.memory_units WHERE bank_id = $1`
	if err := s.db.QueryRowContext(ctx, query, bankID).Scan(&last); err != nil {
		return time.Time{}
	}
	if last == nil {
		return time.Time{}
	}
	return *last
}

// MarkMentalModelSeen stamps the observed watermark and clears any park.
//
// A zero seenAt records the run as having seen nothing new: it keeps the
// watermark where it was and leaves `last_memory_seen_at` NULL when it was NULL
// rather than materializing a value there. The reference writes the watermark it
// read on a run that found no sources exactly when that read has one, so a model
// whose refreshes keep finding an empty scope keeps reporting a null watermark.
func (s *PostgresStore) MarkMentalModelSeen(ctx context.Context, bankID, id string, seenAt time.Time) error {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return schemaErr
	}
	query := `UPDATE ` + quoteIdent(schema) + `.mental_models
	          SET last_memory_seen_at = COALESCE($3::timestamptz, last_memory_seen_at),
	              last_refreshed_at = now(),
	              last_refresh_failed_at = NULL,
	              last_updated = now()
	          WHERE bank_id = $1 AND id = $2`
	var stamp any
	if !seenAt.IsZero() {
		stamp = seenAt.UTC()
	}
	result, err := s.db.ExecContext(ctx, query, bankID, id, stamp)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrMentalModelNotFound
	}
	return nil
}

// MarkMentalModelFullRefresh stamps that a refresh ran in full mode.
//
// It is separate from MarkMentalModelSeen because the two clocks answer
// different questions: the watermark says how far through the bank the last
// read got, this says when the model was last rebuilt from scratch. A delta
// fallback to full is a full run and stamps both; a plain delta stamps only the
// former.
func (s *MemoryStore) MarkMentalModelFullRefresh(ctx context.Context, bankID, id string, fullRefreshedAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return s.writeErr
	}
	model, ok := s.mentalModelRecords[mentalModelKey(bankID, id)]
	if !ok {
		return ErrMentalModelNotFound
	}
	stamp := fullRefreshedAt.UTC()
	model.LastFullRefreshedAt = &stamp
	s.mentalModelRecords[mentalModelKey(bankID, id)] = model
	return nil
}

// MarkMentalModelFullRefresh stamps that a refresh ran in full mode; see the
// memory backend for why it is a separate clock from the watermark.
func (s *PostgresStore) MarkMentalModelFullRefresh(ctx context.Context, bankID, id string, fullRefreshedAt time.Time) error {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return schemaErr
	}
	query := `UPDATE ` + quoteIdent(schema) + `.mental_models
	          SET last_full_refreshed_at = $3, last_updated = now()
	          WHERE bank_id = $1 AND id = $2`
	result, err := s.db.ExecContext(ctx, query, bankID, id, fullRefreshedAt.UTC())
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrMentalModelNotFound
	}
	return nil
}

// RecordMentalModelRefreshFailure parks the model without advancing the
// watermark: the window that failed is the window the next attempt re-reads.
func (s *PostgresStore) RecordMentalModelRefreshFailure(ctx context.Context, bankID, id string, failedAt time.Time) error {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return schemaErr
	}
	query := `UPDATE ` + quoteIdent(schema) + `.mental_models
	          SET last_refresh_failed_at = $3, last_updated = now()
	          WHERE bank_id = $1 AND id = $2`
	result, err := s.db.ExecContext(ctx, query, bankID, id, failedAt.UTC())
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrMentalModelNotFound
	}
	return nil
}

// MentalModelFullRefresher stamps that a refresh ran in full mode.
//
// It is a narrow capability rather than part of Store, like the staleness
// readers: the refresh runner asks for it at the one point where it knows the
// mode, and a backend that cannot record the stamp still serves every other
// mental-model route.
type MentalModelFullRefresher interface {
	// MarkMentalModelFullyRefreshed records that a refresh of this model ran
	// in full mode. It is never called for a delta run, and it does not touch
	// last_refreshed_at, which the refresh's own completion already moved.
	MarkMentalModelFullyRefreshed(ctx context.Context, bankID, id string) error
}

// MarkMentalModelFullyRefreshed is the capability's in-memory implementation.
func (s *MemoryStore) MarkMentalModelFullyRefreshed(ctx context.Context, bankID, id string) error {
	return s.MarkMentalModelFullRefresh(ctx, bankID, id, time.Now().UTC())
}

// MarkMentalModelFullyRefreshed is the capability's PostgreSQL implementation.
func (s *PostgresStore) MarkMentalModelFullyRefreshed(ctx context.Context, bankID, id string) error {
	return s.MarkMentalModelFullRefresh(ctx, bankID, id, time.Now().UTC())
}

// zeroTimeWhenNil reads a timestamp pointer as a value, zero when unset.
func zeroTimeWhenNil(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

// StampFullRefresh records that a refresh of the model ran in full mode, moving
// the clock trigger.full_refresh_cron comes due against.
//
// It goes through the narrow MentalModelFullRefresher capability rather than a
// method on Store, like the staleness readers: the refresh runner is the only
// caller, and a store that cannot record the stamp still serves every
// mental-model route. A store without the capability is a no-op, so a refresh
// never fails on a bookkeeping column.
func StampFullRefresh(ctx context.Context, store Store, bankID, id string) error {
	stamper, ok := store.(MentalModelFullRefresher)
	if !ok {
		return nil
	}
	return stamper.MarkMentalModelFullyRefreshed(ctx, bankID, id)
}
