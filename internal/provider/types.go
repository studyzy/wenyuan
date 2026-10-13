package provider

import "encoding/json"

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

// Message is one turn of a completion conversation.
type Message struct {
	// Role is the speaking party.
	Role Role
	// Content is the turn's text payload.
	Content string
	// Name optionally labels the turn within its role.
	Name string
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

// Duration is a wall-clock span reported by an adapter. It is a distinct
// type so queue wait and provider latency cannot be summed by accident.
type Duration int64
