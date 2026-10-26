// Package reflect drives a reflection as a model-led retrieval loop.
//
// The loop decides which retrieval tool to call each turn, gathers evidence,
// and answers from what it gathered. It is wired into the service when a model
// is configured; without one the store's deterministic reflection is used
// instead.
package reflect

import (
	"context"
	"errors"
	"strings"
	"time"

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
	loop := NewLoop(r.provider, &retrievalAdapter{
		inner: req.Retrieval, bankID: req.BankID, directives: toDirectives(req.Directives),
		tags: req.Tags, tagsMatch: req.TagsMatch, tagGroups: req.TagGroups,
		observationIncludeEntities: req.SearchObservationsIncludeEntities,
	}, Config{
		MaxIterations:              req.MaxIterations,
		Question:                   req.Question,
		MaxTokens:                  req.MaxTokens,
		MaxCompletionTokens:        req.MaxCompletionTokens,
		Budget:                     req.Budget,
		ObservationMaxTokens:       req.SearchObservationsMaxTokens,
		ObservationIncludeEntities: req.SearchObservationsIncludeEntities,
		ResponseSchema:             req.ResponseSchema,
		BankName:                   req.BankName,
		Mission:                    req.Mission,
		Disposition:                req.Disposition,
		AnswerAsDocument:           req.AnswerAsDocument,
	})
	outcome, err := loop.Run(ctx)
	if err != nil {
		return storage.ReflectOutcome{}, runError(err)
	}
	return r.toStorageOutcome(outcome), nil
}

// runError couples a loop failure to the storage-level sentinel that carries
// its status contract. The sentinel picks the status; the loop's own message
// stays the detail a caller reads, which is what makes a provider mismatch
// ("this model produced no usable tool call") distinguishable from a bank that
// genuinely holds nothing.
func runError(err error) error {
	switch {
	case errors.Is(err, ErrNoModel):
		return &codedError{sentinel: storage.ErrReflectModelUnavailable, detail: err}
	case errors.Is(err, ErrNoToolCall):
		return &codedError{sentinel: storage.ErrReflectToolDriving, detail: err}
	case errors.Is(err, ErrNoAnswer):
		return &codedError{sentinel: storage.ErrReflectNoAnswer, detail: err}
	default:
		return err
	}
}

// codedError is a failure that reports the loop's message while matching a
// storage-level sentinel, so the two halves travel together.
type codedError struct {
	sentinel error
	detail   error
}

func (e *codedError) Error() string { return e.detail.Error() }

// Unwrap reports both halves, so errors.Is finds the sentinel and
// errors.As finds the underlying failure.
func (e *codedError) Unwrap() []error { return []error{e.sentinel, e.detail} }

// toStorageOutcome maps the loop's result onto the storage area's shape,
// attributing each piece of evidence to the citation kind it came from.
func (r *Runner) toStorageOutcome(outcome Outcome) storage.ReflectOutcome {
	converted := storage.ReflectOutcome{
		Answer:                outcome.Answer,
		RawAnswer:             outcome.Answer,
		StructuredOutput:      outcome.StructuredOutput,
		StructuredOutputError: outcome.StructuredOutputError,
		Usage: storage.ReflectUsage{
			InputTokens:  outcome.Usage.InputTokens,
			OutputTokens: outcome.Usage.OutputTokens,
			TotalTokens:  outcome.Usage.TotalTokens,
		},
	}
	for _, evidence := range outcome.Evidence {
		// The answer text is not a citation, so only retrieved evidence is
		// reported.
		if evidence.Kind == "answer" {
			continue
		}
		if evidence.Kind == "mental_model" {
			// A perspective is cited as a mental model, with its full text, so a
			// caller sees what the answer was read from rather than a snippet.
			if evidence.ID == "" {
				continue
			}
			converted.MentalModels = append(converted.MentalModels, storage.ReflectMentalModelCitation{
				ID:   evidence.ID,
				Text: evidence.Text,
			})
			continue
		}
		// The citation reports the memory's own fact type (world, experience,
		// observation) when it has one, falling back to the retrieval layer.
		factType := evidence.Type
		if factType == "" {
			factType = evidence.Kind
		}
		converted.Facts = append(converted.Facts, storage.ReflectCitation{
			ID:            evidence.ID,
			Text:          evidence.Text,
			Type:          factType,
			Context:       "",
			OccurredStart: evidence.OccurredStart,
			OccurredEnd:   evidence.OccurredEnd,
			MentionedAt:   evidence.MentionedAt,
			DocumentID:    evidence.DocumentID,
			ChunkID:       evidence.ChunkID,
			Metadata:      evidence.Metadata,
			Tags:          evidence.Tags,
			Attachments:   evidence.Attachments,
		})
	}
	converted.Trace = buildTrace(outcome)
	// The reference's trace carries one `llm_calls` entry per model call the run
	// made — the loop turns, the final synthesis and each auxiliary reshape —
	// with the scope that names it and the milliseconds it took
	// (ReflectLLMCall in engine/memory_engine.py's reflect result). Reporting an
	// empty list for a run that made model calls reads as "no model was used".
	for _, call := range outcome.LLMCalls {
		converted.Trace.LLMCalls = append(converted.Trace.LLMCalls, storage.ReflectModelCall{
			Scope:      call.Scope,
			DurationMs: call.DurationMs,
		})
	}
	return converted
}

