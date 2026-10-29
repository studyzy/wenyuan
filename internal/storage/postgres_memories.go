package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// ListMemories returns one page of a bank's memory units, newest first. A
// state of "invalidated" reads the archive table instead of the live one.
func (s *PostgresStore) ListMemories(ctx context.Context, query MemoryQuery) (MemoryPage, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 MemoryPage
		return zero0, schemaErr
	}
	qualified := quoteIdent(schema)
	table := qualified + `.memory_units`
	invalidatedCols := `NULL::timestamptz, NULL::text`
	if query.State == "invalidated" {
		table = qualified + `.invalidated_memory_units`
		invalidatedCols = `mu.invalidated_at, mu.invalidation_reason`
	}
	// The parameters are bound in the reference's positions, not in whichever
	// order the SQL happens to mention them: `bank_id` is $1, then the filters in
	// the reference's build order — fact_type, document_id, entity_id, then the
	// search term its builder appends last — and the page bounds after them, as
	// limit and offset (engine/memories/pg/curation.py:176-292). A bound only the
	// driver can reject is therefore reported under the reference's number.
	conditions := []string{"mu.bank_id = $1"}
	args := []any{query.BankID}
	var optional []string
	var optionalArgs []any
	next := func(value any) string {
		optionalArgs = append(optionalArgs, value)
		return "$" + itoa(1+len(optionalArgs))
	}
	if query.Type != "" {
		optional = append(optional, "mu.fact_type = "+next(query.Type))
	}
	if query.DocumentID != "" {
		optional = append(optional, "mu.document_id = "+next(query.DocumentID))
	}
	if query.EntityID != "" {
		// A reverse lookup through the stored entity links, which is what makes a
		// `state=invalidated` list with an entity filter return nothing (the
		// archive carries no links). The two spellings of the cast are not
		// interchangeable here: `= $n::uuid` leaves the scalar parameter untyped
		// under this driver (PostgreSQL reads `= $n` against several candidate
		// operators before the cast is seen), while `CAST(… AS uuid)` declares
		// the type on the expression and resolves. It selects exactly the rows
		// the reference's `= $n::uuid` selects.
		optional = append(optional, "mu.id IN (SELECT unit_id FROM "+qualified+
			".unit_entities WHERE entity_id = CAST("+next(query.EntityID)+" AS uuid))")
	}
	if state := query.ConsolidationState; state != "" {
		switch state {
		case "failed":
			optional = append(optional, "mu.consolidation_failed_at IS NOT NULL AND mu.fact_type IN ('experience', 'world')")
		case "pending":
			optional = append(optional, "mu.consolidated_at IS NULL AND mu.consolidation_failed_at IS NULL "+
				"AND mu.fact_type IN ('experience', 'world')")
		case "done":
			optional = append(optional, "mu.consolidated_at IS NOT NULL AND mu.fact_type IN ('experience', 'world')")
		}
	}
	// The search term is bound where the reference appends it — after every
	// other filter and before the window — reading both the fact's text and its
	// context. The query is taken as given: the reference only tests it for
	// emptiness, so whitespace is a search term that matches nothing.
	if search := query.Q; search != "" {
		placeholder := next("%" + search + "%")
		optional = append(optional, "(mu.text ILIKE "+placeholder+" OR mu.context ILIKE "+placeholder+")")
	}
	if clause, tagArgs := listTagClause(query.TagsMatch, query.Tags, 1+len(optionalArgs)+1); clause != "" {
		optional = append(optional, clause)
		optionalArgs = append(optionalArgs, tagArgs...)
	}
	// A compound filter — the caller's resolved read scope — is AND-ed in as an
	// extra predicate, so a scoped reader pages over the memories it may read
	// without replacing the request's own tag filter.
	if groupClause, groupArgs := renderTagGroups(query.TagGroups, 1+len(optionalArgs)); groupClause != "" {
		optional = append(optional, "("+groupClause+")")
		optionalArgs = append(optionalArgs, groupArgs...)
	}
	// A time window replaces the default ordering and drops the rows with no
	// value on the axis it names, so the ordering it drives is total.
	// The default ordering is total too: `id` last breaks the ties a batch of
	// memories shares on both timestamps, so offset pages cannot repeat or skip
	// rows between reads (the reference's #5934).
	orderBy := "mu.mentioned_at DESC NULLS LAST, mu.created_at DESC, mu.id"
	if query.TimeField != "" || query.TimeStart != nil || query.TimeEnd != nil {
		column := "mu." + windowColumn(query.TimeField)
		optional = append(optional, column+" IS NOT NULL")
		if query.TimeStart != nil {
			optional = append(optional, column+" >= "+next(query.TimeStart))
		}
		if query.TimeEnd != nil {
			optional = append(optional, column+" < "+next(query.TimeEnd))
		}
		orderBy = column + " DESC, mu.id"
	}
	conditions = append(conditions, optional...)
	narrow := append(append([]any{}, args...), optionalArgs...)
	// The reference's trailing LIMIT/OFFSET bind after every filter
	// (engine/memories/pg/curation.py:284-292).
	limitPlaceholder := "$" + itoa(len(narrow)+1)
	offsetPlaceholder := "$" + itoa(len(narrow)+2)
	filter := strings.Join(conditions, " AND ")
	args = append(narrow, query.Limit, query.Offset)
	var total int
	if err := s.reader().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM `+table+` mu WHERE `+filter+pageBoundsInFilter(limitPlaceholder, offsetPlaceholder), args...).Scan(&total); err != nil {
		return MemoryPage{}, err
	}
	rows, err := s.reader().QueryContext(ctx,
		`SELECT mu.id, mu.bank_id, COALESCE(mu.text,''), COALESCE(mu.fact_type,''), COALESCE(mu.context,''),
		        mu.occurred_start, mu.occurred_end, mu.mentioned_at, mu.event_date,
		        COALESCE(mu.document_id,''), COALESCE(mu.chunk_id,''), COALESCE(mu.tags, '{}'),
		        COALESCE(mu.metadata, '{}'::jsonb), mu.created_at, mu.updated_at, mu.edited_at,
		        COALESCE(mu.proof_count, 1), COALESCE(mu.source_memory_ids, '{}'::uuid[])::text,
		        mu.consolidated_at, mu.consolidation_failed_at,
		        COALESCE(mu.observation_scopes, 'null'::jsonb)::text, `+invalidatedCols+`
		 FROM `+table+` mu WHERE `+filter+`
		 ORDER BY `+orderBy+` LIMIT `+limitPlaceholder+` OFFSET `+offsetPlaceholder,
		args...)
	if err != nil {
		return MemoryPage{}, err
	}
	defer func() { _ = rows.Close() }()
	units := make([]MemoryUnit, 0)
	for rows.Next() {
		unit, err := scanMemoryUnitRow(rows)
		if err != nil {
			return MemoryPage{}, err
		}
		units = append(units, unit)
	}
	return MemoryPage{Memories: units, Total: total, Limit: query.Limit, Offset: query.Offset}, rows.Err()
}

// listTagClause builds the tag predicate for a list query, mirroring the
// reference's build_tags_where_clause over the `mu.tags` column. An empty tag
// set means "no filter" under every mode except `exact`, which then selects the
// untagged/global scope; `any`/`all` admit untagged rows, the `_strict` modes
// exclude them. paramOffset is the first free placeholder index.
func listTagClause(mode string, tags []string, paramOffset int) (string, []any) {
	if len(tags) == 0 {
		if mode == TagsMatchExact {
			return "(mu.tags IS NULL OR mu.tags = '{}')", nil
		}
		return "", nil
	}
	// The column is cast, not the parameter: PostgreSQL resolves a parameter's
	// type from its context, and `tags` is varchar[], so a bare `$n` under `&&`
	// is left untyped. The reference's own `$n::text[]` names a type this
	// column does not have (`character varying[] && text[]` does not resolve),
	// and its driver has already typed the array by then; casting the column
	// keeps the predicate — membership and strictness — identical while leaving
	// PostgreSQL nothing to infer from the parameter's position.
	column := "mu.tags::text[]"
	placeholder := "$" + itoa(paramOffset)
	switch mode {
	case TagsMatchExact:
		return "(" + column + " @> " + placeholder + " AND " + column + " <@ " + placeholder + ")", []any{tags}
	case TagsMatchAll:
		return "(mu.tags IS NULL OR mu.tags = '{}' OR " + column + " @> " + placeholder + ")", []any{tags}
	case TagsMatchAllStrict:
		return "(mu.tags IS NOT NULL AND mu.tags != '{}' AND " + column + " @> " + placeholder + ")", []any{tags}
	case TagsMatchAnyStrict:
		return "(mu.tags IS NOT NULL AND mu.tags != '{}' AND " + column + " && " + placeholder + ")", []any{tags}
	default: // any
		return "(mu.tags IS NULL OR mu.tags = '{}' OR " + column + " && " + placeholder + ")", []any{tags}
	}
}

// windowColumn maps a `time_field` selector to a column, defaulting to
// created_at (the axis the reference uses when only dates are supplied). The
// name comes from a closed set, so nothing caller-supplied is interpolated.
func windowColumn(field string) string {
	if field == "" {
		return "created_at"
	}
	return timeColumnFor(field)
}

// timeColumnFor maps a time-field selector to a column, from a closed set so
// the name is never interpolated from caller input.
func timeColumnFor(field string) string {
	switch field {
	case "created_at":
		return "created_at"
	case "updated_at":
		return "updated_at"
	case "occurred_start":
		return "occurred_start"
	case "occurred_end":
		return "occurred_end"
	default:
		return "mentioned_at"
	}
}

// SourceMemories returns an observation's source facts straight from the live
// memory_units table, ordered by mention (newest first, nulls last). A source
// that exists only in the invalidation archive is not returned, matching the
// reference's memory_units-only query.
func (s *PostgresStore) SourceMemories(ctx context.Context, bankID string, ids []string) ([]SourceMemoryRow, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 []SourceMemoryRow
		return zero0, schemaErr
	}
	if len(ids) == 0 {
		return nil, nil
	}
	qualified := quoteIdent(schema)
	rows, err := s.reader().QueryContext(ctx,
		`SELECT id, text, COALESCE(fact_type,''), context, occurred_start, mentioned_at
		 FROM `+qualified+`.memory_units
		 WHERE bank_id = $1 AND id = ANY($2::uuid[])
		 ORDER BY mentioned_at DESC NULLS LAST`, bankID, ids)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]SourceMemoryRow, 0, len(ids))
	for rows.Next() {
		var (
			row           SourceMemoryRow
			text, context sql.NullString
			occurred      sql.NullTime
			mentioned     sql.NullTime
		)
		if err := rows.Scan(&row.ID, &text, &row.FactType, &context, &occurred, &mentioned); err != nil {
			return nil, err
		}
		if text.Valid {
			value := text.String
			row.Text = &value
		}
		if context.Valid {
			value := context.String
			row.Context = &value
		}
		if occurred.Valid {
			value := occurred.Time
			row.OccurredStart = &value
		}
		if mentioned.Valid {
			value := mentioned.Time
			row.MentionedAt = &value
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// GetMemory returns one memory unit by identifier, reading the archive table
// when the unit has been invalidated.
func (s *PostgresStore) GetMemory(ctx context.Context, bankID, memoryID string) (MemoryUnit, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 MemoryUnit
		return zero0, schemaErr
	}
	qualified := quoteIdent(schema)
	cols := `id, bank_id, COALESCE(text,''), COALESCE(fact_type,''), COALESCE(context,''),
	                 occurred_start, occurred_end, mentioned_at, event_date,
	                 COALESCE(document_id,''), COALESCE(chunk_id,''), COALESCE(tags, '{}'),
	                 COALESCE(metadata, '{}'::jsonb), created_at, updated_at, edited_at,
	                 COALESCE(proof_count, 1), COALESCE(source_memory_ids, '{}'::uuid[])::text,
	                 consolidated_at, consolidation_failed_at,
	                 COALESCE(observation_scopes, 'null'::jsonb)::text`
	unit, err := scanMemoryUnitRow(s.reader().QueryRowContext(ctx,
		`SELECT `+cols+`, NULL::timestamptz, NULL::text
		 FROM `+qualified+`.memory_units WHERE bank_id = $1 AND id = $2`, bankID, memoryID))
	if err == nil {
		return unit, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return MemoryUnit{}, err
	}
	unit, err = scanMemoryUnitRow(s.reader().QueryRowContext(ctx,
		`SELECT `+cols+`, invalidated_at, invalidation_reason
		 FROM `+qualified+`.invalidated_memory_units WHERE bank_id = $1 AND id = $2`, bankID, memoryID))
	if errors.Is(err, sql.ErrNoRows) {
		return MemoryUnit{}, ErrMemoryNotFound
	}
	return unit, err
}

// MemoryAttachments returns each unit's attachments, keyed by unit id, for the
// memory-detail view. An observation with no attachment ids of its own inherits
// its source facts'.
func (s *PostgresStore) MemoryAttachments(ctx context.Context, bankID string, unitIDs []string) (map[string][]Attachment, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 map[string][]Attachment
		return zero0, schemaErr
	}
	if len(unitIDs) == 0 {
		return nil, nil
	}
	qualified := quoteIdent(schema)
	type unitInfo struct {
		ids      []string
		sources  []string
		factType string
	}
	info := map[string]unitInfo{}
	var sourceIDs []string
	rows, err := s.reader().QueryContext(ctx,
		`SELECT id::text, COALESCE(attachment_ids,'{}')::text, COALESCE(fact_type,''),
		        COALESCE(source_memory_ids,'{}'::uuid[])::text
		 FROM `+qualified+`.memory_units WHERE bank_id = $1 AND id = ANY($2::uuid[])`,
		bankID, unitIDs)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, idsRaw, factType, sourcesRaw string
		if err := rows.Scan(&id, &idsRaw, &factType, &sourcesRaw); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids := decodePostgresTextArray([]byte(idsRaw))
		sources := decodePostgresTextArray([]byte(sourcesRaw))
		info[id] = unitInfo{ids: ids, sources: sources, factType: factType}
		if len(ids) == 0 && factType == string(TypeObservation) {
			sourceIDs = append(sourceIDs, sources...)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	// The source facts' own attachments, for observations that carry none.
	sourceAttachments := map[string][]string{}
	if len(sourceIDs) > 0 {
		srcRows, err := s.reader().QueryContext(ctx,
			`SELECT id::text, COALESCE(attachment_ids,'{}')::text
			 FROM `+qualified+`.memory_units WHERE bank_id = $1 AND id = ANY($2::uuid[])`,
			bankID, sourceIDs)
		if err != nil {
			return nil, err
		}
		for srcRows.Next() {
			var id, idsRaw string
			if err := srcRows.Scan(&id, &idsRaw); err != nil {
				_ = srcRows.Close()
				return nil, err
			}
			sourceAttachments[id] = decodePostgresTextArray([]byte(idsRaw))
		}
		if err := srcRows.Err(); err != nil {
			_ = srcRows.Close()
			return nil, err
		}
		_ = srcRows.Close()
	}
	byIDs := map[string][]string{}
	seen := map[string]bool{}
	var all []string
	for unitID, entry := range info {
		ids := entry.ids
		if len(ids) == 0 && entry.factType == string(TypeObservation) {
			for _, source := range entry.sources {
				ids = append(ids, sourceAttachments[source]...)
			}
		}
		unique := make([]string, 0, len(ids))
		local := map[string]bool{}
		for _, id := range ids {
			if local[id] {
				continue
			}
			local[id] = true
			unique = append(unique, id)
			if !seen[id] {
				seen[id] = true
				all = append(all, id)
			}
		}
		byIDs[unitID] = unique
	}
	resolved, err := s.AttachmentsByShortIDs(ctx, bankID, all)
	if err != nil {
		return nil, err
	}
	out := map[string][]Attachment{}
	for unitID, ids := range byIDs {
		list := make([]Attachment, 0, len(ids))
		for _, id := range ids {
			if attachment, ok := resolved[id]; ok {
				list = append(list, attachment)
			}
		}
		if len(list) > 0 {
			out[unitID] = list
		}
	}
	return out, nil
}

// UpdateMemory applies a partial update to one memory unit. A state of
// "invalidated" moves the row to the archive (cascading its links and entity
// associations); "valid" moves it back.
func (s *PostgresStore) UpdateMemory(ctx context.Context, bankID, memoryID string, patch MemoryPatch) (MemoryUnit, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 MemoryUnit
		return zero0, schemaErr
	}
	current, err := s.GetMemory(ctx, bankID, memoryID)
	if err != nil {
		return MemoryUnit{}, err
	}
	qualified := quoteIdent(schema)
	// An invalidate/revert is applied AFTER the edit fields, never instead of
	// them: the reference's phase 2 applies the edit to the live row first and
	// only then moves the row (engine/memory_engine.py:12194-12260 edit, then
	// :12350-12380 invalidate), so a body carrying both stores the edited text
	// and then archives it, and a body that both edits and reverts archives
	// first — an edit on an archived row is refused, so the edit is dropped —
	// leaving only the revert. A revert therefore stops here, while an
	// invalidation lets the edit run and moves the row afterwards.
	doRevert := false
	if patch.State != nil {
		switch *patch.State {
		case "invalidated":
			// Nothing to classify here: the move itself recognises an
			// already-archived row ("keep it retired, but change why") and only
			// rewrites the reason then, as the reference does.
		case "valid":
			doRevert = true
		}
	}
	// The change history is filed on the consolidation this write queues, so the
	// tracked-writes record is bound before any sweep runs (memory_engine.py:13126-13152).
	track, ctx := beginCurationHistory(ctx, s, bankID, memoryID, curationInvalidate)
	if doRevert {
		if err := s.revertMemory(ctx, bankID, memoryID); err != nil {
			return MemoryUnit{}, err
		}
		track.action = curationActionForHistory(curationRevert)
		track.recordDocument(ctx, s)
		track.file(ctx, s, s.submitEditFollowUps(ctx, bankID))
		return s.GetMemory(ctx, bankID, memoryID)
	}
	if patch.Text != nil {
		current.Text = *patch.Text
	}
	if patch.Tags != nil {
		current.Tags = append([]string(nil), (*patch.Tags)...)
	}
	if patch.Metadata != nil {
		current.Metadata = copyAnyMap(*patch.Metadata)
	}
	if patch.Context != nil {
		current.Context = *patch.Context
	}
	if patch.FactType != nil {
		current.FactType = MemoryUnitType(*patch.FactType)
	}
	if patch.ClearOccurredStart {
		current.OccurredStart = nil
	}
	current.OccurredStart = applyDatePatch(current.OccurredStart, patch.OccurredStart)
	if patch.ClearOccurredEnd {
		current.OccurredEnd = nil
	}
	current.OccurredEnd = applyDatePatch(current.OccurredEnd, patch.OccurredEnd)
	if patch.MentionedAt != nil {
		current.MentionedAt = applyDatePatch(current.MentionedAt, patch.MentionedAt)
	}
	// An explicit occurred_start edit — including a clear — re-derives the
	// legacy single date with retain's own rule (occurred_start or
	// mentioned_at), so clearing an occurrence cannot leave the old date
	// behind. A mentioned_at edit re-derives it the same way, since it is the
	// fallback. An edit touching neither is not an occurrence edit.
	if patch.ClearOccurredStart || patch.OccurredStart != nil || patch.MentionedAt != nil {
		if current.OccurredStart != nil {
			current.EventDate = current.OccurredStart
		} else {
			current.EventDate = current.MentionedAt
		}
	}
	// The entity set is a relation rather than a column, so it is rewritten
	// through its own path instead of the UPDATE below.
	if patch.Entities != nil {
		resolve := patch.ResolveEntities == nil || *patch.ResolveEntities
		if err := s.SetUnitEntities(ctx, bankID, memoryID, *patch.Entities, resolve); err != nil {
			return MemoryUnit{}, err
		}
	}
	// A reference edit is any change to the unit's own content — text, context,
	// dates, fact type or entities — and it re-embeds the unit with retain's own
	// date+entity augmentation. The names are read back after the entity rewrite
	// above, so the vector describes the entity set this same call stores.
	edit := editTouchesContent(patch)
	if edit {
		track.action = curationActionForHistory(curationEdit)
	}
	var embedding []float32
	if edit {
		names := []string(nil)
		if byUnit, err := s.UnitEntityNames(ctx, bankID, []string{memoryID}); err == nil {
			names = byUnit[memoryID]
		}
		embedding = embedUnitText(ctx, s.observationEmbedder, current, names)
	}
	metadata, err := jsonColumn(current.Metadata)
	if err != nil {
		return MemoryUnit{}, err
	}
	args := []any{bankID, memoryID,
		current.Text, normalizeTags(current.Tags), metadata, current.Context,
		string(current.FactType), current.OccurredStart, current.OccurredEnd, current.EventDate,
		current.MentionedAt}
	// The re-embedded vector rides the edit's own UPDATE, exactly as the
	// reference's apply_edit does; when no embedder produced one the clause is
	// omitted so the stored vector is left as it is rather than cleared.
	embeddingClause := ""
	if len(embedding) > 0 {
		args = append(args, NullableEmbedding(embedding))
		embeddingClause = fmt.Sprintf(", embedding = $%d::vector", len(args))
	}
	// The column set is the reference's apply_edit: the edited fields, the
	// re-embedded vector, a reset of both consolidation markers (the fact must
	// be folded again with its new text) and both edit stamps. A patch that
	// names no editable column (only a state, say) leaves the row's own values
	// in place rather than rewriting them with the unchanged ones.
	if editsColumns(patch) {
		query := `UPDATE ` + qualified + `.memory_units
		          SET text = $3, search_vector = ` + s.searchVectorExpr("$3", "$6", "text_signals") + `, tags = $4, metadata = $5::jsonb,
		              context = $6, fact_type = $7, occurred_start = $8, occurred_end = $9, event_date = $10,
		              mentioned_at = $11,
		              consolidated_at = NULL, consolidation_failed_at = NULL, edited_at = now(), updated_at = now()` +
			embeddingClause + `
		          WHERE bank_id = $1 AND id = $2`
		if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
			return MemoryUnit{}, err
		}
	}
	if edit {
		// The edit drops the derived links, so a stale temporal or semantic
		// edge does not survive it; graph maintenance rebuilds the ones that
		// still hold, and the causal edges stay (#2864).
		if err := s.dropNonCausalLinks(ctx, bankID, memoryID); err != nil {
			return MemoryUnit{}, err
		}
		// The unit's old text was the evidence its observations were built on,
		// so an observation resting on this fact alone is now stale and goes.
		if _, err := s.DeleteStaleObservations(ctx, bankID, []string{memoryID}); err != nil {
			return MemoryUnit{}, err
		}
	}
	// The move happens after the edit, as the reference orders it.
	if patch.State != nil && *patch.State == "invalidated" {
		alreadyArchived := current.InvalidatedAt != nil
		if err := s.invalidateMemory(ctx, bankID, memoryID, patch.Reason); err != nil {
			return MemoryUnit{}, err
		}
		if !alreadyArchived {
			// The row just left the live view, so the observations it was
			// evidence for are stale now, and the entities it named may be
			// orphaned; both are swept and the follow-ups queued.
			if _, err := s.DeleteStaleObservations(ctx, bankID, []string{memoryID}); err != nil {
				return MemoryUnit{}, err
			}
			track.recordDocument(ctx, s)
			track.file(ctx, s, s.submitEditFollowUps(ctx, bankID))
		}
	} else if edit {
		track.recordDocument(ctx, s)
		track.file(ctx, s, s.submitEditFollowUps(ctx, bankID))
	}
	return s.GetMemory(ctx, bankID, memoryID)
}

// editsColumns reports whether a patch names any of the memory-unit columns the
// edit UPDATE writes: text, context, a date, the fact type, tags or metadata.
// Entities are a relation rather than a column and are written separately.
func editsColumns(patch MemoryPatch) bool {
	return patch.Text != nil || patch.Context != nil || patch.FactType != nil ||
		patch.OccurredStart != nil || patch.ClearOccurredStart ||
		patch.OccurredEnd != nil || patch.ClearOccurredEnd || patch.MentionedAt != nil ||
		patch.Tags != nil || patch.Metadata != nil
}

// editTouchesContent reports whether a patch changes the unit's own content in
// the way the reference's `doing_edit` does: text, context, either occurrence
// date (including a clear), the mention time, the fact type or the entity set.
// It is exactly the set of changes that re-embed the unit and re-fold its
// observations.
func editTouchesContent(patch MemoryPatch) bool {
	return patch.Text != nil || patch.Context != nil || patch.FactType != nil ||
		patch.OccurredStart != nil || patch.ClearOccurredStart ||
		patch.OccurredEnd != nil || patch.ClearOccurredEnd || patch.MentionedAt != nil ||
		patch.Entities != nil
}

// submitEditFollowUps queues the background work a curation edit leaves behind:
// the consolidation pass that re-folds the fact, submitted only when automatic
// consolidation is on for the bank, and a graph-maintenance sweep, submitted
// unconditionally as the reference does with force_sweep=True (an edit
// re-resolves entities, so the ones the unit named before may now be
// unreferenced even when the unit itself is isolated). A failure to submit
// never fails the edit: the write is already durable, and the periodic sweeps
// pick the work up.
func (s *PostgresStore) submitEditFollowUps(ctx context.Context, bankID string) string {
	consolidationID := ""
	if s.autoConsolidationFor(ctx, bankID) {
		if op, _, err := s.EnqueueConsolidation(ctx, bankID); err != nil {
			slog.Default().Warn("could not submit consolidation after curating a memory",
				"bank_id", bankID, "error", err)
		} else {
			consolidationID = op.OperationID
		}
	}
	if _, _, err := s.EnqueueGraphMaintenance(ctx, bankID); err != nil {
		slog.Default().Warn("could not submit graph maintenance after curating a memory",
			"bank_id", bankID, "error", err)
	}
	return consolidationID
}

// autoConsolidationFor resolves whether an edit submits a consolidation pass:
// the bank's own enable_auto_consolidation override when set, the deployment
// default otherwise. It mirrors the reference's resolve_full_config gate.
func (s *PostgresStore) autoConsolidationFor(ctx context.Context, bankID string) bool {
	bank, err := s.GetBank(ctx, bankID)
	if err != nil {
		return s.autoConsolidation
	}
	if value, ok := bank.Config["enable_auto_consolidation"].(bool); ok {
		return value
	}
	return s.autoConsolidation
}

// curationColumns are the memory-unit columns that survive a move between the
// live table and the invalidation archive. embedding and text_signals do not,
// matching the reference, which drops them on invalidation.
const curationColumns = `id, bank_id, document_id, text, context, event_date, occurred_start, occurred_end,
	mentioned_at, fact_type, metadata, created_at, updated_at, chunk_id, tags, proof_count,
	source_memory_ids, consolidated_at, observation_scopes, consolidation_failed_at, edited_at, attachment_ids`

// invalidateMemory moves a live unit to the archive table, cascading its links
// and entity associations. A unit that is already archived keeps its place and
// only has its reason rewritten, so a repeated invalidation updates the reason
// instead of silently doing nothing.
func (s *PostgresStore) invalidateMemory(ctx context.Context, bankID, memoryID string, reason *string) error {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return schemaErr
	}
	qualified := quoteIdent(schema)
	var reasonArg any
	if reason != nil {
		reasonArg = *reason
	}
	result, err := s.db.ExecContext(ctx,
		`INSERT INTO `+qualified+`.invalidated_memory_units (`+curationColumns+`, invalidation_reason, invalidated_at)
		 SELECT `+curationColumns+`, $3, now()
		 FROM `+qualified+`.memory_units WHERE bank_id = $1 AND id = $2
		 ON CONFLICT (id) DO NOTHING`, bankID, memoryID, reasonArg)
	if err != nil {
		return err
	}
	// A row that was already archived keeps its place and its invalidation time:
	// a new reason is the only thing the repeat changes.
	if archived, err := result.RowsAffected(); err == nil && archived == 0 && reason != nil {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE `+qualified+`.invalidated_memory_units SET invalidation_reason = $3
			 WHERE bank_id = $1 AND id = $2`, bankID, memoryID, reasonArg); err != nil {
			return err
		}
	}
	// The unit is leaving the live table, so the survivors that point at it lose
	// their outgoing edges when its links cascade away: queue them for a relink
	// while the postings are still readable.
	if err := s.enqueueRelinkVictims(ctx, s.db, bankID, []string{memoryID}, false); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM `+qualified+`.memory_links WHERE bank_id = $1 AND (from_unit_id = $2 OR to_unit_id = $2)`,
		bankID, memoryID); err != nil {
		return err
	}
	// The postings this unit carries are about to go, so give back their
	// mention counts first, while the rows are still readable.
	if err := releaseEntityPostings(ctx, s.db, qualified, `ue.unit_id = $1::uuid`, memoryID); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM `+qualified+`.unit_entities WHERE unit_id = $1`, memoryID); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`DELETE FROM `+qualified+`.memory_units WHERE bank_id = $1 AND id = $2`, bankID, memoryID)
	return err
}

