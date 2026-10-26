package storage

import (
	"sort"
	"time"
)

// recallArmTrace is one arm's prepared, best-first hits captured for the trace.
// The hits are the ones that actually entered fusion (after the arm's own score
// floor and per-source cap), so the trace reports what the pipeline saw rather
// than what the backend could have offered.
type recallArmTrace struct {
	arm  string
	hits []recallHit
}

// traceArmNames maps an internal ranking label to the reference's arm name, and
// traceArmOrder fixes the order the retrieval results are reported in. Arms the
// reference has no counterpart for (temporal, entity) are absent: they enter as
// scoring signals, not as candidate lists with their own retrieval results.
var traceArmNames = map[string]string{
	ArmSemantic: "semantic",
	ArmKeyword:  "bm25",
	ArmGraph:    "graph",
}

// finalMemoryUnitTypesOrder is the order the trace reports fact types in, which
// is the order the reference iterates them: experience, world, observation —
// the declared fact-type list, not the alphabetical order.
func finalMemoryUnitTypesOrder() []MemoryUnitType {
	return []MemoryUnitType{TypeExperience, TypeWorld, TypeObservation}
}

// traceEntryPointLimit bounds the legacy entry_points payload: the reference
// records the semantic arm's first ten hits (engine/memory_engine.py:9767).
const traceEntryPointLimit = 10

// traceArmOrder is the reporting order of the reference's retrieval arms.
var traceArmOrder = []string{"semantic", "bm25", "graph"}

// traceArmScoreName returns the score label the reference reports for an arm.
func traceArmScoreName(name string) string {
	switch name {
	case "bm25":
		return "bm25_score"
	case "graph":
		return "activation"
	default:
		return "similarity"
	}
}

// buildRecallTraceDetail assembles the reference-shaped retrieval trace: one
// entry per reference arm and fact type, the RRF merge, and the reranked list
// with the components behind each combined score.
func buildRecallTraceDetail(candidates map[string]*recallCandidate, scored []*recallCandidate, ranked []*recallCandidate, armTraces []recallArmTrace, q RecallQuery, limit int) *RecallTraceDetail {
	detail := &RecallTraceDetail{
		Anchor:    traceAnchor(q),
		Budget:    limit,
		MaxTokens: q.MaxTokens,
		Tags:      q.Tags,
		TagsMatch: q.TagsMatch,
	}
	if q.Temporal != nil {
		if q.Temporal.HasStart {
			start := q.Temporal.Start
			detail.TemporalStart = &start
		}
		if q.Temporal.HasEnd {
			end := q.Temporal.End
			detail.TemporalEnd = &end
		}
	}

	// Retrieval results: reference arms crossed with fact types, each group
	// ranked independently and present even when empty, so an arm that silently
	// stops being dispatched is visible in the trace.
	grouped := map[[2]string][]recallHit{}
	for _, at := range armTraces {
		name, ok := traceArmNames[at.arm]
		if !ok {
			continue
		}
		for _, hit := range at.hits {
			key := [2]string{name, hit.match.Type}
			grouped[key] = append(grouped[key], hit)
		}
	}
	// The reference records its retrieval entries in FACT-TYPE order, each fact
	// type carrying that type's arms: the fact types are what it loops over and
	// the arms are recorded inside that loop (engine/memory_engine.py:9670-9730).
	for _, factType := range finalMemoryUnitTypesOrder() {
		for _, name := range traceArmOrder {
			hits := grouped[[2]string{name, string(factType)}]
			results := make([]TraceArmResult, 0, len(hits))
			for rank, hit := range hits {
				results = append(results, TraceArmResult{
					Rank:      rank + 1,
					NodeID:    hit.match.ID,
					Text:      hit.match.Text,
					Context:   hit.match.Context,
					EventDate: hit.match.OccurredStart,
					FactType:  hit.match.Type,
					Score:     hit.score,
					ScoreName: traceArmScoreName(name),
				})
			}
			detail.RetrievalResults = append(detail.RetrievalResults, TraceArmResults{
				MethodName: name,
				FactType:   string(factType),
				Results:    results,
			})
		}
	}

	// RRF merge: the fused candidate list best first. The order is the plain
	// reciprocal-rank total, before any boost or rerank, which is what the
	// reference reports as the merge.
	merged := make([]*recallCandidate, 0, len(candidates))
	for _, cand := range candidates {
		merged = append(merged, cand)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].rrfScore == merged[j].rrfScore {
			return merged[i].match.ID < merged[j].match.ID
		}
		return merged[i].rrfScore > merged[j].rrfScore
	})
	rrfRank := make(map[string]int, len(merged))
	for index, cand := range merged {
		rrfRank[cand.match.ID] = index + 1
		detail.RRFMerged = append(detail.RRFMerged, TraceRRFEntry{
			NodeID:       cand.match.ID,
			Text:         cand.match.Text,
			RRFScore:     cand.rrfScore,
			SourceRanks:  traceSourceRanks(cand),
			FinalRRFRank: index + 1,
		})
	}

	// Reranked and final_results are built from every SCORED result, not only
	// the returned ones: the reference feeds the tracer `scored_results` after
	// the post-rank floors (engine/memory_engine.py:10060-10076), which is the
	// list Step 5 then truncates to the budget.
	for index, cand := range scored {
		detail.Reranked = append(detail.Reranked, TraceRerankedEntry{
			NodeID:          cand.match.ID,
			Text:            cand.match.Text,
			RerankScore:     cand.match.Final,
			RerankRank:      index + 1,
			RRFRank:         rrfRank[cand.match.ID],
			RankChange:      rrfRank[cand.match.ID] - (index + 1),
			ScoreComponents: cloneFloatMap(cand.components),
		})
	}
	detail.FinalResults = buildTraceFinalResults(scored, merged)
	detail.EntryPoints = buildTraceEntryPoints(armTraces)
	detail.Visits = buildTraceVisits(scored, detail.EntryPoints, ranked)
	return detail
}

