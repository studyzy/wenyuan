package extraction

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/studyzy/wenyuan/internal/provider"
)

// Output-overflow handling and the bank-configured prompt.
//
// A model can stop at its own output cap mid-answer, which truncates the fact
// list. Retrying the same prompt cannot recover the tail, so the text is split
// and each half extracted; the halves' relations are re-based onto the merged
// list. The recursion is bounded so a pathological input cannot fan out.

const (
	// maxOutputSplitDepth bounds how many times an over-long text is halved
	// before the overflow is surfaced. Three levels yield at most eight pieces.
	maxOutputSplitDepth = 3
	// minOutputSplitChars is the floor below which splitting cannot help: if a
	// chunk this small still overflows, halving it only recurses toward a
	// single character, so the overflow is returned instead.
	minOutputSplitChars = 500
)

// isLengthFinish reports whether a stop reason means the model ran out of
// output room rather than finishing naturally.
func isLengthFinish(reason string) bool {
	switch reason {
	case "length", "max_tokens", "max_output_tokens":
		return true
	default:
		return false
	}
}

// complete issues one extraction completion, optionally appending the
// self-correcting exchange when a previous response could not be parsed or used
// the wrong shape. correction is the text of the user turn that carries it;
// an empty previous or correction sends no exchange.
func (e *Extractor) complete(ctx context.Context, text string, opts Options, previous, correction string) (provider.CompletionResult, error) {
	userMessage := provider.Message{Role: provider.RoleUser, Content: buildUserMessage(text, opts)}
	// An item carrying inline images is rebuilt as ordered content parts, so
	// each image sits where its placeholder stood rather than at the end.
	if parts := promptParts(userMessage.Content, opts.Attachments); len(parts) > 0 {
		userMessage.Parts = parts
		userMessage.Content = ""
	}
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: composeExtractionPrompt(opts)},
		userMessage,
	}
	if correction != "" {
		// The correction is a user turn in its own right. A structured answer
		// can leave Content empty, so echoing the previous answer is optional —
		// but the correction must ride along whether or not there is one, or a
		// re-ask would repeat the same prompt and the model would repeat itself.
		if previous != "" {
			messages = append(messages, provider.Message{Role: provider.RoleAssistant, Content: previous})
		}
		messages = append(messages, provider.Message{Role: provider.RoleUser, Content: correction})
	}
	return e.Provider.Complete(ctx, provider.CompletionRequest{
		Messages:       messages,
		ResponseSchema: schemaFor(opts),
		Scope:          e.Scope,
		Strict:         true,
	})
}

// extractModel runs one model extraction for text, retrying by splitting the
// text when the model stopped at its output limit. depth bounds that recursion.
func (e *Extractor) extractModel(ctx context.Context, text string, opts Options, depth int) (Result, error) {
	response, err := e.complete(ctx, text, opts, "", "")
	if err != nil && !provider.IsOutputTooLong(err) {
		return Result{}, fmt.Errorf("fact extraction call failed: %w", err)
	}
	overflowed := provider.IsOutputTooLong(err) || isLengthFinish(response.FinishReason)
	if overflowed {
		if result, ok := e.splitAndExtract(ctx, text, opts, depth, response.Usage); ok {
			return result, nil
		}
		// The text is too small to split usefully: surface the overflow rather
		// than pretend a truncated list was complete.
		if err != nil {
			return Result{}, fmt.Errorf("fact extraction call failed: %w", err)
		}
	}
	facts, parseErr := parseForMode(resolveMode(opts.Mode), response.Structured, opts.Vocabulary)
	if parseErr != nil {
		facts, parseErr = e.repair(ctx, text, response.Content, opts)
		if parseErr != nil {
			return Result{}, parseErr
		}
	} else if len(facts) == 0 && e.MaxCorrectionRetries > 0 && driftWarranted(response.Structured) {
		// The model answered with the right envelope but the wrong fact shape,
		// so nothing was usable. Re-ask with the correction; a bare retry would
		// repeat the same drifted answer because the prompt never changed.
		corrected, correctedResponse, err := e.correctDrift(ctx, text, response, opts)
		if err != nil {
			return Result{}, err
		}
		facts, response = corrected, correctedResponse
	}
	// Verbatim mode stores the chunk's own text, with the model's metadata
	// riding along; the entries collapse to exactly that one fact.
	if resolveMode(opts.Mode) == ModeVerbatim {
		facts = collapseVerbatim(facts, text)
	}
	if len(facts) == 0 {
		// A model that returned nothing usable is treated as having found
		// nothing, not as a failure, so the write still stores the document.
		return Result{Extracted: true, Usage: response.Usage}, nil
	}
	applyDateInference(facts, opts.ReferenceTime)
	dropNonEventDates(facts)
	return Result{Facts: facts, Extracted: true, Usage: response.Usage}, nil
}