// traceInput is a tool call's arguments as the trace reports them: the model's
// own arguments under a `tool` member naming the tool that was called, which is
// the shape the reference's trace entries carry.
func traceInput(call ToolCall) map[string]any {
	input := make(map[string]any, len(call.Input)+1)
	input["tool"] = call.Name
	for key, value := range call.Input {
		input[key] = value
	}
	return input
}

// buildTrace records each tool call the loop ran, with the result it returned,
// so a caller can audit the retrieval behind an answer. The output shape
// follows the reference's tools: a recall call returns `memories`, an
// observation call `observations`, a perspective call `mental_models` — each
// entry carrying the document stamp and temporal fields the model was shown.
func buildTrace(outcome Outcome) *storage.ReflectTrace {
	trace := &storage.ReflectTrace{}
	for _, turn := range outcome.Turns {
		for index, call := range turn.Calls {
			// `done` retrieves nothing: it ends the run and carries the answer,
			// so it is not a retrieval entry in the trace. The reference's tool
			// trace lists only the tools that gathered evidence.
			if call.Name == ToolDone {
				continue
			}
			entry := storage.ReflectToolCall{
				Tool:      call.Name,
				Input:     traceInput(call),
				Iteration: turn.Iteration,
				HasOutput: true,
				ID:        call.ID,
			}
			var batch []Evidence
			if index < len(turn.Batches) {
				batch = turn.Batches[index]
			}
			entry.Output = toolOutput(call, batch)
			trace.ToolCalls = append(trace.ToolCalls, entry)
		}
	}
	return trace
}

// toolOutput renders one tool call's result payload. A call that retrieves
// nothing still reports an empty list, so "found nothing" is distinguishable
// from "never ran".
func toolOutput(call ToolCall, batch []Evidence) map[string]any {
	query, _ := call.Input["query"].(string)
	switch call.Name {
	case ToolRecall:
		return map[string]any{"query": query, "memories": evidencePayloads(batch, "memory")}
	case ToolSearchObservations:
		return map[string]any{"query": query, "observations": evidencePayloads(batch, "observation")}
	case ToolSearchMentalModels:
		// The best-ranked page comes back whole; the rest as snippets, so a
		// second page costs a snippet rather than a whole document on every
		// later turn of the loop. The model reads what it wants with
		// read_mental_models.
		return map[string]any{"mental_models": mentalModelSearchPayloads(batch)}
	case ToolReadMentalModels:
		// A read is the way the model gets a page in full, so every entry
		// carries its content.
		return map[string]any{"mental_models": mentalModelReadPayloads(batch)}
	case ToolExpand:
		// Each item keys off `memory_id` so the expand result is recognisable
		// as such, with the source text the depth resolved to. An item whose
		// document is outside the reader's scope carries no document id, so the
		// withheld source is visible as an omission rather than as content.
		items := make([]map[string]any, 0, len(batch))
		for _, item := range batch {
			entry := map[string]any{"memory_id": item.ID, "text": item.Text}
			if item.DocumentID != "" {
				entry["document_id"] = item.DocumentID
			}
			if item.ChunkID != "" {
				entry["chunk_id"] = item.ChunkID
			}
			items = append(items, entry)
		}
		return map[string]any{"results": items, "count": len(items)}
	default:
		return map[string]any{"count": len(batch)}
	}
}

