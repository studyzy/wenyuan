// Package none implements the no-model provider selection: the configured
// state in which no model is available. It is a supported operating mode, not
// an error state. The store keeps serving deterministically and any path that
// unexpectedly needs a model fails loudly instead of opening a connection.
package none

import (
	"context"

	"github.com/studyzy/wenyuan/internal/provider"
)

// NameNone is the provider name that selects this adapter.
const NameNone = "none"

// Provider is the no-model selection. It holds no transport, so no operation
// can attempt a network connection.
type Provider struct{}

// New returns the no-model provider.
func New() *Provider { return &Provider{} }

// Name reports the registered provider name.
func (p *Provider) Name() string { return NameNone }

// Complete always fails with the typed not-available error naming the
// provider configuration key. It deliberately does not return an empty
// result: a caller that unexpectedly reaches a model path must fail loudly
// rather than proceed on a silently empty answer.
func (p *Provider) Complete(ctx context.Context, req provider.CompletionRequest) (provider.CompletionResult, error) {
	scope := req.Scope
	if scope == "" {
		scope = provider.ScopeChat
	}
	return provider.CompletionResult{}, &provider.NotAvailableError{
		ConfigKey: provider.ConfigKeyProvider,
		Scope:     scope,
	}
}

// Embed always fails with the typed not-available error naming the provider
// configuration key.
func (p *Provider) Embed(ctx context.Context, req provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	return provider.EmbeddingResult{}, &provider.NotAvailableError{
		ConfigKey: provider.ConfigKeyProvider,
		Scope:     provider.ScopeChat,
	}
}

// VerifyConnection succeeds as a no-op. Nothing is reachable to probe, and
// the no-model mode is a valid configured state rather than a failure.
func (p *Provider) VerifyConnection(ctx context.Context) error { return nil }

// Dimensions reports the boundary's declared dimension so the no-model
// selection agrees with every other provider about the stored width.
func (p *Provider) Dimensions() int { return provider.DeclaredDimensions() }

// Ensure the adapter satisfies the boundary.
var _ provider.Provider = (*Provider)(nil)