// revertMemory moves an archived unit back to the live table and clears its
// invalidation fields. Its embedding is not restored; the reference recomputes
// it rather than keeping a cold copy.
func (s *PostgresStore) revertMemory(ctx context.Context, bankID, memoryID string) error {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return schemaErr
	}
	qualified := quoteIdent(schema)
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO `+qualified+`.memory_units (`+curationColumns+`)
		 SELECT `+curationColumns+` FROM `+qualified+`.invalidated_memory_units
		 WHERE bank_id = $1 AND id = $2
		 ON CONFLICT (id) DO NOTHING`, bankID, memoryID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM `+qualified+`.invalidated_memory_units WHERE bank_id = $1 AND id = $2`, bankID, memoryID)
	return err
}

// MemoryHistory returns an observation's recorded changes, oldest first, from
// the observation_history snapshots its consolidation rewrites wrote. Curation
// is not an observation-level change: a memory edit records nothing here, and
// only world/experience facts can be curated at all.
func (s *PostgresStore) MemoryHistory(ctx context.Context, bankID, memoryID string) ([]MemoryHistoryEntry, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 []MemoryHistoryEntry
		return zero0, schemaErr
	}
	if _, err := s.GetMemory(ctx, bankID, memoryID); err != nil {
		return nil, err
	}
	rows, err := s.reader().QueryContext(ctx,
		`SELECT COALESCE(content, '{}'::jsonb), changed_at
		 FROM `+quoteIdent(schema)+`.observation_history
		 WHERE bank_id = $1 AND observation_id = $2
		 ORDER BY changed_at ASC, id ASC`,
		bankID, memoryID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	entries := make([]MemoryHistoryEntry, 0)
	for rows.Next() {
		var content []byte
		var entry MemoryHistoryEntry
		if err := rows.Scan(&content, &entry.ChangedAt); err != nil {
			return nil, err
		}
		entry.decodeSnapshot(content)
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

// decodeSnapshot reads one observation_history `content` blob into an entry,
// tolerating the empty blob an unreadable row leaves behind.
func (entry *MemoryHistoryEntry) decodeSnapshot(content []byte) {
	var snapshot observationHistorySnapshot
	if err := json.Unmarshal(content, &snapshot); err != nil {
		return
	}
	entry.PreviousText = snapshot.PreviousText
	entry.PreviousTags = snapshot.PreviousTags
	entry.PreviousOccurredStart = snapshot.PreviousOccurredStart
	entry.PreviousOccurredEnd = snapshot.PreviousOccurredEnd
	entry.PreviousMentionedAt = snapshot.PreviousMentionedAt
	entry.NewSourceMemoryIDs = snapshot.NewSourceMemoryIDs
}

// DeleteObservations removes the observations of a bank — or of one memory —
// and requeues the source facts behind them so the next consolidation pass
// regenerates them, matching the reference's clear_observations /
// clear_observations_for_memory. An observation is tied to a memory by carrying
// it as evidence, so the memory-scoped filter is an overlap test against the
// observation's source_memory_ids array.
func (s *PostgresStore) DeleteObservations(ctx context.Context, bankID, memoryID string) (int, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 int
		return zero0, schemaErr
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	qualified := quoteIdent(schema)
	if memoryID == "" {
		return s.clearBankObservationsTx(ctx, tx, bankID)
	}
	// The clear's event is filed on the consolidation it queues, and only a
	// clear that removed something is recorded (memory_engine.py:12524-12565).
	track, ctx := beginClearHistory(ctx, s, bankID, memoryID)
	deleted, err := s.deleteStaleObservationsTx(ctx, tx, bankID, []string{memoryID})
	if err != nil {
		return 0, err
	}
	// The memory was a source of the deleted observations, so clear its own
	// consolidated marker too — it needs fresh observations. Bookkeeping only:
	// `updated_at` stays put.
	if deleted > 0 {
		if _, err := tx.ExecContext(ctx,
			`UPDATE `+qualified+`.memory_units SET consolidated_at = NULL
			 WHERE id = $1::uuid AND bank_id = $2 AND fact_type IN ('experience', 'world')`,
			memoryID, bankID); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	if deleted > 0 && s.autoConsolidationFor(ctx, bankID) {
		if op, _, err := s.EnqueueConsolidation(ctx, bankID); err == nil {
			track.file(ctx, s, op.OperationID)
		}
	}
	return deleted, nil
}

// clearBankObservationsTx deletes a bank's observations, requeues every source
// fact and drops the observations' history, in the caller's transaction.
func (s *PostgresStore) clearBankObservationsTx(ctx context.Context, tx Tx, bankID string) (int, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 int
		return zero0, schemaErr
	}
	qualified := quoteIdent(schema)
	var count int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM `+qualified+`.memory_units WHERE bank_id = $1 AND fact_type = 'observation'`,
		bankID).Scan(&count); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM `+qualified+`.memory_units WHERE bank_id = $1 AND fact_type = 'observation'`,
		bankID); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM `+qualified+`.observation_history WHERE bank_id = $1`,
		bankID); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE `+qualified+`.memory_units SET consolidated_at = NULL
		 WHERE bank_id = $1 AND fact_type IN ('experience', 'world')`,
		bankID); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}

