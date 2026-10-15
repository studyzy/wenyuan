package consolidation

import (
	"context"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/provider"
)

// A strategy that claims a scope overrides the mission and the limits there, and
// the overriding mission reaches the prompt the model is sent.
func TestStrategyMissionReachesPrompt(t *testing.T) {
	store := &stubStore{
		facts: []Fact{{ID: "f1", Text: "a fact", Tags: []string{"team:exec"}}},
		config: map[string]any{
			"observations_mission": "bank-wide mission",
			"consolidation_strategies": []any{
				map[string]any{
					"scopes": []any{
						map[string]any{"tags": []any{"team:*"}},
					},
					"observations_mission":       "the team mission",
					"max_observations_per_scope": 3,
				},
			},
		},
	}
	model := &stubProvider{name: "openai"}
	if _, err := New(model, store).Run(context.Background(), "b1"); err != nil {
		t.Fatalf("run: %v", err)
	}
	user := model.lastRequest().Messages[1].Content
	if !strings.Contains(user, "the team mission") {
		t.Fatalf("prompt does not carry the strategy mission:\n%s", user)
	}
	if strings.Contains(user, "bank-wide mission") {
		t.Fatalf("prompt carries the bank-wide mission despite the strategy override:\n%s", user)
	}
	if store.poolLimit != 3 {
		t.Fatalf("observation pool limit = %d, want the strategy cap 3", store.poolLimit)
	}
}

// A scope whose observation cap is reached is told to update only, and a create
// the model still emits is dropped rather than written past the cap.
func TestPerScopeLimitRespected(t *testing.T) {
	store := &stubStore{
		facts: []Fact{{ID: "f1", Text: "a new fact"}},
		observations: []Observation{
			{ID: "o1", Text: "existing one"},
			{ID: "o2", Text: "existing two"},
		},
		config: map[string]any{"max_observations_per_scope": 2},
	}
	model := &stubProvider{
		name: "openai",
		response: provider.CompletionResult{Structured: map[string]any{
			"creates": []any{map[string]any{"text": "brand new conclusion", "source_fact_ids": []any{"f1"}}},
		}},
	}
	result, err := New(model, store).Run(context.Background(), "b1")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.created) != 0 {
		t.Fatalf("created %d observations past a full scope", len(store.created))
	}
	if result.Created != 0 || result.Skipped == 0 {
		t.Fatalf("result = %+v, want no creates and at least one skip", result)
	}
	if user := model.lastRequest().Messages[1].Content; !strings.Contains(user, "OBSERVATION LIMIT REACHED") {
		t.Fatalf("prompt does not carry the capacity note:\n%s", user)
	}
}

// The per-observation source-fact budget truncates the evidence shown for one
// observation, so a tightly configured budget shows fewer source facts.
func TestSourceFactBudgetTruncatesEvidence(t *testing.T) {
	texts := []string{"first source fact", "second source fact", "third source fact"}
	budget := 4 // fits the first 3-token fact, not a second with it
	store := &stubStore{
		facts: []Fact{{ID: "f1", Text: "a fact"}},
		observations: []Observation{{
			ID: "o1", Text: "an observation",
			SourceIDs:   []string{"s1", "s2", "s3"},
			SourceTexts: texts,
		}},
		config: map[string]any{"consolidation_source_facts_max_tokens_per_observation": budget},
	}
	model := &stubProvider{name: "openai"}
	if _, err := New(model, store).Run(context.Background(), "b1"); err != nil {
		t.Fatalf("run: %v", err)
	}
	user := model.lastRequest().Messages[1].Content
	if !strings.Contains(user, texts[0]) {
		t.Fatalf("prompt dropped the first source fact:\n%s", user)
	}
	if strings.Contains(user, texts[1]) || strings.Contains(user, texts[2]) {
		t.Fatalf("prompt carried source facts past the %d-token budget:\n%s", budget, user)
	}
}

// A strategy pattern claims a scope under its own match mode, and the first
// claiming strategy wins whole.
func TestSettingsForScope(t *testing.T) {
	settings := settingsFromConfig(map[string]any{
		"observations_mission": "bank-wide",
		"consolidation_strategies": []any{
			map[string]any{
				"scopes":               []any{map[string]any{"tags": []any{"company:acme"}, "tags_match": "exact"}},
				"observations_mission": "acme only",
			},
			map[string]any{
				"scopes":                     []any{map[string]any{"tags": []any{"company:*"}}},
				"max_observations_per_scope": 7,
			},
		},
	})
	// The exact pattern claims {company:acme} and wins whole: the mission is its
	// own and the cap is the bank-wide value, not the later strategy's 7.
	scoped := settings.forScope([]string{"company:acme"})
	if scoped.Mission != "acme only" {
		t.Fatalf("mission = %q, want the first strategy's", scoped.Mission)
	}
	if scoped.MaxObservationsPerScope != DefaultMaxObservationsPerScope {
		t.Fatalf("cap = %d, want the bank-wide default", scoped.MaxObservationsPerScope)
	}
	// A different acme-company scope is not exactly {company:acme}, so the first
	// strategy does not claim it; the second does.
	other := settings.forScope([]string{"company:acme", "team:exec"})
	if other.Mission != "bank-wide" {
		t.Fatalf("mission = %q, want the bank-wide value", other.Mission)
	}
	if other.MaxObservationsPerScope != 7 {
		t.Fatalf("cap = %d, want the second strategy's 7", other.MaxObservationsPerScope)
	}
}

// A malformed strategy is dropped rather than failing the pass, and defaults
// hold when nothing is configured.
func TestSettingsFromConfigDefaults(t *testing.T) {
	settings := settingsFromConfig(map[string]any{
		"consolidation_strategies":     "not-a-list",
		"max_observations_per_scope":   12,
		"consolidation_llm_batch_size": 4,
	})
	if len(settings.Strategies) != 0 {
		t.Fatalf("strategies = %v, want none", settings.Strategies)
	}
	if settings.MaxObservationsPerScope != 12 || settings.LLMBatchSize != 4 {
		t.Fatalf("settings = %+v", settings)
	}
	if settings.Mission != DefaultMission {
		t.Fatalf("mission = %q, want the default", settings.Mission)
	}
}
