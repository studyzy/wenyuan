// Package openai implements the single OpenAI-compatible HTTP adapter. It
// builds requests against a configured endpoint using the standard library
// HTTP client, decodes responses into the provider boundary's types, and
// classifies vendor responses into the boundary's typed errors.
package openai

import (
	"net/http"
	"sync"
	"time"

	"github.com/studyzy/wenyuan/internal/provider"
)

// NameOpenAI is the provider name that selects this adapter.
const NameOpenAI = "openai"

// OpenAICompatibleProviders maps an operator-selected provider name to the
// endpoint that serves its OpenAI-compatible API. These vendors differ only in
// the endpoint and the default credential requirement, not in the protocol, so
// one adapter serves them all rather than one adapter each.
var OpenAICompatibleProviders = map[string]string{
	"groq":         "https://api.groq.com/openai/v1",
	"ollama":       "http://localhost:11434/v1",
	"ollama-cloud": "https://ollama.com/v1",
	"lmstudio":     "http://localhost:1234/v1",
	"minimax":      "https://api.minimax.io/v1",
	"deepseek":     "https://api.deepseek.com",
	"openrouter":   "https://openrouter.ai/api/v1",
	"requesty":     "https://router.requesty.ai/v1",
	"zai":          "https://api.z.ai/api/coding/paas/v4",
	"opencode-go":  "https://opencode.ai/zen/go/v1",
	"atlas":        "https://api.atlascloud.ai/v1",
	"meta":         "https://api.meta.ai/v1",
	"fireworks":    "https://api.fireworks.ai/inference/v1",
	"volcano":      "https://ark.cn-beijing.volces.com/api/v3",
}

// LocalProviders are the vendors that serve from the loopback address, so a
// missing credential is expected and a stand-in is supplied instead of failing
// selection.
var LocalProviders = map[string]bool{"ollama": true, "lmstudio": true}

// DefaultBaseURL is applied when the operator configures no endpoint.
const DefaultBaseURL = "https://api.openai.com/v1"

// DefaultTimeout is the per-attempt bound applied when neither the call nor
// the scope configures one.
const DefaultTimeout = 120 * time.Second

// HTTPDoer is the transport seam. Tests inject a local double so no test
// reaches a live endpoint or needs a real credential.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Client is the adapter's HTTP client plus its resolved settings. It holds no
// process configuration and no database handle.
type Client struct {
	// Settings are the resolved settings this client was built from.
	Settings provider.Settings
	// HTTP performs requests; nil means a default client with the resolved
	// connect and wall-clock timeouts is used.
	HTTP HTTPDoer
	// Pool is the process-wide permit shared across scopes; nil means only
	// the per-operation bound applies.
	Pool *Pool
	// Now is injectable so backoff and reset-time tests stay deterministic.
	Now func() time.Time
	// Sleep is injectable so retry tests do not wait in real time.
	Sleep func(time.Duration)
	// opOnce and opSem lazily build the per-operation permit once per client.
	// Building it per call would hand every caller its own empty permit, which
	// never blocks and so would not bound concurrency at all.
	opOnce sync.Once
	opSem  semaphore
}

// NewClient builds a client over the resolved settings. When no transport is
// supplied one is constructed with the configured timeouts.
func NewClient(settings provider.Settings, doer HTTPDoer) *Client {
	return NewClientWithPool(settings, doer, nil)
}

// NewClientWithPool builds a client sharing a process-wide permit. The global
// cap composes with the per-operation cap rather than replacing it.
func NewClientWithPool(settings provider.Settings, doer HTTPDoer, pool *Pool) *Client {
	if doer == nil {
		doer = &http.Client{Timeout: settings.Timeout}
	}
	// A loopback vendor serves from the operator's own machine and has no key
	// to supply, so a stand-in is used rather than failing every call on an
	// absent credential.
	if LocalProviders[settings.Provider] && settings.APIKey == "" {
		settings.APIKey = "local"
	}
	return &Client{
		Settings: settings,
		HTTP:     doer,
		Pool:     pool,
		Now:      time.Now,
		Sleep:    time.Sleep,
	}
}

// BaseURL returns the endpoint with any trailing slash removed so request
// paths join predictably.
func (c *Client) BaseURL() string {
	return trimTrailingSlash(c.Settings.BaseURL)
}

// TimeoutFor resolves the per-attempt wall-clock bound from the call, then the
// scope, then the adapter's documented default.
func (c *Client) TimeoutFor(callOverride time.Duration) time.Duration {
	if callOverride > 0 {
		return callOverride
	}
	if c.Settings.Timeout > 0 {
		return c.Settings.Timeout
	}
	return DefaultTimeout
}

func trimTrailingSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

// EmbeddingModel returns the embedding model identifier.
func (c *Client) EmbeddingModel() string {
	if c.Settings.Embedding != nil {
		return c.Settings.Embedding.Model
	}
	return ""
}

// embeddingVendorDimensions returns the adapter-level requested width. It is
// an adapter knob only: the destination's stored width stays the boundary
// constant and this value never changes it.
func (c *Client) embeddingVendorDimensions() int {
	if c.Settings.Embedding != nil {
		return c.Settings.Embedding.VendorDimensions
	}
	return 0
}

// embeddingRetryPolicy returns the embedding retry tuning, falling back to
// the chat scope when the embedding scope configures none.
func (c *Client) embeddingRetryPolicy() provider.RetryPolicy {
	if c.Settings.Embedding != nil {
		return c.Settings.Embedding.Retry
	}
	return c.Settings.Retry
}

// passagePrefix returns the configured passage prefix.
func (c *Client) passagePrefix() string {
	if c.Settings.Embedding != nil {
		return c.Settings.Embedding.PassagePrefix
	}
	return ""
}

// queryPrefix returns the configured query prefix.
func (c *Client) queryPrefix() string {
	if c.Settings.Embedding != nil {
		return c.Settings.Embedding.QueryPrefix
	}
	return ""
}
