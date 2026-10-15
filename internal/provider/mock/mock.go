// Package mock is a deterministic provider for tests and for a deployment that
// wants the model-driven paths exercised without a live endpoint. It echoes the
// last user turn as its completion and returns a fixed-width vector from the
// input's digest, so a call is reproducible across runs.
package mock

import (
	"context"
	"crypto/sha256"
	"encoding/binary"

	"github.com/studyzy/wenyuan/internal/provider"
)

// NameMock is the registered provider name.
const NameMock = "mock"

// Provider is the deterministic adapter.
type Provider struct {
	// Dimensions is the vector width every embedding returns.
	DimensionsValue int
}

// Name reports the provider name.
func (p *Provider) Name() string { return NameMock }

// Complete echoes the last user turn, or a fixed string when there is none.
func (p *Provider) Complete(_ context.Context, req provider.CompletionRequest) (provider.CompletionResult, error) {
	content := "mock completion"
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == provider.RoleUser && req.Messages[i].Content != "" {
			content = req.Messages[i].Content
			break
		}
	}
	return provider.CompletionResult{Content: content, Model: "mock"}, nil
}

// Embed returns one deterministic vector per input, derived from its digest, so
// the same text always embeds the same way.
func (p *Provider) Embed(_ context.Context, req provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	vectors := make([][]float32, 0, len(req.Texts))
	for _, text := range req.Texts {
		vectors = append(vectors, p.vector(text))
	}
	return provider.EmbeddingResult{Vectors: vectors, Dimensions: p.Dimensions()}, nil
}

// vector builds a stable pseudo-random unit-ish vector from a text digest.
func (p *Provider) vector(text string) []float32 {
	width := p.Dimensions()
	vec := make([]float32, width)
	sum := sha256.Sum256([]byte(text))
	seed := binary.BigEndian.Uint64(sum[:8])
	for i := range vec {
		seed = seed*6364136223846793005 + 1442695040888963407
		vec[i] = float32(int64(seed>>33)%1000)/1000 - 0.5
	}
	return vec
}

// VerifyConnection succeeds as a no-op: nothing is reachable to probe.
func (p *Provider) VerifyConnection(context.Context) error { return nil }

// Dimensions reports the vector width, defaulting to the boundary's declared
// width when unset.
func (p *Provider) Dimensions() int {
	if p.DimensionsValue > 0 {
		return p.DimensionsValue
	}
	return provider.DeclaredDimensions()
}

// Ensure the adapter satisfies the boundary.
var _ provider.Provider = (*Provider)(nil)
