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
	items := make([]RecallResultItem, 0, len(selected))
	for _, match := range selected {
		items = append(items, toResultItem(match))
	}
	response := RecallResponse{Results: items}
	inc := resolveRecallInclude(req.Include)
	if inc.entities {
		response.Entities = capEntityChannel(buildEntityChannel(result), inc.entityTokens)
	}
	if inc.chunks {
		response.Chunks = capChunkChannel(buildChunkChannel(result), inc.chunkTokens)
	}
	if inc.sourceFacts {
		var truncated bool
		response.SourceFacts, truncated = capSourceFactChannel(buildSourceFactChannel(result),
			inc.sourceFactTokens, inc.sourceFactPerObservation)
		flag := truncated || result.SourceFactsTruncated
		response.SourceFactsTruncated = &flag
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

// capSourceFactChannel bounds the source-fact channel by total tokens and each
// entry by its per-observation budget, reporting whether anything was clipped.
func capSourceFactChannel(channels map[string]RecallResultItem, maxTokens, perObservation int) (map[string]RecallResultItem, bool) {
	if len(channels) == 0 {
		return channels, false
	}
	truncated := false
	remaining := maxTokens
	for _, key := range sortedResultKeys(channels) {
		entry := channels[key]
		if perObservation > 0 {
			if tokens := tokenCount(entry.Text); tokens > perObservation {
				entry.Text = clipToTokens(entry.Text, perObservation)
				truncated = true
			}
		}
		if maxTokens > 0 {
			tokens := tokenCount(entry.Text)
			if tokens > remaining {
				if remaining <= 0 {
					delete(channels, key)
					truncated = true
					continue
				}
				entry.Text = clipToTokens(entry.Text, remaining)
				remaining = 0
				truncated = true
			} else {
				remaining -= tokens
			}
		}
		channels[key] = entry
	}
	return channels, truncated
}

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

func sortedResultKeys(channels map[string]RecallResultItem) []string {
	keys := make([]string, 0, len(channels))
	for key := range channels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// buildRecallTraceResponse renders the debug trace for a recall that asked for one. The shape
// follows the reference: a query block describing what was searched, and a
// summary block carrying the counts and per-stage timings a reader needs to
// account for the call's duration.
func buildRecallTraceResponse(result storage.RecallResult, req RecallRequest, returned int) map[string]any {
	stageCounts := map[string]any{
		"semantic": result.Trace.StageCounts["semantic"],
		"keyword":  result.Trace.StageCounts["keyword"],
		"reranker": result.Trace.StageCounts["reranker"],
	}
	phaseMetrics := map[string]any{
		"retrieval_ms": result.Trace.PhaseMetrics.RetrievalMillis,
		"fusion_ms":    result.Trace.PhaseMetrics.FusionMillis,
		"assembly_ms":  result.Trace.PhaseMetrics.AssemblyMillis,
		"total_ms":     result.Trace.PhaseMetrics.TotalMillis,
	}
	query := map[string]any{
		"query_text": req.Query,
		"budget":     result.Trace.Budget,
		"max_tokens": recallMaxTokens(req),
		"tags":       nonNilStrings(req.Tags),
	}
	trace := map[string]any{
		"query": query,
		"summary": map[string]any{
			"candidates_found":  result.Trace.CandidatesFound,
			"budget_used":       result.Trace.BudgetUsed,
			"budget_remaining":  result.Trace.Budget - result.Trace.BudgetUsed,
			"total_duration_ms": result.Trace.PhaseMetrics.TotalMillis,
			"results_returned":  returned,
			"phase_metrics":     phaseMetrics,
			"stage_counts":      stageCounts,
		},
		"retrieval_results": []any{},
		"rrf_merged":        []any{},
		"reranked":          []any{},
	}
	if detail := result.Trace.Detail; detail != nil {
		renderTraceDetail(query, trace, detail)
	}
	return trace
}

// renderTraceDetail folds the reference-shaped retrieval detail into the trace:
// the query block reports the anchor the ranking was measured against, and the
// three result lists describe the arms, the fusion and the rerank.
func renderTraceDetail(query, trace map[string]any, detail *storage.RecallTraceDetail) {
	query["timestamp"] = detail.Anchor.UTC().Format(time.RFC3339Nano)
	query["budget"] = detail.Budget
	if detail.TagsMatch != "" {
		query["tags_match"] = detail.TagsMatch
	}
	if detail.TemporalStart != nil || detail.TemporalEnd != nil {
		constraint := map[string]any{}
		if detail.TemporalStart != nil {
			constraint["start"] = detail.TemporalStart.UTC().Format(time.RFC3339Nano)
		}
		if detail.TemporalEnd != nil {
			constraint["end"] = detail.TemporalEnd.UTC().Format(time.RFC3339Nano)
		}
		query["temporal_constraint"] = constraint
	}
	trace["retrieval_results"] = renderArmResults(detail.RetrievalResults)
	trace["rrf_merged"] = renderRRFMerged(detail.RRFMerged)
	trace["reranked"] = renderReranked(detail.Reranked)
}

// renderArmResults renders one entry per retrieval arm and fact type.
func renderArmResults(arms []storage.TraceArmResults) []any {
	out := make([]any, 0, len(arms))
	for _, arm := range arms {
		results := make([]any, 0, len(arm.Results))
		for _, hit := range arm.Results {
			entry := map[string]any{
				"rank":       hit.Rank,
				"node_id":    hit.NodeID,
				"text":       hit.Text,
				"context":    hit.Context,
				"fact_type":  hit.FactType,
				"score":      hit.Score,
				"score_name": hit.ScoreName,
			}
			if hit.EventDate != nil {
				entry["event_date"] = hit.EventDate.UTC().Format(time.RFC3339Nano)
			} else {
				entry["event_date"] = nil
			}
			results = append(results, entry)
		}
		out = append(out, map[string]any{
			"method_name": arm.MethodName,
			"fact_type":   arm.FactType,
			"results":     results,
		})
	}
	return out
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
		out = append(out, map[string]any{
			"node_id":        entry.NodeID,
			"text":           entry.Text,
			"rrf_score":      entry.RRFScore,
			"source_ranks":   sourceRanks,
			"final_rrf_rank": entry.FinalRRFRank,
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
		out = append(out, map[string]any{
			"node_id":          entry.NodeID,
			"text":             entry.Text,
			"rerank_score":     entry.RerankScore,
			"rerank_rank":      entry.RerankRank,
			"rrf_rank":         entry.RRFRank,
			"rank_change":      entry.RankChange,
			"score_components": components,
		})
	}
	return out
}

// toResultItem maps a storage match into a public result item. An empty
// optional value becomes a nil pointer or slice so the JSON encoder emits null,
// matching the reference's null-keeping response.
func toResultItem(match storage.RecallMatch) RecallResultItem {
	return RecallResultItem{
		ID:            match.ID,
		Text:          match.Text,
		Type:          nullIfEmptyString(match.Type),
		Entities:      orEmptySlice(match.Entities),
		Context:       stringValue(match.Context),
		OccurredStart: httpx.FormatTimestampPtr(match.OccurredStart),
		OccurredEnd:   httpx.FormatTimestampPtr(match.OccurredEnd),
		MentionedAt:   httpx.FormatTimestampPtr(match.MentionedAt),
		DocumentID:    nullIfEmptyString(match.DocumentID),
		Metadata:      nonNilMap(match.Metadata),
		ChunkID:       nullIfEmptyString(match.ChunkID),
		Tags:          orEmptySlice(match.Tags),
		SourceFactIDs: nonEmptyStrings(match.SourceMemoryIDs),
		Scores:        toScoreObject(match),
		Attachments:   toAttachments(match.Attachments),
	}
}

// nullIfEmptyString returns a pointer to a non-empty string, or nil for an
// empty one, so an absent value serializes as null.
// stringValue always yields a pointer, so a field the reference renders as the
// stored string keeps an empty string an empty string rather than collapsing it
// to null (a fact retained without a context carries "").
func stringValue(value string) *string { return &value }

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

// buildSourceFactChannel keys the source-fact map by source-fact id, carrying
// the same result shape as the ranked list so a client can read a source
// fact's text, type and scores rather than only its text.
func buildSourceFactChannel(result storage.RecallResult) map[string]RecallResultItem {
	if len(result.SourceFactMap) == 0 {
		return nil
	}
	out := make(map[string]RecallResultItem, len(result.SourceFactMap))
	for id, match := range result.SourceFactMap {
		out[id] = toResultItem(match)
	}
	return out
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

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// nonNilMap returns the map unchanged, or an empty one when it is nil, so a
// recall result always reports `metadata` as an object rather than omitting it.
func nonNilMap(values map[string]any) map[string]any {
	if values == nil {
		return map[string]any{}
	}
	return values
}
