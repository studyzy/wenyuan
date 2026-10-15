package app

import (
	"context"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/mentalmodels"
	"github.com/studyzy/wenyuan/internal/storage"
)

// dryRun previews a refresh without persisting anything: same scope resolution,
// same reflect call, same delta merge — with the content and watermark writes
// skipped. It is what the composition root hands the dry-run route.
func (r mentalModelRefresh) dryRun(ctx context.Context, bankID, modelID string) (*mentalmodels.DryRunResult, error) {
	started := time.Now()
	model, err := r.store.GetMentalModel(ctx, bankID, modelID)
	if err != nil {
		return nil, err
	}

	seenAt := r.store.BankWriteWatermark(ctx, bankID)
	retraction := r.retractionFor(ctx, bankID, model)

	requestedMode := refreshMode(model)
	effectiveMode := "full"
	fallback := ""
	if requestedMode == "delta" {
		switch {
		case strings.TrimSpace(model.Content) == "":
			fallback = "no_baseline_content"
		case model.LastRefreshedSourceQuery != model.SourceQuery:
			fallback = "source_query_changed"
		default:
			effectiveMode = "delta"
		}
	}

	// The preview reads the same window a real refresh would, and asks for the
	// tool-call trace a dry run always reports.
	outcome, err := r.store.Reflect(ctx, storage.ReflectQuery{
		BankID:     bankID,
		Query:      model.SourceQuery,
		Reflection: r.reflection,
		Include:    storage.ReflectInclude{ToolCalls: true},
	})
	if err != nil {
		return nil, err
	}

	candidate := outcome.Answer
	preview := candidate
	var deltaOperations *mentalmodels.DryRunDeltaOps
	if effectiveMode == "delta" {
		if merged, err := r.mergeDelta(ctx, model.Content, candidate, retraction); err == nil && strings.TrimSpace(merged) != "" {
			preview = merged
			deltaOperations = &mentalmodels.DryRunDeltaOps{Applied: []any{}, Skipped: []any{}}
		} else {
			// The delta merge could not run; the next refresh would fall back to
			// a full regeneration, so the preview reports that.
			effectiveMode = "full"
			fallback = "delta_ops_failed"
			preview = candidate
		}
	}

	refreshOutcome := "content_written"
	switch {
	case strings.TrimSpace(candidate) == "":
		refreshOutcome = "refresh_failed_empty_candidate"
	case strings.TrimSpace(preview) == strings.TrimSpace(model.Content):
		refreshOutcome = "content_unchanged"
	}
	// Both writing outcomes persist the candidate: content_unchanged just
	// happens to store text identical to the current document.
	wouldPersist := refreshOutcome == "content_written" || refreshOutcome == "content_unchanged"

	createdBefore := seenAt
	if createdBefore.IsZero() {
		createdBefore = time.Now().UTC()
	}
	window := mentalmodels.DryRunWindow{CreatedBefore: createdBefore.UTC().Format(time.RFC3339Nano)}
	if !seenAt.IsZero() {
		watermark := seenAt.UTC().Format(time.RFC3339Nano)
		window.Watermark = &watermark
	}
	if effectiveMode == "delta" && model.LastMemorySeenAt != nil {
		after := model.LastMemorySeenAt.UTC().Format(time.RFC3339Nano)
		window.CreatedAfter = &after
	}

	usage := mentalmodels.DryRunUsage{
		InputTokens:    outcome.Usage.InputTokens,
		OutputTokens:   outcome.Usage.OutputTokens,
		TotalTokens:    outcome.Usage.TotalTokens,
		CachedTokens:   outcome.Usage.CachedTokens,
		ThoughtsTokens: outcome.Usage.ThoughtsTokens,
	}

	scope := mentalmodels.DryRunScope{
		Tags:                  model.Tags,
		TagsMatch:             resolveTagsMatch(model),
		TagGroups:             triggerSlice(model.Trigger, "tag_groups"),
		FactTypes:             triggerStrings(model.Trigger, "fact_types"),
		ExcludeMentalModels:   triggerBool(model.Trigger, "exclude_mental_models"),
		ExcludeMentalModelIDs: append(triggerStrings(model.Trigger, "exclude_mental_model_ids"), model.ID),
	}

	durationMS := int(time.Since(started).Milliseconds())
	recordedAt := time.Now().UTC().Format(time.RFC3339Nano)
	retractionReport := retractionReportFor(retraction)
	trace := mentalmodels.DryRunTrace{
		RecordedAt:         &recordedAt,
		EffectiveMode:      effectiveMode,
		ModeFallbackReason: fallback,
		Outcome:            refreshOutcome,
		ToolCalls:          toolCallTraces(outcome.Trace),
		LLMCalls:           llmCallTraces(outcome.Trace),
		DeltaOperations:    deltaOperations,
		Retraction:         retractionReport,
		Usage:              &usage,
		DurationMS:         durationMS,
		Warnings:           []string{},
	}

	candidateContent := candidate
	previewContent := preview
	return &mentalmodels.DryRunResult{
		MentalModelID:      model.ID,
		Name:               model.Name,
		RequestedMode:      requestedMode,
		EffectiveMode:      effectiveMode,
		ModeFallbackReason: fallback,
		Outcome:            refreshOutcome,
		WouldPersist:       wouldPersist,
		Scope:              scope,
		Window:             window,
		Facts:              factCounts(outcome),
		BasedOn:            map[string][]any{"memories": renderFacts(outcome)},
		CurrentContent:     model.Content,
		CandidateContent:   candidateContent,
		PreviewContent:     previewContent,
		Diff:               unifiedDiff(model.Content, preview),
		DeltaOperations:    deltaOperations,
		Retraction:         retractionReport,
		Trace:              trace,
		Usage:              usage,
		DurationMS:         durationMS,
		Warnings:           []string{},
	}, nil
}