// EnqueueConsolidation records a consolidation pass for a bank. A scoped submit
// (observationScopes non-nil) never deduplicates against a pending full-bank
// pass — the caller wants a targeted run — and records the scopes in the task
// payload; an unscoped submit reuses an outstanding pass for the bank.
func (s *PostgresStore) EnqueueConsolidation(ctx context.Context, bankID string, observationScopes ...[]string) (Operation, bool, error) {
	op := Operation{
		OperationID: newID(), BankID: bankID, OperationType: OperationTypeConsolidate,
		Status: StatusPending, SerializationKey: ConsolidationSerializationKey(bankID),
	}
	if len(observationScopes) > 0 {
		// A scoped submit targets a subset of the bank, so it never deduplicates
		// against a pass already queued — the caller asked for this run
		// specifically. It is not conditional, so it does not take the bank lock.
		op.Payload = map[string]any{"observation_scopes": observationScopes}
		stored, err := s.PutOperation(ctx, op)
		if err != nil {
			return Operation{}, false, err
		}
		return stored, false, nil
	}
	// An unscoped submit reuses an outstanding full-bank pass. The check-and-
	// insert is atomic, so a burst of submits cannot each see no queued pass and
	// each insert its own — the duplicate passes that race the observation layer
	// and pile up as retry_blocked.
	result, err := s.enqueueDedupeByBank(ctx, op, []string{"pending", "processing"}, true)
	if err != nil {
		return Operation{}, false, err
	}
	return result.Operation, result.Deduplicated, nil
}

