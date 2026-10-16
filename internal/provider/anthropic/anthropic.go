// Package anthropic adapts Anthropic's Messages API onto the model-provider
// boundary.
//
// It is a separate adapter rather than a configuration of the OpenAI-compatible
// one because the wire protocol differs in ways a base URL cannot absorb: the
// system prompt is a top-level field rather than a message, the assistant reply
// is a list of content blocks, `max_tokens` is required, and structured output
// is requested by forcing a tool rather than by a response-format object.
package anthropic

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/provider/cacheaffinity"
)

// NameAnthropic is the provider name that selects this adapter.
const NameAnthropic = "anthropic"

// DefaultBaseURL is applied when the operator configures no endpoint.
const DefaultBaseURL = "https://api.anthropic.com/v1"

// AnthropicVersion is the API version header the Messages API requires.
const AnthropicVersion = "2023-06-01"

// DefaultMaxTokens is sent when a caller names no cap. The Messages API
// requires the field, so there is no "uncapped" request to make.
const DefaultMaxTokens = 4096

// structuredToolName is the forced tool's name for a structured-output call.
const structuredToolName = "structured_response"

// Client is the adapter's HTTP client plus its resolved settings.
type Client struct {
	Settings provider.Settings
	// HTTP performs requests; nil means a default client.
	HTTP HTTPDoer
	// Now is injectable so retry tests stay deterministic.
	Now func() time.Time
	// Sleep is injectable so retry tests do not wait in real time.
	Sleep func(time.Duration)
}

// HTTPDoer is the transport seam, so tests need no live endpoint.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// NewClient builds a client over the resolved settings.
func NewClient(settings provider.Settings, doer HTTPDoer) *Client {
	if doer == nil {
		doer = &http.Client{Timeout: settings.Timeout}
	}
	return &Client{Settings: settings, HTTP: doer, Now: time.Now, Sleep: time.Sleep}
}

// Name reports the registered provider name.
func (c *Client) Name() string { return NameAnthropic }

// Dimensions reports the boundary's declared dimension. Anthropic serves no
// embeddings on this API, so the embedding path is unavailable rather than
// silently returning zero-width vectors.
func (c *Client) Dimensions() int { return provider.DeclaredDimensions() }

// Embed reports that this provider cannot embed, so a deployment that selects
// it for completions and wants vectors must configure an embedding provider
// separately.
func (c *Client) Embed(context.Context, provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	return provider.EmbeddingResult{}, &provider.NotAvailableError{ConfigKey: provider.ConfigKeyEmbeddingProvider, Scope: provider.ScopeEmbedding}
}

// VerifyConnection issues a small probe completion and reports reachability.
//
// The probe is this vendor's own: a short literal with a small budget and no
// retries, so an endpoint that is down is reported down at startup instead of
// being waited out.
func (c *Client) VerifyConnection(ctx context.Context) error {
	policy := provider.AnthropicVerificationRetryPolicy()
	_, err := c.Complete(ctx, provider.CompletionRequest{
		Messages:  []provider.Message{{Role: provider.RoleUser, Content: provider.AnthropicVerificationPrompt}},
		MaxTokens: provider.AnthropicVerificationMaxTokens,
		Scope:     "verification",
		Retry:     &policy,
	})
	if err != nil {
		return &provider.TransientError{Cause: err}
	}
	return nil
}

// messagesRequest is the request body the Messages API accepts.
type messagesRequest struct {
	Model       string          `json:"model"`
	Messages    []messagesWire  `json:"messages"`
	System      []systemBlock   `json:"system,omitempty"`
	MaxTokens   int             `json:"max_tokens"`
	Temperature *float64        `json:"temperature,omitempty"`
	Tools       []toolWire      `json:"tools,omitempty"`
	ToolChoice  *toolChoiceWire `json:"tool_choice,omitempty"`
}

// messagesWire is one conversation turn in the Messages API shape. Content is
// the turn's plain text, or a block list when the turn carries the multi-turn
// cache breakpoint.
type messagesWire struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// systemBlock is one block of the top-level system prompt.
type systemBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

// contentBlock is one content block of a message.
type contentBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

// cacheControl marks where a cacheable prefix ends.
type cacheControl struct {
	Type string `json:"type"`
}

// ephemeralCacheControl returns the breakpoint marker this API accepts. A fresh
// value is returned per marker so no request can mutate another's.
func ephemeralCacheControl() *cacheControl { return &cacheControl{Type: "ephemeral"} }

