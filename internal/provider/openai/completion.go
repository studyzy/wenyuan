package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/studyzy/wenyuan/internal/provider"
)

// chatRequest is the request body an OpenAI-compatible endpoint accepts.
type chatRequest struct {
	Model          string          `json:"model"`
	Messages       []chatMessage   `json:"messages"`
	MaxTokens      int             `json:"max_tokens,omitempty"`
	Temperature    *float64        `json:"temperature,omitempty"`
	ReasoningEff   string          `json:"reasoning_effort,omitempty"`
	ServiceTier    string          `json:"service_tier,omitempty"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
	Tools          []toolSpec      `json:"tools,omitempty"`
	ToolChoice     any             `json:"tool_choice,omitempty"`
}

type chatMessage struct {
	Role string `json:"role"`
	// Content is a string for a text-only turn, or an ordered list of parts for
	// a multimodal one (OpenAI's content-parts shape).
	Content any    `json:"content"`
	Name    string `json:"name,omitempty"`
	// ToolCalls ride an assistant turn; ToolCallID keys a tool result back to
	// its call.
	ToolCalls  []toolCallWire `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type responseFormat struct {
	Type       string          `json:"type"`
	JSONSchema *jsonSchemaSpec `json:"json_schema,omitempty"`
}

type jsonSchemaSpec struct {
	Name   string          `json:"name"`
	Strict bool            `json:"strict"`
	Schema json.RawMessage `json:"schema"`
}

type toolSpec struct {
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
}

type toolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type toolCallWire struct {
	ID       string `json:"id"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// usageWire is the token accounting an endpoint reports.
type usageWire struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	PromptTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

// chatResponse is the response body an OpenAI-compatible endpoint returns.
type chatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content   string         `json:"content"`
			ToolCalls []toolCallWire `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage usageWire `json:"usage"`
}

// Complete runs one text completion, free-form or structured, applying the
// adapter's retry, timeout, and concurrency semantics.
func (c *Client) Complete(ctx context.Context, req provider.CompletionRequest) (provider.CompletionResult, error) {
	body, err := c.buildChatRequest(req)
	if err != nil {
		return provider.CompletionResult{}, err
	}
	policy := retryPolicyFor(req.Retry, c.Settings.Retry)
	queueWait, release, err := c.acquire()
	if err != nil {
		return provider.CompletionResult{}, err
	}
	defer release()

	started := c.now()
	raw, err := c.sendWithRetry(ctx, "chat/completions", body, policy, req.Scope)
	latency := c.now().Sub(started)
	if err != nil {
		return provider.CompletionResult{}, err
	}
	return decodeChatResponse(raw, req, time.Duration(queueWait), latency)
}

// buildChatRequest turns a boundary request into the wire body, attaching a
// JSON-schema response format for structured calls and routing through a
// forced function tool when configured.
func (c *Client) buildChatRequest(req provider.CompletionRequest) ([]byte, error) {
	wire := chatRequest{
		Model:        c.Settings.Model,
		Messages:     toWireMessages(req.Messages),
		MaxTokens:    req.MaxTokens,
		Temperature:  req.Temperature,
		ReasoningEff: c.Settings.ReasoningEffort,
		ServiceTier:  c.Settings.ServiceTier,
	}
	if req.ResponseSchema != nil {
		format, err := c.buildResponseFormat(req)
		if err != nil {
			return nil, err
		}
		wire.ResponseFormat = format
	}
	if len(req.Tools) > 0 {
		wire.Tools = toWireTools(req.Tools)
		wire.ToolChoice = toWireToolChoice(req.ToolChoice)
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return nil, &provider.MalformedResponseError{Detail: "cannot encode request", Cause: err}
	}
	return mergeExtraBody(encoded, c.Settings.ExtraBody)
}

// buildResponseFormat chooses the structured-output mechanism: a strict JSON
// schema, or a generic JSON-object mode when strictness is not requested.
func (c *Client) buildResponseFormat(req provider.CompletionRequest) (*responseFormat, error) {
	if req.Strict {
		raw, err := MarshalStrictSchema(req.ResponseSchema, c.Settings)
		if err != nil {
			return nil, err
		}
		return &responseFormat{
			Type:       "json_schema",
			JSONSchema: &jsonSchemaSpec{Name: "response", Strict: true, Schema: raw},
		}, nil
	}
	if c.useJSONMode() {
		return &responseFormat{Type: "json_object"}, nil
	}
	return nil, nil
}

// decodeChatResponse turns a success body into the boundary result, capturing
// usage before parsing so billed tokens are recorded even when validation
// fails.
func decodeChatResponse(raw []byte, req provider.CompletionRequest, queueWait, latency time.Duration) (provider.CompletionResult, error) {
	var wire chatResponse
	if err := json.Unmarshal(raw, &wire); err != nil {
		return provider.CompletionResult{}, &provider.MalformedResponseError{Detail: "cannot decode chat response", Cause: err}
	}
	if len(wire.Choices) == 0 {
		return provider.CompletionResult{}, &provider.MalformedResponseError{Detail: "response carried no choices"}
	}
	choice := wire.Choices[0]
	result := provider.CompletionResult{
		Usage:        usageFrom(wire.Usage),
		Model:        wire.Model,
		FinishReason: choice.FinishReason,
		ToolCalls:    toolCallsFrom(choice.Message.ToolCalls),
		QueueWait:    provider.Duration(queueWait),
		Latency:      provider.Duration(latency),
	}
	if isOutputLimit(choice.FinishReason) {
		return result, &provider.OutputTooLongError{FinishReason: choice.FinishReason}
	}
	if req.ResponseSchema != nil {
		structured, err := ParseStructured(choice.Message.Content, req.ResponseSchema, req.SkipValidation)
		result.Structured = structured
		if err != nil {
			return result, err
		}
		return result, nil
	}
	result.Content = StripScaffolding(choice.Message.Content)
	return result, nil
}

// usageFrom maps the wire accounting onto the boundary's usage type, keeping
// the visible output separable from the provider's hidden reasoning.
//
// The boundary's Usage treats OutputTokens/TotalTokens as *visible* output,
// with reasoning surfaced separately in ReasoningTokens — the same contract the
// reference's visible_token_usage normalizes to. OpenAI-compatible upstreams do
// not agree on where reasoning lives on the wire: folded (OpenAI's own
// reasoning models) reports total = prompt + completion with completion already
// including reasoning; unfolded (several gateways and proxies) reports
// total = prompt + completion + reasoning with completion already visible-only.
// Subtracting reasoning from completion unconditionally clamps a real
// completion to zero on the unfolded shape whenever reasoning exceeds the
// visible output.
//
// total reads as prompt + visible + reasoning under *both* shapes, so visible
// output is derived from it and never needs the shape to be known; it is then
// clamped to the two admissible readings — completion (unfolded) and
// completion - reasoning (folded) — so a drifted total degrades to the nearer
// count rather than to a bogus one. A missing or zero total leaves nothing to
// derive from and falls back to the folded reading.
//
// A provider that reports no reasoning at all cannot be split, so the whole
// completion is visible output.
func usageFrom(u usageWire) provider.Usage {
	input := u.PromptTokens
	completion := u.CompletionTokens
	thoughts := u.CompletionTokensDetails.ReasoningTokens

	folded := completion - thoughts
	if folded < 0 {
		folded = 0
	}
	var output int
	switch {
	case thoughts == 0:
		output = completion
	case u.TotalTokens > 0:
		derived := (u.TotalTokens - thoughts) - input
		output = min(completion, max(folded, derived))
	default:
		output = folded
	}
	return provider.Usage{
		InputTokens:       input,
		OutputTokens:      output,
		TotalTokens:       input + output,
		CachedInputTokens: u.PromptTokensDetails.CachedTokens,
		ReasoningTokens:   thoughts,
	}
}

func toWireMessages(messages []provider.Message) []chatMessage {
	out := make([]chatMessage, 0, len(messages))
	for _, m := range messages {
		message := chatMessage{Role: string(m.Role), Name: m.Name}
		switch {
		case len(m.ToolCalls) > 0:
			// An assistant turn that asked for tools carries the calls (and no
			// text), so the following tool results have something to answer.
			calls := make([]toolCallWire, 0, len(m.ToolCalls))
			for _, call := range m.ToolCalls {
				var wire toolCallWire
				wire.ID = call.ID
				wire.Type = "function"
				wire.Function.Name = call.Name
				wire.Function.Arguments = string(call.Arguments)
				calls = append(calls, wire)
			}
			message.ToolCalls = calls
			message.Content = nil
		case m.ToolCallID != "":
			// A tool result answers one call by id.
			message.ToolCallID = m.ToolCallID
			message.Content = m.Content
		case len(m.Parts) == 0:
			message.Content = m.Content
		default:
			parts := make([]map[string]any, 0, len(m.Parts))
			for _, part := range m.Parts {
				if part.Type == "image_url" {
					parts = append(parts, map[string]any{
						"type":      "image_url",
						"image_url": map[string]any{"url": part.ImageURL},
					})
					continue
				}
				parts = append(parts, map[string]any{"type": "text", "text": part.Text})
			}
			message.Content = parts
		}
		out = append(out, message)
	}
	return out
}

func toWireTools(tools []provider.ToolDefinition) []toolSpec {
	out := make([]toolSpec, 0, len(tools))
	for _, t := range tools {
		out = append(out, toolSpec{
			Type:     "function",
			Function: toolFunction{Name: t.Name, Description: t.Description, Parameters: t.Parameters},
		})
	}
	return out
}

func toWireToolChoice(policy *provider.ToolSelectionPolicy) any {
	if policy == nil || policy.Mode == "" {
		return nil
	}
	if policy.Name != "" {
		return map[string]any{
			"type":     "function",
			"function": map[string]string{"name": policy.Name},
		}
	}
	return policy.Mode
}

func toolCallsFrom(calls []toolCallWire) []provider.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]provider.ToolCall, 0, len(calls))
	for _, c := range calls {
		out = append(out, provider.ToolCall{
			ID:        c.ID,
			Name:      c.Function.Name,
			Arguments: json.RawMessage(c.Function.Arguments),
		})
	}
	return out
}

