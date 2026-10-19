package app

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// A failed delta apply is a refusal, not a write: the preview shows the stored
// content, nothing would persist, and the run reports the delta-not-applied
// outcome the reference uses.
func TestDryRunDeltaOpsFailureRefusesTheWrite(t *testing.T) {
	store, modelID := refreshFixture(t, "deployment")
	ctx := context.Background()
	existing := "deployments happen on tuesdays"
	if _, err := store.UpdateMentalModel(ctx, "b1", modelID, storage.MentalModelPatch{Content: &existing}); err != nil {
		t.Fatal(err)
	}
	model, err := store.GetMentalModel(ctx, "b1", modelID)
	if err != nil {
		t.Fatal(err)
	}
	model.Trigger = map[string]any{"mode": "delta"}
	model.LastRefreshedSourceQuery = model.SourceQuery
	model.LastRefreshedAt = time.Now().UTC()
	if _, err := store.PutMentalModel(ctx, model); err != nil {
		t.Fatal(err)
	}

	// No provider, so the delta merge cannot run and the run falls back to full.
	runner := newMentalModelRefresh(store, nil, nil)
	result, err := runner.dryRun(ctx, "b1", modelID)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if result.EffectiveMode != "full" || result.ModeFallbackReason == nil || *result.ModeFallbackReason != "delta_ops_failed" {
		t.Fatalf("mode = %q fallback = %v", result.EffectiveMode, result.ModeFallbackReason)
	}
	if result.Outcome != "refresh_failed_delta_not_applied" {
		t.Fatalf("outcome = %q, want refresh_failed_delta_not_applied", result.Outcome)
	}
	if result.WouldPersist {
		t.Error("a refused refresh must not report would_persist")
	}
	if result.PreviewContent != existing {
		t.Fatalf("preview = %q, want the stored content %q", result.PreviewContent, existing)
	}
	if result.Diff != "" {
		t.Fatalf("diff = %q, want empty (current equals preview)", result.Diff)
	}
}

// based_on is keyed by fact type with the reference's entry shape, and every
// fact-type key is present even when empty.
func TestDryRunBasedOnIsKeyedByFactType(t *testing.T) {
	outcome := storage.ReflectOutcome{
		Facts: []storage.ReflectCitation{
			{ID: "o1", Text: "the build failed", Type: "observation", Context: "runtime"},
			{ID: "w1", Text: "deploys on tuesday", Type: "world"},
		},
		MentalModels: []storage.ReflectMentalModelCitation{{ID: "mm1", Text: "release notes"}},
		Directives:   []storage.ReflectDirectiveCitation{{ID: "d1", Name: "tone", Content: "be terse"}},
	}
	based := renderBasedOn(outcome)
	for _, key := range []string{"world", "experience", "opinion", "observation", "mental-models", "directives"} {
		if _, ok := based[key]; !ok {
			t.Fatalf("based_on is missing the %q key: %v", key, based)
		}
	}
	if len(based["experience"]) != 0 || len(based["opinion"]) != 0 {
		t.Fatalf("empty fact types must be present as empty lists: %v", based)
	}
	observations := based["observation"]
	if len(observations) != 1 {
		t.Fatalf("observation bucket = %v", observations)
	}
	entry, ok := observations[0].(map[string]any)
	if !ok {
		t.Fatalf("entry is not an object: %T", observations[0])
	}
	if entry["id"] != "o1" || entry["text"] != "the build failed" ||
		entry["type"] != "observation" || entry["context"] != "runtime" {
		t.Fatalf("observation entry = %v", entry)
	}
	if _, hasMemories := based["memories"]; hasMemories {
		t.Error("based_on must not carry the flattened \"memories\" key")
	}
}

// retrieved counts what the tool calls returned, independently of what the
// agent declared it used — the whole point of reporting the two separately.
func TestDryRunFactsRetrievedIsIndependentOfUsed(t *testing.T) {
	memories := make([]map[string]any, 0, 12)
	for i := range 12 {
		memories = append(memories, map[string]any{"id": fmt.Sprintf("d%d", i), "fact_type": "diagnostics"})
	}
	// The in-process payload is a typed slice, not a []any; the tally must read
	// it the same way.
	outcome := storage.ReflectOutcome{
		Trace: &storage.ReflectTrace{ToolCalls: []storage.ReflectToolCall{
			{Tool: "recall", Output: map[string]any{"memories": memories}, HasOutput: true},
		}},
	}
	counts := factCounts(outcome)
	if counts.Retrieved["diagnostics"] != 12 {
		t.Fatalf("retrieved = %v, want diagnostics:12", counts.Retrieved)
	}
	if len(counts.Used) != 0 {
		t.Fatalf("used = %v, want empty (the agent used none)", counts.Used)
	}
}