// toolWire declares one tool. For a structured-output call its input schema is
// the caller's response schema.
type toolWire struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// toolChoiceWire forces or constrains tool selection. Name is set only for the
// pinned-tool form.
type toolChoiceWire struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

// messagesResponse is the response body the Messages API returns.
type messagesResponse struct {
	Content []struct {
		Type  string          `json:"type"`
		Text  string          `json:"text"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Model      string `json:"model"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// Complete runs one completion through the Messages API.
//
// The system prompt is lifted out of the message list because the API carries
// it as its own field; the remaining turns are sent in order.
func (c *Client) Complete(ctx context.Context, req provider.CompletionRequest) (provider.CompletionResult, error) {
	system, turns := splitSystem(req.Messages)
	body := messagesRequest{
		Model:       c.Settings.Model,
		Messages:    turns,
		MaxTokens:   maxTokensFor(req.MaxTokens),
		Temperature: req.Temperature,
	}
	if system != "" {
		// Anthropic's prompt cache is a prefix match, so the one system block
		// carries the marker that caches the tools and the system prompt
		// together: both are stable per scope, and the reference reuses that
		// prefix across every chunk of a retain and across reflect turns. A
		// prefix below the model's minimum cacheable length is not cached and
		// pays no write premium, so the marker is sent unconditionally.
		body.System = cachedSystemBlocks(system)
	}
	// A structured-output call forces a single tool whose schema is the caller's
	// response schema, which is how this API does constrained decoding.
	if req.ResponseSchema != nil {
		schema, err := json.Marshal(req.ResponseSchema)
		if err != nil {
			return provider.CompletionResult{}, &provider.MalformedResponseError{Detail: "cannot encode response schema", Cause: err}
		}
		body.Tools = []toolWire{{Name: structuredToolName, InputSchema: schema}}
		body.ToolChoice = &toolChoiceWire{Type: "tool", Name: structuredToolName}
	} else if len(req.Tools) > 0 {
		// Without a response schema this is a plain tool-calling turn — the
		// reflection loop drives all of its retrieval this way — so the caller's
		// tools and pinned choice must reach the wire. Dropping them would leave
		// the model answering from its own knowledge with no evidence.
		body.Tools = toWireTools(req.Tools)
		body.ToolChoice = toWireToolChoice(req.ToolChoice)
		// The tool loop resends the whole growing conversation each iteration,
		// so this request's end marker becomes the next iteration's cache read
		// point while the system marker above covers the stable prefix.
		markLastMessageForCaching(body.Messages)
	}
	// An OpenCode Go endpoint rejects a request that omits its session header,
	// so the header rides every call that targets one, regardless of any cache
	// mode. The id is derived from the caller's request, so it is the same id
	// the request would carry through any other adapter.
	return c.call(ctx, body, req.ResponseSchema != nil, cacheaffinity.OpenCodeSession(c.Settings.BaseURL, req.Messages))
}

// cachedSystemBlocks renders the system prompt as the single block this API
// accepts, carrying the ephemeral cache breakpoint.
func cachedSystemBlocks(system string) []systemBlock {
	return []systemBlock{{Type: "text", Text: system, CacheControl: ephemeralCacheControl()}}
}

// markLastMessageForCaching adds a cache breakpoint to the conversation's final
// content block, in place. A plain-text turn becomes a one-block list because
// that is where the marker lives; an empty turn keeps its string form, since
// this API rejects empty text blocks.
func markLastMessageForCaching(messages []messagesWire) {
	if len(messages) == 0 {
		return
	}
	last := &messages[len(messages)-1]
	text, ok := last.Content.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return
	}
	last.Content = []contentBlock{{Type: "text", Text: text, CacheControl: ephemeralCacheControl()}}
}

// toWireTools maps the boundary's tool definitions onto the Messages API
// shape, whose parameter object is named input_schema.
func toWireTools(tools []provider.ToolDefinition) []toolWire {
	out := make([]toolWire, 0, len(tools))
	for _, tool := range tools {
		out = append(out, toolWire{Name: tool.Name, Description: tool.Description, InputSchema: tool.Parameters})
	}
	return out
}

// toWireToolChoice maps the boundary's selection policy onto the API's
// tool_choice object. A pinned tool becomes the named-tool form; otherwise the
// mode maps onto the API's any/none/auto.
func toWireToolChoice(policy *provider.ToolSelectionPolicy) *toolChoiceWire {
	if policy == nil || policy.Mode == "" {
		return nil
	}
	if policy.Name != "" {
		return &toolChoiceWire{Type: "tool", Name: policy.Name}
	}
	switch policy.Mode {
	case "required":
		return &toolChoiceWire{Type: "any"}
	case "none":
		return &toolChoiceWire{Type: "none"}
	default:
		return &toolChoiceWire{Type: "auto"}
	}
}

// call sends one request, retrying transient failures with capped backoff. The
// per-request headers ride every attempt, so a retried call carries the same
// session id as the first.
func (c *Client) call(ctx context.Context, body messagesRequest, structured bool, headers map[string]string) (provider.CompletionResult, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return provider.CompletionResult{}, &provider.MalformedResponseError{Detail: "cannot encode request", Cause: err}
	}
	pol := c.Settings.Retry.Normalize()
	var last error
	for attempt := 0; ; attempt++ {
		raw, err := c.attempt(ctx, encoded, headers)
		if err == nil {
			return decodeResult(raw, structured)
		}
		last = err
		if !shouldRetry(err) || attempt >= pol.MaxRetries {
			return provider.CompletionResult{}, last
		}
		c.sleep(pol.BackoffFor(attempt))
		if ctx.Err() != nil {
			return provider.CompletionResult{}, ctx.Err()
		}
	}
}

