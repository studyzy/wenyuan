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
	// MaxIterations bounds the retrieval turns; zero means the runner's
	// default.
	MaxIterations int
	// Retrieval reads the bank's memories, observations and perspectives.
	Retrieval ReflectionRetrieval
	// Directives are the standing instructions that apply.
	Directives []string
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
	// ReflectExpand returns the source context of specific memories.
	ReflectExpand(ctx context.Context, bankID string, ids []string) ([]ReflectCitation, error)
}
