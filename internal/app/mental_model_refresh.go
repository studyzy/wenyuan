package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
)

// mentalModelRefresh performs one queued refresh of a mental model.
//
// It regenerates the model's *content* by running its source query through the
// reflection loop, which is what makes a refresh produce current prose rather
// than a new list of ids. The previous content and the staleness watermark are
// left untouched on any failure, so a retry reads the same window again and the
// model does not silently advance past memories it never saw.
//
// It lives in the composition root because it needs both the storage area
// (recall and reflect) and the provider boundary (the model that writes the
// prose), and neither area may import the other.
type mentalModelRefresh struct {
	store      storage.Store
	provider   provider.Provider
	reflection storage.ReflectRunner
}

// newMentalModelRefresh builds the runner. A nil provider still refreshes: the
// reflection falls back to its deterministic form, so a deployment with no
// model configured keeps refreshing rather than silently ceasing to.
func newMentalModelRefresh(store storage.Store, model provider.Provider, reflection storage.ReflectRunner) mentalModelRefresh {
	return mentalModelRefresh{store: store, provider: model, reflection: reflection}
}

// The reference's RefreshOperationOutcome values a refresh here can produce.
// The structured-output outcome is unreachable because this repo stores no
// structured document, so it is deliberately not spelled out.
const (
	outcomeContentWritten             = "content_written"
	outcomeContentUnchanged           = "content_unchanged"
	outcomeContentPreservedNoNewFacts = "content_preserved_no_new_facts"
	outcomeFailedError                = "refresh_failed_error"
)

// The reference's RefreshFailureReason values a refresh here can produce. Each
// names an error rather than a refusal: this repo writes content whenever it
// produces any, so there is no `refresh_skipped` counterpart to match.
const (
	reasonRetrievalFailed = "retrieval_failed"
	reasonNoAnswer        = "no_answer"
	reasonUnexpectedError = "unexpected_error"
)

// mentalModelRefreshReport is what one refresh did, in the vocabulary the
// reference records on the operation: what it did with the document, why it
// refused to write, and the memory window it read from.
type mentalModelRefreshReport struct {
	outcome       string
	failureReason string
	// modeFallbackReason is why a requested delta became a full read, in the
	// reference's vocabulary. Empty when the requested mode was used.
	modeFallbackReason string
	// createdAfter is the window's lower bound: the bank write watermark the
	// previous refresh observed. A delta refresh reads only memories changed
	// since it, so it is nil before the first successful refresh.
	createdAfter *time.Time
	// createdBefore is the window's upper bound: the write watermark captured
	// before this run. A memory written after it is newer than the watermark
	// this run persists, so the next refresh reads it. It doubles as the
	// persisted watermark because the bound is the newest write at capture,
	// not a separate database-clock reading.
	createdBefore *time.Time
}

// failed marks a report as a refusal, keeping the window it already recorded.
func (r mentalModelRefreshReport) failed(reason string) mentalModelRefreshReport {
	r.outcome = outcomeFailedError
	r.failureReason = reason
	return r
}

// failedRefresh builds a refusal report for a failure that happened before the
// window was known.
func failedRefresh(reason string) mentalModelRefreshReport {
	return mentalModelRefreshReport{outcome: outcomeFailedError, failureReason: reason}
}

// refreshedOutcome reports by observable effect: a full regeneration or a delta
// merge that changes the document is content_written, one that reproduces it
// byte-for-byte is content_unchanged.
func refreshedOutcome(content, previous string) string {
	if strings.TrimSpace(content) == strings.TrimSpace(previous) {
		return outcomeContentUnchanged
	}
	return outcomeContentWritten
}

// metadata renders the report with the reference's field names, ready to merge
// onto an operation's result metadata.
func (r mentalModelRefreshReport) metadata() map[string]any {
	if r.outcome == "" {
		return nil
	}
	metadata := map[string]any{
		"outcome": r.outcome,
		// failure_reason is written even when there is none, as null: a refresh
		// retries on the same operation row, so a reason left behind by a failed
		// attempt has to be cleared, or a success would read as
		// outcome=content_written with a stale failure_reason.
		"failure_reason": nil,
	}
	if r.failureReason != "" {
		metadata["failure_reason"] = r.failureReason
	}
	// Written even when empty, like failure_reason and for the same reason: the
	// operation row is reused across attempts, so a reason left behind by an
	// earlier attempt must be cleared.
	if r.modeFallbackReason == "" {
		metadata["mode_fallback_reason"] = nil
	} else {
		metadata["mode_fallback_reason"] = r.modeFallbackReason
	}
	if r.createdAfter != nil {
		metadata["created_after"] = r.createdAfter.UTC().Format(time.RFC3339Nano)
	}
	if r.createdBefore != nil {
		metadata["created_before"] = r.createdBefore.UTC().Format(time.RFC3339Nano)
	}
	return metadata
}

