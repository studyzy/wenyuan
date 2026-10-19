package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/worker"
)

// refreshFixture seeds a bank with one memory and one mental model.
func refreshFixture(t *testing.T, sourceQuery string) (*storage.MemoryStore, string) {
	t.Helper()
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: "b1", Name: "b1"}); err != nil {
		t.Fatal(err)
	}
	store.SeedMemory("b1", storage.RecallMatch{
		ID: "m1", Text: "deployments happen on tuesdays", Type: "world",
	})
	model, err := store.PutMentalModel(ctx, storage.MentalModel{
		ID: "model-1", BankID: "b1", Subtype: storage.SubtypePinned,
		Name: "deploy days", SourceQuery: sourceQuery,
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, model.ID
}

// A refresh rewrites the model's content from the reflection's answer and
// stamps the watermark it observed, so the model stops looking stale.
func TestRefreshWritesContentAndWatermark(t *testing.T) {
	store, modelID := refreshFixture(t, "deployment")
	runner := newMentalModelRefresh(store, nil, nil)

	updated, _, err := runner.refresh(context.Background(), "b1", modelID)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if updated.Content == "" {
		t.Error("the refresh must write content, since that is what it exists to regenerate")
	}
	stored, err := store.GetMentalModel(context.Background(), "b1", modelID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.LastRefreshedAt.IsZero() {
		t.Error("a successful refresh must stamp the refresh time")
	}
	if stored.LastRefreshFailedAt != nil {
		t.Error("a successful refresh must clear any park")
	}
}

// A model with no source query has nothing to re-run, so the refresh refuses
// rather than inventing content or wiping what is there.
func TestRefreshWithoutSourceQueryIsRefused(t *testing.T) {
	store, modelID := refreshFixture(t, "   ")
	runner := newMentalModelRefresh(store, nil, nil)

	_, _, err := runner.refresh(context.Background(), "b1", modelID)
	if err == nil {
		t.Fatal("a model with no source query must refuse")
	}
	if !permanentRefreshFailure(err) {
		t.Errorf("err = %v, must classify as permanent so it is not retried forever", err)
	}
}

// A failed refresh leaves the watermark and the content untouched, so the next
// attempt re-reads the same window rather than skipping past memories it never
// saw.
func TestFailedRefreshDoesNotAdvanceTheWatermark(t *testing.T) {
	store, modelID := refreshFixture(t, "deployment")
	ctx := context.Background()
	before, err := store.GetMentalModel(ctx, "b1", modelID)
	if err != nil {
		t.Fatal(err)
	}

	// A reflection that cannot produce an answer stands in for a model failure.
	runner := newMentalModelRefresh(store, nil, failingReflector{})
	if _, _, err := runner.refresh(ctx, "b1", modelID); err == nil {
		t.Fatal("a reflection that fails must fail the refresh")
	}

	after, err := store.GetMentalModel(ctx, "b1", modelID)
	if err != nil {
		t.Fatal(err)
	}
	if after.LastMemorySeenAt != nil {
		t.Errorf("the watermark advanced to %v despite the failure", after.LastMemorySeenAt)
	}
	if after.Content != before.Content {
		t.Error("a failed refresh must not overwrite the content")
	}
}

// The permanence classification is what decides retry behaviour: a refusal the
// same input would earn again must not consume the attempt budget.
func TestPermanentRefreshFailureClassification(t *testing.T) {
	if permanentRefreshFailure(errors.New("some other failure")) {
		t.Error("an unrelated failure must be retried")
	}
	if !permanentRefreshFailure(errRefreshUnavailable) {
		t.Error("a refusal must be permanent")
	}
	wrapped := errors.New("outer: " + errRefreshUnavailable.Error())
	if permanentRefreshFailure(wrapped) {
		t.Error("a message that merely mentions the reason is not a classified failure")
	}
}

// A failure recorded against a model parks it, and a later success clears it.
func TestRefreshFailureParksAndSuccessClears(t *testing.T) {
	store, modelID := refreshFixture(t, "deployment")
	ctx := context.Background()
	failedAt := time.Now().UTC()

	if err := store.RecordMentalModelRefreshFailure(ctx, "b1", modelID, failedAt); err != nil {
		t.Fatal(err)
	}
	parked, err := store.GetMentalModel(ctx, "b1", modelID)
	if err != nil {
		t.Fatal(err)
	}
	if parked.LastRefreshFailedAt == nil {
		t.Fatal("a recorded failure must park the model")
	}
	if err := store.MarkMentalModelSeen(ctx, "b1", modelID, failedAt); err != nil {
		t.Fatal(err)
	}
	cleared, err := store.GetMentalModel(ctx, "b1", modelID)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.LastRefreshFailedAt != nil {
		t.Error("a successful refresh must clear the park")
	}
}

// A failing reflector reports an error, standing in for a model call that could
// not answer.
type failingReflector struct{}

func (failingReflector) Run(context.Context, storage.AgenticReflectRequest) (storage.ReflectOutcome, error) {
	return storage.ReflectOutcome{}, errors.New("the model could not answer")
}

// mergeProvider returns a fixed merged document for the delta call.
type mergeProvider struct{ content string }

func (m mergeProvider) Name() string                           { return "openai" }
func (m mergeProvider) Dimensions() int                        { return provider.DeclaredDimensions() }
func (m mergeProvider) VerifyConnection(context.Context) error { return nil }
func (m mergeProvider) Embed(context.Context, provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	return provider.EmbeddingResult{}, nil
}
func (m mergeProvider) Complete(context.Context, provider.CompletionRequest) (provider.CompletionResult, error) {
	return provider.CompletionResult{Content: m.content}, nil
}

// A delta-mode refresh folds the new evidence into the existing document and
// records the delta reason.
func TestRefreshDeltaModeMergesContent(t *testing.T) {
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
	if _, err := store.PutMentalModel(ctx, model); err != nil {
		t.Fatal(err)
	}

	runner := newMentalModelRefresh(store, mergeProvider{content: "deployments happen on tuesdays and thursdays"}, nil)
	updated, _, err := runner.refresh(ctx, "b1", modelID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Content != "deployments happen on tuesdays and thursdays\n" {
		t.Fatalf("delta content = %q", updated.Content)
	}
	history, err := store.MentalModelHistory(ctx, "b1", modelID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) == 0 || history[0].PreviousContent == nil || *history[0].PreviousContent != existing {
		t.Fatalf("history previous content = %+v, want %q", history, existing)
	}
}

// The retraction section quotes the recorded text, so the merge can remove the
// sentences resting on evidence that no longer exists.
func TestRetractedSectionQuotesRecordedText(t *testing.T) {
	section := retractedSection(storage.RetractedGrounding{
		CitedCount: 3,
		Facts: []storage.GroundedFact{
			{ID: "f2", Type: "world", Text: "Alice is on-call"},
			{ID: "f3", Type: "experience", Text: "we shipped on Friday"},
		},
	})
	if section == "" {
		t.Fatal("a partial retraction must render a section")
	}
	for _, want := range []string{"RETRACTED EVIDENCE", "Alice is on-call", "we shipped on Friday"} {
		if !strings.Contains(section, want) {
			t.Fatalf("section missing %q: %s", want, section)
		}
	}
}

// A document whose every citation is gone is a broken link, not a retraction,
// so nothing is asked of the model.
func TestRetractedSectionSkipsUnresolvable(t *testing.T) {
	section := retractedSection(storage.RetractedGrounding{
		CitedCount: 2,
		Facts: []storage.GroundedFact{
			{ID: "f1", Type: "world", Text: "a"},
			{ID: "f2", Type: "world", Text: "b"},
		},
	})
	if section != "" {
		t.Fatalf("an unresolvable grounding must ask for nothing: %q", section)
	}
	if got := retractedSection(storage.RetractedGrounding{}); got != "" {
		t.Fatalf("no retraction must render nothing: %q", got)
	}
}

// runRefreshOperation queues a refresh for the model and drains it through a
// consumer over the given processor, returning the settled operation record.
func runRefreshOperation(t *testing.T, store *storage.MemoryStore, processor worker.Processor, modelID string) storage.Operation {
	t.Helper()
	ctx := context.Background()
	op, err := store.PutOperation(ctx, storage.Operation{
		BankID:        "b1",
		OperationType: storage.OperationTypeRefreshMentalModel,
		Payload:       map[string]any{storage.MentalModelIDPayloadKey: modelID},
	})
	if err != nil {
		t.Fatal(err)
	}

	consumer := worker.New(store, processor, worker.Config{
		WorkerID: "w-outcome", Concurrency: 1, PollInterval: 5 * time.Millisecond,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	runCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	go func() { _ = consumer.Run(runCtx) }()

	for {
		final, err := store.GetOperation(ctx, op.BankID, op.OperationID)
		if err != nil {
			t.Fatal(err)
		}
		if final.Status.Terminal() {
			return final
		}
		if runCtx.Err() != nil {
			t.Fatalf("the refresh operation never settled: %+v", final)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// A completed refresh records what it did and the memory window it read on the
// operation, in the reference's field names.
func TestRefreshOperationRecordsOutcomeAndWindow(t *testing.T) {
	store, modelID := refreshFixture(t, "deployment")
	// A write through the real path stamps the bank watermark the refresh
	// captures as its window's upper bound.
	if _, err := store.RecordWrite(context.Background(), storage.WriteRequest{
		BankID: "b1", Content: "deployments happen on tuesdays",
	}); err != nil {
		t.Fatal(err)
	}

	final := runRefreshOperation(t, store, archiveProcessor{store: store}, modelID)
	if final.Status != storage.StatusCompleted {
		t.Fatalf("operation status = %s, want completed", final.Status)
	}
	metadata := final.ResultMetadata
	if metadata == nil {
		t.Fatal("a completed refresh must report what it did on the operation record")
	}
	outcome, _ := metadata["outcome"].(string)
	if outcome != outcomeContentWritten && outcome != outcomeContentUnchanged {
		t.Fatalf("outcome = %v, want a content outcome", metadata["outcome"])
	}
	if _, ok := metadata["created_before"]; !ok {
		t.Fatalf("the window's upper bound is missing: %+v", metadata)
	}
	if reason, ok := metadata["failure_reason"]; !ok || reason != nil {
		t.Fatalf("a successful refresh must clear the failure reason: %+v", metadata)
	}
}

// A refused refresh records the failure reason in the same vocabulary, not only
// as a prose error string.
func TestFailedRefreshOperationRecordsFailureReason(t *testing.T) {
	store, modelID := refreshFixture(t, "deployment")

	final := runRefreshOperation(t, store, archiveProcessor{store: store, reflection: failingReflector{}}, modelID)
	if final.Status != storage.StatusFailed {
		t.Fatalf("operation status = %s, want failed", final.Status)
	}
	metadata := final.ResultMetadata
	if metadata == nil {
		t.Fatal("a failed refresh must report why on the operation record")
	}
	if metadata["outcome"] != outcomeFailedError {
		t.Fatalf("outcome = %v, want %s", metadata["outcome"], outcomeFailedError)
	}
	if metadata["failure_reason"] != reasonRetrievalFailed {
		t.Fatalf("failure_reason = %v, want %s", metadata["failure_reason"], reasonRetrievalFailed)
	}
	if final.Error == "" {
		t.Fatal("the prose error must survive alongside the structured reason")
	}
}

// A refusal that happens before the window is captured still records its
// outcome and reason, with no window to report.
func TestRefreshWithoutWindowRecordsFailure(t *testing.T) {
	store, modelID := refreshFixture(t, "   ")

	final := runRefreshOperation(t, store, archiveProcessor{store: store}, modelID)
	if final.Status != storage.StatusFailed {
		t.Fatalf("operation status = %s, want failed", final.Status)
	}
	if final.ResultMetadata["outcome"] != outcomeFailedError {
		t.Fatalf("result metadata = %+v, want a failure outcome", final.ResultMetadata)
	}
	if final.ResultMetadata["failure_reason"] != reasonUnexpectedError {
		t.Fatalf("result metadata = %+v, want the unexpected-error reason", final.ResultMetadata)
	}
}

// A delta refresh needs content to anchor on and a topic that has not moved.
// Both fallbacks matter: a page is created empty, and a template reapply
// rewrites the source query, and in either case a delta over "memories newer
// than the watermark" would read nothing and leave the document as it was.
func TestEffectiveRefreshMode(t *testing.T) {
	delta := map[string]any{"mode": "delta"}
	cases := []struct {
		name       string
		trigger    map[string]any
		content    string
		refreshed  string
		sourceQ    string
		wantMode   string
		wantReason string
	}{
		{"full is asked for", nil, "anything", "", "q", "full", ""},
		{"delta without content falls back", delta, "", "", "q", "full", modeFallbackNoBaselineContent},
		{"delta with whitespace-only content falls back", delta, "  \n ", "", "q", "full", modeFallbackNoBaselineContent},
		{"delta after a topic change falls back", delta, "written", "old topic", "new topic", "full", modeFallbackSourceQueryChanged},
		{"delta on an unchanged topic", delta, "written", "q", "q", "delta", ""},
		{"delta never refreshed reads everything", delta, "hand written", "", "q", "delta", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model := storage.MentalModel{
				Trigger: tc.trigger, Content: tc.content,
				LastRefreshedSourceQuery: tc.refreshed, SourceQuery: tc.sourceQ,
			}
			mode, reason := effectiveRefreshMode(model)
			if mode != tc.wantMode || reason != tc.wantReason {
				t.Fatalf("effectiveRefreshMode = (%q, %q), want (%q, %q)", mode, reason, tc.wantMode, tc.wantReason)
			}
		})
	}
}

// A refresh records the query it ran, and that record is what turns a later
// source-query change into a full read: without it the change is
// indistinguishable from a first refresh and the document keeps answering the
// old question.
func TestRefreshRecordsTheQueryItRan(t *testing.T) {
	store, modelID := refreshFixture(t, "which days do deployments happen")
	delta := map[string]any{"mode": "delta"}
	if _, err := store.UpdateMentalModel(context.Background(), "b1", modelID,
		storage.MentalModelPatch{Content: ptrTo("written already"), Trigger: &delta}); err != nil {
		t.Fatal(err)
	}
	runner := newMentalModelRefresh(store, nil, nil)
	if _, _, err := runner.refresh(context.Background(), "b1", modelID); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	stored, err := store.GetMentalModel(context.Background(), "b1", modelID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.LastRefreshedSourceQuery != "which days do deployments happen" {
		t.Fatalf("last_refreshed_source_query = %q, want the query the refresh ran", stored.LastRefreshedSourceQuery)
	}

	// The topic moves: the next refresh must read the whole scope rather than
	// merge into content that answers a different question.
	query := "when do releases ship"
	if _, err := store.UpdateMentalModel(context.Background(), "b1", modelID, storage.MentalModelPatch{SourceQuery: &query}); err != nil {
		t.Fatal(err)
	}
	changed, err := store.GetMentalModel(context.Background(), "b1", modelID)
	if err != nil {
		t.Fatal(err)
	}
	if mode, reason := effectiveRefreshMode(changed); mode != "full" || reason != modeFallbackSourceQueryChanged {
		t.Fatalf("after a source-query change: (%q, %q), want a full read", mode, reason)
	}
}

// A model that has never been refreshed reads the whole scope: its delta window
// is bounded by the watermark alone, and the creation stamp the schema gives it
// must not be read as "already seen every memory".
func TestFirstRefreshReadsTheWholeScope(t *testing.T) {
	store, modelID := refreshFixture(t, "deployment days")
	query := "deployment days"
	if _, err := store.UpdateMentalModel(context.Background(), "b1", modelID,
		storage.MentalModelPatch{Content: &query, Trigger: &map[string]any{"mode": "delta"}}); err != nil {
		t.Fatal(err)
	}
	runner := newMentalModelRefresh(store, nil, nil)
	model, err := store.GetMentalModel(context.Background(), "b1", modelID)
	if err != nil {
		t.Fatal(err)
	}
	if model.LastMemorySeenAt != nil {
		t.Fatal("the fixture must not carry a watermark, or the case is not exercised")
	}
	if _, report, err := runner.refresh(context.Background(), "b1", modelID); err != nil {
		t.Fatalf("refresh: %v", err)
	} else if report.outcome != outcomeContentWritten {
		t.Fatalf("outcome = %q, want a written document: the first refresh must read the bank", report.outcome)
	}
}

// ptrTo returns a pointer to a copy, for patch fields.
func ptrTo[T any](value T) *T { return &value }

// noSourceFixture builds a bank holding a single model and nothing to read:
// no memory units, no recall projection, and no sibling document with content.
func noSourceFixture(t *testing.T) (*storage.MemoryStore, string) {
	t.Helper()
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: "b1", Name: "b1"}); err != nil {
		t.Fatal(err)
	}
	model, err := store.PutMentalModel(ctx, storage.MentalModel{
		ID: "model-1", BankID: "b1", Subtype: storage.SubtypePinned,
		Name: "empty page", SourceQuery: "what is the secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, model.ID
}

// A refresh over a scope with nothing to read writes no content, but it does
// leave a record: the reflect_response says why nothing was read, and the
// refresh stamp moves so a reader can tell a run that happened from one that
// never did. Nothing was read, so the memory watermark stays where it was.
func TestRefreshWithNoSourcesRecordsTheSkipWithoutWritingContent(t *testing.T) {
	store, modelID := noSourceFixture(t)
	ctx := context.Background()
	planted := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	if _, err := store.UpdateMentalModel(ctx, "b1", modelID, storage.MentalModelPatch{Content: ptrTo("stays as it is")}); err != nil {
		t.Fatal(err)
	}
	model, err := store.GetMentalModel(ctx, "b1", modelID)
	if err != nil {
		t.Fatal(err)
	}
	model.LastRefreshedAt = planted
	if _, err := store.PutMentalModel(ctx, model); err != nil {
		t.Fatal(err)
	}

	runner := newMentalModelRefresh(store, nil, nil)
	_, report, err := runner.refresh(ctx, "b1", modelID)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if report.outcome != outcomeContentPreservedNoNewFacts {
		t.Fatalf("outcome = %q, want %q", report.outcome, outcomeContentPreservedNoNewFacts)
	}
	// The refresh stamps the model in a second write, so the record is read back
	// rather than taken from the value the content update returned.
	updated, err := store.GetMentalModel(ctx, "b1", modelID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Content != "stays as it is" {
		t.Fatalf("content = %q, want the document preserved", updated.Content)
	}
	skipped, _ := updated.ReflectResult["reflect_skipped"].(string)
	if skipped != "no_sources_in_scope" {
		t.Fatalf("reflect_skipped = %q, want no_sources_in_scope", skipped)
	}
	if got, _ := updated.ReflectResult["outcome"].(string); got != outcomeContentPreservedNoNewFacts {
		t.Fatalf("recorded outcome = %q, want %q", got, outcomeContentPreservedNoNewFacts)
	}
	if basedOn, ok := updated.ReflectResult["based_on"].(map[string]any); !ok || len(basedOn) != 0 {
		t.Fatalf("based_on = %#v, want an empty map", updated.ReflectResult["based_on"])
	}
	if !updated.LastRefreshedAt.After(planted) {
		t.Fatalf("last_refreshed_at = %v, want it advanced past %v", updated.LastRefreshedAt, planted)
	}
	if updated.LastMemorySeenAt != nil {
		t.Fatalf("last_memory_seen_at = %v, want it untouched: nothing was read", updated.LastMemorySeenAt)
	}
}
