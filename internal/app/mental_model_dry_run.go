package app

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/mentalmodels"
	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/timefmt"
)

// noSourcesInScopeWarning is what a dry run reports when the model's scope held
// nothing to read — the reason the reflect loop was skipped. It is the
// reference's text, verbatim.
const noSourcesInScopeWarning = "Nothing in this document's scope was readable, so the reflect loop was skipped and the document left as it is. Check the resolved scope — tags, tag_groups and fact_types all narrow it — and, in delta mode, the time window: only memories changed after the last refresh are in range."

// dryRun previews a refresh without persisting anything: same scope resolution,
// same reflect call, same delta merge — with the content and watermark writes
// skipped. It is what the composition root hands the dry-run route.
func (r mentalModelRefresh) dryRun(ctx context.Context, bankID, modelID string) (*mentalmodels.DryRunResult, error) {
	started := time.Now()
	model, err := r.store.GetMentalModel(ctx, bankID, modelID)
	if err != nil {
		return nil, err
	}

	// The snapshot a real refresh bounds its reads with is the database clock at
	// the moment the run starts, read from the model row itself — not the newest
	// data watermark. The two are different answers: the snapshot says "whatever
	// is committed at this instant", while the newest row says "how far through
	// the bank memory has got", and the reference reports the first as the
	// window's upper bound and the second as the watermark a refresh would
	// persist. A quiet bank demonstrates it: its snapshot is still now, its
	// watermark stays where the last write left it.
	refreshCutoff := r.snapshotFor(ctx, bankID, modelID)
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

	// A reflect over a scope with nothing to read is the most expensive reflect
	// there is, and a real refresh refuses to pay it — so the preview asks the
	// same question first rather than running one the refresh would skip. The
	// window it asks about is the one the effective mode reads: a delta run only
	// sees memories newer than its watermark.
	since := time.Time{}
	if effectiveMode == "delta" && model.LastMemorySeenAt != nil {
		since = *model.LastMemorySeenAt
	}
	hasSources := r.hasSources(ctx, bankID, model, since)

	var outcome storage.ReflectOutcome
	if hasSources {
		// The preview reads the same window a real refresh would, and asks for
		// the tool-call trace a dry run always reports.
		outcome, err = r.store.Reflect(ctx, storage.ReflectQuery{
			BankID:     bankID,
			Query:      model.SourceQuery,
			Reflection: r.reflection,
			Include:    storage.ReflectInclude{ToolCalls: true},
		})
		if err != nil {
			return nil, err
		}
	}

	// The delta branch was taken (baseline content, unchanged query). It either
	// applies or fails, but either way this run read a delta-scoped window, which
	// is what the reference gates the delta-not-applied refusal on.
	useDelta := effectiveMode == "delta"
	candidate := outcome.Answer
	preview := candidate
	deltaApplied := false
	var deltaOperations *mentalmodels.DryRunDeltaOps
	if useDelta && hasSources {
		if merged, err := r.mergeDelta(ctx, model.Content, candidate, retraction); err == nil && strings.TrimSpace(merged) != "" {
			preview = merged
			deltaApplied = true
			deltaOperations = &mentalmodels.DryRunDeltaOps{Applied: []any{}, Skipped: []any{}}
		} else {
			// The delta merge could not run, so the effective mode is full; the
			// candidate, though, was synthesised over a delta-scoped recall only,
			// and the outcome branch below refuses to write it.
			effectiveMode = "full"
			fallback = "delta_ops_failed"
			preview = candidate
		}
	}

	// The delta window's lower bound: the newest memory the previous refresh saw,
	// or the time it last refreshed. It is what the reference gates the
	// delta-not-applied refusal on.
	createdAfter := deltaWindowLowerBound(model)

	refreshOutcome := "content_written"
	switch {
	case !hasSources && !deltaApplied:
		// Nothing in this model's scope was readable, so no candidate was even
		// synthesised: the document is preserved as it stands. Checked before the
		// empty-candidate guard, which would otherwise read the by-construction
		// empty candidate as an upstream failure.
		refreshOutcome = "content_preserved_no_new_facts"
	case strings.TrimSpace(preview) == "":
		refreshOutcome = "refresh_failed_empty_candidate"
	case useDelta && !deltaApplied && createdAfter != nil:
		// Writing a delta-window candidate as the whole document would drop
		// everything grounded in older memories, so a real refresh refuses it.
		refreshOutcome = "refresh_failed_delta_not_applied"
	case strings.TrimSpace(preview) == model.Content:
		refreshOutcome = "content_unchanged"
	}
	// Both writing outcomes persist the candidate: content_unchanged just
	// happens to store text identical to the current document. A refusal or a
	// no-new-facts run persists nothing, so its preview is the stored content.
	writesContent := refreshOutcome == "content_written" || refreshOutcome == "content_unchanged"
	previewContent := preview
	if !writesContent {
		previewContent = model.Content
	}

	if refreshCutoff.IsZero() {
		// The row vanished between the read above and the snapshot; a preview of
		// a model that no longer exists has nothing to report either way, so the
		// wall clock stands in rather than emitting the zero time.
		refreshCutoff = time.Now().UTC()
	}
	// The window reports the watermark a real refresh would persist: the newest
	// in-scope memory visible at the snapshot, never the wall clock — a clock
	// reading can sit ahead of a row that has not committed yet, and reading it
	// back would stamp that row as already seen.
	//
	// Which reading answers that is the backend's, and the difference is
	// observable: the PostgreSQL backend asks the full scope query (tags,
	// tag_groups, fact_types, the delta window), while the in-memory one has only
	// the bank write watermark. That is exactly what a real refresh persists
	// there, so the preview and the refresh agree on every backend even where the
	// answer is coarser than the reference's.
	watermark := r.inScopeWatermark(ctx, bankID, model, refreshCutoff)
	window := mentalmodels.DryRunWindow{CreatedBefore: refreshCutoff.UTC().Format(time.RFC3339Nano)}
	if !watermark.IsZero() {
		formatted := watermark.UTC().Format(time.RFC3339Nano)
		window.Watermark = &formatted
	}
	if useDelta && createdAfter != nil {
		after := createdAfter.UTC().Format(time.RFC3339Nano)
		window.CreatedAfter = &after
	}

	usage := mentalmodels.DryRunUsage{
		InputTokens:    outcome.Usage.InputTokens,
		OutputTokens:   outcome.Usage.OutputTokens,
		TotalTokens:    outcome.Usage.TotalTokens,
		CachedTokens:   outcome.Usage.CachedTokens,
		ThoughtsTokens: outcome.Usage.ThoughtsTokens,
	}

	// A null fallback reason (no fallback happened) is emitted as null, not "".
	var fallbackPtr *string
	if fallback != "" {
		fallbackPtr = &fallback
	}
	scope := mentalmodels.DryRunScope{
		Tags:                  nonNilTags(model.Tags),
		TagsMatch:             resolveTagsMatch(model),
		TagGroups:             triggerSlice(model.Trigger, "tag_groups"),
		FactTypes:             triggerStrings(model.Trigger, "fact_types"),
		ExcludeMentalModels:   triggerBool(model.Trigger, "exclude_mental_models"),
		ExcludeMentalModelIDs: append(triggerStrings(model.Trigger, "exclude_mental_model_ids"), model.ID),
	}

	durationMS := int(time.Since(started).Milliseconds())
	// The reference renders the trace's timestamp through Python's
	// A pydantic datetime is rendered in the `...Z` form here, not Python's
	// `datetime.isoformat()` offset form: the trace's recorded_at is a member of a
	// response model, so the framework serializes it and writes Z for UTC. The
	// live reference answers `2026-10-20T16:06:57.624805Z` (checked 2026-10-20) —
	// six fractional digits with a Z suffix, which is FormatZuluTimestamp rather
	// than the `+00:00` spelling FormatTimestamp produces for the same instant.
	recordedAt := timefmt.FormatZuluTimestamp(time.Now().UTC())
	retractionReport := retractionReportFor(retraction)
	warnings := []string{}
	if !hasSources {
		warnings = append(warnings, noSourcesInScopeWarning)
	}
	// The evidence a real refresh would record. A run that never reached the
	// reflect loop has none by construction, and the reference reports that as an
	// empty map rather than a fixed set of empty fact-type keys.
	basedOn := renderBasedOn(outcome)
	if !hasSources {
		basedOn = map[string][]any{}
	}
	// The delta window a real refresh would have scoped its retrieval to; only a
	// delta run bounds its tool calls with it.
	var deltaWindow *time.Time
	if useDelta {
		deltaWindow = createdAfter
	}
	trace := mentalmodels.DryRunTrace{
		RecordedAt:         &recordedAt,
		EffectiveMode:      effectiveMode,
		ModeFallbackReason: fallbackPtr,
		Outcome:            refreshOutcome,
		ToolCalls:          toolCallTraces(outcome.Trace, deltaWindow),
		LLMCalls:           llmCallTraces(outcome.Trace),
		DeltaOperations:    deltaOperations,
		Retraction:         retractionReport,
		Usage:              &usage,
		DurationMS:         durationMS,
		Warnings:           warnings,
	}

	return &mentalmodels.DryRunResult{
		MentalModelID:      model.ID,
		Name:               model.Name,
		RequestedMode:      requestedMode,
		EffectiveMode:      effectiveMode,
		ModeFallbackReason: fallbackPtr,
		Outcome:            refreshOutcome,
		WouldPersist:       writesContent,
		Scope:              scope,
		Window:             window,
		Facts:              factCounts(outcome),
		BasedOn:            basedOn,
		CurrentContent:     model.Content,
		CandidateContent:   candidate,
		PreviewContent:     previewContent,
		Diff:               unifiedDiff(model.Content, previewContent),
		DeltaOperations:    deltaOperations,
		Retraction:         retractionReport,
		Trace:              trace,
		Usage:              usage,
		DurationMS:         durationMS,
		Warnings:           warnings,
	}, nil
}

