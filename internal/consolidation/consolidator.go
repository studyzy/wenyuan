// Package consolidation turns raw facts into consolidated observations.
//
// A consolidation pass reads the facts of a bank that have not yet been folded
// into an observation, pools the observations that already exist, and asks the
// model to decide which facts support an existing observation, which form a new
// one, and which supersede one outright. The model answers with three arrays —
// creates, updates, deletes — and the pass applies them to storage.
//
// Observations are memory units of type "observation", so a recall can rank
// them beside the raw facts they summarize, and each is linked to the facts it
// rests on through the observation-source table. This is distinct from a mental
// model, which is a user-defined query refreshed on demand rather than a
// conclusion the engine derives bottom-up.
package consolidation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/studyzy/wenyuan/internal/provider"
)

// ErrNoModel reports a consolidation pass attempted with no model configured.
// The pass is model-driven; without one there is nothing to decide what follows
// from the facts.
var ErrNoModel = errors.New("consolidation requires a configured model")

// DefaultMaxFacts bounds how many facts one pass considers. A pass that read
// every fact of a large bank would exceed the context window and spend the
// caller's budget for no better decision, so the newest facts are folded first
// and the rest wait for the next pass.
const DefaultMaxFacts = 200

// DefaultMaxObservations bounds the pool of existing observations offered as
// merge candidates. Newer observations are the likelier matches.
const DefaultMaxObservations = 100

// Fact is one raw memory unit offered as new evidence.
type Fact struct {
	ID            string
	Text          string
	OccurredStart string
	OccurredEnd   string
	MentionedAt   string
}

// Observation is one existing consolidated conclusion offered as a merge
// candidate, or produced by the pass.
type Observation struct {
	ID          string
	Text        string
	ProofCount  int
	SourceIDs   []string
	OccurredAt  string
	MentionedAt string
}

// Create is a new observation the model decided to record.
type Create struct {
	Text      string   `json:"text"`
	SourceIDs []string `json:"source_fact_ids"`
	Reason    string   `json:"reason"`
}

// Update rewrites one existing observation and attaches new evidence to it.
type Update struct {
	ObservationID string   `json:"observation_id"`
	Text          string   `json:"text"`
	SourceIDs     []string `json:"source_fact_ids"`
	Reason        string   `json:"reason"`
}

// Delete removes one observation the new facts superseded outright.
type Delete struct {
	ObservationID string `json:"observation_id"`
	Reason        string `json:"reason"`
}

// Decision is one pass's answer.
type Decision struct {
	Creates []Create `json:"creates"`
	Updates []Update `json:"updates"`
	Deletes []Delete `json:"deletes"`
}

// Result reports what a pass applied.
type Result struct {
	Created int
	Updated int
	Deleted int
	Usage   provider.Usage
}

// Store is what the pass needs from storage. It is an interface so the pass can
// be exercised without a database, and so the storage area keeps owning the
// queries.
type Store interface {
	// ConsolidationFacts returns the newest raw facts of a bank that have not
	// yet been folded into an observation.
	ConsolidationFacts(ctx context.Context, bankID string, limit int) ([]Fact, error)
	// ConsolidationObservations returns the bank's existing observations,
	// newest first, as merge candidates.
	ConsolidationObservations(ctx context.Context, bankID string, limit int) ([]Observation, error)
	// CreateObservation records a new observation resting on the given facts.
	CreateObservation(ctx context.Context, bankID, text string, sourceIDs []string) (string, error)
	// UpdateObservation rewrites an observation and attaches more evidence.
	UpdateObservation(ctx context.Context, bankID, observationID, text string, sourceIDs []string) error
	// DeleteObservation removes one observation.
	DeleteObservation(ctx context.Context, bankID, observationID string) error
	// MarkConsolidated records that the given facts have been folded, so the
	// next pass does not reconsider them.
	MarkConsolidated(ctx context.Context, bankID string, factIDs []string) error
}

// Consolidator runs one pass.
type Consolidator struct {
	provider        provider.Provider
	store           Store
	maxFacts        int
	maxObservations int
	scope           string
}

// New builds a consolidator over the provider and store.
func New(p provider.Provider, store Store) *Consolidator {
	return &Consolidator{
		provider:        p,
		store:           store,
		maxFacts:        DefaultMaxFacts,
		maxObservations: DefaultMaxObservations,
		scope:           "consolidate",
	}
}

// Available reports whether a model is configured.
func (c *Consolidator) Available() bool {
	if c == nil || c.provider == nil {
		return false
	}
	// The no-model selection is a real provider that reports its unavailability
	// through VerifyConnection rather than by being absent.
	return c.provider.Name() != "none"
}

