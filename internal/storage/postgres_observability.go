package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// BankStats summarises one bank's operational state.
func (s *PostgresStore) BankStats(ctx context.Context, bankID string) (BankStats, error) {
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
	if stats.TotalObservations, err = s.scalarInt(ctx, `SELECT COUNT(*) FROM `+qualified+`.memory_units WHERE bank_id = $1 AND type = 'observation'`, bankID); err != nil {
		return BankStats{}, err
	}

	if stats.NodesByFactType, err = s.groupedInts(ctx,
		`SELECT COALESCE(type,''), COUNT(*) FROM `+qualified+`.memory_units WHERE bank_id = $1 GROUP BY type`, bankID); err != nil {
		return BankStats{}, err
	}
	if stats.LinksByLinkType, err = s.groupedInts(ctx,
		`SELECT COALESCE(link_type,''), COUNT(*) FROM `+qualified+`.memory_links WHERE bank_id = $1 GROUP BY link_type`, bankID); err != nil {
		return BankStats{}, err
	}
	if stats.OperationsByStatus, err = s.groupedInts(ctx,
		`SELECT COALESCE(status,''), COUNT(*) FROM `+qualified+`.async_operations WHERE bank_id = $1 GROUP BY status`, bankID); err != nil {
		return BankStats{}, err
	}
	stats.PendingOperations = stats.OperationsByStatus[string(StatusPending)] + stats.OperationsByStatus[string(StatusProcessing)]
	stats.FailedOperations = stats.OperationsByStatus[string(StatusFailed)]
	if stats.PendingConsolidation, err = s.scalarInt(ctx,
		`SELECT COUNT(*) FROM `+qualified+`.async_operations WHERE bank_id = $1 AND status IN ('pending','processing') AND operation_type = $2`,
		bankID, OperationTypeConsolidate); err != nil {
		return BankStats{}, err
	}
	if stats.FailedConsolidation, err = s.scalarInt(ctx,
		`SELECT COUNT(*) FROM `+qualified+`.async_operations WHERE bank_id = $1 AND status = 'failed' AND operation_type = $2`,
		bankID, OperationTypeConsolidate); err != nil {
		return BankStats{}, err
	}
	var lastWrite sql.NullTime
	if err := s.db.QueryRowContext(ctx,
		`SELECT MAX(updated_at) FROM `+qualified+`.async_operations WHERE bank_id = $1`, bankID).Scan(&lastWrite); err != nil {
		return BankStats{}, err
	}
	stats.LastMemoryWriteAt = nullTimePtr(lastWrite)

	var lastConsolidated sql.NullTime
	if err := s.db.QueryRowContext(ctx,
		`SELECT MAX(completed_at) FROM `+qualified+`.async_operations WHERE bank_id = $1 AND operation_type = $2 AND status = 'completed'`,
		bankID, OperationTypeConsolidate).Scan(&lastConsolidated); err != nil {
		return BankStats{}, err
	}
	stats.LastConsolidatedAt = nullTimePtr(lastConsolidated)
	return stats, nil
}

// MemoriesTimeseries buckets a bank's memory counts over a period.
func (s *PostgresStore) MemoriesTimeseries(ctx context.Context, bankID, period, timeField string) (Timeseries, error) {
	period, trunc := normalizePeriod(period)
	// The bucket expression is chosen from a closed set, so the column name is
	// never interpolated from caller input.
	column := "mentioned_at"
	if timeField == "created_at" {
		column = "mentioned_at"
	}
	bucket := `date_trunc('` + trunc + `', COALESCE(` + column + `, occurred_start, mentioned_at))`
	query := `SELECT ` + bucket + ` AS bucket, COUNT(*)
	          FROM ` + quoteIdent(s.schema) + `.memory_units
	          WHERE bank_id = $1 AND ` + bucket + ` IS NOT NULL
	          GROUP BY bucket ORDER BY bucket ASC`
	rows, err := s.db.QueryContext(ctx, query, bankID)
	if err != nil {
		return Timeseries{}, err
	}
	defer func() { _ = rows.Close() }()
	buckets := make([]TimeseriesBucket, 0)
	for rows.Next() {
		var entry TimeseriesBucket
		if err := rows.Scan(&entry.Time, &entry.Count); err != nil {
			return Timeseries{}, err
		}
		buckets = append(buckets, entry)
	}
	if err := rows.Err(); err != nil {
		return Timeseries{}, err
	}
	return Timeseries{BankID: bankID, Period: period, Trunc: trunc, TimeField: "mentioned_at", Buckets: buckets}, nil
}