// evidencePayloads renders the retrieved evidence as tool-result objects,
// dropping empty fields so a bank that stamps nothing sends no metadata key.
func evidencePayloads(batch []Evidence, kind string) []map[string]any {
	out := make([]map[string]any, 0, len(batch))
	for _, item := range batch {
		if kind != "" && item.Kind != kind && item.Kind != "answer" && item.Kind != "document" {
			continue
		}
		payload := map[string]any{"text": item.Text}
		if item.ID != "" {
			payload["id"] = item.ID
		}
		if item.DocumentID != "" {
			payload["document_id"] = item.DocumentID
		}
		if item.ChunkID != "" {
			payload["chunk_id"] = item.ChunkID
		}
		if len(item.Metadata) > 0 {
			payload["metadata"] = item.Metadata
		}
		if len(item.Tags) > 0 {
			payload["tags"] = item.Tags
		}
		if len(item.Entities) > 0 {
			payload["entities"] = item.Entities
		}
		if item.OccurredStart != nil {
			payload["occurred_start"] = item.OccurredStart.UTC().Format(time.RFC3339)
		}
		if item.OccurredEnd != nil {
			payload["occurred_end"] = item.OccurredEnd.UTC().Format(time.RFC3339)
		}
		if item.MentionedAt != nil {
			payload["mentioned_at"] = item.MentionedAt.UTC().Format(time.RFC3339)
		}
		if len(item.Attachments) > 0 {
			attachments := make([]map[string]any, 0, len(item.Attachments))
			for _, attachment := range item.Attachments {
				attachments = append(attachments, map[string]any{
					"id": attachment.ID, "kind": attachment.Kind, "uri": attachment.URI,
				})
			}
			payload["attachments"] = attachments
		}
		out = append(out, payload)
	}
	return out
}

// snippetChars is the length a search result's snippet is capped at, matching
// the knowledge-page search this mirrors.
const snippetChars = 280

// mentalModelSearchPayloads renders a search result: the best-ranked page in
// full, the rest as snippets carrying their size so the model can judge what
// reading one would cost.
func mentalModelSearchPayloads(batch []Evidence) []map[string]any {
	pages := mentalModelEvidence(batch)
	out := make([]map[string]any, 0, len(pages))
	for index, item := range pages {
		payload := map[string]any{"id": item.ID}
		if index == 0 {
			payload["content"] = item.Text
		} else {
			payload["snippet"] = knowledgeSnippet(item.Text)
			payload["content_chars"] = len(item.Text)
		}
		addMentalModelFields(payload, item)
		out = append(out, payload)
	}
	return out
}

// mentalModelReadPayloads renders a read result: every requested page in full.
func mentalModelReadPayloads(batch []Evidence) []map[string]any {
	pages := mentalModelEvidence(batch)
	out := make([]map[string]any, 0, len(pages))
	for _, item := range pages {
		payload := map[string]any{"id": item.ID, "content": item.Text}
		addMentalModelFields(payload, item)
		out = append(out, payload)
	}
	return out
}

// mentalModelEvidence keeps only the perspective evidence, preserving rank
// order.
func mentalModelEvidence(batch []Evidence) []Evidence {
	out := make([]Evidence, 0, len(batch))
	for _, item := range batch {
		if item.Kind == "mental_model" {
			out = append(out, item)
		}
	}
	return out
}

