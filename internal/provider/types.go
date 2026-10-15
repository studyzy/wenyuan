package provider

import (
	"context"
	"encoding/json"
)

// Role identifies the speaking party in a completion conversation.
type Role string

const (
	// RoleSystem primes the model's behaviour.
	RoleSystem Role = "system"
	// RoleUser carries caller input.
	RoleUser Role = "user"
	// RoleAssistant carries a prior model turn.
	RoleAssistant Role = "assistant"
	// RoleTool carries the output of a tool the model asked for.
	RoleTool Role = "tool"
)

// ContentPart is one ordered piece of a multimodal message. A message that
// carries Parts sends them in place of Content, in the order written; a message
// without them keeps the plain-string form, so a text-only request is
// byte-identical to before.
type ContentPart struct {
	// Type is the part's kind: "text" or "image_url".
	Type string
	// Text is the part's text, for a text part.
	Text string
	// ImageURL is the data URI an image_url part carries.
	ImageURL string
}

// Message is one turn of a completion conversation.
type Message struct {
	// Role is the speaking party.
	Role Role
	// Content is the turn's text payload. It is used when Parts is empty.
	Content string
	// Parts, when non-empty, is the turn's ordered multimodal content. It
	// replaces Content so an image sits exactly where its placeholder stood.
	Parts []ContentPart
	// Name optionally labels the turn within its role.
	Name string
	// ToolCalls lists the functions an assistant turn asked the caller to run.
	// A message that carries them is an assistant turn; each call is answered by
	// a following tool-role message keyed by ToolCallID.
	ToolCalls []ToolCall
	// ToolCallID keys a tool-role message to the assistant call it answers.
	ToolCallID string
}

// ToolDefinition describes a function a model may ask the caller to run.
type ToolDefinition struct {
	// Name is the function identifier.
	Name string
	// Description explains when the model should call the function.
	Description string
	// Parameters is the function's JSON-schema parameter object.
	Parameters json.RawMessage
}

// ToolSelectionPolicy controls whether and which tool a model may call.
type ToolSelectionPolicy struct {
	// Mode is one of none, auto, required.
	Mode string
	// Name pins one tool when Mode names a specific function.
	Name string
}

// Usage reports the billed token accounting for one completion.
type Usage struct {
	// InputTokens is the prompt token count.
	InputTokens int
	// OutputTokens is the generated token count.
	OutputTokens int
	// TotalTokens is the provider-reported sum.
	TotalTokens int
	// CachedInputTokens is the portion served from a provider cache.
	CachedInputTokens int
	// ReasoningTokens is the portion spent on internal reasoning.
	ReasoningTokens int
}

// CompletionRequest is one text-completion call.
type CompletionRequest struct {
	// Messages is the ordered conversation.
	Messages []Message
	// ResponseSchema, when set, asks for structured output validated against it.
	ResponseSchema *Schema
	// SkipValidation returns raw parsed data instead of a validated value.
	SkipValidation bool
	// MaxTokens caps the generated token count; zero means unset.
	MaxTokens int
	// Temperature is the sampling temperature; nil means omit the parameter.
	Temperature *float64
	// Scope labels the call for tracing and per-operation policy.
	Scope string
	// Strict requests grammar-enforced structured output.
	Strict bool
	// Tools are the functions the model may call.
	Tools []ToolDefinition
	// ToolChoice controls tool selection.
	ToolChoice *ToolSelectionPolicy
	// Retry overrides the adapter's default retry policy when non-nil.
	Retry *RetryPolicy
}

// CompletionResult is the outcome of a completion call.
type CompletionResult struct {
	// Content is the model's assistant text.
	Content string
	// Structured carries the parsed value when a response schema was supplied.
	Structured any
	// ToolCalls lists the functions the model asked the caller to run.
	ToolCalls []ToolCall
	// Usage is the billed token accounting.
	Usage Usage
	// Model is the provider-reported model identifier, when given.
	Model string
	// FinishReason is the provider-reported stop reason.
	FinishReason string
	// QueueWait is the time spent waiting for a concurrency permit.
	QueueWait Duration
	// Latency is the time the provider spent on the call.
	Latency Duration
}

// ToolCall is one function invocation a model requested.
type ToolCall struct {
	// ID identifies the call for its matching result.
	ID string
	// Name is the requested function.
	Name string
	// Arguments is the model-supplied JSON argument object.
	Arguments json.RawMessage
}

// EmbeddingRequest is one embedding-generation call.
type EmbeddingRequest struct {
	// Texts are the inputs to embed, in caller order.
	Texts []string
	// Kind selects the passage or query prefix.
	Kind EmbeddingKind
	// Retry overrides the adapter's default retry policy when non-nil.
	Retry *RetryPolicy
}

// EmbeddingKind selects which configured input prefix is applied.
type EmbeddingKind string

const (
	// EmbeddingPassage embeds stored passages.
	EmbeddingPassage EmbeddingKind = "passage"
	// EmbeddingQuery embeds search queries.
	EmbeddingQuery EmbeddingKind = "query"
)

// EmbeddingResult is the outcome of an embedding call.
type EmbeddingResult struct {
	// Vectors holds one vector per input text, in input order.
	Vectors [][]float32
	// Dimensions is the width of every returned vector.
	Dimensions int
}

// RerankRequest asks a cross-encoder to score candidate documents against one
// query. It is a separate boundary from Provider because only some backends
// serve a reranking model, and a deployment without one must keep working.
type RerankRequest struct {
	// Query is the text every document is scored against.
	Query string
	// Documents are the candidate texts, in caller order. The result
	// preserves this order so the caller can pair scores back to candidates.
	Documents []string
	// Model names the reranking model; empty means the adapter's default.
	Model string
}

// RerankResult is the outcome of a reranking call.
type RerankResult struct {
	// Scores holds one relevance score per input document, in input order.
	// Scores are the backend's own scale and are not normalized here.
	Scores []float64
}

// Reranker is the cross-encoder boundary. A provider may implement it when it
// can score query-document pairs; the recall pipeline uses it when present and
// falls back to the best per-arm score when it is not.
type Reranker interface {
	// Rerank scores each document against the query, in input order.
	Rerank(ctx context.Context, req RerankRequest) (RerankResult, error)
}

// ProviderReranker returns the provider's reranking capability, or nil when it
// has none. It is a type assertion rather than a Provider method so a provider
// that cannot rerank does not have to declare a method it would only stub.
func ProviderReranker(p Provider) Reranker {
	if capable, ok := p.(RerankCapable); ok {
		return capable.RerankCapability()
	}
	reranker, _ := p.(Reranker)
	return reranker
}

// RerankCapable is implemented by a wrapper that delegates to an inner
// provider. It lets ProviderReranker look through the wrapper, so wrapping a
// provider for tracing does not hand callers a reranker the inner provider
// does not have.
type RerankCapable interface {
	RerankCapability() Reranker
}

// Duration is a wall-clock span reported by an adapter. It is a distinct
// type so queue wait and provider latency cannot be summed by accident.
type Duration int64