// refresh runs one refresh and returns the updated model, alongside a report of
// what it did for the operation record.
//
// modeOverride is the queued operation's one-run mode ("full" for a "full
// refresh now" or a full_refresh_cron tick), or "" to run the model's own mode.
// It never changes what is stored in the trigger: the next refresh reads the
// trigger again and goes back to editing in place.
func (r mentalModelRefresh) refresh(ctx context.Context, bankID, modelID string, modeOverride string) (storage.MentalModel, mentalModelRefreshReport, error) {
	model, err := r.store.GetMentalModel(ctx, bankID, modelID)
	if err != nil {
		return storage.MentalModel{}, failedRefresh(reasonUnexpectedError), err
	}
	// An empty source query is not a refusal. The reference binds whatever the
	// model stores into the reflect call that drives a refresh
	// (engine/memory_engine.py:17358 `source_query = mental_model["source_query"]`,
	// passed as `query=` at :17420) with no emptiness guard anywhere on the path,
	// and a knowledge page created with `source_query: ""` refreshes to
	// completion there. Failing it instead records `last_refresh_failed_at` on the
	// model, which the tree and the page read both show, and the operation stays
	// failed forever.

	// The watermark is captured before the run, not after: it records what this
	// refresh could have seen, so a memory written while the run was in flight
	// is still treated as unseen and triggers the next refresh.
	seenAt := r.store.BankWriteWatermark(ctx, bankID)

	// The window this refresh read from, reported on the operation. It is set
	// before any decision so every branch — a write, a refusal, an error —
	// reports the same bounds rather than leaving the window on the floor.
	report := mentalModelRefreshReport{createdAfter: model.LastMemorySeenAt}
	if !seenAt.IsZero() {
		bound := seenAt
		report.createdBefore = &bound
	}

	// Evidence the document cites that has since gone is computed before the
	// run, so a delta merge can be told to unsay it: a retraction is the
	// absence of a row, which no watermark and no "new facts" list reports.
	retraction := r.retractionFor(ctx, bankID, model)

	// Having nothing to read is not a cheap reflect — it is the most expensive
	// one there is: every forced retrieval turn comes back empty and the loop
	// runs to its iteration limit. So ask first whether this model's scope has
	// anything in it, and if not keep the document and report that nothing was
	// read. The window's lower bound is the delta watermark in delta mode and
	// open otherwise, matching the recall the run would have made.
	// A delta refresh needs something to be a delta *from*: content to anchor on
	// and a topic that has not moved. When either is missing the reference falls
	// back to a full read and records why, so the window below is only narrowed
	// for a delta that will actually be taken.
	mode, modeFallbackReason := effectiveRefreshMode(model, modeOverride)
	report.modeFallbackReason = modeFallbackReason

	// The lower bound is the watermark a successful refresh persisted, and
	// nothing else: a model no refresh has stamped yet has seen nothing, so its
	// delta window is open and its first refresh reads the whole scope. Falling
	// back to the refresh time here would be wrong even though the staleness
	// check does it - a model is *stamped* at creation (so it does not look
	// stale the moment it is made), and reading that stamp as "already seen
	// every memory" left every new knowledge page permanently empty.
	since := time.Time{}
	if mode == "delta" && model.LastMemorySeenAt != nil {
		since = *model.LastMemorySeenAt
	}
	if !r.hasSources(ctx, bankID, model, since) {
		// Nothing in scope was readable, so the reflect loop is skipped and the
		// document is left exactly as it is. The run is still recorded: the
		// reflect_response says why nothing was read, and the refresh stamp
		// advances so a reader can tell a refresh that ran from one that never
		// did. The memory watermark is deliberately left alone — the window was
		// never read, so nothing has been seen.
		//
		// The payload is ordered the way the reference's is: text, outcome,
		// based_on, the skip reason, then the empty mental_models placeholder.
		reflectResult := storedReflectResponse("", outcomeContentPreservedNoNewFacts, "no_sources_in_scope", storage.ReflectOutcome{})
		sourceQuery := model.SourceQuery
		updated, err := r.store.UpdateMentalModel(ctx, bankID, modelID, storage.MentalModelPatch{
			ReflectResult:            reflectResult,
			LastRefreshedSourceQuery: &sourceQuery,
		})
		if err != nil {
			return storage.MentalModel{}, report.failed(reasonUnexpectedError), err
		}
		if err := r.store.MarkMentalModelSeen(ctx, bankID, modelID, time.Time{}); err != nil {
			return storage.MentalModel{}, report.failed(reasonUnexpectedError), err
		}
		// A full run that found nothing in scope still read everything there is,
		// so it counts as the last full refresh. Left unstamped, a full_refresh_cron
		// on a quiet scope would come due again on every scheduler tick.
		if mode == refreshModeFull {
			if err := r.stampFullRefresh(ctx, bankID, modelID); err != nil {
				return storage.MentalModel{}, report.failed(reasonUnexpectedError), err
			}
		}
		report.outcome = outcomeContentPreservedNoNewFacts
		return updated, report, nil
	}

	// The refresh reads through the same reflection path the API exposes, so a
	// refreshed note reflects current content and reasoning rather than a
	// snapshot taken when the note was created. It answers as a document: the
	// model states its structure and the markdown is rendered from it. The
	// model's resolved tag scope rides along, so a scoped note refreshes over
	// its own memories rather than the whole bank.
	scope := storage.ScopeFromModel(model)
	outcome, err := r.store.Reflect(ctx, storage.ReflectQuery{
		BankID:           bankID,
		Query:            model.SourceQuery,
		Reflection:       r.reflection,
		AnswerAsDocument: true,
		Tags:             scope.Tags,
		TagsMatch:        scope.TagsMatch,
		TagGroups:        scope.TagGroups,
	})
	if err != nil {
		// The previous content stands. A refresh that cannot produce something
		// safe to store must not overwrite what is there with nothing.
		return storage.MentalModel{}, report.failed(reasonRetrievalFailed), fmt.Errorf("%w: %w", errRefreshUnavailable, err)
	}
	if strings.TrimSpace(outcome.Answer) == "" {
		return storage.MentalModel{}, report.failed(reasonNoAnswer), fmt.Errorf("%w: the reflection produced no answer", errRefreshUnavailable)
	}

	observations := map[string]any{"observations": renderFacts(outcome)}
	content := outcome.Answer
	// Delta mode edits the existing document surgically: unchanged sections are
	// preserved, stale content removed, new content added. It falls back to the
	// full answer when the model cannot produce a merge, or when the source
	// query changed since the last refresh (the existing content no longer
	// answers the same question).
	if mode == "delta" {
		if merged, err := r.mergeDelta(ctx, model.Content, outcome.Answer, retraction); err == nil && strings.TrimSpace(merged) != "" {
			content = merged
		}
	}
	// The stored document is the render of its structure, and that render always
	// ends in a single newline. Producing the same shape here means the stored
	// markdown and the structure a later delta operates on agree.
	content = renderContentDocument(content)
	// The history snapshot carries what the write replaces, so a reader can
	// diff the document against the version this refresh superseded.
	previousContent := model.Content
	previousReflect := slimReflectResponse(model.ReflectResult)
	// The grounding is recorded with the content it supports, so a later
	// staleness check can ask which of these facts still exist. The payload is
	// the reference's — text, based_on keyed by fact type, and an empty
	// mental_models list, because a cited model appears under based_on's
	// "mental-models" key rather than twice — and it is what a GET reports as
	// reflect_response, so the shape is observable.
	refreshOutcome := refreshedOutcome(content, model.Content)
	reflectResult := storedReflectResponse(outcome.Answer, refreshOutcome, "", outcome)
	// The query this refresh ran is recorded with the content it produced: a
	// later delta refresh compares it against the model's current source query
	// and reads the whole scope when the topic has moved.
	sourceQuery := model.SourceQuery
	updated, err := r.store.UpdateMentalModel(ctx, bankID, modelID, storage.MentalModelPatch{
		Content:                  &content,
		Observations:             &observations,
		ReflectResult:            reflectResult,
		LastRefreshedSourceQuery: &sourceQuery,
	})
	if err != nil {
		return storage.MentalModel{}, report.failed(reasonUnexpectedError), err
	}
	if err := r.store.RecordMentalModelHistory(ctx, bankID, modelID, storage.MentalModelHistoryEntry{
		ChangedAt:               time.Now().UTC(),
		PreviousContent:         &previousContent,
		PreviousReflectResponse: previousReflect,
	}); err != nil {
		return storage.MentalModel{}, report.failed(reasonUnexpectedError), err
	}
	// Stamping the watermark only after a successful write is what makes a
	// failed refresh retryable: the model still looks stale, so the next tick
	// tries again instead of waiting for new memories.
	if err := r.store.MarkMentalModelSeen(ctx, bankID, modelID, seenAt); err != nil {
		return storage.MentalModel{}, report.failed(reasonUnexpectedError), err
	}
	// Only a run that finished in full mode moves the full-refresh clock: a plain
	// delta leaves it where it was, so frequent delta refreshes never push the
	// full schedule back.
	if mode == refreshModeFull {
		if err := r.stampFullRefresh(ctx, bankID, modelID); err != nil {
			return storage.MentalModel{}, report.failed(reasonUnexpectedError), err
		}
	}
	report.outcome = refreshOutcome
	return updated, report, nil
}