// EnqueueCallerRequestedConsolidation records a consolidation pass an explicit
// /consolidate started: the task payload carries caller_requested, which lets
// the pass's chain keep draining past the auto-consolidation gate
// (memory_engine.py:23716-23717, api/http.py:9992).
func (s *PostgresStore) EnqueueCallerRequestedConsolidation(ctx context.Context, bankID string) (Operation, bool, error) {
	op := Operation{
		OperationID: newID(), BankID: bankID, OperationType: OperationTypeConsolidate,
		Status: StatusPending, SerializationKey: ConsolidationSerializationKey(bankID),
		Payload: map[string]any{"caller_requested": true},
	}
	result, err := s.enqueueDedupeByBank(ctx, op, []string{"pending", "processing"}, true)
	if err != nil {
		return Operation{}, false, err
	}
	return result.Operation, result.Deduplicated, nil
}

// EnqueueGraphMaintenance records a graph-maintenance pass for a bank, reusing
// one already pending or running for the bank. A sweep drains the bank's
// relink and entity-prune queues to empty, so a running one already covers work
// queued after it started — matching the reference's dedupe_by_bank including
// processing. The check-and-insert is atomic for the same reason
// EnqueueConsolidation's is.
func (s *PostgresStore) EnqueueGraphMaintenance(ctx context.Context, bankID string) (Operation, bool, error) {
	op := Operation{
		OperationID: newID(), BankID: bankID, OperationType: OperationTypeGraphMaintenance,
		Status: StatusPending, SerializationKey: GraphMaintenanceSerializationKey(bankID),
	}
	result, err := s.enqueueDedupeByBank(ctx, op, []string{"pending", "processing"}, false)
	if err != nil {
		return Operation{}, false, err
	}
	return result.Operation, result.Deduplicated, nil
}