// addMentalModelFields attaches the optional stamp fields a page may carry.
func addMentalModelFields(payload map[string]any, item Evidence) {
	if len(item.Tags) > 0 {
		payload["tags"] = item.Tags
	}
	if item.MentionedAt != nil {
		payload["updated_at"] = item.MentionedAt.UTC().Format(time.RFC3339)
	}
}

// knowledgeSnippet is what a search result shows for a page's opening text,
// including when the page has no body: an empty body is a real state a searcher
// has to act on, and the empty string does not convey it.
func knowledgeSnippet(content string) string {
	if strings.TrimSpace(content) == "" {
		return "No content yet."
	}
	runes := []rune(content)
	if len(runes) > snippetChars {
		runes = runes[:snippetChars]
	}
	return strings.TrimSpace(string(runes))
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
	directives []Directive
	// tags, tagsMatch and tagGroups are the reader's tag scope, so expand can
	// confine the source text it returns to documents the reader may see.
	tags      []string
	tagsMatch string
	tagGroups []storage.TagGroup
	// observationIncludeEntities carries the caller's resolved switch: the
	// reference attaches resolved entity names to each observation by default.
	observationIncludeEntities bool
}

// ReflectMentalModels returns perspectives matching a query.
func (a *retrievalAdapter) ReflectMentalModels(ctx context.Context, query string, maxResults int) ([]Evidence, error) {
	citations, err := a.inner.ReflectMentalModels(ctx, a.bankID, query, maxResults)
	return toEvidence("mental_model", citations), err
}

// ReflectObservations returns consolidated conclusions matching a query.
func (a *retrievalAdapter) ReflectObservations(ctx context.Context, query string, maxTokens int) ([]Evidence, error) {
	citations, err := a.inner.ReflectObservations(ctx, a.bankID, query, maxTokens)
	evidence := toEvidence("observation", citations)
	if !a.observationIncludeEntities {
		for index := range evidence {
			evidence[index].Entities = nil
		}
	}
	return evidence, err
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

// ReflectExpand returns more context for specific memories, opened to the
// requested depth and confined to the reader's tag scope.
func (a *retrievalAdapter) ReflectExpand(ctx context.Context, ids []string, depth string) ([]Evidence, error) {
	citations, err := a.inner.ReflectExpand(ctx, storage.ReflectExpandRequest{
		BankID: a.bankID, IDs: ids, Depth: depth,
		Tags: a.tags, TagsMatch: a.tagsMatch, TagGroups: a.tagGroups,
	})
	return toEvidence("memory", citations), err
}

// Directives returns the standing instructions that apply to this request.
func (a *retrievalAdapter) Directives(context.Context) []Directive {
	directives := make([]Directive, 0, len(a.directives))
	for _, directive := range a.directives {
		if strings.TrimSpace(directive.Content) != "" {
			directives = append(directives, directive)
		}
	}
	return directives
}

// toDirectives maps the store's directive candidates onto the loop's rendering
// shape, keeping the name the prompt bolds ahead of the content.
func toDirectives(directives []storage.ReflectDirective) []Directive {
	out := make([]Directive, 0, len(directives))
	for _, directive := range directives {
		out = append(out, Directive{Name: directive.Name, Content: directive.Content})
	}
	return out
}

// toEvidence maps stored citations onto the loop's evidence shape, tagging each
// with the kind of retrieval that produced it.
func toEvidence(kind string, citations []storage.ReflectCitation) []Evidence {
	out := make([]Evidence, 0, len(citations))
	for _, citation := range citations {
		out = append(out, Evidence{
			Kind:          kind,
			Type:          citation.Type,
			ID:            citation.ID,
			Text:          citation.Text,
			DocumentID:    citation.DocumentID,
			ChunkID:       citation.ChunkID,
			Metadata:      citation.Metadata,
			Tags:          citation.Tags,
			Entities:      citation.Entities,
			OccurredStart: citation.OccurredStart,
			OccurredEnd:   citation.OccurredEnd,
			MentionedAt:   citation.MentionedAt,
			Attachments:   citation.Attachments,
			Fresh:         citation.Fresh,
		})
	}
	return out
}