// stampFullRefresh records that a completed refresh ran in full mode, which moves
// the clock trigger.full_refresh_cron comes due against. A backend that cannot
// record it is not an error: the refresh itself is complete, and the next full
// tick simply finds the model due again.
func (r mentalModelRefresh) stampFullRefresh(ctx context.Context, bankID, modelID string) error {
	return storage.StampFullRefresh(ctx, r.store, bankID, modelID)
}

// renderContentDocument normalizes a refresh candidate into the markdown that
// gets stored: the text with surrounding blank space removed and a single
// trailing newline. The reference derives the stored markdown by re-rendering
// the document's structure, and that render always ends in a newline; keeping
// the same shape here means a stored document and its derived structure agree.
func renderContentDocument(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}
	return trimmed + "\n"
}

// hasSources reports whether the model's refresh scope has anything to read.
//
// A reflect over a scope with no sources is the most expensive reflect there
// is: every forced retrieval turn returns nothing, the evidence guardrail then
// refuses every `done`, and the loop pays a forced synthesis on top. So a
// refresh checks first. A backend that cannot answer the question returns true,
// so the run falls back to the reflect rather than silently skipping work.
func (r mentalModelRefresh) hasSources(ctx context.Context, bankID string, model storage.MentalModel, since time.Time) bool {
	checker, ok := r.store.(storage.ScopeSourceChecker)
	if !ok {
		return true
	}
	if checker.ScopeHasSources(ctx, bankID, model, since) {
		return true
	}
	// A sibling document is readable evidence in its own right, unless the
	// model's trigger excludes mental models from its scope.
	if triggerExcludesMentalModels(model.Trigger) {
		return false
	}
	siblings, err := r.store.ListMentalModels(ctx, bankID, "")
	if err != nil {
		return true
	}
	for _, sibling := range siblings {
		if sibling.ID != model.ID && strings.TrimSpace(sibling.Content) != "" {
			return true
		}
	}
	return false
}