// isOutputLimit reports whether a stop reason means the generation stopped at
// its own output token limit.
func isOutputLimit(reason string) bool {
	return reason == "length" || reason == "max_tokens" || reason == "max_output_tokens"
}

// mergeExtraBody merges operator-supplied provider-specific fields into an
// encoded request body, with operator fields winning on collision.
func mergeExtraBody(encoded []byte, extra map[string]any) ([]byte, error) {
	if len(extra) == 0 {
		return encoded, nil
	}
	var base map[string]any
	if err := json.Unmarshal(encoded, &base); err != nil {
		return nil, &provider.MalformedResponseError{Detail: "cannot decode request", Cause: err}
	}
	for k, v := range extra {
		base[k] = v
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(base); err != nil {
		return nil, &provider.MalformedResponseError{Detail: "cannot encode request", Cause: err}
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// useJSONMode resolves whether a generic JSON-object mode is used.
func (c *Client) useJSONMode() bool {
	if c.Settings.JSONMode != nil {
		return *c.Settings.JSONMode
	}
	return false
}

// retryPolicyFor resolves the retry policy as call override over scope over
// the documented defaults.
func retryPolicyFor(callOverride *provider.RetryPolicy, scope provider.RetryPolicy) provider.RetryPolicy {
	if callOverride != nil {
		return callOverride.Normalize()
	}
	return scope.Normalize()
}

// now reads the clock, defaulting to the real one.
func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}
