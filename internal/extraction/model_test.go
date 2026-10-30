package extraction

import (
	"context"
	"errors"
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

// recordingProvider answers each call with the next canned result and keeps
// every request, so a test can see what the correction turn carried.
type recordingProvider struct {
	results []provider.CompletionResult
	calls   []provider.CompletionRequest
}

func (r *recordingProvider) Name() string { return "openai" }

func (r *recordingProvider) Complete(_ context.Context, req provider.CompletionRequest) (provider.CompletionResult, error) {
	r.calls = append(r.calls, req)
	if len(r.calls) > len(r.results) {
		return provider.CompletionResult{}, errors.New("unexpected extra call")
	}
	return r.results[len(r.calls)-1], nil
}

func (r *recordingProvider) Embed(context.Context, provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	return provider.EmbeddingResult{}, nil
}
func (r *recordingProvider) VerifyConnection(context.Context) error { return nil }
func (r *recordingProvider) Dimensions() int                        { return provider.EmbeddingDimensions }

// A reply whose top-level object carries no `facts` list is a broken envelope,
// not an empty extraction. Reading it as "no facts" would commit the document
// with zero memories and report success, so the extractor re-asks once with the
// envelope correction and then fails the chunk (reference #5582,
// fact_extraction.py:2014-2030, :2175-2196).
func TestReplyWithoutFactsListFailsTheChunk(t *testing.T) {
	envelope := provider.CompletionResult{Structured: map[string]any{"summary": "nothing to see"}}
	stub := &recordingProvider{results: []provider.CompletionResult{envelope, envelope, envelope}}
	extractor := New(stub)
	// The reference's delete-and-retry budget is the retain LLM's max-retries.
	extractor.MaxCorrectionRetries = 1
	_, err := extractor.Extract(context.Background(), "Some text to extract from.")
	if err == nil {
		t.Fatal("a reply without a facts list must fail the chunk, not report zero facts")
	}
	if !strings.Contains(err.Error(), "no top-level \"facts\"") {
		t.Fatalf("the failure must name the broken envelope, got %v", err)
	}
	if len(stub.calls) != 2 {
		t.Fatalf("calls = %d, want the original ask plus one envelope correction", len(stub.calls))
	}
	last := stub.calls[1]
	correction := last.Messages[len(last.Messages)-1]
	if correction.Role != provider.RoleUser || !strings.Contains(correction.Content, `top-level "facts"
array`) {
		t.Fatalf("the retry must carry the envelope correction, got %+v", correction)
	}
}

// A corrected reply is used, so one bad envelope does not fail a chunk the
// model can answer properly when asked again.
func TestEnvelopeCorrectionRecovers(t *testing.T) {
	stub := &recordingProvider{results: []provider.CompletionResult{
		{Structured: map[string]any{"result": map[string]any{}}},
		{Structured: map[string]any{"facts": []any{map[string]any{"text": "Alice works at Google."}}}},
	}}
	extractor := New(stub)
	extractor.MaxCorrectionRetries = 1
	result, err := extractor.Extract(context.Background(), "Alice works at Google.")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Facts) != 1 || result.Facts[0].Text != "Alice works at Google." {
		t.Fatalf("result = %+v", result)
	}
}

// The reference also accepts a bare top-level array of fact objects, which it
// wraps as {"facts": [...]}.
func TestTopLevelFactArrayIsAccepted(t *testing.T) {
	stub := &recordingProvider{results: []provider.CompletionResult{
		{Structured: []any{map[string]any{"text": "Alice works at Google."}}},
	}}
	result, err := New(stub).Extract(context.Background(), "Alice works at Google.")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("result = %+v", result)
	}
}
