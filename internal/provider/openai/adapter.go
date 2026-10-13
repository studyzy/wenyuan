package openai

import (
	"context"
	"strings"

	"github.com/studyzy/wenyuan/internal/provider"
)

// Name reports the registered provider name.
func (c *Client) Name() string { return NameOpenAI }

// Dimensions reports the boundary's declared dimension. The adapter never
// computes a width locally: the stored width is the boundary constant.
func (c *Client) Dimensions() int { return provider.DeclaredDimensions() }

// VerifyConnection issues a small probe completion and reports whether the
// provider is reachable, naming the provider and model on failure.
func (c *Client) VerifyConnection(ctx context.Context) error {
	_, err := c.Complete(ctx, provider.CompletionRequest{
		Messages:  []provider.Message{{Role: provider.RoleUser, Content: "ping"}},
		MaxTokens: 1,
		Scope:     "verification",
	})
	if err != nil {
		return &provider.TransientError{Cause: &verifyError{provider: NameOpenAI, model: c.Settings.Model, cause: err}}
	}
	return nil
}

// verifyError describes a failed connection probe while keeping the
// underlying failure reachable.
type verifyError struct {
	provider string
	model    string
	cause    error
}

func (e *verifyError) Error() string {
	model := e.model
	if model == "" {
		model = "default"
	}
	return "provider " + e.provider + " model " + model + " is unreachable: " + e.cause.Error()
}

func (e *verifyError) Unwrap() error { return e.cause }

// Register adds the OpenAI-compatible adapter to a registry. A blank base URL
// falls back to the adapter's documented default.
func Register(reg *provider.Registry) {
	reg.Register(provider.Registration{
		Name:               NameOpenAI,
		RequiresCredential: true,
		DefaultBaseURL:     DefaultBaseURL,
		Build: func(settings provider.Settings) (provider.Provider, error) {
			if strings.TrimSpace(settings.BaseURL) == "" {
				settings.BaseURL = DefaultBaseURL
			}
			return NewClient(settings, nil), nil
		},
	})
}

// Ensure the adapter satisfies the boundary.
var _ provider.Provider = (*Client)(nil)