// RecoverConsolidations returns a bank's permanently-failed sources to the
// not-yet-consolidated state and reports how many. The count is per stranded
// fact, not per round; only source facts (world/experience) are eligible, so an
// observation that somehow carries a failure marker is left alone. No pass is
// queued: the reference's retry runs the requeue UPDATE and returns the count,
// leaving the next consolidation to pick the sources up.
func (s *PostgresStore) RecoverConsolidations(ctx context.Context, bankID string) (int, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 int
		return zero0, schemaErr
	}
	qualified := quoteIdent(schema)
	query := `WITH requeued AS (
	              UPDATE ` + qualified + `.memory_units
	              SET consolidation_failed_at = NULL, consolidated_at = NULL
	              WHERE bank_id = $1
	                AND consolidation_failed_at IS NOT NULL
	                AND fact_type IN ('experience', 'world')
	              RETURNING 1
	          )
	          SELECT count(*) FROM requeued`
	var retried int
	if err := s.db.QueryRowContext(ctx, query, bankID).Scan(&retried); err != nil {
		return 0, err
	}
	return retried, nil
}

// scanMemoryUnitRow reads the memory-unit projection.
func scanMemoryUnitRow(row rowScanner) (MemoryUnit, error) {
	var unit MemoryUnit
	var unitType string
	var occurredStart, occurredEnd, mentionedAt, eventDate sql.NullTime
	var createdAt, updatedAt, editedAt, invalidatedAt sql.NullTime
	var consolidatedAt, consolidationFailedAt sql.NullTime
	var invalidationReason sql.NullString
	var sourceIDsRaw, scopesRaw sql.NullString
	var tagsRaw, metadataRaw []byte
	if err := row.Scan(&unit.ID, &unit.BankID, &unit.Text, &unitType, &unit.Context,
		&occurredStart, &occurredEnd, &mentionedAt, &eventDate,
		&unit.DocumentID, &unit.ChunkID, &tagsRaw, &metadataRaw,
		&createdAt, &updatedAt, &editedAt,
		&unit.ProofCount, &sourceIDsRaw, &consolidatedAt, &consolidationFailedAt, &scopesRaw,
		&invalidatedAt, &invalidationReason); err != nil {
		return MemoryUnit{}, err
	}
	unit.FactType = MemoryUnitType(unitType)
	unit.OccurredStart = nullTimePtr(occurredStart)
	unit.OccurredEnd = nullTimePtr(occurredEnd)
	unit.MentionedAt = nullTimePtr(mentionedAt)
	unit.EventDate = nullTimePtr(eventDate)
	unit.Tags = decodePostgresTextArray(tagsRaw)
	unit.Metadata = decodeJSONMap(metadataRaw)
	unit.CreatedAt = nullTimePtr(createdAt)
	unit.UpdatedAt = nullTimePtr(updatedAt)
	unit.EditedAt = nullTimePtr(editedAt)
	unit.SourceMemoryIDs = decodePostgresTextArray([]byte(sourceIDsRaw.String))
	unit.ConsolidatedAt = nullTimePtr(consolidatedAt)
	unit.ConsolidationFailedAt = nullTimePtr(consolidationFailedAt)
	if scopesRaw.Valid && scopesRaw.String != "" && scopesRaw.String != "null" {
		unit.ObservationScopes = json.RawMessage(scopesRaw.String)
	}
	unit.InvalidatedAt = nullTimePtr(invalidatedAt)
	if invalidationReason.Valid {
		reason := invalidationReason.String
		unit.InvalidationReason = &reason
	}
	return unit, nil
}

