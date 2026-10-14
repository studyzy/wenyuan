package storage

import (
	"context"
	"sort"
	"strings"
	"time"
)

// The in-memory backend implements the maintenance capabilities too, so a job
// is exercised on the default offline path rather than only against a database.
// It covers one schema: the store is constructed for one, and the tenant
// extension reports that one.

// PurgeExpiredRows removes expired rows from the in-memory stand-in for one
// table.
func (s *MemoryStore) PurgeExpiredRows(ctx context.Context, schema, table, tsColumn string, cutoff time.Time, batchSize, maxBatches int) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return 0, s.writeErr
	}
	rows, ok := s.retentionRows[retentionKey(schema, table)]
	if !ok {
		return 0, nil
	}
	kept := rows[:0]
	removed := 0
	for _, row := range rows {
		if row.at.Before(cutoff) {
			removed++
			continue
		}
		kept = append(kept, row)
	}
	s.retentionRows[retentionKey(schema, table)] = kept
	return removed, nil
}

// retentionRow is one row in the in-memory retention stand-in.
type retentionRow struct {
	id string
	at time.Time
}

// RetentionKey identifies one table's rows in the in-memory stand-in.
func retentionKey(schema, table string) string { return schema + "." + table }

// SeedRetentionRow records a row for the retention jobs to consider. It exists
// so the jobs can be exercised without a database.
func (s *MemoryStore) SeedRetentionRow(schema, table, id string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.retentionRows == nil {
		s.retentionRows = map[string][]retentionRow{}
	}
	key := retentionKey(schema, table)
	s.retentionRows[key] = append(s.retentionRows[key], retentionRow{id: id, at: at})
}

// RetentionRowCount reports how many rows the stand-in holds for one table.
func (s *MemoryStore) RetentionRowCount(schema, table string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.retentionRows[retentionKey(schema, table)])
}

// BanksNeedingConsolidation reports banks holding eligible facts with no
// consolidation queued.
func (s *MemoryStore) BanksNeedingConsolidation(ctx context.Context, schema string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	pending := map[string]bool{}
	for _, op := range s.operations {
		if op.OperationType != OperationTypeConsolidate {
			continue
		}
		if op.Status == StatusPending || op.Status == StatusProcessing {
			pending[op.BankID] = true
		}
	}
	found := map[string]bool{}
	for _, unit := range s.units {
		if unit.ConsolidatedAt != nil || unit.ConsolidationFailedAt != nil {
			continue
		}
		if unit.FactType != TypeExperience && unit.FactType != TypeWorld {
			continue
		}
		if pending[unit.BankID] {
			continue
		}
		found[unit.BankID] = true
	}
	out := make([]string, 0, len(found))
	for bankID := range found {
		out = append(out, bankID)
	}
	sort.Strings(out)
	return out, nil
}

// MentalModelsWithSchedule returns the scheduled models with no refresh queued.
func (s *MemoryStore) MentalModelsWithSchedule(ctx context.Context, schema string) ([]ScheduledMentalModel, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	inFlight := map[string]bool{}
	for _, op := range s.operations {
		if op.OperationType != OperationTypeRefreshMentalModel {
			continue
		}
		if op.Status != StatusPending && op.Status != StatusProcessing {
			continue
		}
		if modelID, ok := op.Payload[MentalModelIDPayloadKey].(string); ok {
			inFlight[modelID] = true
		}
	}
	out := make([]ScheduledMentalModel, 0)
	for _, model := range s.mentalModelRecords {
		cron := strings.TrimSpace(stringFromTrigger(model.Trigger, "refresh_cron"))
		if cron == "" || inFlight[model.ID] {
			continue
		}
		out = append(out, ScheduledMentalModel{
			BankID:          model.BankID,
			MentalModelID:   model.ID,
			RefreshCron:     cron,
			LastRefreshedAt: model.LastRefreshedAt,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MentalModelID < out[j].MentalModelID })
	return out, nil
}

// PruneTerminalOperations removes settled operations past their retention.
func (s *MemoryStore) PruneTerminalOperations(ctx context.Context, schema string, cutoff time.Time, batchSize int) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if batchSize <= 0 {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return 0, s.writeErr
	}
	candidates := make([]string, 0)
	for id, op := range s.operations {
		if !op.Status.Terminal() || !op.UpdatedAt.Before(cutoff) {
			continue
		}
		candidates = append(candidates, id)
	}
	sort.Slice(candidates, func(i, j int) bool {
		left, right := s.operations[candidates[i]], s.operations[candidates[j]]
		if !left.UpdatedAt.Equal(right.UpdatedAt) {
			return left.UpdatedAt.Before(right.UpdatedAt)
		}
		return candidates[i] < candidates[j]
	})
	if len(candidates) > batchSize {
		candidates = candidates[:batchSize]
	}
	for _, id := range candidates {
		delete(s.operations, id)
	}
	return len(candidates), nil
}
