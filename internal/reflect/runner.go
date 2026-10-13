// Package reflect drives a reflection as a model-led retrieval loop.
//
// The loop decides which retrieval tool to call each turn, gathers evidence,
// and answers from what it gathered. It is wired into the service when a model
// is configured; without one the store's deterministic reflection is used
// instead.
package reflect

import (
	"context"
	"strings"

	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
)

// Runner performs a reflection over the retrieval the store exposes.
//
// It is the adapter between two areas that must not know each other: storage
// declares the retrieval and outcome shapes, this package owns the loop and the
// provider call, and the adapter below is the only place the two meet.
type Runner struct {
	provider provider.Provider
	// Scope labels the provider calls for tracing.
	Scope string
}

// NewRunner builds a runner over the model boundary.
func NewRunner(p provider.Provider) *Runner {
	return &Runner{provider: p, Scope: "reflect"}
}

// Run performs the reflection and reports the answer with the evidence it used.
func (r *Runner) Run(ctx context.Context, req storage.AgenticReflectRequest) (storage.ReflectOutcome, error) {
	loop := NewLoop(r.provider, &retrievalAdapter{inner: req.Retrieval, bankID: req.BankID, directives: req.Directives}, Config{
		MaxIterations: req.MaxIterations,
		Question:      req.Question,
		MaxTokens:     req.MaxTokens,
	})
	outcome, err := loop.Run(ctx)
	if err != nil {
		return storage.ReflectOutcome{}, err
	}
	return r.toStorageOutcome(outcome), nil
}

// toStorageOutcome maps the loop's result onto the storage area's shape,
// attributing each piece of evidence to the citation kind it came from.
func (r *Runner) toStorageOutcome(outcome Outcome) storage.ReflectOutcome {
	converted := storage.ReflectOutcome{
		Answer:    outcome.Answer,
		RawAnswer: outcome.Answer,
		Usage: storage.ReflectUsage{
			InputTokens:  outcome.Usage.InputTokens,
			OutputTokens: outcome.Usage.OutputTokens,
			TotalTokens:  outcome.Usage.TotalTokens,
		},
	}
	for _, evidence := range outcome.Evidence {
		// The answer text is not a citation, so only retrieved evidence is
		// reported as facts.
		if evidence.Kind == "answer" {
			continue
		}
		converted.Facts = append(converted.Facts, storage.ReflectCitation{
			ID:         evidence.ID,
			Text:       evidence.Text,
			Type:       evidence.Kind,
			DocumentID: evidence.DocumentID,
			ChunkID:    evidence.ChunkID,
		})
	}
	return converted
}

// retrievalAdapter narrows the store's reflection retrieval onto the loop's
// shape.
//
// The store's methods take a bank identifier because they run against a
// database; the loop's do not, because the loop serves one bank and should not
// have to carry it through every call. Binding the bank here is what keeps the
// loop unaware of it.
type retrievalAdapter struct {
	inner      storage.ReflectionRetrieval
	bankID     string
	directives []string
}

// ReflectMentalModels returns perspectives matching a query.
func (a *retrievalAdapter) ReflectMentalModels(ctx context.Context, query string, maxResults int) ([]Evidence, error) {
	citations, err := a.inner.ReflectMentalModels(ctx, a.bankID, query, maxResults)
	return toEvidence("mental_model", citations), err
}

// ReflectObservations returns consolidated conclusions matching a query.
func (a *retrievalAdapter) ReflectObservations(ctx context.Context, query string, maxTokens int) ([]Evidence, error) {
	citations, err := a.inner.ReflectObservations(ctx, a.bankID, query, maxTokens)
	return toEvidence("observation", citations), err
}

// ReflectMemories returns raw memories matching a query.
func (a *retrievalAdapter) ReflectMemories(ctx context.Context, query string, maxTokens int) ([]Evidence, error) {
	citations, err := a.inner.ReflectMemories(ctx, a.bankID, query, maxTokens)
	return toEvidence("memory", citations), err
}

// ReflectReadModels returns specific perspectives by identifier.
func (a *retrievalAdapter) ReflectReadModels(ctx context.Context, ids []string) ([]Evidence, error) {
	citations, err := a.inner.ReflectReadModels(ctx, a.bankID, ids)
	return toEvidence("mental_model", citations), err
}

// ReflectExpand returns more context for specific memories.
func (a *retrievalAdapter) ReflectExpand(ctx context.Context, ids []string) ([]Evidence, error) {
	citations, err := a.inner.ReflectExpand(ctx, a.bankID, ids)
	return toEvidence("memory", citations), err
}

// Directives returns the standing instructions that apply to this request.
func (a *retrievalAdapter) Directives(context.Context) []string {
	directives := make([]string, 0, len(a.directives))
	for _, directive := range a.directives {
		if trimmed := strings.TrimSpace(directive); trimmed != "" {
			directives = append(directives, trimmed)
		}
	}
	return directives
}

// toEvidence maps stored citations onto the loop's evidence shape, tagging each
// with the kind of retrieval that produced it.
func toEvidence(kind string, citations []storage.ReflectCitation) []Evidence {
	out := make([]Evidence, 0, len(citations))
	for _, citation := range citations {
		out = append(out, Evidence{
			Kind:       kind,
			ID:         citation.ID,
			Text:       citation.Text,
			DocumentID: citation.DocumentID,
			ChunkID:    citation.ChunkID,
		})
	}
	return out
}
