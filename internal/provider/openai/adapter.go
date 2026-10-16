package openai

import (
	"context"
	"strings"

	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/provider/cacheaffinity"
)

// Name reports the registered provider name. It is the name the operator
// selected, which for the OpenAI-compatible vendors is their own name rather
// than "openai", so error messages and configuration checks name the endpoint
// the operator actually configured.
func (c *Client) Name() string {
	if c.Settings.Provider != "" {
		return c.Settings.Provider
	}
	return NameOpenAI
}

// Dimensions reports the boundary's declared dimension. The adapter never
// computes a width locally: the stored width is the boundary constant.
func (c *Client) Dimensions() int { return provider.DeclaredDimensions() }

// VerifyConnection issues a small probe completion and reports whether the
// provider is reachable, naming the provider and model on failure.
//
// The prompt is a literal, not a greeting: a probe is only useful if it passes
// on every endpoint the deployment supports, and an endpoint that answers the
// prompt at all is the whole question. The budget is the operational
// verification allowance, so a provider that needs a couple of tokens of
// scaffolding before its answer is not judged unreachable.
func (c *Client) VerifyConnection(ctx context.Context) error {
	policy := provider.VerificationRetryPolicy()
	_, err := c.Complete(ctx, provider.CompletionRequest{
		Messages:  []provider.Message{{Role: provider.RoleUser, Content: provider.VerificationPrompt}},
		MaxTokens: provider.VerificationMaxTokens,
		Scope:     "verification",
		Retry:     &policy,
	})
	if err != nil {
		return &provider.TransientError{Cause: &verifyError{provider: c.Name(), model: c.Settings.Model, cause: err}}
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

// Register adds the OpenAI-compatible adapter to a registry. Every vendor that
// speaks the OpenAI protocol is registered here against its own endpoint, so
// selecting one of them needs no separate adapter; a blank base URL falls back
// to the adapter's documented default for the selected vendor.
func Register(reg *provider.Registry) {
	register(reg, NameOpenAI, DefaultBaseURL, true)
	for name, baseURL := range OpenAICompatibleProviders {
		register(reg, name, baseURL, true)
	}
}

// register adds one provider name against its default endpoint. A loopback
// vendor does not require a credential, because it serves from the operator's
// own machine and has no key to supply.
func register(reg *provider.Registry, name, defaultBaseURL string, cloud bool) {
	local := LocalProviders[name]
	reg.Register(provider.Registration{
		Name:               name,
		RequiresCredential: cloud && !local,
		DefaultBaseURL:     defaultBaseURL,
		Build: func(settings provider.Settings) (provider.Provider, error) {
			if strings.TrimSpace(settings.BaseURL) == "" {
				settings.BaseURL = defaultBaseURL
			}
			settings.Provider = name
			// A typo in the cache-affinity mode fails at construction, where the
			// operator sees it, rather than silently disabling a hint whose
			// effect is invisible in the response.
			if _, err := cacheaffinity.Parse(settings.CacheAffinity); err != nil {
				return nil, err
			}
			return NewClient(settings, nil), nil
		},
	})
}

// Ensure the adapter satisfies the boundary.
var _ provider.Provider = (*Client)(nil)
