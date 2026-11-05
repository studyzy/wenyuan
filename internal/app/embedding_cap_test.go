package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
)

// recordingBackend records the texts it is handed and returns fixed-width
// vectors, so the embedding boundary can be exercised without a live provider.
type recordingBackend struct {
	batches   [][]string
	dims      int
	vectorLen int
	err       error
}

func (b *recordingBackend) Name() string    { return "recording" }
func (b *recordingBackend) Model() string   { return "recording" }
func (b *recordingBackend) Dimensions() int { return b.dims }

func (b *recordingBackend) Embed(_ context.Context, req provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	b.batches = append(b.batches, append([]string(nil), req.Texts...))
	if b.err != nil {
		return provider.EmbeddingResult{}, b.err
	}
	vectors := make([][]float32, len(req.Texts))
	for i := range vectors {
		vectors[i] = make([]float32, b.vectorLen)
	}
	return provider.EmbeddingResult{Vectors: vectors, Dimensions: b.dims}, nil
}

// The resolved value passes through untouched: the unset key became the
// shipped default at resolution, an explicit positive cap is kept, and a zero
// or negative one reaches the boundary as "off" so a deployment can send
// untruncated text.
func TestEmbeddingConfigDefaultsInputCap(t *testing.T) {
	if got := embeddingConfigFor(config.EmbeddingConfig{MaxInputTokens: 8192}).MaxInputTokens; got != 8192 {
		t.Fatalf("resolved default cap = %d, want 8192", got)
	}
	if got := embeddingConfigFor(config.EmbeddingConfig{MaxInputTokens: 512}).MaxInputTokens; got != 512 {
		t.Fatalf("explicit cap = %d, want 512", got)
	}
	if got := embeddingConfigFor(config.EmbeddingConfig{}).MaxInputTokens; got != 0 {
		t.Fatalf("zero cap = %d, want 0 (off)", got)
	}
	if got := embeddingConfigFor(config.EmbeddingConfig{MaxInputTokens: -1}).MaxInputTokens; got != -1 {
		t.Fatalf("negative cap = %d, want -1 (disable)", got)
	}
}

// Each input is cut in token units, and the prefix the backend will add is
// charged against the budget before the cut.
func TestCappedEmbedderTruncatesByTokensChargingPrefix(t *testing.T) {
	backend := &recordingBackend{dims: 4, vectorLen: 4}
	c := cappedEmbedder{
		inner:          backend,
		maxInputTokens: 20,
		passagePrefix:  "passage: ",
	}
	long := strings.Repeat("word ", 400)
	result, err := c.Embed(context.Background(), provider.EmbeddingRequest{
		Texts: []string{long}, Kind: provider.EmbeddingPassage,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Vectors) != 1 {
		t.Fatalf("vectors = %d, want 1", len(result.Vectors))
	}
	sent := backend.batches[0][0]
	budget := 20 - storage.CountTokens("passage: ")
	got := storage.CountTokens(sent)
	if got > budget || got == 0 {
		t.Fatalf("sent %d tokens, want within (0, %d]", got, budget)
	}
	if got >= storage.CountTokens(long) {
		t.Fatal("a long input was not truncated")
	}
}

// A query prefix that costs more than the whole budget leaves no room for the
// payload, which arrives empty rather than over the cap.
func TestCappedEmbedderChargesQueryPrefixAgainstBudget(t *testing.T) {
	backend := &recordingBackend{dims: 4, vectorLen: 4}
	c := cappedEmbedder{
		inner:          backend,
		maxInputTokens: 20,
		queryPrefix:    strings.Repeat("context ", 30),
	}
	if _, err := c.Embed(context.Background(), provider.EmbeddingRequest{
		Texts: []string{strings.Repeat("word ", 400)}, Kind: provider.EmbeddingQuery,
	}); err != nil {
		t.Fatal(err)
	}
	if got := backend.batches[0][0]; got != "" {
		t.Fatalf("payload = %q, want empty after the prefix consumed the budget", got)
	}
}

// A non-positive cap disables truncation entirely.
func TestCappedEmbedderDisabledOnNonPositiveCap(t *testing.T) {
	for _, cap := range []int{0, -3} {
		backend := &recordingBackend{dims: 4, vectorLen: 4}
		c := cappedEmbedder{inner: backend, maxInputTokens: cap}
		long := strings.Repeat("word ", 400)
		if _, err := c.Embed(context.Background(), provider.EmbeddingRequest{Texts: []string{long}}); err != nil {
			t.Fatal(err)
		}
		if got := backend.batches[0][0]; got != long {
			t.Fatalf("cap %d truncated the input", cap)
		}
	}
}

// Every returned vector is checked against the backend's declared width, even
// when truncation is disabled.
func TestCappedEmbedderRejectsWrongWidthVector(t *testing.T) {
	backend := &recordingBackend{dims: 4, vectorLen: 3}
	c := cappedEmbedder{inner: backend, maxInputTokens: 20}
	_, err := c.Embed(context.Background(), provider.EmbeddingRequest{Texts: []string{"hello"}})
	if err == nil {
		t.Fatal("a vector whose width differs from the backend dimension must be rejected")
	}
	if !strings.Contains(err.Error(), "width 3, want 4") {
		t.Fatalf("error = %q, want both widths named", err)
	}
}

func TestCappedEmbedderPropagatesBackendError(t *testing.T) {
	backend := &recordingBackend{dims: 4, vectorLen: 4, err: errors.New("provider down")}
	c := cappedEmbedder{inner: backend, maxInputTokens: 20}
	if _, err := c.Embed(context.Background(), provider.EmbeddingRequest{Texts: []string{"hello"}}); err == nil {
		t.Fatal("a backend error must propagate")
	}
}

func TestCappedEmbedderDelegatesIdentity(t *testing.T) {
	backend := &recordingBackend{dims: 7, vectorLen: 7}
	c := cappedEmbedder{inner: backend, maxInputTokens: 20}
	if c.Name() != backend.Name() || c.Model() != backend.Model() || c.Dimensions() != 7 {
		t.Fatal("identity methods must delegate to the inner backend")
	}
}