// triggerExcludesMentalModels reports whether the trigger removes every other
// document from the model's scope.
func triggerExcludesMentalModels(trigger map[string]any) bool {
	excluded, _ := trigger["exclude_mental_models"].(bool)
	return excluded
}

// effectiveRefreshMode decides between a delta refresh and a full one, and
// names the reason when the requested mode cannot be used.
//
// The reference takes a delta only when the document has content to anchor on
// and the topic has not shifted since the content was written; otherwise it
// reads the whole scope again. Both conditions matter here: a page is created
// with no content, and a template reapply rewrites the source query, and in
// either case a delta over "memories newer than the watermark" would read
// nothing and leave the document as it was.
//
// A one-run override of "full" replaces the requested mode before the fallback
// rules run, so a delta model asked for a full refresh reads its whole scope and
// is not reported as having fallen back — the caller asked for exactly that.
func effectiveRefreshMode(model storage.MentalModel, modeOverride string) (mode, fallbackReason string) {
	if modeOverride == refreshModeFull || refreshMode(model) != "delta" {
		return "full", ""
	}
	if strings.TrimSpace(model.Content) == "" {
		return "full", modeFallbackNoBaselineContent
	}
	if model.LastRefreshedSourceQuery != "" && model.LastRefreshedSourceQuery != model.SourceQuery {
		return "full", modeFallbackSourceQueryChanged
	}
	return "delta", ""
}