// nonNilTags reports a model's tags as an empty list rather than null when it
// carries none, matching how the reference reads the tags column back.
func nonNilTags(tags []string) []string {
	if tags == nil {
		return []string{}
	}
	return tags
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

// factCounts reports the facts a refresh saw: retrieved, the facts the reflect
// agent's tool calls actually returned, and used, the facts the agent declared
// it based the answer on. The two are deliberately independent — retrieval
// finding plenty while the agent uses none is the signature of an off-topic
// source query, and nothing else on this surface reports it.
func factCounts(outcome storage.ReflectOutcome) mentalmodels.DryRunFactCounts {
	used := map[string]int{}
	for _, fact := range outcome.Facts {
		used[fact.Type]++
	}
	return mentalmodels.DryRunFactCounts{Retrieved: retrievedFactCounts(outcome.Trace), Used: used}
}

// retrievedFactCounts tallies what a refresh's tool calls returned, by fact
// type, exactly as the reference does: every returned memory, observation or
// mental model is counted once under its own type, deduplicated by id.
func retrievedFactCounts(trace *storage.ReflectTrace) map[string]int {
	counts := map[string]int{}
	if trace == nil {
		return counts
	}
	seen := map[[2]string]bool{}
	tally := func(factType string, id any) {
		if id != nil {
			key := [2]string{factType, fmt.Sprint(id)}
			if seen[key] {
				return
			}
			seen[key] = true
		}
		counts[factType]++
	}
	for _, call := range trace.ToolCalls {
		switch call.Tool {
		case "recall":
			for _, memory := range toolResultObjects(call.Output, "memories") {
				factType, _ := memory["fact_type"].(string)
				if factType == "" {
					factType = "world"
				}
				tally(factType, memory["id"])
			}
		case "search_observations":
			for _, observation := range toolResultObjects(call.Output, "observations") {
				tally("observation", observation["id"])
			}
		case "search_mental_models":
			for _, model := range toolResultObjects(call.Output, "mental_models") {
				tally("mental-models", model["id"])
			}
		}
	}
	return counts
}

// toolResultObjects reads a tool call's result list out of its output payload,
// returning nothing when the payload carries no such list. The list may be a
// []any (JSON-decoded) or a typed slice (built in-process), so it is walked by
// reflection.
func toolResultObjects(output map[string]any, key string) []map[string]any {
	items := sliceElements(output[key])
	objects := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if object, ok := item.(map[string]any); ok {
			objects = append(objects, object)
		}
	}
	return objects
}

