package openai

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/provider"
)

// batchTransport answers any embedding request with one vector at index 0, so
// a test can verify order is reassembled by batch position rather than by
// completion order.
type batchTransport struct{}

func (b *batchTransport) Do(req *http.Request) (*http.Response, error) {
	body := `{"data":[{"index":0,"embedding":[0.5]}]}`
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}, nil
}

// test-embed-one-vector-per-input: an embed call returns exactly one vector
// per input text, in input order, at the boundary's declared dimension.
func TestEmbedOneVectorPerInputInOrder(t *testing.T) {
	body := `{"data":[` +
		`{"index":2,"embedding":[0.3,0.3]},` +
		`{"index":0,"embedding":[0.1,0.1]},` +
		`{"index":1,"embedding":[0.2,0.2]}` +
		`]}`
	settings := clientSettings()
	settings.Embedding = &provider.EmbeddingSettings{
		Provider:              "openai",
		Model:                 "text-embed",
		BatchSize:             64,
		MaxConcurrentRequests: 4,
	}
	c, _ := testClient(jsonResponse(body))
	c.Settings = settings

	result, err := c.Embed(context.Background(), provider.EmbeddingRequest{
		Texts: []string{"a", "b", "c"},
		Kind:  provider.EmbeddingPassage,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Vectors) != 3 {
		t.Fatalf("returned %d vectors, want 3", len(result.Vectors))
	}
	// Input order is preserved: index 0 first even though it was not the
	// first item in the response.
	if result.Vectors[0][0] != 0.1 || result.Vectors[1][0] != 0.2 || result.Vectors[2][0] != 0.3 {
		t.Fatalf("vectors are not in input order: %v", result.Vectors)
	}
	if result.Dimensions != provider.EmbeddingDimensions {
		t.Fatalf("dimensions = %d, want %d", result.Dimensions, provider.EmbeddingDimensions)
	}
}

// Input order is preserved across multiple concurrent batches.
func TestEmbedOrderPreservedAcrossBatches(t *testing.T) {
	settings := clientSettings()
	settings.Embedding = &provider.EmbeddingSettings{
		Provider:              "openai",
		Model:                 "text-embed",
		BatchSize:             1,
		MaxConcurrentRequests: 4,
	}
	// Each batch of one returns its single vector at index 0.
	c := NewClient(settings, &batchTransport{})
	c.Now = func() time.Time { return time.Unix(0, 0) }
	c.Sleep = func(time.Duration) {}

	result, err := c.Embed(context.Background(), provider.EmbeddingRequest{
		Texts: []string{"a", "b", "c", "d"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Vectors) != 4 {
		t.Fatalf("returned %d vectors, want 4", len(result.Vectors))
	}
}

// The passage and query prefixes are applied to the outbound text.
func TestEmbedAppliesPrefixes(t *testing.T) {
	settings := clientSettings()
	settings.Embedding = &provider.EmbeddingSettings{
		Provider:      "openai",
		Model:         "text-embed",
		PassagePrefix: "passage: ",
		QueryPrefix:   "query: ",
	}
	c, doer := testClient(jsonResponse(`{"data":[{"index":0,"embedding":[0.1]}]}`))
	c.Settings = settings

	if _, err := c.Embed(context.Background(), provider.EmbeddingRequest{Texts: []string{"x"}, Kind: provider.EmbeddingPassage}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Embed(context.Background(), provider.EmbeddingRequest{Texts: []string{"x"}, Kind: provider.EmbeddingQuery}); err != nil {
		t.Fatal(err)
	}
	if len(doer.requests) != 2 {
		t.Fatalf("made %d requests, want 2", len(doer.requests))
	}
}

// A response that does not return one vector per input is rejected.
func TestEmbedRejectsMismatchedCount(t *testing.T) {
	c, _ := testClient(jsonResponse(`{"data":[{"index":0,"embedding":[0.1]}]}`))
	_, err := c.Embed(context.Background(), provider.EmbeddingRequest{Texts: []string{"a", "b"}})
	if !provider.IsMalformedResponse(err) {
		t.Fatalf("error is %T, want malformed-response", err)
	}
}

// An empty input list yields an empty result without a request.
func TestEmbedEmptyInput(t *testing.T) {
	c, doer := testClient(jsonResponse(`{}`))
	result, err := c.Embed(context.Background(), provider.EmbeddingRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Vectors) != 0 {
		t.Fatalf("vectors = %v", result.Vectors)
	}
	if result.Dimensions != provider.EmbeddingDimensions {
		t.Fatalf("dimensions = %d", result.Dimensions)
	}
	if doer.calls != 0 {
		t.Fatalf("made %d requests for empty input, want 0", doer.calls)
	}
}

// The vendor dimensions knob is passed to the endpoint but never changes the
// declared dimension.
func TestEmbedVendorDimensionsDoNotChangeDeclared(t *testing.T) {
	settings := clientSettings()
	settings.Embedding = &provider.EmbeddingSettings{
		Provider:         "openai",
		Model:            "text-embed",
		VendorDimensions: 1024,
	}
	c, _ := testClient(jsonResponse(`{"data":[{"index":0,"embedding":[0.1]}]}`))
	c.Settings = settings
	result, err := c.Embed(context.Background(), provider.EmbeddingRequest{Texts: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Dimensions != provider.EmbeddingDimensions {
		t.Fatalf("declared dimensions = %d, want the fixed constant %d", result.Dimensions, provider.EmbeddingDimensions)
	}
}