// dropNonEventDates clears the occurred dates of a fact the model explicitly
// classified as anything but an event. The pipeline keeps
// occurred_start/occurred_end only for an event; a date on a conversation fact
// is silently discarded, matching the reference. A model that named no fact kind
// is left alone, because inference from the text still applies to it.
func dropNonEventDates(facts []Fact) {
	for index := range facts {
		kind := strings.TrimSpace(facts[index].Kind)
		if kind != "" && !strings.EqualFold(kind, "event") {
			facts[index].OccurredStart = ""
			facts[index].OccurredEnd = ""
		}
	}
}

// splitAndExtract halves an over-long text and extracts each half, merging the
// results. It reports false when the text cannot be usefully split or a half
// failed, so the caller can surface the original overflow instead.
func (e *Extractor) splitAndExtract(ctx context.Context, text string, opts Options, depth int, usage provider.Usage) (Result, bool) {
	if depth >= maxOutputSplitDepth {
		return Result{}, false
	}
	first, second, ok := splitForOutputRetry(text)
	if !ok {
		return Result{}, false
	}
	left, err := e.extractModel(ctx, first, opts, depth+1)
	if err != nil {
		return Result{}, false
	}
	right, err := e.extractModel(ctx, second, opts, depth+1)
	if err != nil {
		return Result{}, false
	}
	return Result{
		Facts:     mergeFacts(left.Facts, right.Facts),
		Extracted: true,
		Usage:     addUsage(usage, left.Usage, right.Usage),
	}, true
}

// mergeFacts concatenates two halves' facts, shifting the second half's causal
// targets past the first half so an edge that pointed within its own half still
// points at the same fact in the merged list.
func mergeFacts(first, second []Fact) []Fact {
	out := make([]Fact, 0, len(first)+len(second))
	out = append(out, first...)
	offset := len(first)
	for _, fact := range second {
		if len(out) >= maxFactsPerItem {
			break
		}
		if len(fact.CausalRelations) > 0 {
			shifted := make([]CausalRelation, 0, len(fact.CausalRelations))
			for _, relation := range fact.CausalRelations {
				shifted = append(shifted, CausalRelation{TargetIndex: relation.TargetIndex + offset})
			}
			fact.CausalRelations = shifted
		}
		out = append(out, fact)
	}
	if len(out) > maxFactsPerItem {
		out = out[:maxFactsPerItem]
	}
	return out
}

// addUsage sums the token accounting of a retried extraction's parts.
func addUsage(base provider.Usage, parts ...provider.Usage) provider.Usage {
	total := base
	for _, part := range parts {
		total.InputTokens += part.InputTokens
		total.OutputTokens += part.OutputTokens
		total.TotalTokens += part.TotalTokens
		total.CachedInputTokens += part.CachedInputTokens
		total.ReasoningTokens += part.ReasoningTokens
	}
	return total
}

// splitForOutputRetry splits text into two halves, preferring a sentence or
// paragraph boundary near the middle so a fact is not cut in two. It reports
// false when the text is below the floor or a non-empty half cannot be formed.
func splitForOutputRetry(text string) (string, string, bool) {
	stripped := strings.TrimSpace(text)
	if utf8.RuneCountInString(stripped) <= minOutputSplitChars {
		return "", "", false
	}
	mid := len(stripped) / 2
	window := len(stripped) / 5
	low := mid - window
	if low < 0 {
		low = 0
	}
	high := mid + window
	if high > len(stripped) {
		high = len(stripped)
	}
	best := mid
	for _, separator := range []string{". ", "! ", "? ", "\n\n"} {
		if index := strings.LastIndex(stripped[low:high], separator); index != -1 {
			best = low + index + len(separator)
			break
		}
	}
	// A byte cut can land inside a multi-byte rune; back up to the rune start.
	for best > 0 && best < len(stripped) && !utf8.RuneStart(stripped[best]) {
		best++
	}
	first := strings.TrimSpace(stripped[:best])
	second := strings.TrimSpace(stripped[best:])
	if first == "" || second == "" || first == stripped || second == stripped {
		return "", "", false
	}
	return first, second, true
}
