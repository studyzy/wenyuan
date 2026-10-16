package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// BankStats summarises one bank's operational state.
//
// The summary is expensive to compute and intentionally approximate, so it is
// served from the cross-process cache table when a fresh row exists. A cache
// that is unreachable or absent degrades to computing without caching rather
// than failing the endpoint, because a stale-free answer is never worth an
// error here.
func (s *PostgresStore) BankStats(ctx context.Context, bankID string) (BankStats, error) {
	if s.bankStatsCacheTTL > 0 {
		if cached, ok := s.cachedBankStats(ctx, bankID); ok {
			return cached, nil
		}
	}
	stats, err := s.computeBankStats(ctx, bankID)
	if err != nil {
		return BankStats{}, err
	}
	if s.bankStatsCacheTTL > 0 {
		s.storeBankStats(ctx, bankID, stats)
	}
	return stats, nil
}

// cachedBankStats reads a fresh cached summary. A miss, an unreadable row or an
// absent table all report false so the caller computes.
func (s *PostgresStore) cachedBankStats(ctx context.Context, bankID string) (BankStats, bool) {
	query := `SELECT payload FROM ` + quoteIdent(s.schema) + `.bank_stats_cache
	          WHERE bank_id = $1 AND computed_at > now() - make_interval(secs => $2::double precision)`
	var payload []byte
	if err := s.reader().QueryRowContext(ctx, query, bankID, s.bankStatsCacheTTL.Seconds()).Scan(&payload); err != nil {
		return BankStats{}, false
	}
	var stats BankStats
	if err := json.Unmarshal(payload, &stats); err != nil {
		return BankStats{}, false
	}
	return stats, true
}

// storeBankStats writes a computed summary back. The write is best-effort: a
// cache that cannot be written leaves the answer intact.
func (s *PostgresStore) storeBankStats(ctx context.Context, bankID string, stats BankStats) {
	payload, err := json.Marshal(stats)
	if err != nil {
		return
	}
	query := `INSERT INTO ` + quoteIdent(s.schema) + `.bank_stats_cache (bank_id, payload, computed_at)
	          VALUES ($1, $2::jsonb, now())
	          ON CONFLICT (bank_id) DO UPDATE SET payload = EXCLUDED.payload, computed_at = now()`
	_, _ = s.db.ExecContext(ctx, query, bankID, string(payload))
}