// resolveTagsMatch reports the tag match mode a refresh resolves to: the
// trigger's override, else all_strict when the model carries tags and any when
// it does not.
func resolveTagsMatch(model storage.MentalModel) string {
	if declared := triggerString(model.Trigger, "tags_match"); declared != "" {
		return declared
	}
	if len(model.Tags) > 0 {
		return "all_strict"
	}
	return "any"
}

// factCounts counts the cited facts by type. The repo's reflect runner reports
// the facts the agent based its answer on, so retrieved and used are the same
// set here.
func factCounts(outcome storage.ReflectOutcome) mentalmodels.DryRunFactCounts {
	used := map[string]int{}
	for _, fact := range outcome.Facts {
		used[fact.Type]++
	}
	retrieved := map[string]int{}
	for kind, count := range used {
		retrieved[kind] = count
	}
	return mentalmodels.DryRunFactCounts{Retrieved: retrieved, Used: used}
}

// toolCallTraces renders the reflect tool calls a dry run reports.
func toolCallTraces(trace *storage.ReflectTrace) []mentalmodels.DryRunToolCall {
	if trace == nil {
		return []mentalmodels.DryRunToolCall{}
	}
	out := make([]mentalmodels.DryRunToolCall, 0, len(trace.ToolCalls))
	for _, call := range trace.ToolCalls {
		entry := mentalmodels.DryRunToolCall{
			Tool:       call.Tool,
			Input:      call.Input,
			DurationMS: call.DurationMs,
			Iteration:  call.Iteration,
		}
		if entry.Input == nil {
			entry.Input = map[string]any{}
		}
		if call.HasOutput {
			entry.Output = call.Output
			count := len(call.Output)
			entry.ResultCount = &count
		}
		out = append(out, entry)
	}
	return out
}

// llmCallTraces renders the reflect model calls a dry run reports.
func llmCallTraces(trace *storage.ReflectTrace) []mentalmodels.DryRunLLMCall {
	if trace == nil {
		return []mentalmodels.DryRunLLMCall{}
	}
	out := make([]mentalmodels.DryRunLLMCall, 0, len(trace.LLMCalls))
	for _, call := range trace.LLMCalls {
		out = append(out, mentalmodels.DryRunLLMCall{Scope: call.Scope, DurationMS: call.DurationMs})
	}
	return out
}

// retractionReportFor renders the facts a document cites that no longer exist.
func retractionReportFor(retraction storage.RetractedGrounding) *mentalmodels.DryRunRetraction {
	if len(retraction.Facts) == 0 {
		return nil
	}
	ids := make([]string, 0, len(retraction.Facts))
	texts := make([]string, 0, len(retraction.Facts))
	for _, fact := range retraction.Facts {
		ids = append(ids, fact.ID)
		texts = append(texts, fact.Text)
	}
	return &mentalmodels.DryRunRetraction{
		FactIDs:   ids,
		FactTexts: texts,
		Applied:   !retraction.Unresolvable(),
	}
}

// triggerString reads a string member out of a trigger bag.
func triggerString(trigger map[string]any, key string) string {
	if trigger == nil {
		return ""
	}
	if value, ok := trigger[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

// triggerBool reads a boolean member out of a trigger bag.
func triggerBool(trigger map[string]any, key string) bool {
	if trigger == nil {
		return false
	}
	value, _ := trigger[key].(bool)
	return value
}

// triggerStrings reads a list-of-strings member out of a trigger bag.
func triggerStrings(trigger map[string]any, key string) []string {
	if trigger == nil {
		return nil
	}
	raw, ok := trigger[key].([]any)
	if !ok {
		if typed, ok := trigger[key].([]string); ok {
			return typed
		}
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, entry := range raw {
		if value, ok := entry.(string); ok {
			out = append(out, value)
		}
	}
	return out
}

// triggerSlice reads an arbitrary list member out of a trigger bag.
func triggerSlice(trigger map[string]any, key string) []any {
	if trigger == nil {
		return nil
	}
	raw, _ := trigger[key].([]any)
	return raw
}
