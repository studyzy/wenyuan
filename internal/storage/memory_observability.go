package storage

import (
	"context"
	"sort"
	"time"
)

// BankStats summarises one bank's operational state.
func (s *MemoryStore) BankStats(ctx context.Context, bankID string) (BankStats, error) {
	if err := ctx.Err(); err != nil {
		return BankStats{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.readErr != nil {
		return BankStats{}, s.readErr
	}

	stats := BankStats{
		BankID:             bankID,
		NodesByFactType:    map[string]int{},
		LinksByLinkType:    map[string]int{},
		OperationsByStatus: map[string]int{},
	}
	for _, unit := range s.units {
		if unit.BankID != bankID {
			continue
		}
		stats.TotalNodes++
		stats.NodesByFactType[string(unit.Type)]++
	}
	for _, link := range s.links {
		if link.BankID != bankID {
			continue
		}
		stats.TotalLinks++
		stats.LinksByLinkType[string(link.LinkType)]++
	}
	for _, doc := range s.documents {
		if doc.BankID == bankID {
			stats.TotalDocuments++
		}
	}
	for _, op := range s.operations {
		if op.BankID != bankID {
			continue
		}
		stats.OperationsByStatus[string(op.Status)]++
		switch op.Status {
		case StatusPending, StatusProcessing:
			stats.PendingOperations++
			if op.OperationType == OperationTypeConsolidate {
				stats.PendingConsolidation++
			}
		case StatusFailed:
			stats.FailedOperations++
			if op.OperationType == OperationTypeConsolidate {
				stats.FailedConsolidation++
			}
		}
	}
	for _, unit := range s.units {
		if unit.BankID == bankID && unit.Type == TypeObservation {
			stats.TotalObservations++
		}
	}
	return stats, nil
}

// MemoriesTimeseries buckets a bank's memory counts over a period.
func (s *MemoryStore) MemoriesTimeseries(ctx context.Context, bankID, period, timeField string) (Timeseries, error) {
	if err := ctx.Err(); err != nil {
		return Timeseries{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.readErr != nil {
		return Timeseries{}, s.readErr
	}

	period, trunc := normalizePeriod(period)
	if timeField == "" {
		timeField = "created_at"
	}
	counts := map[time.Time]int{}
	for _, unit := range s.units {
		if unit.BankID != bankID {
			continue
		}
		stamp := unit.MentionedAt
		if timeField == "created_at" || stamp == nil {
			if unit.OccurredStart != nil {
				stamp = unit.OccurredStart
			}
		}
		if stamp == nil {
			continue
		}
		counts[truncate(*stamp, trunc)]++
	}
	return Timeseries{
		BankID: bankID, Period: period, Trunc: trunc, TimeField: timeField,
		Buckets: sortedTimeseriesBuckets(counts),
	}, nil
}

// ListAuditLog returns one page of the audit trail.
func (s *MemoryStore) ListAuditLog(ctx context.Context, query AuditQuery) (AuditPage, error) {
	if err := ctx.Err(); err != nil {
		return AuditPage{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.readErr != nil {
		return AuditPage{}, s.readErr
	}
	matched := make([]AuditEntry, 0, len(s.auditEntries))
	for _, entry := range s.auditEntries {
		if query.BankID != "" && entry.BankID != query.BankID {
			continue
		}
		if query.Action != "" && entry.Action != query.Action {
			continue
		}
		if query.Transport != "" && entry.Transport != query.Transport {
			continue
		}
		matched = append(matched, entry)
	}
	sort.SliceStable(matched, func(i, j int) bool {
		if !matched[i].StartedAt.Equal(matched[j].StartedAt) {
			return matched[i].StartedAt.After(matched[j].StartedAt)
		}
		return matched[i].ID < matched[j].ID
	})
	total := len(matched)
	start, end := pageBounds(total, query.Offset, query.Limit)
	return AuditPage{Entries: matched[start:end], Total: total, Limit: query.Limit, Offset: query.Offset}, nil
}

// AuditLogStats buckets the audit trail over a period.
func (s *MemoryStore) AuditLogStats(ctx context.Context, bankID, period string) (AuditStats, error) {
	if err := ctx.Err(); err != nil {
		return AuditStats{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	period, trunc := normalizePeriod(period)
	buckets := map[time.Time]map[string]int{}
	for _, entry := range s.auditEntries {
		if bankID != "" && entry.BankID != bankID {
			continue
		}
		bucket := truncate(entry.StartedAt, trunc)
		if buckets[bucket] == nil {
			buckets[bucket] = map[string]int{}
		}
		buckets[bucket][entry.Action]++
	}
	out := make([]AuditBucket, 0, len(buckets))
	for stamp, actions := range buckets {
		total := 0
		for _, count := range actions {
			total += count
		}
		out = append(out, AuditBucket{Time: stamp, Actions: actions, Total: total})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return AuditStats{BankID: bankID, Period: period, Trunc: trunc, Bucket: out}, nil
}

// ListLLMRequests returns one page of the model-request trace.
func (s *MemoryStore) ListLLMRequests(ctx context.Context, query LLMRequestQuery) (LLMRequestPage, error) {
	if err := ctx.Err(); err != nil {
		return LLMRequestPage{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.readErr != nil {
		return LLMRequestPage{}, s.readErr
	}
	matched := make([]LLMRequest, 0, len(s.llmRequests))
	for _, request := range s.llmRequests {
		if query.BankID != "" && request.BankID != query.BankID {
			continue
		}
		if query.Status != "" && request.Status != query.Status {
			continue
		}
		matched = append(matched, request)
	}
	sort.SliceStable(matched, func(i, j int) bool {
		if !matched[i].StartedAt.Equal(matched[j].StartedAt) {
			return matched[i].StartedAt.After(matched[j].StartedAt)
		}
		return matched[i].ID < matched[j].ID
	})
	total := len(matched)
	start, end := pageBounds(total, query.Offset, query.Limit)
	return LLMRequestPage{Requests: matched[start:end], Total: total, Limit: query.Limit, Offset: query.Offset}, nil
}

// LLMRequestStats buckets the model-request trace over a period.
func (s *MemoryStore) LLMRequestStats(ctx context.Context, bankID, period string) (LLMRequestStats, error) {
	if err := ctx.Err(); err != nil {
		return LLMRequestStats{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	period, trunc := normalizePeriod(period)
	type accumulator struct {
		statuses map[string]int
		tokens   LLMTokenSums
	}
	buckets := map[time.Time]*accumulator{}
	for _, request := range s.llmRequests {
		if bankID != "" && request.BankID != bankID {
			continue
		}
		bucket := truncate(request.StartedAt, trunc)
		if buckets[bucket] == nil {
			buckets[bucket] = &accumulator{statuses: map[string]int{}}
		}
		buckets[bucket].statuses[request.Status]++
		addToken := func(target *int, value *int) {
			if value != nil {
				*target += *value
			}
		}
		addToken(&buckets[bucket].tokens.InputTokens, request.InputTokens)
		addToken(&buckets[bucket].tokens.OutputTokens, request.OutputTokens)
		addToken(&buckets[bucket].tokens.CachedTokens, request.CachedTokens)
		addToken(&buckets[bucket].tokens.ThoughtsTokens, request.ThoughtsTokens)
		addToken(&buckets[bucket].tokens.TotalTokens, request.TotalTokens)
	}
	out := make([]LLMRequestBucket, 0, len(buckets))
	for stamp, acc := range buckets {
		total := 0
		for _, count := range acc.statuses {
			total += count
		}
		out = append(out, LLMRequestBucket{Time: stamp, Statuses: acc.statuses, Total: total, Tokens: acc.tokens})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return LLMRequestStats{BankID: bankID, Period: period, Trunc: trunc, Bucket: out}, nil
}

// ListObservationScopes returns the distinct observation scopes of a bank.
func (s *MemoryStore) ListObservationScopes(ctx context.Context, bankID string, limit, offset int) (ObservationScopePage, error) {
	if err := ctx.Err(); err != nil {
		return ObservationScopePage{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	counts := map[string]int{}
	for _, unit := range s.units {
		if unit.BankID != bankID || unit.Type != TypeObservation {
			continue
		}
		scope := "default"
		if raw, ok := unit.Metadata["scope"].(string); ok && raw != "" {
			scope = raw
		}
		counts[scope]++
	}
	out := make([]ObservationScope, 0, len(counts))
	for scope, count := range counts {
		out = append(out, ObservationScope{Scope: scope, Count: count})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Scope < out[j].Scope })
	total := len(out)
	start, end := pageBounds(total, offset, limit)
	return ObservationScopePage{Scopes: out[start:end], Total: total, Limit: limit, Offset: offset}, nil
}

// normalizePeriod maps a period name to its bucket width, defaulting to a day.
func normalizePeriod(period string) (string, string) {
	switch period {
	case "hour":
		return "hour", "hour"
	case "week":
		return "week", "week"
	case "month":
		return "month", "month"
	case "day", "":
		return "day", "day"
	default:
		return period, period
	}
}

// truncate floors a timestamp to the bucket width.
func truncate(value time.Time, width string) time.Time {
	switch width {
	case "hour":
		return value.UTC().Truncate(time.Hour)
	case "week":
		day := value.UTC().Truncate(24 * time.Hour)
		return day.AddDate(0, 0, -int(day.Weekday()))
	case "month":
		return time.Date(value.UTC().Year(), value.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	default:
		return value.UTC().Truncate(24 * time.Hour)
	}
}

// sortedTimeseriesBuckets renders a count map in time order.
func sortedTimeseriesBuckets(counts map[time.Time]int) []TimeseriesBucket {
	out := make([]TimeseriesBucket, 0, len(counts))
	for stamp, count := range counts {
		out = append(out, TimeseriesBucket{Time: stamp, Count: count})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out
}