// Run performs one consolidation pass over a bank. It returns the counts it
// applied. With no model configured it reports ErrNoModel rather than inventing
// observations.
func (c *Consolidator) Run(ctx context.Context, bankID string) (Result, error) {
	if !c.Available() {
		return Result{}, ErrNoModel
	}
	facts, err := c.store.ConsolidationFacts(ctx, bankID, c.maxFacts)
	if err != nil {
		return Result{}, fmt.Errorf("read facts: %w", err)
	}
	if len(facts) == 0 {
		return Result{}, nil
	}
	observations, err := c.store.ConsolidationObservations(ctx, bankID, c.maxObservations)
	if err != nil {
		return Result{}, fmt.Errorf("read observations: %w", err)
	}

	response, err := c.provider.Complete(ctx, provider.CompletionRequest{
		Messages: []provider.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: buildInput(facts, observations)},
		},
		ResponseSchema: decisionSchema(),
		Scope:          c.scope,
	})
	if err != nil {
		return Result{}, fmt.Errorf("consolidation call failed: %w", err)
	}
	decision, err := parseDecision(response.Structured, response.Content)
	if err != nil {
		return Result{}, err
	}

	result := Result{Usage: response.Usage}
	factIDs := make([]string, 0, len(facts))
	for _, fact := range facts {
		factIDs = append(factIDs, fact.ID)
	}
	touched := map[string]bool{}
	for _, create := range decision.Creates {
		if strings.TrimSpace(create.Text) == "" {
			continue
		}
		sources := validSourceIDs(create.SourceIDs, factIDs)
		if _, err := c.store.CreateObservation(ctx, bankID, create.Text, sources); err != nil {
			return result, fmt.Errorf("create observation: %w", err)
		}
		markTouched(touched, sources)
		result.Created++
	}
	for _, update := range decision.Updates {
		if update.ObservationID == "" || strings.TrimSpace(update.Text) == "" {
			continue
		}
		sources := validSourceIDs(update.SourceIDs, factIDs)
		if err := c.store.UpdateObservation(ctx, bankID, update.ObservationID, update.Text, sources); err != nil {
			return result, fmt.Errorf("update observation: %w", err)
		}
		markTouched(touched, sources)
		result.Updated++
	}
	for _, del := range decision.Deletes {
		// A delete entry without a target names nothing and would remove an
		// arbitrary observation, so it is skipped rather than guessed at.
		if del.ObservationID == "" {
			continue
		}
		if err := c.store.DeleteObservation(ctx, bankID, del.ObservationID); err != nil {
			return result, fmt.Errorf("delete observation: %w", err)
		}
		result.Deleted++
	}
	// Facts no decision referenced are not marked, so the next pass offers them
	// again rather than losing them.
	consumed := make([]string, 0, len(touched))
	for _, id := range factIDs {
		if touched[id] {
			consumed = append(consumed, id)
		}
	}
	if len(consumed) > 0 {
		if err := c.store.MarkConsolidated(ctx, bankID, consumed); err != nil {
			return result, fmt.Errorf("mark consolidated: %w", err)
		}
	}
	return result, nil
}

// markTouched records which facts a decision actually consumed.
func markTouched(touched map[string]bool, ids []string) {
	for _, id := range ids {
		touched[id] = true
	}
}

// validSourceIDs keeps only the source ids naming a fact in this batch, so a
// model that invents an identifier cannot link an observation to nothing.
func validSourceIDs(ids []string, known []string) []string {
	allowed := make(map[string]bool, len(known))
	for _, id := range known {
		allowed[id] = true
	}
	kept := make([]string, 0, len(ids))
	for _, id := range ids {
		if allowed[id] {
			kept = append(kept, id)
		}
	}
	return kept
}

// parseDecision reads the model's answer. The structured form is preferred; the
// prose form is accepted so a model that ignored the schema still contributes.
func parseDecision(structured any, content string) (Decision, error) {
	if structured != nil {
		encoded, err := json.Marshal(structured)
		if err == nil {
			var decision Decision
			if err := json.Unmarshal(encoded, &decision); err == nil {
				decision.normalize()
				return decision, nil
			}
		}
	}
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return Decision{}, nil
	}
	if start := strings.Index(trimmed, "{"); start >= 0 {
		if end := strings.LastIndex(trimmed, "}"); end > start {
			var decision Decision
			if err := json.Unmarshal([]byte(trimmed[start:end+1]), &decision); err == nil {
				decision.normalize()
				return decision, nil
			}
		}
	}
	// A model that answered in neither form contributed nothing; that is not an
	// error, because a pass with no decision simply leaves the facts for later.
	return Decision{}, nil
}

// normalize replaces nil slices with empty ones so a reader never has to tell
// an absent array from an empty one.
func (d *Decision) normalize() {
	if d.Creates == nil {
		d.Creates = []Create{}
	}
	if d.Updates == nil {
		d.Updates = []Update{}
	}
	if d.Deletes == nil {
		d.Deletes = []Delete{}
	}
}
