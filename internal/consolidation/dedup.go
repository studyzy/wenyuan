package consolidation

import (
	"context"
	"encoding/json"
	"math"
	"strings"

	"github.com/studyzy/wenyuan/internal/provider"
)

// dedupNeighbourLimit bounds the in-scope observations a duplicate probe
// considers. Only the nearest few can be the twin of a restated conclusion.
const dedupNeighbourLimit = 5

// dedupAdjudicationScope labels the focused merge-or-keep call, so a deployment
// can trace and budget it apart from the main consolidation call.
const dedupAdjudicationScope = "observation_dedup"

// ObservationDuplicateStore is the optional capability a store provides when a
// deployment can embed. The store embeds the candidate observation text and
// returns that embedding together with the in-scope observations that carry a
// stored embedding, nearest first, found by a dense + keyword probe with strict
// tag matching and no graph or temporal arm. candidates is bounded by limit.
//
// It reports embedded=false — with no error — when there is no embedding model
// or when the text produced no vector. That is the signal that no duplicate
// decision can be made at all; the pass does not fall back to a text heuristic,
// because deciding on a different scale than the configured cosine threshold
// would fold conclusions the operator asked to keep apart.
type ObservationDuplicateStore interface {
	ObservationDuplicates(ctx context.Context, bankID string, scope []string, text string, limit int) (probe []float32, candidates []Observation, embedded bool, err error)
}

// DuplicateMergeStore is the optional capability a store provides for the
// conditional fold a "merge" verdict needs. Both methods must overwrite the
// survivor's text with mergedText, widen its temporal window to cover the
// folded sources and union the source ids — but only while the survivor still
// carries the text observed at probe time. A twin (or the updated row) that
// vanished or was rewritten in the window between the probe and the write must
// make the fold report folded=false, so the caller keeps the distinct row
// instead of clobbering a concurrent edit.
//
// A store without this capability degrades to an unconditional fold through the
// ordinary observation write (see mergeCreateIntoTwin / mergeUpdateIntoTwin).
type DuplicateMergeStore interface {
	// FoldCreateIntoDuplicate folds the sources of a skipped create into twinID,
	// writing mergedText in place of the text the probe read (expectedTwinText).
	// It reports false when the twin is gone or was rewritten since the probe.
	FoldCreateIntoDuplicate(ctx context.Context, bankID, twinID, expectedTwinText, mergedText string, sourceIDs []string) (bool, error)
	// FoldUpdateIntoDuplicate folds the just-updated observation (updatedID,
	// carrying updatedText) into twinID with mergedText and retires updatedID.
	// Both rows are gated on the text the probe read (updatedText for the row,
	// expectedTwinText for the twin); either having changed reports false.
	FoldUpdateIntoDuplicate(ctx context.Context, bankID, updatedID, updatedText, twinID, expectedTwinText, mergedText string) (bool, error)
}

// duplicateVerdict is the outcome of probing one observation against its
// in-scope neighbours. twin carries the matched neighbour and its text as the
// probe read it; shouldMerge and mergedText hold the focused verdict.
type duplicateVerdict struct {
	twin        Observation
	shouldMerge bool
	mergedText  string
}

