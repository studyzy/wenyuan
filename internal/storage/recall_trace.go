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
func buildRecallTraceDetail(candidates map[string]*recallCandidate, ranked []*recallCandidate, armTraces []recallArmTrace, q RecallQuery, limit int) *RecallTraceDetail {
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
	for _, name := range traceArmOrder {
		for _, factType := range FinalMemoryUnitTypes() {
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

	// Reranked: the final order, carrying the components the combined score was
	// built from so the trace and the response agree on why the top result won.
	for index, cand := range ranked {
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
	return detail
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