// computeBankStats runs the aggregates the summary is derived from.
func (s *PostgresStore) computeBankStats(ctx context.Context, bankID string) (BankStats, error) {
	stats := BankStats{
		BankID:             bankID,
		NodesByFactType:    map[string]int{},
		LinksByLinkType:    map[string]int{},
		OperationsByStatus: map[string]int{},
	}
	qualified := quoteIdent(s.schema)

	// Each count is a separate aggregate so one missing table cannot fail the
	// whole summary; a failure is reported rather than silently zeroed.
	var err error
	if stats.TotalNodes, err = s.scalarInt(ctx, `SELECT COUNT(*) FROM `+qualified+`.memory_units WHERE bank_id = $1`, bankID); err != nil {
		return BankStats{}, err
	}
	if stats.TotalDocuments, err = s.scalarInt(ctx, `SELECT COUNT(*) FROM `+qualified+`.documents WHERE bank_id = $1`, bankID); err != nil {
		return BankStats{}, err
	}
	if stats.TotalLinks, err = s.scalarInt(ctx, `SELECT COUNT(*) FROM `+qualified+`.memory_links WHERE bank_id = $1`, bankID); err != nil {
		return BankStats{}, err
	}
	if stats.TotalObservations, err = s.scalarInt(ctx, `SELECT COUNT(*) FROM `+qualified+`.memory_units WHERE bank_id = $1 AND fact_type = 'observation'`, bankID); err != nil {
		return BankStats{}, err
	}

	if stats.NodesByFactType, err = s.groupedInts(ctx,
		`SELECT COALESCE(fact_type,''), COUNT(*) FROM `+qualified+`.memory_units WHERE bank_id = $1 GROUP BY fact_type`, bankID); err != nil {
		return BankStats{}, err
	}
	if stats.LinksByLinkType, err = s.groupedInts(ctx,
		`SELECT COALESCE(link_type,''), COUNT(*) FROM `+qualified+`.memory_links WHERE bank_id = $1 GROUP BY link_type`, bankID); err != nil {
		return BankStats{}, err
	}
	// Links are attributed to their source unit's fact type, and the breakdown
	// crosses that with the link type.
	stats.LinksByFactType = map[string]int{}
	stats.LinksBreakdown = map[string]map[string]int{}
	breakdownRows, err := s.reader().QueryContext(ctx,
		`SELECT COALESCE(u.fact_type,''), COALESCE(l.link_type,''), COUNT(*)
		 FROM `+qualified+`.memory_links l
		 JOIN `+qualified+`.memory_units u ON u.id = l.from_unit_id
		 WHERE l.bank_id = $1 GROUP BY u.fact_type, l.link_type`, bankID)
	if err != nil {
		return BankStats{}, err
	}
	for breakdownRows.Next() {
		var factType, linkType string
		var count int
		if err := breakdownRows.Scan(&factType, &linkType, &count); err != nil {
			_ = breakdownRows.Close()
			return BankStats{}, err
		}
		stats.LinksByFactType[factType] += count
		if stats.LinksBreakdown[factType] == nil {
			stats.LinksBreakdown[factType] = map[string]int{}
		}
		stats.LinksBreakdown[factType][linkType] = count
	}
	if err := breakdownRows.Err(); err != nil {
		_ = breakdownRows.Close()
		return BankStats{}, err
	}
	_ = breakdownRows.Close()
	if stats.OperationsByStatus, err = s.groupedInts(ctx,
		`SELECT COALESCE(status,''), COUNT(*) FROM `+qualified+`.async_operations WHERE bank_id = $1 GROUP BY status`, bankID); err != nil {
		return BankStats{}, err
	}
	stats.PendingOperations = stats.OperationsByStatus[string(StatusPending)] + stats.OperationsByStatus[string(StatusProcessing)]
	stats.FailedOperations = stats.OperationsByStatus[string(StatusFailed)]
	// pending and failed are the consolidator's own backlog gauges, not operation
	// counters: pending is the source facts still eligible for a pass, failed the
	// ones the model gave up on. They are disjoint, and a failed fact leaves the
	// failed bucket only via the consolidation-recovery endpoint.
	if stats.PendingConsolidation, stats.FailedConsolidation, err = s.consolidationBacklog(ctx, bankID); err != nil {
		return BankStats{}, err
	}
	var lastWrite sql.NullTime
	if err := s.reader().QueryRowContext(ctx,
		`SELECT MAX(updated_at) FROM `+qualified+`.async_operations WHERE bank_id = $1`, bankID).Scan(&lastWrite); err != nil {
		return BankStats{}, err
	}
	stats.LastMemoryWriteAt = nullTimePtr(lastWrite)

	var lastConsolidated sql.NullTime
	if err := s.reader().QueryRowContext(ctx,
		`SELECT MAX(completed_at) FROM `+qualified+`.async_operations WHERE bank_id = $1 AND operation_type = $2 AND status = 'completed'`,
		bankID, OperationTypeConsolidate).Scan(&lastConsolidated); err != nil {
		return BankStats{}, err
	}
	stats.LastConsolidatedAt = nullTimePtr(lastConsolidated)
	return stats, nil
}