// splitSystem lifts the system turns out of the message list into the single
// system field the API expects, keeping the remaining turns in order.
func splitSystem(messages []provider.Message) (string, []messagesWire) {
	var system strings.Builder
	turns := make([]messagesWire, 0, len(messages))
	for _, message := range messages {
		if string(message.Role) == "system" {
			if system.Len() > 0 {
				system.WriteString("\n\n")
			}
			system.WriteString(message.Content)
			continue
		}
		turns = append(turns, messagesWire{Role: string(message.Role), Content: message.Content})
	}
	return system.String(), turns
}

// maxTokensFor resolves the token cap, substituting the API's required default
// when the caller named none.
func maxTokensFor(requested int) int {
	if requested > 0 {
		return requested
	}
	return DefaultMaxTokens
}

// decodeResult turns a success body into the boundary's result shape.
func decodeResult(raw []byte, structured bool) (provider.CompletionResult, error) {
	var wire messagesResponse
	if err := json.Unmarshal(raw, &wire); err != nil {
		return provider.CompletionResult{}, &provider.MalformedResponseError{Detail: "cannot decode response", Cause: err}
	}
	result := provider.CompletionResult{
		Model:        wire.Model,
		FinishReason: wire.StopReason,
		Usage: provider.Usage{
			InputTokens:  wire.Usage.InputTokens,
			OutputTokens: wire.Usage.OutputTokens,
			TotalTokens:  wire.Usage.InputTokens + wire.Usage.OutputTokens,
		},
	}
	for _, block := range wire.Content {
		switch block.Type {
		case "text":
			result.Content += block.Text
		case "tool_use":
			// The forced structured-output tool carries the parsed value; every
			// other tool_use is a call the caller must run itself.
			if structured && block.Name == structuredToolName {
				var value any
				if err := json.Unmarshal(block.Input, &value); err != nil {
					return provider.CompletionResult{}, &provider.MalformedResponseError{Detail: "structured response was not valid JSON", Cause: err}
				}
				result.Structured = value
				continue
			}
			result.ToolCalls = append(result.ToolCalls, provider.ToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: block.Input,
			})
		}
	}
	if structured {
		// A schema-constrained call that returned no forced tool fell back to
		// text, so the structured value is read from the reply body.
		if result.Structured == nil {
			var value any
			if err := json.Unmarshal([]byte(extractJSON(result.Content)), &value); err == nil {
				result.Structured = value
			}
		}
	}
	return result, nil
}

// extractJSON strips a markdown code fence if the model wrapped its JSON, so a
// fenced reply still parses.
func extractJSON(content string) string {
	trimmed := strings.TrimSpace(content)
	if fence := strings.Index(trimmed, "```json"); fence >= 0 {
		rest := trimmed[fence+len("```json"):]
		if end := strings.Index(rest, "```"); end >= 0 {
			return strings.TrimSpace(rest[:end])
		}
	}
	if fence := strings.Index(trimmed, "```"); fence >= 0 {
		rest := trimmed[fence+len("```"):]
		if end := strings.Index(rest, "```"); end >= 0 {
			return strings.TrimSpace(rest[:end])
		}
	}
	return trimmed
}

// Register adds the Anthropic adapter to a registry.
func Register(reg *provider.Registry) {
	reg.Register(provider.Registration{
		Name:               NameAnthropic,
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