// sliceElements returns a value's elements when it is a slice or array, and nil
// otherwise. It accepts both []any and typed slices, so a payload built
// in-process and one decoded from JSON are read the same way.
func sliceElements(value any) []any {
	if value == nil {
		return nil
	}
	rv := reflect.ValueOf(value)
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return nil
	}
	out := make([]any, 0, rv.Len())
	for i := range rv.Len() {
		out = append(out, rv.Index(i).Interface())
	}
	return out
}

// renderBasedOn renders the evidence a dry run would ground the document on,
// keyed by fact type — the shape a refresh persists under
// reflect_response.based_on. Every fact-type key is present, even when empty,
// matching the reference's fixed key set.
func renderBasedOn(outcome storage.ReflectOutcome) map[string][]any {
	based := map[string][]any{
		"world":         {},
		"experience":    {},
		"opinion":       {},
		"observation":   {},
		"mental-models": {},
		"directives":    {},
	}
	memoryFactTypes := map[string]bool{"world": true, "experience": true, "opinion": true, "observation": true}
	for _, fact := range outcome.Facts {
		if !memoryFactTypes[fact.Type] {
			continue
		}
		based[fact.Type] = append(based[fact.Type], map[string]any{
			"id": fact.ID, "text": fact.Text, "type": fact.Type, "context": fact.Context,
		})
	}
	for _, model := range outcome.MentalModels {
		based["mental-models"] = append(based["mental-models"], map[string]any{
			"id": model.ID, "text": model.Text, "type": "mental-models", "context": model.Context,
		})
	}
	for _, directive := range outcome.Directives {
		based["directives"] = append(based["directives"], map[string]any{
			"id": directive.ID, "text": directive.Content, "type": "directives", "context": nil,
		})
	}
	return based
}

