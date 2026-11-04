package memoryops

import (
	"sort"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// BuildRecallResponse maps a storage-level recall outcome into the public
// response envelope, including the optional side channels and trace object.
// The result list is bounded by the caller's token cap in rank order, and each
// requested side channel is bounded by its own resolved budget. The reference
// keeps nulls for this route, so a channel that was not requested (or that the
// engine left empty) stays null rather than disappearing.
func BuildRecallResponse(result storage.RecallResult, req RecallRequest) RecallResponse {
	selected := applyTokenCap(result.Matches, recallMaxTokens(req))
	inc := resolveRecallInclude(req.Include)
	items := make([]RecallResultItem, 0, len(selected))
	for _, match := range selected {
		items = append(items, toResultItem(match, inc.sourceFacts))
	}
	response := RecallResponse{Results: items}
	if inc.entities {
		response.Entities = capEntityChannel(buildEntityChannel(result), inc.entityTokens)
	}
	if inc.chunks {
		response.Chunks = capChunkChannel(buildChunkChannel(result), inc.chunkTokens)
	}
	if inc.sourceFacts {
		response.SourceFacts, response.SourceFactsTruncated = selectSourceFacts(selected, result.SourceFactMap, inc)
	}
	if req.Trace {
		response.Trace = buildRecallTraceResponse(result, req, len(items))
	}
	return response
}

// capEntityChannel bounds the entity channel by total observation tokens,
// clipping the last observations that fit and dropping the rest.
func capEntityChannel(channels map[string]EntityStateEntry, maxTokens int) map[string]EntityStateEntry {
	if maxTokens <= 0 || len(channels) == 0 {
		return channels
	}
	remaining := maxTokens
	for _, key := range sortedEntityKeys(channels) {
		entry := channels[key]
		kept := entry.Observations[:0]
		for _, observation := range entry.Observations {
			tokens := tokenCount(observation.Text)
			if remaining <= 0 {
				break
			}
			if tokens > remaining {
				observation.Text = clipToTokens(observation.Text, remaining)
				tokens = remaining
			}
			remaining -= tokens
			kept = append(kept, observation)
		}
		entry.Observations = kept
		channels[key] = entry
	}
	return channels
}

// capChunkChannel bounds the chunk channel by total tokens, marking a clipped
// chunk as truncated and dropping the chunks beyond the budget.
func capChunkChannel(channels map[string]ChunkEntry, maxTokens int) map[string]ChunkEntry {
	if maxTokens <= 0 || len(channels) == 0 {
		return channels
	}
	remaining := maxTokens
	for _, key := range sortedChunkKeys(channels) {
		entry := channels[key]
		tokens := tokenCount(entry.Text)
		if tokens > remaining {
			if remaining <= 0 {
				delete(channels, key)
				continue
			}
			entry.Text = clipToTokens(entry.Text, remaining)
			entry.Truncated = true
			remaining = 0
		} else {
			remaining -= tokens
		}
		channels[key] = entry
	}
	return channels
}

// selectSourceFacts applies the reference's source-fact budget
// (engine/source_facts.py:39-87, `select_source_facts_within_budget`) to the
// observations that survived the fact budget.
//
// Two properties come from that algorithm and neither is optional:
//
//   - A non-negative `max_tokens_per_observation` gives every observation its
//     own budget and the total is then unused; otherwise ONE shared budget is
//     spent in observation-rank order, so truncation hits the tail.
//   - A fact that does not fit is SKIPPED, never clipped — the reference's
//     selection only ever adds whole texts. A budget of 0 therefore returns no
//     fact at all and reports `source_facts_truncated: true` rather than an
//     empty map.
//
// `truncated` means exactly "some advertised id has no entry": a skip that a
// later observation's budget still admits is not truncation.
func selectSourceFacts(selected []storage.RecallMatch, available map[string]storage.RecallMatch, inc recallInclude) (map[string]RecallResultItem, *bool) {
	// Source ids in observation-rank order, plus each observation's own list.
	ordered := make([]string, 0)
	perObservation := make([][]string, 0)
	for _, match := range selected {
		if match.Type != "observation" {
			continue
		}
		ids := make([]string, 0, len(match.SourceMemoryIDs))
		for _, id := range match.SourceMemoryIDs {
			ids = append(ids, id)
			if !stringInSlice(ordered, id) {
				ordered = append(ordered, id)
			}
		}
		perObservation = append(perObservation, ids)
	}
	if len(ordered) == 0 {
		// No observation cites anything, so the reference never fills the map and
		// reports the channel as null.
		return nil, boolPointer(false)
	}
	take := func(id string) (storage.RecallMatch, bool) {
		match, ok := available[id]
		return match, ok
	}
	seen := make(map[string]bool, len(ordered))
	kept := make([]string, 0, len(ordered))
	skipped := make(map[string]bool)
	add := func(id string) {
		if seen[id] {
			return
		}
		seen[id] = true
		kept = append(kept, id)
	}
	if inc.sourceFactPerObservation >= 0 {
		// Per-observation capping: each observation selects from its own list up
		// to its own budget, and the global budget is unused.
		for _, ids := range perObservation {
			spent := 0
			for _, id := range ids {
				match, ok := take(id)
				if !ok {
					continue
				}
				cost := tokenCount(match.Text)
				if spent+cost > inc.sourceFactPerObservation {
					skipped[id] = true
					continue
				}
				spent += cost
				add(id)
			}
		}
	} else {
		spent := 0
		for _, id := range ordered {
			match, ok := take(id)
			if !ok {
				continue
			}
			cost := tokenCount(match.Text)
			if inc.sourceFactTokens >= 0 && spent+cost > inc.sourceFactTokens {
				skipped[id] = true
				continue
			}
			spent += cost
			add(id)
		}
	}
	truncated := false
	for id := range skipped {
		if !seen[id] {
			truncated = true
			break
		}
	}
	if len(kept) == 0 {
		return nil, boolPointer(truncated)
	}
	out := make(map[string]RecallResultItem, len(kept))
	for _, id := range kept {
		match, _ := take(id)
		out[id] = sourceFactItem(match)
	}
	return out, boolPointer(truncated)
}

// stringInSlice reports whether a slice already holds a value.
func stringInSlice(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// boolPointer returns a pointer to a bool, so an optional envelope member can
// carry an explicit false.
func boolPointer(value bool) *bool { return &value }

// clipToTokens clips a text to at most maxTokens whitespace fields.
func clipToTokens(text string, maxTokens int) string {
	if maxTokens <= 0 {
		return ""
	}
	fields := strings.Fields(text)
	if len(fields) <= maxTokens {
		return text
	}
	return strings.Join(fields[:maxTokens], " ")
}

func sortedEntityKeys(channels map[string]EntityStateEntry) []string {
	keys := make([]string, 0, len(channels))
	for key := range channels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedChunkKeys(channels map[string]ChunkEntry) []string {
	keys := make([]string, 0, len(channels))
	for key := range channels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// buildRecallTraceResponse renders the trace for a recall that asked for one.
//
// The shape is the reference's SearchTrace, and it is fixed: the query block,
// the four list payloads (retrieval_results, rrf_merged, reranked, and the
// legacy entry_points/visits), the summary and final_results are each emitted
// in the reference's declared order, and a list the tracer did not populate is
// emitted empty rather than omitted
// (engine/search/trace.py:158-192, engine/search/tracer.py:394-437).
func buildRecallTraceResponse(result storage.RecallResult, req RecallRequest, returned int) any {
	detail := result.Trace.Detail
	if detail != nil {
		return renderTrace(detail, result, req, returned)
	}
	return renderEmptyTrace(result, req, returned)
}

// renderTrace renders a populated trace: the query block, the arm lists, the
// fusion and rerank payloads, the legacy graph payload and the summary.
func renderTrace(detail *storage.RecallTraceDetail, result storage.RecallResult, req RecallRequest, returned int) any {
	// The trace is an OpenRowModel in the reference but its members are
	// declared there, so it reaches the wire in declaration order — a Go map
	// would emit them alphabetically.
	return traceObject{
		{Key: "query", Value: renderTraceQuery(detail, req, result)},
		{Key: "retrieval_results", Value: renderArmResults(detail)},
		{Key: "rrf_merged", Value: renderRRFMerged(detail.RRFMerged)},
		{Key: "reranked", Value: renderReranked(detail.Reranked)},
		{Key: "entry_points", Value: renderEntryPoints(detail.EntryPoints)},
		{Key: "visits", Value: renderVisits(detail.Visits)},
		{Key: "summary", Value: renderTraceSummary(detail, returned)},
		{Key: "final_results", Value: renderFinalResults(detail.FinalResults)},
	}
}

// renderTraceQuery renders the query block. Every member is present, and the
// embedding is always a list (empty when the deployment has no embedder), which
// is what the reference's QueryInfo declares.
func renderTraceQuery(detail *storage.RecallTraceDetail, req RecallRequest, result storage.RecallResult) any {
	embedding := make([]any, 0, len(detail.QueryEmbedding))
	for _, dimension := range detail.QueryEmbedding {
		embedding = append(embedding, dimension)
	}
	// The reference's QueryInfo declares these members in this order, so the
	// block reaches the wire in declaration order rather than alphabetically.
	var constraint any
	if detail.TemporalStart != nil || detail.TemporalEnd != nil {
		constraint = traceObject{
			{Key: "start", Value: constraintStamp(detail.TemporalStart)},
			{Key: "end", Value: constraintStamp(detail.TemporalEnd)},
		}
	}
	return traceObject{
		{Key: "query_text", Value: req.Query},
		{Key: "query_embedding", Value: embedding},
		{Key: "timestamp", Value: detail.Anchor.UTC().Format(time.RFC3339Nano)},
		{Key: "budget", Value: detail.Budget},
		{Key: "max_tokens", Value: recallMaxTokens(req)},
		{Key: "tags", Value: optionalStrings(req.Tags)},
		{Key: "tags_match", Value: detail.TagsMatch},
		{Key: "temporal_constraint", Value: constraint},
	}
}

// constraintStamp renders one end of the temporal constraint, null when that
// end was not supplied.
func constraintStamp(at *time.Time) any {
	if at == nil {
		return nil
	}
	return at.UTC().Format(time.RFC3339Nano)
}

// renderTraceSummary renders the summary block: the visit counts, the search
// duration and the ordered phase list.
func renderTraceSummary(detail *storage.RecallTraceDetail, returned int) any {
	visited := len(detail.Visits)
	phases := make([]any, 0, len(detail.PhaseMetrics))
	for _, metric := range detail.PhaseMetrics {
		details := make(map[string]any, len(metric.Details))
		for key, value := range metric.Details {
			details[key] = value
		}
		phases = append(phases, traceObject{
			{Key: "phase_name", Value: metric.Name},
			{Key: "duration_seconds", Value: metric.Seconds},
			{Key: "details", Value: details},
		})
	}
	return traceObject{
		{Key: "total_nodes_visited", Value: visited},
		{Key: "entry_points_found", Value: len(detail.EntryPoints)},
		{Key: "budget_used", Value: visited},
		{Key: "budget_remaining", Value: detail.Budget - visited},
		{Key: "total_duration_seconds", Value: detail.TotalDurationSeconds},
		{Key: "results_returned", Value: returned},
		{Key: "phase_metrics", Value: phases},
	}
}

// renderEmptyTrace renders the fixed trace shape for a recall whose pipeline
// produced no detail payload (a deployment with the search tracer off): every
// list still appears, empty, so a client reading the trace unconditionally sees
// the same keys.
func renderEmptyTrace(result storage.RecallResult, req RecallRequest, returned int) any {
	detail := &storage.RecallTraceDetail{
		Anchor:    time.Now().UTC(),
		Budget:    result.Trace.Budget,
		TagsMatch: tagsMatchOrDefault(req.TagsMatch),
	}
	return renderTrace(detail, result, req, returned)
}

// renderEntryPoints renders the legacy entry_points payload.
func renderEntryPoints(points []storage.TraceEntryPoint) []any {
	out := make([]any, 0, len(points))
	for _, point := range points {
		out = append(out, traceObject{
			{Key: "node_id", Value: point.NodeID},
			{Key: "text", Value: point.Text},
			{Key: "similarity_score", Value: point.SimilarityScore},
			{Key: "rank", Value: point.Rank},
		})
	}
	return out
}

// renderVisits renders the legacy visits payload, with each visit's weight
// breakdown expanded the way the reference's WeightComponents model is.
func renderVisits(visits []storage.TraceVisit) []any {
	out := make([]any, 0, len(visits))
	for _, visit := range visits {
		weights := traceObject{
			{Key: "activation", Value: visit.Activation},
			{Key: "semantic_similarity", Value: visit.SemanticSimilarity},
			{Key: "recency", Value: visit.Recency},
			{Key: "frequency", Value: visit.Frequency},
			{Key: "final_weight", Value: visit.FinalWeight},
			{Key: "activation_contribution", Value: 0.3 * visit.Activation},
			{Key: "semantic_contribution", Value: 0.3 * visit.SemanticSimilarity},
			{Key: "recency_contribution", Value: 0.25 * visit.Recency},
			{Key: "frequency_contribution", Value: 0.15 * visit.Frequency},
		}
		var eventDate any
		if visit.EventDate != nil {
			eventDate = visit.EventDate.UTC().Format(time.RFC3339Nano)
		}
		var finalRank any
		if visit.FinalRank != nil {
			finalRank = *visit.FinalRank
		}
		out = append(out, traceObject{
			{Key: "step", Value: visit.Step},
			{Key: "node_id", Value: visit.NodeID},
			{Key: "text", Value: visit.Text},
			{Key: "context", Value: visit.Context},
			{Key: "event_date", Value: eventDate},
			{Key: "is_entry_point", Value: visit.IsEntryPoint},
			{Key: "weights", Value: weights},
			{Key: "final_rank", Value: finalRank},
		})
	}
	return out
}

// renderFinalResults renders the final_results payload: one ordered record per
// scored result.
func renderFinalResults(rows []storage.TraceResultRow) []any {
	out := make([]any, 0, len(rows))
	for _, row := range rows {
		// The storage layer assembles the keys in the reference's own dict layout
		// (traceFinalResultRow), so they are replayed in that order rather than
		// collapsed into a map, which would sort them.
		record := make(traceObject, 0, len(row.Keys))
		for index, key := range row.Keys {
			record = append(record, httpx.OrderedMember{Key: key, Value: row.Values[index]})
		}
		out = append(out, record)
	}
	return out
}

// traceObject is the trace's rendering of an object whose members the reference
// declares in a model: the members reach the wire in declaration order, which a
// Go map (alphabetical) would not reproduce.
type traceObject = httpx.OrderedObject

// optionalStrings renders an optional string list: the list itself when one was
// given, null otherwise. The reference keeps its `tags: None` a null.
func optionalStrings(values []string) any {
	if values == nil {
		return nil
	}
	return values
}

// renderArmResults renders one entry per retrieval arm and fact type.
func renderArmResults(detail *storage.RecallTraceDetail) []any {
	out := make([]any, 0, len(detail.RetrievalResults))
	for _, arm := range detail.RetrievalResults {
		results := make([]any, 0, len(arm.Results))
		for _, hit := range arm.Results {
			var eventDate any
			if hit.EventDate != nil {
				eventDate = hit.EventDate.UTC().Format(time.RFC3339Nano)
			}
			results = append(results, traceObject{
				{Key: "rank", Value: hit.Rank},
				{Key: "node_id", Value: hit.NodeID},
				{Key: "text", Value: hit.Text},
				{Key: "context", Value: hit.Context},
				{Key: "event_date", Value: eventDate},
				{Key: "fact_type", Value: hit.FactType},
				{Key: "score", Value: hit.Score},
				{Key: "score_name", Value: hit.ScoreName},
			})
		}
		// The arm's own duration and metadata are part of the reference's
		// RetrievalMethodResults (engine/search/trace.py:99-107); an arm whose
		// time was not measured reports 0.0, and its metadata carries the
		// budget the reference records for that arm (memory_engine.py:9676-9760).
		out = append(out, traceObject{
			{Key: "method_name", Value: arm.MethodName},
			{Key: "fact_type", Value: arm.FactType},
			{Key: "results", Value: results},
			{Key: "duration_seconds", Value: detail.ArmDurations[arm.MethodName+"/"+arm.FactType]},
			{Key: "metadata", Value: armMetadata(arm.MethodName, detail.Budget)},
		})
	}
	return out
}

// armMetadata is the per-method metadata the reference records next to an
// arm's results: the retrieval limit for semantic and bm25, and the budget for
// graph and temporal (engine/memory_engine.py:9676-9760).
func armMetadata(method string, budget int) any {
	switch method {
	case "graph", "temporal":
		return traceObject{{Key: "budget", Value: budget}}
	default:
		return traceObject{{Key: "limit", Value: budget}}
	}
}

// renderRRFMerged renders the fused candidate list with each candidate's rank
// per contributing arm.
func renderRRFMerged(entries []storage.TraceRRFEntry) []any {
	out := make([]any, 0, len(entries))
	for _, entry := range entries {
		sourceRanks := make(map[string]any, len(entry.SourceRanks))
		for key, rank := range entry.SourceRanks {
			sourceRanks[key] = rank
		}
		out = append(out, traceObject{
			{Key: "node_id", Value: entry.NodeID},
			{Key: "text", Value: entry.Text},
			{Key: "rrf_score", Value: entry.RRFScore},
			{Key: "source_ranks", Value: sourceRanks},
			{Key: "final_rrf_rank", Value: entry.FinalRRFRank},
		})
	}
	return out
}

// renderReranked renders the ranked list with the components behind each score.
func renderReranked(entries []storage.TraceRerankedEntry) []any {
	out := make([]any, 0, len(entries))
	for _, entry := range entries {
		components := make(map[string]any, len(entry.ScoreComponents))
		for key, value := range entry.ScoreComponents {
			components[key] = value
		}
		out = append(out, traceObject{
			{Key: "node_id", Value: entry.NodeID},
			{Key: "text", Value: entry.Text},
			{Key: "rerank_score", Value: entry.RerankScore},
			{Key: "rerank_rank", Value: entry.RerankRank},
			{Key: "rrf_rank", Value: entry.RRFRank},
			{Key: "rank_change", Value: entry.RankChange},
			{Key: "score_components", Value: components},
		})
	}
	return out
}

// toResultItem maps a storage match into a public result item. An empty
// optional value becomes a nil pointer or slice so the JSON encoder emits null,
// matching the reference's null-keeping response. `source_fact_ids` is only
// reported when the caller asked for source facts: the reference populates it
// from the source-fact enrichment pass and leaves it null otherwise.
func toResultItem(match storage.RecallMatch, includeSourceFacts bool) RecallResultItem {
	item := RecallResultItem{
		ID:            match.ID,
		Text:          match.Text,
		Type:          nullIfEmptyString(match.Type),
		Entities:      nonEmptyStrings(match.Entities),
		Context:       contextValue(match),
		OccurredStart: httpx.FormatTimestampPtr(match.OccurredStart),
		OccurredEnd:   httpx.FormatTimestampPtr(match.OccurredEnd),
		MentionedAt:   httpx.FormatTimestampPtr(match.MentionedAt),
		DocumentID:    nullIfEmptyString(match.DocumentID),
		Metadata:      nonNilMap(match.Metadata),
		ChunkID:       nullIfEmptyString(match.ChunkID),
		Tags:          orEmptySlice(match.Tags),
		Scores:        toScoreObject(match),
		Attachments:   toAttachments(match.Attachments),
	}
	if includeSourceFacts {
		item.SourceFactIDs = nonEmptyStrings(match.SourceMemoryIDs)
	}
	return item
}

// nullIfEmptyString returns a pointer to a non-empty string, or nil for an
// empty one, so an absent value serializes as null.
// stringValue always yields a pointer, so a field the reference renders as the
// stored string keeps an empty string an empty string rather than collapsing it
// to null (a fact retained without a context carries "").
func stringValue(value string) *string { return &value }

// contextValue renders a result's context: null for a unit whose context column
// is NULL — an observation, which the reference's insert never gives one — and
// the stored string otherwise, including the empty one a fact with no context
// carries. The two are different values on the wire, which is why the absence is
// tracked rather than inferred from the string.
func contextValue(match storage.RecallMatch) *string {
	if match.ContextAbsent {
		return nil
	}
	return stringValue(match.Context)
}

// orEmptySlice returns the slice unchanged, or an empty (non-nil) slice, so a
// collection the reference declares as a list serializes as [] rather than null.
func orEmptySlice(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func nullIfEmptyString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// nonEmptyStrings returns the slice unchanged, or nil when it is empty, so an
// absent collection serializes as null.
func nonEmptyStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return values
}

// toScoreObject projects the storage stage scores into the public score object.
func toScoreObject(match storage.RecallMatch) RecallScoreObject {
	obj := RecallScoreObject{Final: match.Final}
	if v, ok := match.Scores["semantic"]; ok {
		val := v
		obj.Semantic = &val
	}
	if v, ok := match.Scores["keyword"]; ok {
		val := v
		obj.Keyword = &val
	}
	if v, ok := match.Scores["reranker"]; ok {
		val := v
		obj.Reranker = &val
	}
	return obj
}

// toAttachments maps storage attachments, reporting null when there are none so
// the reference's null-keeping shape is preserved.
func toAttachments(attachments []storage.RecallAttachment) []Attachment {
	if len(attachments) == 0 {
		return nil
	}
	out := make([]Attachment, 0, len(attachments))
	for _, a := range attachments {
		out = append(out, attachmentFromRecall(a))
	}
	return out
}

// buildEntityChannel keys the entity side channel by entity name.
func buildEntityChannel(result storage.RecallResult) map[string]EntityStateEntry {
	if len(result.EntityStates) == 0 {
		return nil
	}
	out := make(map[string]EntityStateEntry, len(result.EntityStates))
	for _, state := range result.EntityStates {
		obs := make([]EntityObservationEntry, 0, len(state.Observations))
		for _, o := range state.Observations {
			obs = append(obs, EntityObservationEntry{Text: o.Text, MentionedAt: httpx.FormatTimestampPtr(o.MentionedAt)})
		}
		out[state.Name] = EntityStateEntry{
			EntityID:      state.ID,
			CanonicalName: state.CanonicalName,
			Observations:  obs,
		}
	}
	return out
}

// buildChunkChannel keys the chunk side channel by chunk id.
func buildChunkChannel(result storage.RecallResult) map[string]ChunkEntry {
	if len(result.ChunkEntries) == 0 {
		return nil
	}
	out := make(map[string]ChunkEntry, len(result.ChunkEntries))
	for _, c := range result.ChunkEntries {
		out[c.ID] = ChunkEntry{ID: c.ID, Text: c.Text, ChunkIndex: c.Index, Truncated: c.Truncated}
	}
	return out
}

// sourceFactItem maps one source fact into the entry the source-fact channel
// carries: the ranked-result shape with the members the reference does not
// hydrate for a source row cleared.
func sourceFactItem(match storage.RecallMatch) RecallResultItem {
	item := toResultItem(match, false)
	item.Entities = nil
	item.Tags = nonEmptyStrings(match.Tags)
	return item
}

// applyTokenCap returns the highest-ranked facts that fit the token cap. A
// non-positive cap returns no facts. An oversized fact skips only itself. If
// the cap is positive, memories matched, but no single fact fits, the top fact
// is returned whole and over the cap rather than an empty list.
func applyTokenCap(matches []storage.RecallMatch, cap int) []storage.RecallMatch {
	if len(matches) == 0 {
		return matches
	}
	if cap <= 0 {
		return nil
	}
	selected := make([]storage.RecallMatch, 0, len(matches))
	spent := 0
	for _, match := range matches {
		cost := tokenCount(match.Text)
		if spent+cost > cap {
			continue
		}
		selected = append(selected, match)
		spent += cost
	}
	if len(selected) == 0 {
		return matches[:1]
	}
	return selected
}

// tokenCount is the token cost the recall token budget is measured in: the
// real BPE count under the selected vocabulary, so a cap means the same thing
// to this service as it does to a caller budgeting a model's context. An empty
// text still costs one token, so it can never be free.
func tokenCount(text string) int {
	if n := storage.CountTokens(text); n > 0 {
		return n
	}
	return 1
}

// nonNilMap returns the map unchanged, or an empty one when it is nil, so a
// recall result always reports `metadata` as an object rather than omitting it.
func nonNilMap(values map[string]any) map[string]any {
	if values == nil {
		return map[string]any{}
	}
	return values
}
