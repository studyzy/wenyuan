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
	var last *time.Time
	query := `SELECT MAX(updated_at) FROM ` +
		quoteIdent(s.schema) + `.memory_units WHERE bank_id = $1`
	if err := s.db.QueryRowContext(ctx, query, bankID).Scan(&last); err != nil {
		return time.Time{}
	}
	if last == nil {
		return time.Time{}
	}
	return *last
}

// MarkMentalModelSeen stamps the observed watermark and clears any park.
func (s *PostgresStore) MarkMentalModelSeen(ctx context.Context, bankID, id string, seenAt time.Time) error {
	query := `UPDATE ` + quoteIdent(s.schema) + `.mental_models
	          SET last_memory_seen_at = COALESCE($3, last_memory_seen_at),
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

// RecordMentalModelRefreshFailure parks the model without advancing the
// watermark: the window that failed is the window the next attempt re-reads.
func (s *PostgresStore) RecordMentalModelRefreshFailure(ctx context.Context, bankID, id string, failedAt time.Time) error {
	query := `UPDATE ` + quoteIdent(s.schema) + `.mental_models
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