// ListAuditLog returns one page of the audit trail.
func (s *PostgresStore) ListAuditLog(ctx context.Context, query AuditQuery) (AuditPage, error) {
	filter := `($1 = '' OR bank_id = $1) AND ($2 = '' OR action = $2) AND ($3 = '' OR transport = $3)`
	qualified := quoteIdent(s.schema)
	var total int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM `+qualified+`.audit_log WHERE `+filter,
		query.BankID, query.Action, query.Transport).Scan(&total); err != nil {
		return AuditPage{}, err
	}
	limit := clampLimit(query.Limit, 50)
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, action, transport, COALESCE(bank_id,''), started_at, ended_at,
		        COALESCE(request, 'null'::jsonb), COALESCE(response, 'null'::jsonb),
		        COALESCE(metadata, '{}'::jsonb)
		 FROM `+qualified+`.audit_log WHERE `+filter+`
		 ORDER BY started_at DESC, id ASC LIMIT $4 OFFSET $5`,
		query.BankID, query.Action, query.Transport, limit, query.Offset)
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
func (s *PostgresStore) AuditLogStats(ctx context.Context, bankID, period string) (AuditStats, error) {
	period, trunc := normalizePeriod(period)
	qualified := quoteIdent(s.schema)
	var start sql.NullTime
	if err := s.db.QueryRowContext(ctx,
		`SELECT MIN(started_at) FROM `+qualified+`.audit_log WHERE ($1 = '' OR bank_id = $1)`,
		bankID).Scan(&start); err != nil {
		return AuditStats{}, err
	}
	query := `SELECT date_trunc('` + trunc + `', started_at) AS bucket, action, COUNT(*)
	          FROM ` + qualified + `.audit_log WHERE ($1 = '' OR bank_id = $1)
	          GROUP BY bucket, action ORDER BY bucket ASC`
	rows, err := s.db.QueryContext(ctx, query, bankID)
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
	filter := `($1 = '' OR bank_id = $1) AND ($2 = '' OR status = $2)`
	qualified := quoteIdent(s.schema)
	var total int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM `+qualified+`.llm_requests WHERE `+filter,
		query.BankID, query.Status).Scan(&total); err != nil {
		return LLMRequestPage{}, err
	}
	limit := clampLimit(query.Limit, 50)
	rows, err := s.db.QueryContext(ctx, `SELECT `+llmRequestSelect+`
		 FROM `+qualified+`.llm_requests WHERE `+filter+`
		 ORDER BY started_at DESC, id ASC LIMIT $3 OFFSET $4`,
		query.BankID, query.Status, limit, query.Offset)
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

// LLMRequestStats buckets the model-request trace over a period.
func (s *PostgresStore) LLMRequestStats(ctx context.Context, bankID, period string) (LLMRequestStats, error) {
	period, trunc := normalizePeriod(period)
	qualified := quoteIdent(s.schema)
	var start sql.NullTime
	if err := s.db.QueryRowContext(ctx,
		`SELECT MIN(started_at) FROM `+qualified+`.llm_requests WHERE ($1 = '' OR bank_id = $1)`,
		bankID).Scan(&start); err != nil {
		return LLMRequestStats{}, err
	}
	query := `SELECT date_trunc('` + trunc + `', started_at) AS bucket, status, COUNT(*),
	                 COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
	                 COALESCE(SUM(cached_tokens),0), COALESCE(SUM(thoughts_tokens),0),
	                 COALESCE(SUM(total_tokens),0)
	          FROM ` + qualified + `.llm_requests WHERE ($1 = '' OR bank_id = $1)
	          GROUP BY bucket, status ORDER BY bucket ASC`
	rows, err := s.db.QueryContext(ctx, query, bankID)
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
	          WHERE bank_id = $1 AND type = 'observation'
	          GROUP BY scope ORDER BY scope ASC`
	rows, err := s.db.QueryContext(ctx, query, bankID)
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
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&value); err != nil {
		return 0, err
	}
	return value, nil
}

// groupedInts runs a group-and-count query into a map.
func (s *PostgresStore) groupedInts(ctx context.Context, query string, args ...any) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
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
