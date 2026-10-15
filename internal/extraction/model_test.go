package extraction

import (
	"context"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/provider"
)

// overflowProvider stops at its output limit on the first call, then answers
// each subsequent call with one fact naming the text it received, so a test can
// see how many pieces the retry produced.
type overflowProvider struct {
	calls      int
	alwaysStop bool
}

func (o *overflowProvider) Name() string { return "openai" }

func (o *overflowProvider) Complete(_ context.Context, req provider.CompletionRequest) (provider.CompletionResult, error) {
	o.calls++
	result := provider.CompletionResult{FinishReason: "length", Usage: provider.Usage{OutputTokens: 10}}
	if o.calls == 1 || o.alwaysStop {
		return result, &provider.OutputTooLongError{FinishReason: "length"}
	}
	// On the retry, return one fact per call, tagged with a word from the text
	// so the merged list can be inspected.
	text := req.Messages[1].Content
	return provider.CompletionResult{
		Structured: map[string]any{"facts": []any{
			map[string]any{"text": "fact about " + strings.Fields(text)[0]},
		}},
		Usage: provider.Usage{OutputTokens: 5},
	}, nil
}

func (o *overflowProvider) Embed(context.Context, provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	return provider.EmbeddingResult{}, nil
}
func (o *overflowProvider) VerifyConnection(context.Context) error { return nil }
func (o *overflowProvider) Dimensions() int                        { return provider.EmbeddingDimensions }

// A response cut off by the output limit is split and re-extracted, so the
// missing tail is recovered instead of silently dropped.
func TestOutputOverflowRetriesBySplitting(t *testing.T) {
	stub := &overflowProvider{}
	text := strings.Repeat("This is a sentence about the system. ", 20)
	result, err := New(stub).Extract(context.Background(), text)
	if err != nil {
		t.Fatal(err)
	}
	if stub.calls != 3 {
		t.Fatalf("calls = %d, want one overflow plus two halves", stub.calls)
	}
	if !result.Extracted || len(result.Facts) != 2 {
		t.Fatalf("result = %+v", result)
	}
	if result.Usage.OutputTokens != 20 {
		t.Fatalf("usage not summed across the retry: %+v", result.Usage)
	}
}

// A finish reason of length with no error is also treated as an overflow, which
// is how a provider that does not raise the typed error reports it.
func TestLengthFinishReasonIsAlsoRetried(t *testing.T) {
	stub := &lengthOnlyProvider{}
	text := strings.Repeat("This is a sentence about the system. ", 20)
	result, err := New(stub).Extract(context.Background(), text)
	if err != nil {
		t.Fatal(err)
	}
	if stub.calls != 3 || len(result.Facts) != 2 {
		t.Fatalf("calls = %d, facts = %d", stub.calls, len(result.Facts))
	}
}

// lengthOnlyProvider reports the length stop reason without an error.
type lengthOnlyProvider struct {
	calls int
}

func (o *lengthOnlyProvider) Name() string { return "openai" }

func (o *lengthOnlyProvider) Complete(_ context.Context, req provider.CompletionRequest) (provider.CompletionResult, error) {
	o.calls++
	if o.calls == 1 {
		return provider.CompletionResult{FinishReason: "max_tokens"}, nil
	}
	return provider.CompletionResult{Structured: map[string]any{"facts": []any{
		map[string]any{"text": "a fact"},
	}}}, nil
}

func (o *lengthOnlyProvider) Embed(context.Context, provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	return provider.EmbeddingResult{}, nil
}
func (o *lengthOnlyProvider) VerifyConnection(context.Context) error { return nil }
func (o *lengthOnlyProvider) Dimensions() int                        { return provider.EmbeddingDimensions }

// A text too small to split usefully surfaces the overflow rather than
// recursing toward a single character.
func TestOutputOverflowBelowFloorSurfacesError(t *testing.T) {
	stub := &overflowProvider{alwaysStop: true}
	if _, err := New(stub).Extract(context.Background(), "A short fact."); err == nil {
		t.Fatal("an unsplittable overflow must be returned")
	}
	if stub.calls != 1 {
		t.Fatalf("calls = %d, want no retry for an unsplittable text", stub.calls)
	}
}
