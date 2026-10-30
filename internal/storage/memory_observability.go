package storage

import (
	"context"
	"sort"
	"strings"
	"time"
)

// CountAsyncOperations groups the store's non-terminal operations by type and
// status, the shape the async-operations backlog gauge reports. A null operation
// type reads as "unknown", as the reference maps it, and the bank identifier is
// carried through only when perBank is set.
func (s *MemoryStore) CountAsyncOperations(ctx context.Context, perBank bool) ([]AsyncOperationGroup, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	type key struct{ operationType, status, bankID string }
	counts := map[key]int{}
	for _, op := range s.operations {
		switch op.Status {
		case StatusPending, StatusProcessing, StatusFailed:
		default:
			continue
		}
		operationType := op.OperationType
		if operationType == "" {
			operationType = "unknown"
		}
		k := key{operationType: operationType, status: string(op.Status)}
		if perBank {
			k.bankID = op.BankID
		}
		counts[k]++
	}
	out := make([]AsyncOperationGroup, 0, len(counts))
	for k, count := range counts {
		out = append(out, AsyncOperationGroup{OperationType: k.operationType, Status: k.status, BankID: k.bankID, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].OperationType != out[j].OperationType {
			return out[i].OperationType < out[j].OperationType
		}
		if out[i].Status != out[j].Status {
			return out[i].Status < out[j].Status
		}
		return out[i].BankID < out[j].BankID
	})
	return out, nil
}

// BankStats summarises one bank's operational state.
func (s *MemoryStore) BankStats(ctx context.Context, bankID string) (BankStats, error) {
	return s.BankStatsRefreshing(ctx, bankID, false)
}

// BankStatsRefreshing serves a bank summary, recomputing and refreshing the
// cached entry when forceRefresh is set — the reference's refresh=true query
// parameter, which bypasses the cached value.
func (s *MemoryStore) BankStatsRefreshing(ctx context.Context, bankID string, forceRefresh bool) (BankStats, error) {
	if err := ctx.Err(); err != nil {
		return BankStats{}, err
	}
	// The summary is approximate and expensive to derive, so a fresh cached one
	// is served rather than recomputed on every poll, unless the caller asks for
	// a forced recompute. The cache is per-process and bounded, so a deployment
	// that reads many banks cannot hold every summary it has ever computed.
	if !forceRefresh {
		if cached, ok := s.statsCache.get(s.schema, bankID); ok {
			return cached, nil
		}
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
		if unit.FactType == TypeObservation {
			stats.TotalObservations++
		}
		// pending and failed consolidation are the consolidator's own backlog
		// gauges on the source units (world/experience), not operation counters:
		// pending is the facts still eligible for a pass, failed the ones the
		// model gave up on. They are disjoint.
		if unit.FactType == TypeExperience || unit.FactType == TypeWorld {
			if unit.ConsolidationFailedAt != nil {
				stats.FailedConsolidation++
			} else if unit.ConsolidatedAt == nil {
				stats.PendingConsolidation++
			}
		}
		if unit.UpdatedAt != nil && (stats.LastMemoryWriteAt == nil || unit.UpdatedAt.After(*stats.LastMemoryWriteAt)) {
			stats.LastMemoryWriteAt = unit.UpdatedAt
		}
		if unit.ConsolidatedAt != nil && (stats.LastConsolidatedAt == nil || unit.ConsolidatedAt.After(*stats.LastConsolidatedAt)) {
			stats.LastConsolidatedAt = unit.ConsolidatedAt
		}
	}
	for _, link := range s.links {
		if link.BankID != bankID {
			continue
		}
		stats.LinksByLinkType[string(link.LinkType)]++
	}
	// Entity links are not stored as rows; derive the total from the unit/entity
	// membership under the writer's per-entity cap and expose it as the "entity"
	// category, as the reference does.
	entityUnits := map[string]int{}
	for unitID, entityIDs := range s.unitEntities {
		unit, ok := s.units[unitID]
		if !ok || unit.BankID != bankID {
			continue
		}
		for _, entityID := range entityIDs {
			entityUnits[entityID]++
		}
	}
	entityLinks := 0
	for _, n := range entityUnits {
		if n-1 > maxLinksPerEntity {
			entityLinks += maxLinksPerEntity
		} else if n > 0 {
			entityLinks += n - 1
		}
	}
	if entityLinks > 0 {
		stats.LinksByLinkType[string(LinkEntity)] = entityLinks
	}
	// total_links sums the per-type counts, so the derived entity category is
	// included. links_by_fact_type and links_breakdown are retained in the
	// response shape but deliberately left empty, as the reference does; the
	// maps stay non-nil so they serialize as {} rather than null.
	stats.TotalLinks = 0
	for _, count := range stats.LinksByLinkType {
		stats.TotalLinks += count
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
		case StatusPending:
			stats.PendingOperations++
		case StatusFailed:
			stats.FailedOperations++
		}
	}
	s.statsCache.put(s.schema, bankID, stats)
	return stats, nil
}