// DirectUnitEntityNames returns the entities each unit mentions through its own
// postings, keyed by unit id. It is the listing's read: the listing does not
// inherit an observation's source memories' entities the way the detail read
// does, because the reference's list query joins unit_entities alone. Units
// with no postings are absent from the map rather than mapped to an empty slice.
func (s *PostgresStore) DirectUnitEntityNames(ctx context.Context, bankID string, unitIDs []string) (map[string][]string, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 map[string][]string
		return zero0, schemaErr
	}
	if len(unitIDs) == 0 {
		return map[string][]string{}, nil
	}
	qualified := quoteIdent(schema)
	rows, err := s.db.QueryContext(ctx,
		`SELECT ue.unit_id::text, e.canonical_name
		 FROM `+qualified+`.unit_entities ue
		 JOIN `+qualified+`.entities e ON e.id = ue.entity_id
		 WHERE ue.unit_id = ANY($1::uuid[]) AND e.bank_id = $2
		 ORDER BY 1, 2`, unitIDs, bankID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	byUnit := map[string][]string{}
	for rows.Next() {
		var unitID, name string
		if err := rows.Scan(&unitID, &name); err != nil {
			return nil, err
		}
		byUnit[unitID] = append(byUnit[unitID], name)
	}
	return byUnit, rows.Err()
}

// ensure strings is used by the knowledge search.
var _ = strings.TrimSpace

// pageBoundsInFilter gives the page-bound parameters a type without changing a
// row. The reference's SQL mentions them only in its trailing `LIMIT $n OFFSET
// $n+1` (engine/memories/pg/curation.py:284-292, 320) and never has to go
// further: asyncpg resolves positional parameters by their bound value. This
// driver hands PostgreSQL a text-format statement whose parameters all arrive
// as text, so a position the statement does not otherwise mention has no type
// to be parsed as. An inert predicate does that job — the positions are named
// with the type they will be bound with, and the predicate is unconditionally
// true.
func pageBoundsInFilter(limitPlaceholder, offsetPlaceholder string) string {
	return " AND " + limitPlaceholder + "::bigint IS NOT NULL AND " +
		offsetPlaceholder + "::bigint IS NOT NULL"
}