// The reference's ModeFallbackReason values, the members a refresh here can
// produce.
const (
	modeFallbackNoBaselineContent  = "no_baseline_content"
	modeFallbackSourceQueryChanged = "source_query_changed"
)

// refreshModeFull is the one-run mode override a refresh operation may carry,
// the same value the refresh request body's mode admits.
const refreshModeFull = "full"

// refreshMode reads the refresh mode from the model's trigger, defaulting to
// full.
func refreshMode(model storage.MentalModel) string {
	if model.Trigger == nil {
		return "full"
	}
	if mode, ok := model.Trigger["mode"].(string); ok {
		return strings.ToLower(strings.TrimSpace(mode))
	}
	return "full"
}

const deltaMergePrompt = `You maintain a knowledge document. You are given the existing
document and new evidence gathered since it was written. Return the updated
document: preserve unchanged sections byte-for-byte, remove content the new
evidence contradicts, and add content the new evidence supports. Return only the
document, with no preamble or commentary.`

// mergeDelta asks the model to fold new evidence into an existing document.
func (r mentalModelRefresh) mergeDelta(ctx context.Context, existing, evidence string, retraction storage.RetractedGrounding) (string, error) {
	if r.provider == nil || r.provider.Name() == "none" {
		return "", errRefreshUnavailable
	}
	request := "Existing document:\n" + existing + "\n\nNew evidence:\n" + evidence
	if section := retractedSection(retraction); section != "" {
		request += "\n\n" + section
	}
	response, err := r.provider.Complete(ctx, provider.CompletionRequest{
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: deltaMergePrompt},
			{Role: provider.RoleUser, Content: request},
		},
		Scope: "mental_model_delta",
	})
	if err != nil {
		return "", err
	}
	return response.Content, nil
}

// errRefreshUnavailable marks a refresh that could not produce a document safe
// to store. It is distinct from a transport failure so the worker can record
// the reason on the model without retrying forever on a permanent refusal.
var errRefreshUnavailable = errors.New("mental model refresh unavailable")

// permanentRefreshFailure reports whether a refresh failure must not be retried.
//
// A model with no source query, or a reflection that produced no answer, will
// behave the same way on the next attempt. Retrying only spends the attempt
// budget and, for an automatic refresh, parks the model later than it needed
// to be parked.
func permanentRefreshFailure(err error) bool {
	return errors.Is(err, errRefreshUnavailable)
}

// retractionFor reports which of the model's cited facts no longer exist.
//
// The check never runs while consolidation is pending: an equivalent fact
// re-ingested under a fresh id is a retraction only until the pass that folds
// it lands, and acting on it early would have the model unsay a sentence its
// evidence is about to support again.
func (r mentalModelRefresh) retractionFor(ctx context.Context, bankID string, model storage.MentalModel) storage.RetractedGrounding {
	lookup, ok := r.store.(storage.LiveMemoryIDs)
	if !ok {
		return storage.RetractedGrounding{}
	}
	cited := storage.CitedGrounding(model)
	if len(cited) == 0 {
		return storage.RetractedGrounding{}
	}
	ids := make([]string, 0, len(cited))
	for _, fact := range cited {
		ids = append(ids, fact.ID)
	}
	live, err := lookup.LiveMemoryIDs(ctx, bankID, ids)
	if err != nil {
		return storage.RetractedGrounding{}
	}
	return storage.RetractionOf(model, live)
}

// retractedSection renders the evidence a document cites that no longer exists,
// so the delta merge can remove the sentences resting on it.
//
// The recorded text is quoted, not the id: the fact is gone, so the model has
// nothing else to match the sentence against.
func retractedSection(retraction storage.RetractedGrounding) string {
	if len(retraction.Facts) == 0 || retraction.Unresolvable() {
		// Losing all the grounding at once is a broken link rather than a
		// retraction, and acting on it would have the model unsay a document it
		// has nothing to check against.
		return ""
	}
	var b strings.Builder
	b.WriteString("## RETRACTED EVIDENCE\n")
	b.WriteString("These facts no longer exist, so remove any statement that rests on them:\n\n")
	for _, fact := range retraction.Facts {
		b.WriteString("- ")
		b.WriteString(strings.TrimSpace(fact.Text))
		b.WriteByte('\n')
	}
	return b.String()
}

