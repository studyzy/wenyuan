package reflect

import (
	"context"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
)

// pinnedTool returns the tool a request was forced to call, or "" when the
// model was left to choose.
func pinnedTool(req provider.CompletionRequest) string {
	if req.ToolChoice == nil || req.ToolChoice.Name == "" {
		return ""
	}
	return req.ToolChoice.Name
}

// sawGuidance reports whether any message of a request carries the fresh-page
// guidance.
func sawGuidance(req provider.CompletionRequest) bool {
	for _, message := range req.Messages {
		if strings.Contains(message.Content, "These pages are fresh") {
			return true
		}
	}
	return false
}

// test-forced-descent-first-layer: the loop opens by constraining the model to
// the hierarchy's cheapest layer, so it cannot answer from raw facts — or from
// nothing — while a reusable perspective is untried.
func TestFirstTurnIsForcedToMentalModels(t *testing.T) {
	p := &scriptedProvider{name: "openai", script: turnScript(
		toolCall(ToolSearchMentalModels, map[string]any{"query": "q"}),
		toolCall(ToolDone, nil),
	)}
	retrieval := &stubRetrieval{models: []Evidence{{Kind: "mental_model", ID: "p1", Text: "stale page"}}}
	if _, err := NewLoop(p, retrieval, Config{Question: "q"}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := pinnedTool(p.requests[0]); got != ToolSearchMentalModels {
		t.Fatalf("first turn tool choice = %q, want %q", got, ToolSearchMentalModels)
	}
}

// test-forced-descent-released-layer-by-layer: with no fresh perspective to
// release it, the forcing walks mental models, then observations, then raw
// recall, and only then hands control back to the model.
func TestForcedDescentWalksTheHierarchy(t *testing.T) {
	p := &scriptedProvider{name: "openai", script: turnScript(
		toolCall(ToolSearchMentalModels, map[string]any{"query": "q"}),
		toolCall(ToolSearchObservations, map[string]any{"query": "q"}),
		toolCall(ToolRecall, map[string]any{"query": "q"}),
		toolCall(ToolDone, nil),
	)}
	retrieval := &stubRetrieval{models: []Evidence{{Kind: "mental_model", ID: "p1", Text: "stale page"}}}
	if _, err := NewLoop(p, retrieval, Config{Question: "q"}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{ToolSearchMentalModels, ToolSearchObservations, ToolRecall, ""}
	if len(p.requests) < len(want) {
		t.Fatalf("provider saw %d requests, want at least %d", len(p.requests), len(want))
	}
	for i, name := range want {
		if got := pinnedTool(p.requests[i]); got != name {
			t.Fatalf("turn %d tool choice = %q, want %q", i+1, got, name)
		}
	}
}

// test-fresh-page-short-circuit: when every retrieved perspective is fresh and
// usable, the remaining forced steps are released, and the result the model is
// about to read says why.
func TestFreshPagesReleaseForcing(t *testing.T) {
	p := &scriptedProvider{name: "openai", script: turnScript(
		toolCall(ToolSearchMentalModels, map[string]any{"query": "q"}),
		toolCall(ToolDone, nil),
	)}
	retrieval := &stubRetrieval{models: []Evidence{
		{Kind: "mental_model", ID: "p1", Text: "fresh page one", Fresh: true},
		{Kind: "mental_model", ID: "p2", Text: "fresh page two", Fresh: true},
	}}
	if _, err := NewLoop(p, retrieval, Config{Question: "q"}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := pinnedTool(p.requests[1]); got != "" {
		t.Fatalf("the turn after fresh pages was forced to %q, want auto", got)
	}
	if !sawGuidance(p.requests[1]) {
		t.Fatal("the fresh-page guidance never reached the model on the next turn")
	}
}

// test-stale-or-empty-page-keeps-forcing: a single stale page, an empty one, or
// no page at all keeps the forced descent going.
func TestUnusablePagesKeepForcing(t *testing.T) {
	cases := map[string][]Evidence{
		"stale among fresh": {
			{Kind: "mental_model", ID: "p1", Text: "fresh", Fresh: true},
			{Kind: "mental_model", ID: "p2", Text: "stale", Fresh: false},
		},
		"fresh but empty": {
			{Kind: "mental_model", ID: "p1", Text: "   ", Fresh: true},
		},
		"no pages": nil,
	}
	for name, models := range cases {
		t.Run(name, func(t *testing.T) {
			p := &scriptedProvider{name: "openai", script: turnScript(
				toolCall(ToolSearchMentalModels, map[string]any{"query": "q"}),
				toolCall(ToolDone, nil),
			)}
			retrieval := &stubRetrieval{models: models}
			if _, err := NewLoop(p, retrieval, Config{Question: "q"}).Run(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := pinnedTool(p.requests[1]); got != ToolSearchObservations {
				t.Fatalf("tool choice after unusable pages = %q, want %q", got, ToolSearchObservations)
			}
		})
	}
}

// test-high-budget-keeps-forcing: the short-circuit is for low and mid effort
// only; a high-budget reflection always walks the full hierarchy.
func TestHighBudgetKeepsForcing(t *testing.T) {
	p := &scriptedProvider{name: "openai", script: turnScript(
		toolCall(ToolSearchMentalModels, map[string]any{"query": "q"}),
		toolCall(ToolDone, nil),
	)}
	retrieval := &stubRetrieval{models: []Evidence{{Kind: "mental_model", ID: "p1", Text: "fresh page", Fresh: true}}}
	if _, err := NewLoop(p, retrieval, Config{Question: "q", Budget: storage.ReflectBudgetHigh}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := pinnedTool(p.requests[1]); got != ToolSearchObservations {
		t.Fatalf("high-budget tool choice after fresh pages = %q, want %q", got, ToolSearchObservations)
	}
}

// test-runner-plumbs-the-budget: the storage-level request's budget reaches the
// loop, so a high-budget reflection keeps the full forced descent.
func TestRunnerPassesBudgetToTheLoop(t *testing.T) {
	model := &scriptedProvider{name: "openai-compatible", script: turnScript(
		toolCall(ToolSearchMentalModels, map[string]any{"query": "q"}),
		toolCall(ToolDone, nil),
	)}
	retrieval := &storageRetrieval{
		models: []storage.ReflectCitation{{ID: "p1", Text: "fresh page", Fresh: true}},
	}
	if _, err := NewRunner(model).Run(context.TODO(), storage.AgenticReflectRequest{
		BankID: "b1", Question: "q", Retrieval: retrieval, Budget: storage.ReflectBudgetHigh,
	}); err != nil {
		t.Fatal(err)
	}
	if got := pinnedTool(model.requests[1]); got != ToolSearchObservations {
		t.Fatalf("high-budget tool choice = %q, want %q", got, ToolSearchObservations)
	}
}

// test-ignored-pin-does-not-advance-the-descent: a provider that ignores or
// downgrades tool_choice and runs a different layer must not advance the forced
// descent. Advancing anyway would abandon every remaining forced layer, so the
// loop keeps the pinned layer and retries it on the next turn instead of
// skipping it.
func TestIgnoredPinDoesNotAdvanceTheDescent(t *testing.T) {
	p := &scriptedProvider{name: "openai-compatible", script: turnScript(
		// Pinned to search_mental_models, but the model runs recall instead.
		toolCall(ToolRecall, map[string]any{"query": "q"}),
		// It now honours the pin, so the descent advances to observations.
		toolCall(ToolSearchMentalModels, map[string]any{"query": "q"}),
		toolCall(ToolSearchObservations, map[string]any{"query": "q"}),
		toolCall(ToolDone, nil),
	)}
	retrieval := &stubRetrieval{
		models: []Evidence{{Kind: "mental_model", ID: "p1", Text: "stale page"}},
		facts:  []Evidence{{Kind: "memory", ID: "m1", Text: "raw fact"}},
	}
	if _, err := NewLoop(p, retrieval, Config{Question: "q"}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{ToolSearchMentalModels, ToolSearchMentalModels, ToolSearchObservations, ToolRecall}
	if len(p.requests) < len(want) {
		t.Fatalf("provider saw %d requests, want at least %d", len(p.requests), len(want))
	}
	for i, name := range want {
		if got := pinnedTool(p.requests[i]); got != name {
			t.Fatalf("turn %d tool choice = %q, want %q", i+1, got, name)
		}
	}
}