// consolidationBacklog returns the pending and failed source-fact counts for a
// bank in one scan, mirroring the reference's consolidation_freshness: pending
// carries the candidate predicate (consolidated_at IS NULL AND
// consolidation_failed_at IS NULL over world/experience facts), failed counts
// the facts the model gave up on. The two are disjoint.
func (s *PostgresStore) consolidationBacklog(ctx context.Context, bankID string) (int, int, error) {
	query := `SELECT
	              COUNT(*) FILTER (
	                  WHERE consolidated_at IS NULL AND consolidation_failed_at IS NULL
	                    AND fact_type IN ('experience', 'world')
	              ),
	              COUNT(*) FILTER (
	                  WHERE consolidation_failed_at IS NOT NULL
	                    AND fact_type IN ('experience', 'world')
	              )
	          FROM ` + quoteIdent(s.schema) + `.memory_units WHERE bank_id = $1`
	var pending, failed int
	if err := s.reader().QueryRowContext(ctx, query, bankID).Scan(&pending, &failed); err != nil {
		return 0, 0, err
	}
	return pending, failed, nil
}

// MemoriesTimeseries buckets a bank's memory counts over a period.
func (s *PostgresStore) MemoriesTimeseries(ctx context.Context, bankID, period, timeField string) (Timeseries, error) {
	window := ResolveTimeseriesWindow(period)
	timeField = ResolveTimeseriesTimeField(timeField)
	// The column name comes from a closed set, so nothing caller-supplied is
	// interpolated into the statement. The event axes fall back per row to the
	// record's own creation time, so a row without an event stamp still counts.
	column := timeField
	if column != "created_at" {
		column = "COALESCE(" + column + ", created_at)"
	}
	bucket := `date_trunc('` + window.Trunc + `', ` + column + `)`
	since := time.Now().UTC().Add(-window.Step * time.Duration(window.Count))
	query := `SELECT ` + bucket + ` AS bucket, COUNT(*),
	                 COUNT(*) FILTER (WHERE fact_type = 'world') AS world,
	                 COUNT(*) FILTER (WHERE fact_type = 'experience') AS experience,
	                 COUNT(*) FILTER (WHERE fact_type = 'observation') AS observation
	          FROM ` + quoteIdent(s.schema) + `.memory_units
	          WHERE bank_id = $1 AND ` + column + ` IS NOT NULL AND ` + column + ` >= $2
	          GROUP BY bucket ORDER BY bucket ASC`
	rows, err := s.reader().QueryContext(ctx, query, bankID, since)
	if err != nil {
		return Timeseries{}, err
	}
	defer func() { _ = rows.Close() }()
	buckets := make([]TimeseriesBucket, 0)
	for rows.Next() {
		var entry TimeseriesBucket
		if err := rows.Scan(&entry.Time, &entry.Count, &entry.World, &entry.Experience, &entry.Observation); err != nil {
			return Timeseries{}, err
		}
		buckets = append(buckets, entry)
	}
	if err := rows.Err(); err != nil {
		return Timeseries{}, err
	}
	return Timeseries{BankID: bankID, Period: window.Period, Trunc: window.Trunc, TimeField: timeField, Buckets: buckets}, nil
}