// buildTraceEntryPoints reports the semantic arm's first ten hits, ranked 1..N,
// which is the reference's legacy `entry_points` payload
// (engine/memory_engine.py:9767-9781). The similarity is clamped to [0, 1] the
// way the tracer clamps it (engine/search/tracer.py:132).
func buildTraceEntryPoints(armTraces []recallArmTrace) []TraceEntryPoint {
	var hits []recallHit
	for _, trace := range armTraces {
		if traceArmNames[trace.arm] == "semantic" {
			hits = append(hits, trace.hits...)
			break
		}
	}
	if len(hits) == 0 {
		return []TraceEntryPoint{}
	}
	if len(hits) > traceEntryPointLimit {
		hits = hits[:traceEntryPointLimit]
	}
	out := make([]TraceEntryPoint, 0, len(hits))
	for index, hit := range hits {
		out = append(out, TraceEntryPoint{
			NodeID:          hit.match.ID,
			Text:            hit.match.Text,
			SimilarityScore: clampUnit(hit.score),
			Rank:            index + 1,
		})
	}
	return out
}

// buildTraceVisits reports one record per scored result with the components the
// reference's weight breakdown carries (engine/search/tracer.py:155-198). The
// semantic similarity and recency are the candidate's own signals; frequency is
// the pipeline's constant 0.0 (engine/memory_engine.py:10308-10316). A result
// that survived into the returned list reports its rank, the rest report null.
func buildTraceVisits(scored []*recallCandidate, entryPoints []TraceEntryPoint, ranked []*recallCandidate) []TraceVisit {
	if len(scored) == 0 {
		return []TraceVisit{}
	}
	entry := make(map[string]bool, len(entryPoints))
	for _, point := range entryPoints {
		entry[point.NodeID] = true
	}
	rank := make(map[string]int, len(ranked))
	for index, cand := range ranked {
		rank[cand.match.ID] = index + 1
	}
	out := make([]TraceVisit, 0, len(scored))
	for index, cand := range scored {
		visit := TraceVisit{
			Step:               index + 1,
			NodeID:             cand.match.ID,
			Text:               cand.match.Text,
			Context:            cand.match.Context,
			EventDate:          cand.match.OccurredStart,
			IsEntryPoint:       entry[cand.match.ID],
			Activation:         cand.rrfScore,
			SemanticSimilarity: clampUnit(cand.armScores[ArmSemantic]),
			Frequency:          0,
			FinalWeight:        cand.match.Final,
		}
		if value, ok := cand.components["recency"]; ok {
			visit.Recency = clampUnit(value)
		}
		if final, ok := rank[cand.match.ID]; ok {
			visit.FinalRank = &final
		}
		out = append(out, visit)
	}
	return out
}

// buildTraceFinalResults reports one record per scored result with every score
// the pipeline computed for it, in the reference's key order
// (engine/search/types.py:206-257).
func buildTraceFinalResults(scored []*recallCandidate, merged []*recallCandidate) []TraceResultRow {
	if len(scored) == 0 {
		return []TraceResultRow{}
	}
	rrfRank := make(map[string]int, len(merged))
	for index, cand := range merged {
		rrfRank[cand.match.ID] = index + 1
	}
	out := make([]TraceResultRow, 0, len(scored))
	for _, cand := range scored {
		out = append(out, traceFinalResultRow(cand, rrfRank[cand.match.ID]))
	}
	return out
}