// renderFacts renders the evidence a refresh rested on, so the model records
// what it was built from and not only what it says. The same shape is what the
// staleness check reads the grounding back from.
func renderFacts(outcome storage.ReflectOutcome) []any {
	facts := make([]any, 0, len(outcome.Facts))
	for _, fact := range outcome.Facts {
		facts = append(facts, map[string]any{"id": fact.ID, "text": fact.Text, "type": fact.Type})
	}
	return facts
}

// reflectResponseFactTypes is the order a stored reflect_response's based_on
// lists the fact types it ground the document on in. The reference builds the
// map from what the reflect run cited — a type with no cited fact contributes no
// key at all — and the framework serializes it with insertion order, which is
// the order the run's own `based_on` dict was built in. These six are the
// reference's, in its order, and "mental-models" is hyphenated so it reads like
// the recall type it names.
var reflectResponseFactTypes = []string{
	"world", "opinion", "directives", "experience", "observation", "mental-models",
}

// storedReflectResponse builds the reflect_response payload a refresh stores: the
// answer it produced, the evidence that grounded it keyed by fact type, an empty
// mental_models list, and the outcome that says what the write did — plus the
// skip reason when the run read nothing.
//
// The mental_models list stays empty on purpose. The reference fills based_on's
// "mental-models" key with the pages the run cited and keeps this list as a
// compatibility placeholder, so a model cited as evidence appears once, under
// the key that names its type.
func storedReflectResponse(text, outcomeName, skip string, outcome storage.ReflectOutcome) *map[string]any {
	based := make(map[string]any, len(reflectResponseFactTypes))
	for _, group := range renderFactsByType(outcome) {
		based[group.factType] = group.facts
	}
	stored := map[string]any{
		"text":          text,
		"outcome":       outcomeName,
		"based_on":      based,
		"mental_models": []any{},
	}
	if skip != "" {
		stored["reflect_skipped"] = skip
	}
	return &stored
}

// reflectFactGroup is one fact type's cited evidence, before it is folded into
// the stored map.
type reflectFactGroup struct {
	factType string
	facts    []any
}

// renderFactsByType groups the run's cited evidence into the reference's stored
// based_on shape: one list per fact type the run cited, in the reference's
// declaration order, each entry carrying the fact's id, text, type and context.
// A fact type the run cited nothing from contributes no key at all — the
// reference builds this dict from what the reflect run returned.
func renderFactsByType(outcome storage.ReflectOutcome) []reflectFactGroup {
	byType := make(map[string][]any, len(reflectResponseFactTypes))
	appendFact := func(factType string, entry map[string]any) {
		byType[factType] = append(byType[factType], entry)
	}
	for _, fact := range outcome.Facts {
		appendFact(fact.Type, map[string]any{
			"id": fact.ID, "text": fact.Text, "type": fact.Type, "context": fact.Context,
		})
	}
	for _, model := range outcome.MentalModels {
		appendFact("mental-models", map[string]any{
			"id": model.ID, "text": model.Text, "type": "mental-models", "context": model.Context,
		})
	}
	for _, directive := range outcome.Directives {
		appendFact("directives", map[string]any{
			"id": directive.ID, "text": directive.Content, "type": "directives", "context": nil,
		})
	}
	out := make([]reflectFactGroup, 0, len(byType))
	for _, factType := range reflectResponseFactTypes {
		if facts, present := byType[factType]; present {
			out = append(out, reflectFactGroup{factType: factType, facts: facts})
		}
	}
	return out
}

// historyReflectKeys are the two members the reference keeps in a history
// snapshot's previous_reflect_response: the evidence that grounded the version
// (based_on), and how that version was produced (trace, present only when
// trigger.keep_trace was on).
var historyReflectKeys = []string{"based_on", "trace"}

// slimReflectResponse is the slice of a reflect_response the reference writes
// into a mental model's history entry (memory_engine.py:18674-18695): only
// `based_on` and `trace` survive, because the full payload — text, fact bodies,
// scoring, embeddings — can be hundreds of kilobytes and is persisted per entry.
//
// The slice is observable: a history read reports exactly the members this
// returns. An entry with neither member is stored as null, which is what the
// reference's `slim or None` produces.
func slimReflectResponse(previous map[string]any) map[string]any {
	if previous == nil {
		return nil
	}
	slim := make(map[string]any, len(historyReflectKeys))
	for _, key := range historyReflectKeys {
		if value, present := previous[key]; present && value != nil {
			slim[key] = value
		}
	}
	if len(slim) == 0 {
		return nil
	}
	return slim
}