// RecordAuditLog appends one audit entry to the trail.
func (s *PostgresStore) RecordAuditLog(ctx context.Context, entry AuditEntry) error {
	if entry.ID == "" {
		entry.ID = newID()
	}
	if entry.StartedAt.IsZero() {
		entry.StartedAt = time.Now().UTC()
	}
	request, err := marshalNullableJSON(mapOrNil(entry.Request))
	if err != nil {
		return err
	}
	response, err := marshalNullableJSON(mapOrNil(entry.Response))
	if err != nil {
		return err
	}
	metadata, err := marshalMetadata(entry.Metadata)
	if err != nil {
		return err
	}
	var endedAt any
	if entry.EndedAt != nil {
		endedAt = *entry.EndedAt
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO `+quoteIdent(s.schema)+`.audit_log
		    (id, action, transport, bank_id, started_at, ended_at, request, response, metadata)
		 VALUES ($1::uuid, $2, $3, $4, $5, $6, $7::jsonb, $8::jsonb, $9::jsonb)`,
		entry.ID, entry.Action, entry.Transport, nullString(entry.BankID),
		entry.StartedAt.UTC(), endedAt, request, response, metadata)
	return err
}

// mapOrNil returns nil for an empty map, so a caller that has nothing to record
// leaves the jsonb column NULL rather than the empty object.
func mapOrNil(value map[string]any) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

// ListAuditLog returns one page of the audit trail.
func (s *PostgresStore) ListAuditLog(ctx context.Context, query AuditQuery) (AuditPage, error) {
	filter := `($1 = '' OR bank_id = $1) AND ($2 = '' OR action = $2) AND ($3 = '' OR transport = $3)
	           AND ($4::timestamptz IS NULL OR started_at >= $4)
	           AND ($5::timestamptz IS NULL OR started_at < $5)`
	args := []any{query.BankID, query.Action, query.Transport, query.StartDate, query.EndDate}
	qualified := quoteIdent(s.schema)
	var total int
	if err := s.reader().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM `+qualified+`.audit_log WHERE `+filter,
		args...).Scan(&total); err != nil {
		return AuditPage{}, err
	}
	limit := clampLimit(query.Limit, 50)
	rows, err := s.reader().QueryContext(ctx,
		`SELECT id, action, transport, COALESCE(bank_id,''), started_at, ended_at,
		        COALESCE(request, 'null'::jsonb), COALESCE(response, 'null'::jsonb),
		        COALESCE(metadata, '{}'::jsonb)
		 FROM `+qualified+`.audit_log WHERE `+filter+`
		 ORDER BY started_at DESC, id ASC LIMIT $6 OFFSET $7`,
		append(args, limit, query.Offset)...)
	if err != nil {
		return AuditPage{}, err
	}
	defer func() { _ = rows.Close() }()
	entries := make([]AuditEntry, 0)
	for rows.Next() {
		var entry AuditEntry
		var endedAt sql.NullTime
		var request, response, metadata []byte
		if err := rows.Scan(&entry.ID, &entry.Action, &entry.Transport, &entry.BankID,
			&entry.StartedAt, &endedAt, &request, &response, &metadata); err != nil {
			return AuditPage{}, err
		}
		entry.EndedAt = nullTimePtr(endedAt)
		entry.Request = decodeJSONMap(request)
		entry.Response = decodeJSONMap(response)
		entry.Metadata = decodeJSONMap(metadata)
		entries = append(entries, entry)
	}
	return AuditPage{Entries: entries, Total: total, Limit: query.Limit, Offset: query.Offset}, rows.Err()
}

// AuditLogStats buckets the audit trail over a period.
func (s *PostgresStore) AuditLogStats(ctx context.Context, bankID, period, action string) (AuditStats, error) {
	period, trunc := normalizePeriod(period)
	qualified := quoteIdent(s.schema)
	where := `($1 = '' OR bank_id = $1) AND ($2 = '' OR action = $2)`
	var start sql.NullTime
	if err := s.reader().QueryRowContext(ctx,
		`SELECT MIN(started_at) FROM `+qualified+`.audit_log WHERE `+where,
		bankID, action).Scan(&start); err != nil {
		return AuditStats{}, err
	}
	query := `SELECT date_trunc('` + trunc + `', started_at) AS bucket, action, COUNT(*)
	          FROM ` + qualified + `.audit_log WHERE ` + where + `
	          GROUP BY bucket, action ORDER BY bucket ASC`
	rows, err := s.reader().QueryContext(ctx, query, bankID, action)
	if err != nil {
		return AuditStats{}, err
	}
	defer func() { _ = rows.Close() }()
	grouped := map[time.Time]map[string]int{}
	order := make([]time.Time, 0)
	for rows.Next() {
		var stamp time.Time
		var action string
		var count int
		if err := rows.Scan(&stamp, &action, &count); err != nil {
			return AuditStats{}, err
		}
		if grouped[stamp] == nil {
			grouped[stamp] = map[string]int{}
			order = append(order, stamp)
		}
		grouped[stamp][action] = count
	}
	if err := rows.Err(); err != nil {
		return AuditStats{}, err
	}
	buckets := make([]AuditBucket, 0, len(order))
	for _, stamp := range order {
		total := 0
		for _, count := range grouped[stamp] {
			total += count
		}
		buckets = append(buckets, AuditBucket{Time: stamp, Actions: grouped[stamp], Total: total})
	}
	stats := AuditStats{BankID: bankID, Period: period, Trunc: trunc, Bucket: buckets}
	if start.Valid {
		stats.Start = start.Time
	}
	return stats, nil
}

