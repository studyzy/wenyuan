package memoryops

import (
	"strings"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// neutralPlaceholder replaces a full identifier the model was never shown, so a
// fabricated citation is neutralized rather than returned as a real one.
const neutralPlaceholder = "[redacted]"

// buildReflectResponse maps the storage outcome into the public response
// envelope, applying each request gate and the post-generation shaping.
func buildReflectResponse(req ReflectRequest, outcome storage.ReflectOutcome) (ReflectResponse, error) {
	response := ReflectResponse{
		Text: resolveAnswerIdentifiers(req, outcome),
	}
	if req.Include != nil && req.Include.Facts != nil {
		response.BasedOn = buildCitedEvidence(outcome)
	}
	if req.Include != nil && req.Include.ToolCalls != nil {
		response.Trace = buildTrace(outcome, toolOutputEnabled(req.Include.ToolCalls.Output))
	}
	usage := ReflectUsage{
		InputTokens:    outcome.Usage.InputTokens,
		OutputTokens:   outcome.Usage.OutputTokens,
		TotalTokens:    outcome.Usage.TotalTokens,
		CachedTokens:   outcome.Usage.CachedTokens,
		ThoughtsTokens: outcome.Usage.ThoughtsTokens,
	}
	response.Usage = &usage
	if req.ResponseSchema != nil {
		applyStructuredOutput(&response, req.ResponseSchema, outcome)
	}
	return response, nil
}

// buildCitedEvidence assembles the cited-evidence object. It is always an
// object with the three named collections, each a non-nil array, even when the
// answer used nothing.
func buildCitedEvidence(outcome storage.ReflectOutcome) *CitedEvidence {
	evidence := &CitedEvidence{
		Memories:     make([]ReflectCitedFact, 0, len(outcome.Facts)),
		MentalModels: make([]ReflectCitedMentalModel, 0, len(outcome.MentalModels)),
		Directives:   make([]ReflectCitedDirective, 0, len(outcome.Directives)),
	}
	for _, f := range outcome.Facts {
		attachments := toAttachments(f.Attachments)
		evidence.Memories = append(evidence.Memories, ReflectCitedFact{
			ID:            f.ID,
			Text:          f.Text,
			Type:          nullIfEmptyString(f.Type),
			Context:       nullIfEmptyString(f.Context),
			OccurredStart: httpx.FormatTimestampPtr(f.OccurredStart),
			OccurredEnd:   httpx.FormatTimestampPtr(f.OccurredEnd),
			MentionedAt:   httpx.FormatTimestampPtr(f.MentionedAt),
			DocumentID:    nullIfEmptyString(f.DocumentID),
			ChunkID:       nullIfEmptyString(f.ChunkID),
			Metadata:      nonNilMap(f.Metadata),
			Tags:          nonEmptyStrings(f.Tags),
			Attachments:   attachments,
		})
	}
	for _, m := range outcome.MentalModels {
		evidence.MentalModels = append(evidence.MentalModels, ReflectCitedMentalModel{ID: m.ID, Text: m.Text, Context: m.Context})
	}
	for _, d := range outcome.Directives {
		evidence.Directives = append(evidence.Directives, ReflectCitedDirective{ID: d.ID, Name: d.Name, Content: d.Content})
	}
	return evidence
}

// toolOutputEnabled reports whether each trace tool entry carries its output
// payload. A nil flag is "not false", so the payload is carried.
func toolOutputEnabled(output *bool) bool {
	return output == nil || *output
}

// buildTrace assembles the execution trace, carrying each tool entry's output
// payload only when enabled.
func buildTrace(outcome storage.ReflectOutcome, includeOutput bool) *ReflectTrace {
	trace := &ReflectTrace{
		ToolCalls: make([]ReflectToolCall, 0),
		LLMCalls:  make([]ReflectModelCall, 0),
	}
	if outcome.Trace == nil {
		return trace
	}
	for _, call := range outcome.Trace.ToolCalls {
		entry := ReflectToolCall{
			Tool:       call.Tool,
			Input:      call.Input,
			DurationMs: call.DurationMs,
			Iteration:  call.Iteration,
		}
		if includeOutput && call.HasOutput {
			entry.Output = call.Output
		}
		trace.ToolCalls = append(trace.ToolCalls, entry)
	}
	for _, call := range outcome.Trace.LLMCalls {
		trace.LLMCalls = append(trace.LLMCalls, ReflectModelCall{Scope: call.Scope, DurationMs: call.DurationMs})
	}
	return trace
}

// resolveAnswerIdentifiers replaces a full identifier the model was never shown
// with a neutral placeholder. Real identifiers the loop exposed are kept.
func resolveAnswerIdentifiers(req ReflectRequest, outcome storage.ReflectOutcome) string {
	answer := outcome.Answer
	known := map[string]bool{}
	for _, f := range outcome.Facts {
		known[f.ID] = true
	}
	for _, m := range outcome.MentalModels {
		known[m.ID] = true
	}
	// A full identifier the loop never exposed is masked. Only identifiers that
	// look like opaque tokens (no whitespace) are considered candidates.
	for _, token := range strings.Fields(answer) {
		if !looksLikeIdentifier(token) {
			continue
		}
		if !known[token] {
			answer = strings.ReplaceAll(answer, token, neutralPlaceholder)
		}
	}
	return answer
}

// looksLikeIdentifier reports whether a token is plausibly a full memory
// identifier: a long opaque alphanumeric run carrying a separating character.
func looksLikeIdentifier(token string) bool {
	if len(token) < 8 {
		return false
	}
	hasSeparator := false
	hasLetter := false
	for _, r := range token {
		switch {
		case r == '-' || r == '_' || r == ':':
			hasSeparator = true
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			hasLetter = true
		}
	}
	return hasSeparator && hasLetter
}

// applyStructuredOutput records the structured-output object for the answer and
// the reason it is missing, if it is.
//
// The reshape is the model's job: the reflection loop makes a second call that
// reads the answer back into the caller's schema, and reports what it got.
// Without a model the reshape has no call to make, so the answer is parsed
// directly — that path can only recover an answer that already is a JSON
// document, which is exactly what a deployment with no model can offer.
func applyStructuredOutput(response *ReflectResponse, schema map[string]any, outcome storage.ReflectOutcome) {
	if outcome.StructuredOutput != nil || outcome.StructuredOutputError != "" {
		response.StructuredOutput = outcome.StructuredOutput
		response.StructuredOutputError = nullIfEmptyString(outcome.StructuredOutputError)
		return
	}
	parsed, err := parseStructuredOutput(schema, outcome.Answer)
	if err != nil {
		message := err.Error()
		response.StructuredOutputError = &message
		return
	}
	response.StructuredOutput = parsed
}
