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
		LinksByFactType:    map[string]int{},
		LinksBreakdown:     map[string]map[string]int{},
		OperationsByStatus: map[string]int{},
	}
	for _, unit := range s.units {
		if unit.BankID != bankID {
			continue
		}
		stats.TotalNodes++
		stats.NodesByFactType[string(unit.FactType)]++
	}
	for _, link := range s.links {
		if link.BankID != bankID {
			continue
		}
		stats.TotalLinks++
		stats.LinksByLinkType[string(link.LinkType)]++
		// The link is attributed to its source unit's fact type, so
		// links_by_fact_type and links_breakdown describe where links come from.
		factType := string(s.units[link.FromUnitID].FactType)
		stats.LinksByFactType[factType]++
		if stats.LinksBreakdown[factType] == nil {
			stats.LinksBreakdown[factType] = map[string]int{}
		}
		stats.LinksBreakdown[factType][string(link.LinkType)]++
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
		if unit.BankID == bankID && unit.FactType == TypeObservation {
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

	window := ResolveTimeseriesWindow(period)
	timeField = ResolveTimeseriesTimeField(timeField)
	// The window covers everything since one full period back, on the resolved
	// axis, so a row older than the window is not counted.
	since := time.Now().UTC().Add(-window.Step * time.Duration(window.Count))
	type accumulator struct {
		count       int
		world       int
		experience  int
		observation int
	}
	counts := map[time.Time]*accumulator{}
	for _, unit := range s.units {
		if unit.BankID != bankID {
			continue
		}
		stamp := seriesStamp(unit, timeField)
		if stamp == nil || stamp.Before(since) {
			continue
		}
		bucket := truncateBucket(*stamp, window.Trunc)
		if counts[bucket] == nil {
			counts[bucket] = &accumulator{}
		}
		counts[bucket].count++
		switch unit.FactType {
		case TypeWorld:
			counts[bucket].world++
		case TypeExperience:
			counts[bucket].experience++
		case TypeObservation:
			counts[bucket].observation++
		}
	}
	buckets := make([]TimeseriesBucket, 0, len(counts))
	for stamp, acc := range counts {
		buckets = append(buckets, TimeseriesBucket{
			Time: stamp, Count: acc.count,
			World: acc.world, Experience: acc.experience, Observation: acc.observation,
		})
	}
	sort.SliceStable(buckets, func(i, j int) bool { return buckets[i].Time.Before(buckets[j].Time) })
	return Timeseries{
		BankID: bankID, Period: window.Period, Trunc: window.Trunc, TimeField: timeField,
		Buckets: buckets,
	}, nil
}

// seriesStamp reads the timestamp the series buckets this unit by. The event
// axes fall back to the record's own creation time when the row carries no event
// stamp, so a backfilled corpus still shows up in the chart.
func seriesStamp(unit MemoryUnit, timeField string) *time.Time {
	switch timeField {
	case "mentioned_at":
		if unit.MentionedAt != nil {
			return unit.MentionedAt
		}
		if unit.OccurredStart != nil {
			return unit.OccurredStart
		}
	case "occurred_start":
		if unit.OccurredStart != nil {
			return unit.OccurredStart
		}
		if unit.MentionedAt != nil {
			return unit.MentionedAt
		}
	}
	return unit.CreatedAt
}

// RecordAuditLog appends one audit entry to the in-process trail.
func (s *MemoryStore) RecordAuditLog(ctx context.Context, entry AuditEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.diagMu.Lock()
	defer s.diagMu.Unlock()
	if entry.ID == "" {
		entry.ID = newID()
	}
	if entry.StartedAt.IsZero() {
		entry.StartedAt = time.Now().UTC()
	}
	s.auditEntries = append(s.auditEntries, entry)
	return nil
}

// ListAuditLog returns one page of the audit trail.
func (s *MemoryStore) ListAuditLog(ctx context.Context, query AuditQuery) (AuditPage, error) {
	if err := ctx.Err(); err != nil {
		return AuditPage{}, err
	}
	s.diagMu.Lock()
	defer s.diagMu.Unlock()
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
		if query.StartDate != nil && entry.StartedAt.Before(*query.StartDate) {
			continue
		}
		if query.EndDate != nil && !entry.StartedAt.Before(*query.EndDate) {
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
func (s *MemoryStore) AuditLogStats(ctx context.Context, bankID, period, action string) (AuditStats, error) {
	if err := ctx.Err(); err != nil {
		return AuditStats{}, err
	}
	s.diagMu.Lock()
	defer s.diagMu.Unlock()
	period, trunc := normalizePeriod(period)
	buckets := map[time.Time]map[string]int{}
	for _, entry := range s.auditEntries {
		if bankID != "" && entry.BankID != bankID {
			continue
		}
		if action != "" && entry.Action != action {
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
	s.diagMu.Lock()
	defer s.diagMu.Unlock()
	if s.readErr != nil {
		return LLMRequestPage{}, s.readErr
	}
	matched := make([]LLMRequest, 0, len(s.llmRequests))
	for _, request := range s.llmRequests {
		if matchesLLMRequest(request, query) {
			matched = append(matched, request)
		}
	}
	sort.SliceStable(matched, func(i, j int) bool {
		if !matched[i].StartedAt.Equal(matched[j].StartedAt) {
			return matched[i].StartedAt.After(matched[j].StartedAt)
		}
		return matched[i].ID < matched[j].ID
	})
	if query.Group {
		return groupLLMRuns(matched, query), nil
	}
	total := len(matched)
	start, end := pageBounds(total, query.Offset, query.Limit)
	return LLMRequestPage{Requests: matched[start:end], Total: total, Limit: query.Limit, Offset: query.Offset}, nil
}

// matchesLLMRequest reports whether a stored trace row passes every filter the
// query sets. An unset filter (empty string, nil bound) matches everything.
func matchesLLMRequest(request LLMRequest, query LLMRequestQuery) bool {
	if query.BankID != "" && request.BankID != query.BankID {
		return false
	}
	if query.Status != "" && request.Status != query.Status {
		return false
	}
	if query.Operation != "" && request.Operation != query.Operation {
		return false
	}
	if query.Scope != "" && request.Scope != query.Scope {
		return false
	}
	if query.Provider != "" && request.Provider != query.Provider {
		return false
	}
	if query.TraceID != "" && request.TraceID != query.TraceID {
		return false
	}
	if query.DocumentID != "" && metadataString(request.Metadata, "document_id") != query.DocumentID {
		return false
	}
	if query.MemoryID != "" && !metadataHoldsID(request.Metadata, "memory_ids", query.MemoryID) &&
		!metadataHoldsID(request.Metadata, "source_memory_ids", query.MemoryID) {
		return false
	}
	if query.StartDate != nil && request.StartedAt.Before(*query.StartDate) {
		return false
	}
	if query.EndDate != nil && !request.StartedAt.Before(*query.EndDate) {
		return false
	}
	return true
}

// groupLLMRuns pages whole operation runs. A run is one trace; an untraced row
// is a run of its own, keyed by its identifier. Runs are ordered by their most
// recent call, and the page's rows are returned newest first.
func groupLLMRuns(matched []LLMRequest, query LLMRequestQuery) LLMRequestPage {
	type run struct {
		key  string
		end  time.Time
		rows []LLMRequest
	}
	order := make([]*run, 0)
	index := map[string]*run{}
	for _, request := range matched {
		key := request.TraceID
		if key == "" {
			key = "single:" + request.ID
		}
		entry := index[key]
		if entry == nil {
			entry = &run{key: key}
			index[key] = entry
			order = append(order, entry)
		}
		entry.rows = append(entry.rows, request)
		if request.StartedAt.After(entry.end) {
			entry.end = request.StartedAt
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		if !order[i].end.Equal(order[j].end) {
			return order[i].end.After(order[j].end)
		}
		return order[i].key < order[j].key
	})
	total := len(order)
	start, end := pageBounds(total, query.Offset, query.Limit)
	requests := make([]LLMRequest, 0)
	for _, entry := range order[start:end] {
		requests = append(requests, entry.rows...)
	}
	sort.SliceStable(requests, func(i, j int) bool {
		if !requests[i].StartedAt.Equal(requests[j].StartedAt) {
			return requests[i].StartedAt.After(requests[j].StartedAt)
		}
		return requests[i].ID < requests[j].ID
	})
	return LLMRequestPage{Requests: requests, Total: total, Limit: query.Limit, Offset: query.Offset}
}

// metadataString reads a string value from a row's metadata, empty when absent.
func metadataString(metadata map[string]any, key string) string {
	value, ok := metadata[key].(string)
	if !ok {
		return ""
	}
	return value
}

// metadataHoldsID reports whether a metadata array holds the identifier.
func metadataHoldsID(metadata map[string]any, key, id string) bool {
	values, ok := metadata[key].([]any)
	if !ok {
		return false
	}
	for _, value := range values {
		if text, ok := value.(string); ok && text == id {
			return true
		}
	}
	return false
}

// RecordLLMRequest appends one model-request trace row.
func (s *MemoryStore) RecordLLMRequest(ctx context.Context, request LLMRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.diagMu.Lock()
	defer s.diagMu.Unlock()
	if request.ID == "" {
		request.ID = newID()
	}
	if request.StartedAt.IsZero() {
		request.StartedAt = time.Now().UTC()
	}
	s.llmRequests = append(s.llmRequests, request)
	return nil
}

// LLMRequestStats buckets the model-request trace over a period.
func (s *MemoryStore) LLMRequestStats(ctx context.Context, bankID, period, operation string) (LLMRequestStats, error) {
	if err := ctx.Err(); err != nil {
		return LLMRequestStats{}, err
	}
	s.diagMu.Lock()
	defer s.diagMu.Unlock()
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
		if operation != "" && request.Operation != operation {
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
		if unit.BankID != bankID || unit.FactType != TypeObservation {
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
//
// The chart endpoints accept the reference's day-scale periods ("1d", "7d",
// "30d", "90d", default "7d") and always bucket by day — the period only picks
// the window, never the width. Those names are echoed back as the period but
// truncate by day, so date_trunc receives a unit PostgreSQL knows rather than
// the literal "1d".
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
		return period, "day"
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