// ListLLMRequests returns one page of the model-request trace.
func (s *PostgresStore) ListLLMRequests(ctx context.Context, query LLMRequestQuery) (LLMRequestPage, error) {
	qualified := quoteIdent(s.schema)
	clauses := []string{"($1 = '' OR bank_id = $1)"}
	args := []any{query.BankID}
	for _, exact := range []struct{ column, value string }{
		{"status", query.Status},
		{"operation", query.Operation},
		{"scope", query.Scope},
		{"provider", query.Provider},
		{"trace_id", query.TraceID},
	} {
		if exact.value == "" {
			continue
		}
		args = append(args, exact.value)
		clauses = append(clauses, exact.column+" = $"+strconv.Itoa(len(args)))
	}
	if query.DocumentID != "" {
		// The document is carried in per-call metadata, not a column.
		args = append(args, query.DocumentID)
		clauses = append(clauses, "metadata->>'document_id' = $"+strconv.Itoa(len(args)))
	}
	if query.MemoryID != "" {
		// Match the run that produced the unit (metadata.memory_ids) or consumed
		// it as a consolidation source (metadata.source_memory_ids).
		args = append(args, query.MemoryID)
		placeholder := "$" + strconv.Itoa(len(args))
		clauses = append(clauses,
			"(metadata->'memory_ids' ? "+placeholder+" OR metadata->'source_memory_ids' ? "+placeholder+")")
	}
	if query.StartDate != nil {
		args = append(args, query.StartDate)
		clauses = append(clauses, "started_at >= $"+strconv.Itoa(len(args)))
	}
	if query.EndDate != nil {
		args = append(args, query.EndDate)
		clauses = append(clauses, "started_at < $"+strconv.Itoa(len(args)))
	}
	where := strings.Join(clauses, " AND ")
	limit := clampLimit(query.Limit, 50)

	var total int
	if err := s.reader().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM `+qualified+`.llm_requests WHERE `+where,
		args...).Scan(&total); err != nil {
		return LLMRequestPage{}, err
	}

	if query.Group {
		// A "run" is one trace_id; an untraced row is a run of its own. The page
		// counts and returns whole runs, most-recently-active first.
		runKey := `COALESCE(trace_id, 'single:' || id::text)`
		var runTotal int
		if err := s.reader().QueryRowContext(ctx,
			`SELECT COUNT(*) FROM (SELECT 1 FROM `+qualified+`.llm_requests WHERE `+where+
				` GROUP BY `+runKey+`) q`, args...).Scan(&runTotal); err != nil {
			return LLMRequestPage{}, err
		}
		keyQuery := `SELECT ` + runKey + ` AS run_key, MAX(started_at) AS run_end
		             FROM ` + qualified + `.llm_requests WHERE ` + where + `
		             GROUP BY ` + runKey + `
		             ORDER BY run_end DESC, run_key ASC
		             LIMIT $` + strconv.Itoa(len(args)+1) + ` OFFSET $` + strconv.Itoa(len(args)+2)
		keyRows, err := s.reader().QueryContext(ctx, keyQuery, append(append([]any{}, args...), limit, query.Offset)...)
		if err != nil {
			return LLMRequestPage{}, err
		}
		runKeys := make([]string, 0)
		for keyRows.Next() {
			var key string
			var runEnd time.Time
			if err := keyRows.Scan(&key, &runEnd); err != nil {
				_ = keyRows.Close()
				return LLMRequestPage{}, err
			}
			runKeys = append(runKeys, key)
		}
		if err := keyRows.Err(); err != nil {
			_ = keyRows.Close()
			return LLMRequestPage{}, err
		}
		_ = keyRows.Close()
		requests := make([]LLMRequest, 0)
		if len(runKeys) > 0 {
			rowQuery := `SELECT ` + llmRequestSelect + ` FROM ` + qualified + `.llm_requests
			             WHERE ` + where + ` AND ` + runKey + ` = ANY($` + strconv.Itoa(len(args)+1) + `::text[])
			             ORDER BY started_at DESC, id ASC`
			rows, err := s.reader().QueryContext(ctx, rowQuery, append(append([]any{}, args...), runKeys)...)
			if err != nil {
				return LLMRequestPage{}, err
			}
			defer func() { _ = rows.Close() }()
			for rows.Next() {
				request, err := scanLLMRequest(rows)
				if err != nil {
					return LLMRequestPage{}, err
				}
				requests = append(requests, request)
			}
			if err := rows.Err(); err != nil {
				return LLMRequestPage{}, err
			}
		}
		return LLMRequestPage{Requests: requests, Total: runTotal, Limit: query.Limit, Offset: query.Offset}, nil
	}

	rows, err := s.reader().QueryContext(ctx, `SELECT `+llmRequestSelect+`
		 FROM `+qualified+`.llm_requests WHERE `+where+`
		 ORDER BY started_at DESC, id ASC LIMIT $`+strconv.Itoa(len(args)+1)+` OFFSET $`+strconv.Itoa(len(args)+2),
		append(append([]any{}, args...), limit, query.Offset)...)
	if err != nil {
		return LLMRequestPage{}, err
	}
	defer func() { _ = rows.Close() }()
	requests := make([]LLMRequest, 0)
	for rows.Next() {
		request, err := scanLLMRequest(rows)
		if err != nil {
			return LLMRequestPage{}, err
		}
		requests = append(requests, request)
	}
	return LLMRequestPage{Requests: requests, Total: total, Limit: query.Limit, Offset: query.Offset}, rows.Err()
}

// llmRequestSelect is the shared projection for the trace.
const llmRequestSelect = `id::text, COALESCE(bank_id,''), COALESCE(operation,''), COALESCE(scope,''),
       COALESCE(trace_id,''), COALESCE(span_id,''), COALESCE(parent_span_id,''),
       COALESCE(provider,''), COALESCE(model,''), status, started_at, ended_at, duration_ms,
       input_tokens, output_tokens, cached_tokens, thoughts_tokens, total_tokens,
       input, output, COALESCE(error,''), COALESCE(llm_info, '{}'::jsonb), COALESCE(metadata, '{}'::jsonb)`

// RecordLLMRequest inserts one model-request trace row.
//
// The row is written after the call it records, so its identifier and start
// time are generated here when the recorder left them unset. A failure to
// record is returned to the caller, which drops it: a trace row is never worth
// failing the model call that produced the answer.
func (s *PostgresStore) RecordLLMRequest(ctx context.Context, request LLMRequest) error {
	if request.ID == "" {
		request.ID = newID()
	}
	if request.StartedAt.IsZero() {
		request.StartedAt = time.Now().UTC()
	}
	input, err := marshalNullableJSON(request.Input)
	if err != nil {
		return err
	}
	output, err := marshalNullableJSON(request.Output)
	if err != nil {
		return err
	}
	llmInfo, err := marshalMetadata(request.LLMInfo)
	if err != nil {
		return err
	}
	metadata, err := marshalMetadata(request.Metadata)
	if err != nil {
		return err
	}
	query := `INSERT INTO ` + quoteIdent(s.schema) + `.llm_requests
	          (id, bank_id, operation, scope, trace_id, span_id, parent_span_id, provider, model, status,
	           started_at, ended_at, duration_ms, input_tokens, output_tokens, cached_tokens, thoughts_tokens,
	           total_tokens, input, output, error, llm_info, metadata)
	          VALUES ($1::uuid,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,
	                  $19::jsonb,$20::jsonb,$21,$22::jsonb,$23::jsonb)`
	_, err = s.db.ExecContext(ctx, query,
		request.ID, nullString(request.BankID), nullString(request.Operation), nullString(request.Scope),
		nullString(request.TraceID), nullString(request.SpanID), nullString(request.ParentSpanID),
		nullString(request.Provider), nullString(request.Model), request.Status,
		request.StartedAt, request.EndedAt, request.DurationMS,
		request.InputTokens, request.OutputTokens, request.CachedTokens, request.ThoughtsTokens, request.TotalTokens,
		input, output, nullString(request.Error), llmInfo, metadata)
	return err
}

// marshalNullableJSON encodes a jsonb value of any shape, returning nil for a
// nil value so the column stays NULL rather than the literal string "null".
func marshalNullableJSON(value any) ([]byte, error) {
	if value == nil {
		return nil, nil
	}
	return json.Marshal(value)
}

// LLMRequestStats buckets the model-request trace over a period.
func (s *PostgresStore) LLMRequestStats(ctx context.Context, bankID, period, operation string) (LLMRequestStats, error) {
	period, trunc := normalizePeriod(period)
	qualified := quoteIdent(s.schema)
	where := `($1 = '' OR bank_id = $1) AND ($2 = '' OR operation = $2)`
	var start sql.NullTime
	if err := s.reader().QueryRowContext(ctx,
		`SELECT MIN(started_at) FROM `+qualified+`.llm_requests WHERE `+where,
		bankID, operation).Scan(&start); err != nil {
		return LLMRequestStats{}, err
	}
	query := `SELECT date_trunc('` + trunc + `', started_at) AS bucket, status, COUNT(*),
	                 COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
	                 COALESCE(SUM(cached_tokens),0), COALESCE(SUM(thoughts_tokens),0),
	                 COALESCE(SUM(total_tokens),0)
	          FROM ` + qualified + `.llm_requests WHERE ` + where + `
	          GROUP BY bucket, status ORDER BY bucket ASC`
	rows, err := s.reader().QueryContext(ctx, query, bankID, operation)
	if err != nil {
		return LLMRequestStats{}, err
	}
	defer func() { _ = rows.Close() }()
	type accumulator struct {
		statuses map[string]int
		tokens   LLMTokenSums
	}
	grouped := map[time.Time]*accumulator{}
	order := make([]time.Time, 0)
	for rows.Next() {
		var stamp time.Time
		var status string
		var count int
		var input, output, cached, thoughts, total int
		if err := rows.Scan(&stamp, &status, &count, &input, &output, &cached, &thoughts, &total); err != nil {
			return LLMRequestStats{}, err
		}
		if grouped[stamp] == nil {
			grouped[stamp] = &accumulator{statuses: map[string]int{}}
			order = append(order, stamp)
		}
		grouped[stamp].statuses[status] = count
		grouped[stamp].tokens.InputTokens += input
		grouped[stamp].tokens.OutputTokens += output
		grouped[stamp].tokens.CachedTokens += cached
		grouped[stamp].tokens.ThoughtsTokens += thoughts
		grouped[stamp].tokens.TotalTokens += total
	}
	if err := rows.Err(); err != nil {
		return LLMRequestStats{}, err
	}
	buckets := make([]LLMRequestBucket, 0, len(order))
	for _, stamp := range order {
		acc := grouped[stamp]
		total := 0
		for _, count := range acc.statuses {
			total += count
		}
		buckets = append(buckets, LLMRequestBucket{
			Time: stamp, Statuses: acc.statuses, Total: total, Tokens: acc.tokens,
		})
	}
	stats := LLMRequestStats{BankID: bankID, Period: period, Trunc: trunc, Bucket: buckets}
	if start.Valid {
		stats.Start = start.Time
	}
	return stats, nil
}

// ListObservationScopes returns the distinct observation scopes of a bank.
func (s *PostgresStore) ListObservationScopes(ctx context.Context, bankID string, limit, offset int) (ObservationScopePage, error) {
	qualified := quoteIdent(s.schema)
	// An observation's scope lives in its metadata, so the distinct values are
	// read from there rather than from a dedicated column.
	query := `SELECT COALESCE(NULLIF(metadata->>'scope',''), 'default') AS scope, COUNT(*)
	          FROM ` + qualified + `.memory_units
	          WHERE bank_id = $1 AND fact_type = 'observation'
	          GROUP BY scope ORDER BY scope ASC`
	rows, err := s.reader().QueryContext(ctx, query, bankID)
	if err != nil {
		return ObservationScopePage{}, err
	}
	defer func() { _ = rows.Close() }()
	all := make([]ObservationScope, 0)
	for rows.Next() {
		var entry ObservationScope
		if err := rows.Scan(&entry.Scope, &entry.Count); err != nil {
			return ObservationScopePage{}, err
		}
		all = append(all, entry)
	}
	if err := rows.Err(); err != nil {
		return ObservationScopePage{}, err
	}
	total := len(all)
	start, end := pageBounds(total, offset, limit)
	if start > total {
		start = total
	}
	if end > total {
		end = total
	}
	return ObservationScopePage{Scopes: all[start:end], Total: total, Limit: limit, Offset: offset}, nil
}

// scanLLMRequest reads the shared projection.
func scanLLMRequest(row rowScanner) (LLMRequest, error) {
	var request LLMRequest
	var endedAt sql.NullTime
	var duration, input, output, cached, thoughts, total sql.NullInt64
	var inputPayload, outputPayload, llmInfo, metadata []byte
	if err := row.Scan(&request.ID, &request.BankID, &request.Operation, &request.Scope,
		&request.TraceID, &request.SpanID, &request.ParentSpanID, &request.Provider,
		&request.Model, &request.Status, &request.StartedAt, &endedAt, &duration,
		&input, &output, &cached, &thoughts, &total,
		&inputPayload, &outputPayload, &request.Error, &llmInfo, &metadata); err != nil {
		return LLMRequest{}, err
	}
	request.EndedAt = nullTimePtr(endedAt)
	request.DurationMS = nullIntPtr(duration)
	request.InputTokens = nullIntPtr(input)
	request.OutputTokens = nullIntPtr(output)
	request.CachedTokens = nullIntPtr(cached)
	request.ThoughtsTokens = nullIntPtr(thoughts)
	request.TotalTokens = nullIntPtr(total)
	request.Input = decodeAny(inputPayload)
	request.Output = decodeAny(outputPayload)
	request.LLMInfo = decodeJSONMap(llmInfo)
	request.Metadata = decodeJSONMap(metadata)
	return request, nil
}

// scalarInt runs a count query.
func (s *PostgresStore) scalarInt(ctx context.Context, query string, args ...any) (int, error) {
	var value int
	if err := s.reader().QueryRowContext(ctx, query, args...).Scan(&value); err != nil {
		return 0, err
	}
	return value, nil
}

// groupedInts runs a group-and-count query into a map.
func (s *PostgresStore) groupedInts(ctx context.Context, query string, args ...any) (map[string]int, error) {
	rows, err := s.reader().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int{}
	for rows.Next() {
		var key string
		var count int
		if err := rows.Scan(&key, &count); err != nil {
			return nil, err
		}
		if key == "" {
			key = "unknown"
		}
		out[key] = count
	}
	return out, rows.Err()
}

// nullIntPtr returns a pointer to a nullable integer.
func nullIntPtr(value sql.NullInt64) *int {
	if !value.Valid {
		return nil
	}
	converted := int(value.Int64)
	return &converted
}

// decodeAny decodes a jsonb value of any shape.
func decodeAny(raw []byte) any {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}