// deltaWindowLowerBound is the lower bound of a delta refresh's window: the
// newest memory the previous refresh saw, or the time it last refreshed. It is
// the same value the reference uses both for the window and for the
// delta-not-applied refusal.
func deltaWindowLowerBound(model storage.MentalModel) *time.Time {
	if model.LastMemorySeenAt != nil {
		return model.LastMemorySeenAt
	}
	if model.LastRefreshedAt.IsZero() {
		return nil
	}
	refreshed := model.LastRefreshedAt
	return &refreshed
}

// toolCallTraces renders the reflect tool calls a dry run reports.
//
// deltaWindow is the delta refresh's lower bound, stamped onto the calls whose
// retrieval it narrowly scoped; the others take no time bound and leave it null.
func toolCallTraces(trace *storage.ReflectTrace, deltaWindow *time.Time) []mentalmodels.DryRunToolCall {
	if trace == nil {
		return []mentalmodels.DryRunToolCall{}
	}
	var updatedAt *string
	if deltaWindow != nil {
		formatted := deltaWindow.UTC().Format(time.RFC3339Nano)
		updatedAt = &formatted
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
		if reason, ok := call.Input["reason"].(string); ok && reason != "" {
			entry.Reason = &reason
		}
		if len(call.Output) > 0 {
			entry.Output = call.Output
		}
		for _, key := range []string{"memories", "observations", "mental_models", "chunks", "results"} {
			if items := sliceElements(call.Output[key]); items != nil {
				count := len(items)
				entry.ResultCount = &count
				break
			}
		}
		if call.Tool == "recall" || call.Tool == "search_observations" {
			entry.UpdatedAt = updatedAt
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

// snapshotFor reads the database-time snapshot a refresh bounds its reads with:
// the current timestamp, read through the model row so a model that no longer
// exists yields the zero time. A store without the capability (the in-memory
// backend) has no database clock to ask, so its own clock answers.
func (r mentalModelRefresh) snapshotFor(ctx context.Context, bankID, modelID string) time.Time {
	if reader, ok := r.store.(mentalModelSnapshotReader); ok {
		return reader.MentalModelRefreshSnapshot(ctx, bankID, modelID)
	}
	return time.Now().UTC()
}

// inScopeWatermark is the newest in-scope memory visible at the snapshot: the
// value a successful refresh would persist as last_memory_seen_at. The zero time
// means the scope holds no memory at all.
func (r mentalModelRefresh) inScopeWatermark(ctx context.Context, bankID string, model storage.MentalModel, cutoff time.Time) time.Time {
	reader, ok := r.store.(mentalModelScopeWatermarkReader)
	if !ok {
		return time.Time{}
	}
	return reader.MentalModelScopeWatermark(ctx, bankID, model, cutoff)
}

// mentalModelSnapshotReader is the capability a backend implements to answer the
// database-time snapshot a refresh bounds its reads with. It is optional so the
// in-memory backend — which has no database clock — can fall back to its own.
type mentalModelSnapshotReader interface {
	MentalModelRefreshSnapshot(ctx context.Context, bankID, modelID string) time.Time
}

// mentalModelScopeWatermarkReader is the capability a backend implements to
// answer the newest in-scope memory at a snapshot.
type mentalModelScopeWatermarkReader interface {
	MentalModelScopeWatermark(ctx context.Context, bankID string, model storage.MentalModel, cutoff time.Time) time.Time
}
