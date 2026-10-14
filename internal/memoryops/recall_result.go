package memoryops

import (
	"github.com/studyzy/wenyuan/internal/storage"
)

// BuildRecallResponse maps a storage-level recall outcome into the public
// response envelope, including the optional side channels and trace object.
// The result list is bounded by the caller's token cap in rank order.
func BuildRecallResponse(result storage.RecallResult, req RecallRequest) RecallResponse {
	selected := applyTokenCap(result.Matches, req.MaxTokens)
	items := make([]RecallResultItem, 0, len(selected))
	for _, match := range selected {
		items = append(items, toResultItem(match))
	}
	response := RecallResponse{
		Results:   items,
		Truncated: result.Truncated,
	}
	response.Entities = buildEntityChannel(result)
	response.Chunks = buildChunkChannel(result)
	response.SourceFacts = buildSourceFactChannel(result)
	response.SourceFactsTruncated = result.SourceFactsTruncated
	if req.Trace {
		response.Trace = buildRecallTraceResponse(result, req, len(items))
	}
	return response
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
	return map[string]any{
		"query": map[string]any{
			"query_text": req.Query,
			"budget":     result.Trace.Budget,
			"max_tokens": req.MaxTokens,
			"tags":       nonNilStrings(req.Tags),
		},
		"summary": map[string]any{
			"candidates_found":  result.Trace.CandidatesFound,
			"budget_used":       result.Trace.BudgetUsed,
			"budget_remaining":  result.Trace.Budget - result.Trace.BudgetUsed,
			"total_duration_ms": result.Trace.PhaseMetrics.TotalMillis,
			"results_returned":  returned,
			"phase_metrics":     phaseMetrics,
			"stage_counts":      stageCounts,
		},
	}
}

// toResultItem maps a storage match into a public result item.
func toResultItem(match storage.RecallMatch) RecallResultItem {
	return RecallResultItem{
		ID:            match.ID,
		Text:          match.Text,
		Type:          match.Type,
		Context:       match.Context,
		OccurredStart: match.OccurredStart,
		OccurredEnd:   match.OccurredEnd,
		MentionedAt:   match.MentionedAt,
		ChunkID:       match.ChunkID,
		DocumentID:    match.DocumentID,
		Metadata:      match.Metadata,
		SourceFactIDs: nonNilStrings(match.SourceMemoryIDs),
		Scores:        toScoreObject(match),
		Entities:      nonNilStrings(match.Entities),
		Tags:          nonNilStrings(match.Tags),
		Attachments:   toAttachments(match.Attachments),
	}
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

// toAttachments maps storage attachments, defaulting to an empty collection.
func toAttachments(attachments []storage.RecallAttachment) []Attachment {
	out := make([]Attachment, 0, len(attachments))
	for _, a := range attachments {
		out = append(out, Attachment{ID: a.ID, Kind: a.Kind, URI: a.URI})
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
			obs = append(obs, EntityObservationEntry{Text: o.Text, MentionedAt: o.MentionedAt})
		}
		out[state.Name] = EntityStateEntry{
			ID:            state.ID,
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
		out[c.ID] = ChunkEntry{ID: c.ID, Text: c.Text, Index: c.Index, Truncated: c.Truncated}
	}
	return out
}

// buildSourceFactChannel keys the source-fact map by source-fact id.
func buildSourceFactChannel(result storage.RecallResult) map[string]string {
	if len(result.SourceFactMap) == 0 {
		return nil
	}
	return result.SourceFactMap
}

// applyTokenCap returns the highest-ranked facts that fit the token cap. An
// omitted cap (nil) is unbounded. An explicit non-positive cap returns no
// facts. An oversized fact skips only itself. If the cap is positive, memories
// matched, but no single fact fits, the top fact is returned whole and over the
// cap rather than an empty list.
func applyTokenCap(matches []storage.RecallMatch, capPtr *int) []storage.RecallMatch {
	if len(matches) == 0 {
		return matches
	}
	if capPtr == nil {
		return matches
	}
	cap := *capPtr
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

// tokenCount approximates the token cost of a text as its whitespace fields; an
// empty text costs one token.
func tokenCount(text string) int {
	n := 0
	inWord := false
	for _, r := range text {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			inWord = false
			continue
		}
		if !inWord {
			n++
			inWord = true
		}
	}
	if n == 0 {
		return 1
	}
	return n
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