// InvalidateBankStats drops a bank's cached summary so the next read recomputes,
// the in-process counterpart of the reference's cross-process cache eviction.
func (s *MemoryStore) InvalidateBankStats(ctx context.Context, bankID string) {
	s.statsCache.invalidate(s.schema, bankID)
}

// invalidate drops one bank's cached summary. A nil cache (one built with no
// TTL) has nothing to drop.
func (c *bankStatsCache) invalidate(schema, bankID string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, bankCacheKey(schema, bankID))
}

// MemoriesTimeseries buckets a bank's memory counts over a period, narrowed to
// the memories the reader's tag filter admits. The reference resolves the
// caller's read scope before the query and AND-s it into the row filter
// (engine/memory_engine.py:17046 passes it as `tag_groups` to
// engine/memories/pg/counts.py:138-148), so a confined caller counts only what
// it may read rather than the whole bank.
func (s *MemoryStore) MemoriesTimeseries(ctx context.Context, bankID, period, timeField string, scopeFilter []TagGroup) (Timeseries, error) {
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
		// The caller's resolved read scope narrows the counted set; a nil
		// filter counts every memory in the bank, which is what an unscoped
		// caller expects.
		if len(scopeFilter) > 0 && !TagsVisibleUnderScope(unit.Tags, scopeFilter) {
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

// GetAuditLog returns one audit entry of the bank, bodies included.
//
// The identifier is read the way the reference reads it — parsed as a UUID, so
// the uppercase, brace- or urn-wrapped spellings name the same entry, and a
// spelling that does not parse is a missing entry rather than a match.
func (s *MemoryStore) GetAuditLog(ctx context.Context, bankID, logID string) (AuditEntry, error) {
	if err := ctx.Err(); err != nil {
		return AuditEntry{}, err
	}
	s.diagMu.Lock()
	defer s.diagMu.Unlock()
	if s.readErr != nil {
		return AuditEntry{}, s.readErr
	}
	canonical, ok := canonicalUUID(logID)
	if !ok {
		return AuditEntry{}, ErrAuditEntryNotFound
	}
	for _, entry := range s.auditEntries {
		if entry.ID == canonical && entry.BankID == bankID {
			return entry, nil
		}
	}
	return AuditEntry{}, ErrAuditEntryNotFound
}

// AuditLogStats buckets the audit trail over a period.
func (s *MemoryStore) AuditLogStats(ctx context.Context, bankID, period, action string) (AuditStats, error) {
	if err := ctx.Err(); err != nil {
		return AuditStats{}, err
	}
	s.diagMu.Lock()
	defer s.diagMu.Unlock()
	trunc, start := chartWindow(period, time.Now().UTC())
	buckets := map[time.Time]map[string]int{}
	for _, entry := range s.auditEntries {
		if bankID != "" && entry.BankID != bankID {
			continue
		}
		if action != "" && entry.Action != action {
			continue
		}
		if entry.StartedAt.Before(start) {
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
	return AuditStats{BankID: bankID, Period: period, Trunc: trunc, Start: start, Bucket: out}, nil
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
	if query.CallType != "" && LLMCallTypeOf(request.Scope) != query.CallType {
		return false
	}
	if query.Provider != "" && request.Provider != query.Provider {
		return false
	}
	if query.TraceID != "" && request.TraceID != query.TraceID {
		return false
	}
	if query.DocumentIDSet && !metadataHasString(request.Metadata, "document_id", query.DocumentID) {
		return false
	}
	if query.MemoryIDSet && !metadataHoldsID(request.Metadata, "memory_ids", query.MemoryID) &&
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

// metadataHasString reports whether a row's metadata holds key with exactly the
// given string value. An absent key is not a match, mirroring the reference,
// whose SQL compares `metadata->>'key'` and so never matches a missing key.
func metadataHasString(metadata map[string]any, key, want string) bool {
	value, ok := metadata[key].(string)
	return ok && value == want
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

// LLMRequestStats buckets the model-request trace over a period, optionally
// narrowed to one kind of model call.
func (s *MemoryStore) LLMRequestStats(ctx context.Context, bankID, period, operation, callType string) (LLMRequestStats, error) {
	if err := ctx.Err(); err != nil {
		return LLMRequestStats{}, err
	}
	s.diagMu.Lock()
	defer s.diagMu.Unlock()
	trunc, start := chartWindow(period, time.Now().UTC())
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
		if callType != "" && LLMCallTypeOf(request.Scope) != callType {
			continue
		}
		if request.StartedAt.Before(start) {
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
	return LLMRequestStats{BankID: bankID, Period: period, Trunc: trunc, Start: start, Bucket: out}, nil
}

// ListObservationScopes returns the distinct observation scopes of a bank.
func (s *MemoryStore) ListObservationScopes(ctx context.Context, bankID string, limit, offset int) (ObservationScopePage, error) {
	if err := ctx.Err(); err != nil {
		return ObservationScopePage{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	counts := map[string]int{}
	byKey := map[string][]string{}
	for _, unit := range s.units {
		if unit.BankID != bankID || unit.FactType != TypeObservation {
			continue
		}
		// The scope is the exact set of tags the observation carries, normalized
		// by sorting; the empty list is the global/untagged scope.
		tags := append([]string(nil), unit.Tags...)
		sort.Strings(tags)
		key := strings.Join(tags, "\x00")
		counts[key]++
		byKey[key] = tags
	}
	out := make([]ObservationScope, 0, len(counts))
	for key, count := range counts {
		out = append(out, ObservationScope{Tags: byKey[key], Count: count})
	}
	// Most populous scope first, then by the scope's tag order.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return slicesLess(out[i].Tags, out[j].Tags)
	})
	total := len(out)
	start, end := pageBounds(total, offset, limit)
	return ObservationScopePage{Scopes: out[start:end], Total: total, Limit: limit, Offset: offset}, nil
}

// chartWindow maps a chart period onto its bucket width and window start. The
// audit- and LLM-request histograms bucket by day for every period — the period
// selects the window (1d / 7d / 30d), never the width — and the window start is
// returned as-is so the response echoes the resolved instant.
func chartWindow(period string, now time.Time) (string, time.Time) {
	switch period {
	case "1d":
		return "day", now.AddDate(0, 0, -1)
	case "30d":
		return "day", now.AddDate(0, 0, -30)
	default: // 7d default
		return "day", now.AddDate(0, 0, -7)
	}
}

// slicesLess orders two string slices lexicographically, the comparator the
// observation-scope listing uses to order equally-populous scopes the way the
// reference's `ORDER BY scope` does over the tag array.
func slicesLess(left, right []string) bool {
	for i := 0; i < len(left) && i < len(right); i++ {
		if left[i] != right[i] {
			return left[i] < right[i]
		}
	}
	return len(left) < len(right)
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