// duplicateObservation probes one candidate observation text against the
// in-scope observations and, when it finds a neighbour at or above the dedup
// threshold, asks for a focused merge-or-keep verdict. excludeID skips one
// observation (the row an UPDATE is rewriting, which would otherwise match
// itself at cosine 1).
//
// A threshold of 1 disables dedup; a store that cannot embed, or a probe that
// yields no vector, decides nothing — dedup simply does not happen. There is no
// text-similarity fallback on purpose: deciding on a different scale than the
// configured cosine threshold could fold conclusions the operator asked to keep
// apart.
func (c *Consolidator) duplicateObservation(ctx context.Context, bankID string, scope []string, text, excludeID string) (duplicateVerdict, bool) {
	if c.tuning.DedupThreshold >= 1 {
		return duplicateVerdict{}, false
	}
	detector, ok := c.store.(ObservationDuplicateStore)
	if !ok {
		return duplicateVerdict{}, false
	}
	probe, candidates, embedded, err := detector.ObservationDuplicates(ctx, bankID, scope, text, dedupNeighbourLimit)
	if err != nil || !embedded || len(probe) == 0 {
		return duplicateVerdict{}, false
	}
	var best Observation
	bestSim := c.tuning.DedupThreshold
	found := false
	for _, candidate := range candidates {
		if candidate.ID == "" || candidate.ID == excludeID || len(candidate.Embedding) == 0 {
			continue
		}
		sim := cosineSimilarity(probe, candidate.Embedding)
		if sim >= bestSim {
			bestSim = sim
			best = candidate
			found = true
		}
	}
	if !found {
		return duplicateVerdict{}, false
	}
	shouldMerge, mergedText := c.adjudicateMerge(ctx, text, best.Text)
	return duplicateVerdict{twin: best, shouldMerge: shouldMerge, mergedText: mergedText}, true
}

// adjudicateMerge runs one focused, independent model call that decides whether
// two highly-similar observations assert the same fact. It returns merge only
// for a well-formed "merge" verdict; a failed call, an unparsable answer, an
// unknown action or a "keep" verdict all resolve to keep, which is the safe
// outcome — keeping both rows never loses a distinct conclusion.
func (c *Consolidator) adjudicateMerge(ctx context.Context, newText, existingText string) (bool, string) {
	if c.provider == nil {
		return false, ""
	}
	request := provider.CompletionRequest{
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: dedupVerdictInput(newText, existingText)},
		},
		ResponseSchema: dedupVerdictSchema(),
		Scope:          dedupAdjudicationScope,
	}
	response, err := c.provider.Complete(ctx, request)
	if err != nil {
		return false, ""
	}
	action, text := parseDedupVerdict(response.Structured, response.Content)
	if action != "merge" {
		return false, ""
	}
	if strings.TrimSpace(text) == "" {
		text = existingText
	}
	return true, text
}

// dedupVerdictInput is the focused prompt for the one-on-one decision. It is
// deliberately short: two texts, one JSON object back, and a conservative rule
// — only the same fact merges, and any material difference keeps both.
func dedupVerdictInput(newText, existingText string) string {
	var b strings.Builder
	b.WriteString("Two observations are being compared before one is saved. Decide whether they state the same fact.\n\n")
	b.WriteString("Candidate:\n")
	b.WriteString(newText)
	b.WriteString("\n\nAlready recorded:\n")
	b.WriteString(existingText)
	b.WriteString("\n\nReply with a single JSON object and nothing else: ")
	b.WriteString("{\"action\":\"merge\",\"text\":\"...\",\"reason\":\"...\"} or {\"action\":\"keep\",\"text\":\"\",\"reason\":\"...\"}. ")
	b.WriteString("Choose \"merge\" only when both assert the same fact and differ merely in wording; ")
	b.WriteString("then set \"text\" to one observation that preserves every detail from both. ")
	b.WriteString("Choose \"keep\" when any important detail differs — a number or quantity, a named entity, ")
	b.WriteString("the language, a negation, or a condition — and leave \"text\" empty. ")
	b.WriteString("When unsure, keeping both is the safe answer.")
	return b.String()
}

// dedupVerdictSchema declares the structured form of the focused verdict.
func dedupVerdictSchema() *provider.Schema {
	return &provider.Schema{
		Type: "object",
		Properties: map[string]*provider.Schema{
			"action": {Type: "string"},
			"text":   {Type: "string"},
			"reason": {Type: "string"},
		},
		Required: []string{"action"},
	}
}

// dedupVerdictResponse is the model's focused answer.
type dedupVerdictResponse struct {
	Action string "json:\"action\""
	Text   string "json:\"text\""
	Reason string "json:\"reason\""
}