// traceFinalResultRow assembles one final_results entry. The keys and their
// order are the reference's own dict layout.
func traceFinalResultRow(cand *recallCandidate, rrfRank int) TraceResultRow {
	row := TraceResultRow{}
	add := func(key string, value any) {
		row.Keys = append(row.Keys, key)
		row.Values = append(row.Values, value)
	}
	add("id", cand.match.ID)
	add("text", cand.match.Text)
	add("fact_type", cand.match.Type)
	add("context", cand.match.Context)
	add("event_date", traceTime(cand.match.OccurredStart))
	add("occurred_start", traceTime(cand.match.OccurredStart))
	add("occurred_end", traceTime(cand.match.OccurredEnd))
	add("mentioned_at", traceTime(cand.match.MentionedAt))
	add("document_id", cand.match.DocumentID)
	add("chunk_id", cand.match.ChunkID)
	add("tags", cand.match.Tags)
	add("metadata", cand.match.Metadata)
	add("attachment_ids", traceAttachmentIDs(cand.match.Attachments))
	add("semantic_similarity", traceArmScore(cand, "semantic"))
	add("bm25_score", traceArmScore(cand, "bm25"))
	add("rrf_score", cand.rrfScore)
	add("rrf_rank", rrfRank)
	// One "<arm>_rank" member per arm that surfaced the candidate, in the
	// reference's reported arm order — the dict is built by `result.update(...)`
	// over the candidate's source_ranks (engine/search/types.py:240-241).
	for _, arm := range traceArmOrder {
		if r, ok := cand.armRanks[armNameForReport(arm)]; ok {
			add(arm+"_rank", r)
		}
	}
	add("cross_encoder_score", componentOrZero(cand, "cross_encoder_score"))
	add("cross_encoder_score_normalized", componentOrZero(cand, "cross_encoder_score_normalized"))
	add("rrf_normalized", componentOrZero(cand, "rrf_normalized"))
	add("temporal", componentOrDefault(cand, "temporal", 0.5))
	add("recency", componentOrDefault(cand, "recency", 0.5))
	add("proof_norm", componentOrDefault(cand, "proof_norm", 0.5))
	add("combined_score", componentOrZero(cand, "combined_score"))
	add("weight", cand.match.Final)
	add("activation", cand.match.Final)
	return row
}

// armNameForReport maps a reported arm name back onto the pipeline's label.
func armNameForReport(name string) string {
	for internal, reported := range traceArmNames {
		if reported == name {
			return internal
		}
	}
	return name
}

// traceArmScore returns an arm's reported score, or 0 when that arm did not
// surface the candidate. The reference's RetrievalResult carries each arm's own
// score (`similarity`, `bm25_score`, `activation`), so a graph-surfaced
// candidate reports its activation there, not a zero.
func traceArmScore(cand *recallCandidate, reported string) float64 {
	return cand.armScores[armNameForReport(reported)]
}

// componentOrZero returns a score component, or 0 when the pipeline did not
// compute it.
func componentOrZero(cand *recallCandidate, key string) float64 {
	return cand.components[key]
}

// componentOrDefault returns a score component, or the reference's neutral
// default when the pipeline did not compute it.
func componentOrDefault(cand *recallCandidate, key string, fallback float64) float64 {
	if value, ok := cand.components[key]; ok {
		return value
	}
	return fallback
}

// traceTime renders a timestamp the way the reference's datetime encoder does:
// ISO-8601, or null when there is none.
func traceTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}

// traceAttachmentIDs renders the attachment ids the reference reports, or null
// when the result carries none.
func traceAttachmentIDs(attachments []RecallAttachment) any {
	if len(attachments) == 0 {
		return nil
	}
	ids := make([]string, 0, len(attachments))
	for _, attachment := range attachments {
		ids = append(ids, attachment.ID)
	}
	return ids
}

// clampUnit bounds a similarity-like value to [0, 1], absorbing the tiny
// overshoots normalization produces.
func clampUnit(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

// traceSourceRanks maps a candidate's per-arm ranks onto the reference's
// "<method>_rank" keys, skipping arms the reference does not report.
func traceSourceRanks(cand *recallCandidate) map[string]int {
	if len(cand.armRanks) == 0 {
		return map[string]int{}
	}
	out := make(map[string]int, len(cand.armRanks))
	for arm, rank := range cand.armRanks {
		name, ok := traceArmNames[arm]
		if !ok {
			continue
		}
		out[name+"_rank"] = rank
	}
	return out
}

// traceAnchor returns the as-of time the recency signal was measured against:
// the caller's query timestamp when supplied, otherwise the execution time. It
// is the input that produced the ranking, not the moment the trace was built.
func traceAnchor(q RecallQuery) time.Time {
	if q.QueryTimestamp != nil {
		return q.QueryTimestamp.UTC()
	}
	return time.Now().UTC()
}

// cloneFloatMap copies a score-component map so the trace cannot alias a map the
// pipeline later mutates.
func cloneFloatMap(values map[string]float64) map[string]float64 {
	if values == nil {
		return map[string]float64{}
	}
	out := make(map[string]float64, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

// attachRecallTracePhases fills the trace payload that comes from the recall's
// own instrumentation rather than from its candidate lists: the ordered phase
// list with the seconds each took, the search's total duration, and the query
// embedding the semantic arm ranked against.
func attachRecallTracePhases(trace *RecallTrace, phases *recallPhases, q RecallQuery) {
	if trace == nil || trace.Detail == nil {
		return
	}
	metrics, total := phases.snapshot()
	trace.Detail.PhaseMetrics = metrics
	trace.Detail.TotalDurationSeconds = total
	if len(q.QueryEmbedding) > 0 {
		embedding := make([]float64, 0, len(q.QueryEmbedding))
		for _, dimension := range q.QueryEmbedding {
			embedding = append(embedding, float64(dimension))
		}
		trace.Detail.QueryEmbedding = embedding
	}
}
