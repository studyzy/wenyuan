package storage

import "context"

// ReflectRunner performs a reflection over the retrieval the store exposes.
//
// It is an interface rather than a concrete type so the storage area keeps
// owning its queries while the loop that decides what to retrieve lives
// outside it, next to the provider it depends on.
type ReflectRunner interface {
	// Run performs the reflection and reports the answer with the evidence it
	// used.
	Run(ctx context.Context, req AgenticReflectRequest) (ReflectOutcome, error)
}

// AgenticReflectRequest is what a runner needs from the caller.
type AgenticReflectRequest struct {
	// BankID scopes the reflection.
	BankID string
	// Question is what the caller asked.
	Question string
	// MaxTokens caps the answer length; zero means the runner's default.
	MaxTokens int
	// MaxCompletionTokens is the transport-level output cap for the final
	// synthesis call from the deployment's reflect tuning; zero leaves the
	// visible-length target as the cap.
	MaxCompletionTokens int
	// MaxIterations bounds the retrieval turns; zero means the runner's
	// default.
	MaxIterations int
	// Budget is the caller's effort level (storage.ReflectBudget*); it tunes
	// the loop's forced-descent short-circuit.
	Budget string
	// ResponseSchema is the caller's structured-output schema; when set, the
	// runner makes a second call that reshapes the answer into it.
	ResponseSchema map[string]any
	// Retrieval reads the bank's memories, observations and perspectives.
	Retrieval ReflectionRetrieval
	// SearchObservationsMaxTokens is the resolved per-bank budget the
	// search_observations tool uses when the model omits a max_tokens argument.
	// A zero value keeps the shipped default.
	SearchObservationsMaxTokens int
	// SearchObservationsIncludeEntities attaches resolved entity names to each
	// observation a search returns, which the reference does by default.
	SearchObservationsIncludeEntities bool
	// Directives are the standing instructions that apply.
	Directives []ReflectDirective
	// BankName, Mission and Disposition are the bank's profile: the name and
	// mission as the prompt writes them, and the graded traits. Mission is the
	// resolved reflect mission (a config override beats the bank column).
	BankName    string
	Mission     string
	Disposition map[string]int
	// AnswerAsDocument takes the answer from the loop's `done` call as a
	// structure rather than composing prose afterwards.
	AnswerAsDocument bool
	// Tags, TagsMatch and TagGroups are the caller's resolved tag scope, so a
	// retrieval that must honour the reader's scope (expand's document
	// visibility) has it.
	Tags      []string
	TagsMatch string
	TagGroups []TagGroup
}

// ReflectExpandRequest is what the expand tool asks the retrieval surface for:
// the memories to open, how far to open them, and the reader's tag scope, so
// the document the source text came from can be checked before it is read.
type ReflectExpandRequest struct {
	BankID    string
	IDs       []string
	Depth     string
	Tags      []string
	TagsMatch string
	TagGroups []TagGroup
}

// ReflectionRetrieval is the read surface a reflection loop needs. The store
// implements it, so the loop never reaches a table directly. The method names
// carry a reflection prefix because the store already exposes caller-facing
// reads under the plain names.
type ReflectionRetrieval interface {
	// ReflectMentalModels returns perspectives matching a query.
	ReflectMentalModels(ctx context.Context, bankID, query string, maxResults int) ([]ReflectCitation, error)
	// ReflectObservations returns consolidated conclusions matching a query.
	ReflectObservations(ctx context.Context, bankID, query string, maxTokens int) ([]ReflectCitation, error)
	// ReflectMemories returns raw memories matching a query.
	ReflectMemories(ctx context.Context, bankID, query string, maxTokens int) ([]ReflectCitation, error)
	// ReflectReadModels returns specific perspectives by identifier.
	ReflectReadModels(ctx context.Context, bankID string, ids []string) ([]ReflectCitation, error)
	// ReflectExpand returns the source context of specific memories, scoped to
	// the reader.
	ReflectExpand(ctx context.Context, req ReflectExpandRequest) ([]ReflectCitation, error)
}