// parseDedupVerdict reads the focused answer from a structured value or prose
// JSON. An action outside {merge, keep}, or no usable object at all, resolves to
// keep so an unreliable answer never folds two distinct conclusions.
func parseDedupVerdict(structured any, content string) (string, string) {
	if structured != nil {
		encoded, err := json.Marshal(structured)
		if err == nil {
			var verdict dedupVerdictResponse
			if json.Unmarshal(encoded, &verdict) == nil {
				return normalizeDedupAction(verdict.Action), verdict.Text
			}
		}
	}
	trimmed := strings.TrimSpace(content)
	if start := strings.Index(trimmed, "{"); start >= 0 {
		if end := strings.LastIndex(trimmed, "}"); end > start {
			var verdict dedupVerdictResponse
			if json.Unmarshal([]byte(trimmed[start:end+1]), &verdict) == nil {
				return normalizeDedupAction(verdict.Action), verdict.Text
			}
		}
	}
	return "keep", ""
}

func normalizeDedupAction(action string) string {
	if strings.EqualFold(strings.TrimSpace(action), "merge") {
		return "merge"
	}
	return "keep"
}

// adjudicationPlan holds the pre-write verdicts for one response. It is filled
// before any storage write, so the model calls it carries never run while a
// write lock is held. An entry whose twin.ID is empty means no neighbour reached
// the threshold and no verdict was asked.
type adjudicationPlan struct {
	creates []duplicateVerdict
	updates []duplicateVerdict
}

// prepareAdjudications resolves every create's and update's duplicate verdict in
// one pass, before the guard and before any write. Update verdicts are computed
// only when the store can perform the conditional fold they lead to.
func (c *Consolidator) prepareAdjudications(ctx context.Context, bankID string, scope []string, creates []Create, updates []Update) adjudicationPlan {
	plan := adjudicationPlan{
		creates: make([]duplicateVerdict, len(creates)),
		updates: make([]duplicateVerdict, len(updates)),
	}
	for i, create := range creates {
		if strings.TrimSpace(create.Text) == "" {
			continue
		}
		if verdict, found := c.duplicateObservation(ctx, bankID, scope, create.Text, ""); found {
			plan.creates[i] = verdict
		}
	}
	if _, canFold := c.store.(DuplicateMergeStore); canFold {
		for i, update := range updates {
			if update.ObservationID == "" || strings.TrimSpace(update.Text) == "" {
				continue
			}
			if verdict, found := c.duplicateObservation(ctx, bankID, scope, update.Text, update.ObservationID); found {
				plan.updates[i] = verdict
			}
		}
	}
	return plan
}

// mergeCreateIntoTwin folds a create the verdict called a duplicate into its
// twin. With the conditional-fold capability the twin is gated on its probe-time
// text, so a rewrite in between makes the fold report false and the caller keeps
// the create. Without it the ordinary observation write records the merge with
// the synthesized text and the create's sources.
func (c *Consolidator) mergeCreateIntoTwin(ctx context.Context, bankID string, verdict duplicateVerdict, sources []string) (bool, error) {
	if merger, ok := c.store.(DuplicateMergeStore); ok {
		return merger.FoldCreateIntoDuplicate(ctx, bankID, verdict.twin.ID, verdict.twin.Text, verdict.mergedText, sources)
	}
	if err := c.store.UpdateObservation(ctx, bankID, verdict.twin.ID, verdict.mergedText, sources); err != nil {
		return false, err
	}
	return true, nil
}

// mergeUpdateIntoTwin folds a just-updated observation into the twin the
// verdict matched it to and retires the updated row. It is only called when the
// store provides the conditional fold, so both rows are gated on their
// probe-time text.
func (c *Consolidator) mergeUpdateIntoTwin(ctx context.Context, bankID, updatedID, updatedText string, verdict duplicateVerdict) (bool, error) {
	merger, ok := c.store.(DuplicateMergeStore)
	if !ok {
		return false, nil
	}
	return merger.FoldUpdateIntoDuplicate(ctx, bankID, updatedID, updatedText, verdict.twin.ID, verdict.twin.Text, verdict.mergedText)
}

// cosineSimilarity returns the cosine of two equal-width vectors, in [-1, 1].
// Vectors of differing widths, or with a zero magnitude, score 0 rather than
// producing a NaN, so an unusable pair can never spuriously clear a threshold.
func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}