// A tool that returns the same fact twice counts it once.
func TestDryRunRetrievedFactsAreDeduplicated(t *testing.T) {
	outcome := storage.ReflectOutcome{
		Trace: &storage.ReflectTrace{ToolCalls: []storage.ReflectToolCall{
			{Tool: "search_observations", Output: map[string]any{"observations": []any{
				map[string]any{"id": "o1"}, map[string]any{"id": "o1"}, map[string]any{"id": "o2"},
			}}, HasOutput: true},
		}},
	}
	if got := factCounts(outcome).Retrieved["observation"]; got != 2 {
		t.Fatalf("observation retrieved = %d, want 2 (deduplicated)", got)
	}
}

// A preview over a scope with nothing to read skips the reflect loop the real
// refresh would skip, and reports the preserved-content outcome and why: it must
// not run — or report — a reflect whose result the refresh itself would throw
// away.
func TestDryRunWithNoSourcesSkipsTheReflect(t *testing.T) {
	store, modelID := noSourceFixture(t)
	ctx := context.Background()
	if _, err := store.UpdateMentalModel(ctx, "b1", modelID, storage.MentalModelPatch{Content: ptrTo("as it stands")}); err != nil {
		t.Fatal(err)
	}

	runner := newMentalModelRefresh(store, nil, nil)
	result, err := runner.dryRun(ctx, "b1", modelID)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if result.Outcome != "content_preserved_no_new_facts" {
		t.Fatalf("outcome = %q, want content_preserved_no_new_facts", result.Outcome)
	}
	if result.WouldPersist {
		t.Error("a preview with nothing to read must not report would_persist")
	}
	if result.CandidateContent != "" {
		t.Fatalf("candidate = %q, want empty: no reflect ran", result.CandidateContent)
	}
	if result.PreviewContent != "as it stands" {
		t.Fatalf("preview = %q, want the stored content", result.PreviewContent)
	}
	if len(result.BasedOn) != 0 {
		t.Fatalf("based_on = %#v, want empty", result.BasedOn)
	}
	if len(result.Trace.ToolCalls) != 0 || len(result.Trace.LLMCalls) != 0 {
		t.Fatalf("trace = %+v, want no calls", result.Trace)
	}
	if result.Usage.TotalTokens != 0 {
		t.Fatalf("usage = %+v, want no tokens spent", result.Usage)
	}
	if len(result.Warnings) != 1 || result.Warnings[0] != noSourcesInScopeWarning {
		t.Fatalf("warnings = %#v, want the no-sources explanation", result.Warnings)
	}
}

// The reference stamps a trace with datetime.isoformat(), so the string always
// carries six fractional digits (and none at all only when they are zero). An
// RFC3339Nano rendering trims trailing zeros and writes a different string for
// the same instant, which a client parsing the trace would see as a different
// format from the reference's.
func TestDryRunRecordedAtUsesTheReferenceRendering(t *testing.T) {
	store, modelID := noSourceFixture(t)
	ctx := context.Background()
	if _, err := store.UpdateMentalModel(ctx, "b1", modelID, storage.MentalModelPatch{Content: ptrTo("as it stands")}); err != nil {
		t.Fatal(err)
	}

	runner := newMentalModelRefresh(store, nil, nil)
	result, err := runner.dryRun(ctx, "b1", modelID)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if result.Trace.RecordedAt == nil {
		t.Fatal("a dry run always records when it ran")
	}
	stamp := *result.Trace.RecordedAt
	// Python's isoformat writes either bare seconds or exactly six digits.
	bare := len("2006-01-02T15:04:05") + len("+00:00")
	withMicros := len("2006-01-02T15:04:05") + len(".000000") + len("+00:00")
	if len(stamp) != withMicros && len(stamp) != bare {
		t.Fatalf("recorded_at = %q (len %d), want six fractional digits or none", stamp, len(stamp))
	}
	if _, err := time.Parse("2006-01-02T15:04:05.000000+00:00", stamp); err != nil {
		t.Fatalf("recorded_at = %q does not parse as the reference's rendering: %v", stamp, err)
	}
	if !strings.HasSuffix(stamp, "+00:00") {
		t.Fatalf("recorded_at = %q, want Python's +00:00 offset rather than Z", stamp)
	}
}
