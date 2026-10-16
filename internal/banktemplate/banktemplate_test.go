package banktemplate

import (
	"context"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

func newStore(t *testing.T) *storage.MemoryStore {
	t.Helper()
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.Background(), storage.Bank{BankID: "b1", Name: "One"}); err != nil {
		t.Fatal(err)
	}
	return store
}

// Load parses a decoded manifest and rejects an unsupported version as a
// structural failure rather than a content one.
func TestLoadRejectsUnsupportedVersionAsStructural(t *testing.T) {
	_, err := Load(map[string]any{"version": "999"})
	if err == nil {
		t.Fatal("want an error for an unsupported version")
	}
	verr, ok := err.(*ValidationError)
	if !ok || !verr.Structural {
		t.Fatalf("err = %#v, want a structural *ValidationError", err)
	}
}

// A content failure is reported as semantic, so a caller can tell the two
// kinds apart when logging a bad server-owned template.
func TestLoadRejectsEmptyNameAsSemantic(t *testing.T) {
	_, err := Load(map[string]any{
		"version": "1",
		"mental_models": []any{
			map[string]any{"id": "goals", "name": "  ", "source_query": "what goals?"},
		},
	})
	verr, ok := err.(*ValidationError)
	if !ok || verr.Structural {
		t.Fatalf("err = %#v, want a semantic *ValidationError", err)
	}
}

// A valid manifest round-trips, and Parse keeps the numeric max_tokens typed.
func TestLoadParsesValidManifest(t *testing.T) {
	manifest, err := Load(map[string]any{
		"version":       "1",
		"mental_models": []any{map[string]any{"id": "goals", "name": "Goals", "source_query": "q", "max_tokens": float64(1024)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.MentalModels) != 1 || manifest.MentalModels[0].MaxTokens != 1024 {
		t.Fatalf("models = %+v", manifest.MentalModels)
	}
}

// BankPatch routes the row-backed fields to their own columns and leaves every
// other key a configuration override.
func TestBankPatchRoutesTraitsAndMission(t *testing.T) {
	patch := BankPatch(map[string]any{
		"disposition_skepticism": float64(5),
		"reflect_mission":        "help",
		"retain_chunk_size":      float64(300),
	})
	if patch.Disposition == nil || (*patch.Disposition)["skepticism"] != 5 {
		t.Fatalf("disposition = %#v", patch.Disposition)
	}
	if patch.Mission == nil || *patch.Mission != "help" {
		t.Fatalf("mission = %#v", patch.Mission)
	}
	if patch.Config == nil || (*patch.Config)["retain_chunk_size"] == nil {
		t.Fatalf("config = %#v", patch.Config)
	}
	if _, leaked := (*patch.Config)["disposition_skepticism"]; leaked {
		t.Fatalf("a trait leaked into the config overrides: %#v", patch.Config)
	}
}

// CreateHook builds a hook that provisions each new bank, and a nil hook when
// the manifest is empty so an unconfigured store is left untouched.
func TestCreateHookProvisionsAndIsNilWhenUnset(t *testing.T) {
	if hook := CreateHook(newStore(t), nil); hook != nil {
		t.Fatal("an unset manifest must yield a nil hook")
	}
	if hook := CreateHook(newStore(t), map[string]any{}); hook != nil {
		t.Fatal("an empty manifest must yield a nil hook")
	}

	store := newStore(t)
	hook := CreateHook(store, map[string]any{
		"version":    "1",
		"directives": []any{map[string]any{"name": "tone", "content": "be nice"}},
	})
	if hook == nil {
		t.Fatal("a valid manifest must yield a hook")
	}
	if err := hook(context.Background(), "b1"); err != nil {
		t.Fatal(err)
	}
	directives, err := store.ListDirectives(context.Background(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if len(directives) != 1 {
		t.Fatalf("directives = %+v", directives)
	}
}

// A malformed manifest yields a nil hook, so a bad server-level setting never
// blocks bank creation.
func TestCreateHookSwallowsMalformedManifest(t *testing.T) {
	hook := CreateHook(newStore(t), map[string]any{"version": "999"})
	if hook != nil {
		t.Fatal("a malformed manifest must yield a nil hook")
	}
}

// ApplyResources creates the manifest's models and directives and queues a
// content refresh per model, then leaves an unchanged re-apply alone.
func TestApplyResourcesCreatesAndIsIdempotent(t *testing.T) {
	store := newStore(t)
	manifest := Manifest{
		MentalModels: []MentalModel{{ID: "goals", Name: "Goals", SourceQuery: "what goals?"}},
		Directives:   []Directive{{Name: "tone", Content: "be nice"}},
	}

	result, err := ApplyResources(context.Background(), store, "b1", manifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.MentalModelsCreated) != 1 || len(result.DirectivesCreated) != 1 {
		t.Fatalf("first apply = %+v", result)
	}
	if len(result.OperationIDs) != 1 {
		t.Fatalf("first apply queued %d refreshes, want 1", len(result.OperationIDs))
	}

	again, err := ApplyResources(context.Background(), store, "b1", manifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.MentalModelsCreated) != 0 || len(again.MentalModelsUpdated) != 0 {
		t.Fatalf("an unchanged re-apply wrote a model: %+v", again)
	}
	if len(again.DirectivesCreated) != 0 || len(again.DirectivesUpdated) != 0 {
		t.Fatalf("an unchanged re-apply wrote a directive: %+v", again)
	}
	if len(again.OperationIDs) != 0 {
		t.Fatalf("an unchanged re-apply queued work: %+v", again)
	}
}

// Apply writes the manifest's config and its resources.
func TestApplyWritesConfigAndResources(t *testing.T) {
	store := newStore(t)
	result, err := Apply(context.Background(), store, "b1", Manifest{
		Bank:       map[string]any{"retain_chunk_size": float64(300)},
		Directives: []Directive{{Name: "tone", Content: "be nice"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.ConfigApplied {
		t.Fatal("config was not reported as applied")
	}
	bank, err := store.GetBank(context.Background(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if bank.Config["retain_chunk_size"] != float64(300) {
		t.Fatalf("bank config = %#v", bank.Config)
	}
	directives, err := store.ListDirectives(context.Background(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if len(directives) != 1 {
		t.Fatalf("directives = %+v", directives)
	}
}
