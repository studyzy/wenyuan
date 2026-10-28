package banktemplate

import (
	"context"
	"encoding/json"
	"strings"
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

// BankPatch routes every key — the disposition traits and the reflect mission
// included — to the bank's config overrides, which is where the reference's
// template import persists the whole bank section.
func TestBankPatchRoutesEveryKeyToConfig(t *testing.T) {
	patch := BankPatch(map[string]any{
		"disposition_skepticism": float64(5),
		"reflect_mission":        "help",
		"retain_chunk_size":      float64(300),
	})
	if patch.Disposition != nil || patch.Mission != nil {
		t.Fatalf("no field belongs on the legacy columns: %+v", patch)
	}
	if patch.Config == nil {
		t.Fatal("every key must land in the config overrides")
	}
	want := map[string]any{
		"disposition_skepticism": float64(5),
		"reflect_mission":        "help",
		"retain_chunk_size":      float64(300),
	}
	for key, value := range want {
		if got := (*patch.Config)[key]; got != value {
			t.Fatalf("config[%q] = %#v, want %#v", key, got, value)
		}
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

// A template value the freshly-created bank already stored as a configuration
// override is left alone, so the create request's own fields win; a template
// value the bank does not carry is applied into the config column — the only
// place the template's disposition traits and reflect mission now live.
func TestCreateHookKeepsStoredConfigAndAppliesTheRest(t *testing.T) {
	store := newStore(t)
	stored := map[string]any{"disposition_skepticism": 1}
	if _, err := store.UpdateBank(context.Background(), "b1", storage.BankPatch{Config: &stored}); err != nil {
		t.Fatal(err)
	}
	hook := CreateHook(store, map[string]any{
		"version": "1",
		"bank": map[string]any{
			"disposition_skepticism": 5,
			"reflect_mission":        "help",
		},
	})
	if hook == nil {
		t.Fatal("a valid manifest must yield a hook")
	}
	if err := hook(context.Background(), "b1"); err != nil {
		t.Fatal(err)
	}
	bank, err := store.GetBank(context.Background(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if got := bank.Config["disposition_skepticism"]; got != 1 {
		t.Fatalf("disposition_skepticism = %#v, want the stored 1 to win", got)
	}
	if got := bank.Config["reflect_mission"]; got != "help" {
		t.Fatalf("reflect_mission = %#v, want the template's help", got)
	}
	if bank.Mission != "" {
		t.Fatalf("the legacy mission column must stay unset: %q", bank.Mission)
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

// A manifest that omits tags says nothing about them, so re-applying it must
// not clear the tags a previous apply stored.
func TestApplyResourcesPreservesTagsWhenManifestOmitsThem(t *testing.T) {
	store := newStore(t)
	withTags := Manifest{MentalModels: []MentalModel{
		{ID: "m", Name: "M", SourceQuery: "q", Tags: []string{"imported"}},
	}}
	if _, err := ApplyResources(context.Background(), store, "b1", withTags); err != nil {
		t.Fatal(err)
	}
	withoutTags := Manifest{MentalModels: []MentalModel{{ID: "m", Name: "M", SourceQuery: "q"}}}
	again, err := ApplyResources(context.Background(), store, "b1", withoutTags)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.MentalModelsUpdated) != 0 || len(again.OperationIDs) != 0 {
		t.Fatalf("an omitted tag list must not update the model: %+v", again)
	}
	model, err := store.GetMentalModel(context.Background(), "b1", "m")
	if err != nil {
		t.Fatal(err)
	}
	if len(model.Tags) != 1 || model.Tags[0] != "imported" {
		t.Fatalf("stored tags = %v, want [imported]", model.Tags)
	}
}

// A model created from a manifest that names no trigger stores the reference's
// full default trigger object, not an empty one.
func TestApplyResourcesStoresDefaultTrigger(t *testing.T) {
	store := newStore(t)
	manifest := Manifest{MentalModels: []MentalModel{{ID: "m", Name: "M", SourceQuery: "q"}}}
	if _, err := ApplyResources(context.Background(), store, "b1", manifest); err != nil {
		t.Fatal(err)
	}
	model, err := store.GetMentalModel(context.Background(), "b1", "m")
	if err != nil {
		t.Fatal(err)
	}
	if model.Trigger["mode"] != "full" || model.Trigger["refresh_after_consolidation"] != false ||
		model.Trigger["keep_trace"] != false {
		t.Fatalf("stored trigger = %#v, want the reference's full default", model.Trigger)
	}
	if len(model.Trigger) != 17 {
		t.Fatalf("stored trigger has %d members, want the full model", len(model.Trigger))
	}
}

// The structural messages are the reference's Pydantic output, served verbatim:
// the dotted loc, the "Value error, " prefix on a custom validator, and the
// duplicate-list message that the model's own list validator raises. A
// duplicate is a structural failure, not one of the semantic checks.
func TestValidateStructuralMessagesMatchReference(t *testing.T) {
	model := func(id string, maxTokens int) MentalModel {
		return MentalModel{ID: id, Name: "n", SourceQuery: "q", MaxTokens: maxTokens}
	}
	cases := []struct {
		name     string
		manifest Manifest
		want     string
	}{
		{"bad id", Manifest{Version: "1", MentalModels: []MentalModel{model("BAD", 2048)}},
			"mental_models.0.id: Value error, Mental model id 'BAD' must be alphanumeric lowercase with hyphens, starting with a letter or digit."},
		{"max tokens below the floor", Manifest{Version: "1", MentalModels: []MentalModel{model("m", 10)}},
			"mental_models.0.max_tokens: Input should be greater than or equal to 256"},
		{"max tokens above the ceiling", Manifest{Version: "1", MentalModels: []MentalModel{model("m", 9000)}},
			"mental_models.0.max_tokens: Input should be less than or equal to 8192"},
		{"duplicate model ids", Manifest{Version: "1", MentalModels: []MentalModel{model("m", 2048), model("m", 2048)}},
			"mental_models: Value error, Duplicate mental model ids: ['m']"},
		{"duplicate directive names", Manifest{Version: "1", Directives: []Directive{{Name: "d", Content: "c"}, {Name: "d", Content: "c2"}}},
			"directives: Value error, Duplicate directive names: ['d']"},
		{"bank disposition below the floor", Manifest{Version: "1", Bank: map[string]any{"disposition_empathy": 0}},
			"bank.disposition_empathy: Input should be greater than or equal to 1"},
		{"non-numeric version", Manifest{Version: "abc"},
			"version: Value error, version must be a numeric string, got 'abc'"},
	}
	for _, tc := range cases {
		structural, _ := Validate(tc.manifest)
		if len(structural) != 1 || structural[0] != tc.want {
			t.Errorf("%s: structural = %q, want [%q]", tc.name, structural, tc.want)
		}
	}
}

// The extraction-mode message renders the accepted modes the way Python's
// tuple repr does, because the reference interpolates its own tuple.
func TestValidateExtractionModeMessageMatchesReference(t *testing.T) {
	_, semantic := Validate(Manifest{Version: "1", Bank: map[string]any{"retain_extraction_mode": "bogus"}})
	want := "bank.retain_extraction_mode: must be one of ('concise', 'verbose', 'custom', 'verbatim', 'chunks'), got 'bogus'"
	if len(semantic) != 1 || semantic[0] != want {
		t.Fatalf("semantic = %q, want [%q]", semantic, want)
	}
}

// A version past the supported one is reported with the reference's own wording
// — the product name included, because that string is what a client shows its
// operator. The reference interpolates its own name there
// (api/http.py:4096-4097); this service names itself instead.
func TestValidateUnsupportedVersionNamesTheReference(t *testing.T) {
	structural, _ := Validate(Manifest{Version: "2"})
	want := "version: Value error, version '2' is not supported by this server (max supported: 1). Please upgrade WenYuan."
	if len(structural) != 1 || structural[0] != want {
		t.Fatalf("structural = %q, want [%q]", structural, want)
	}
}

// `int(v)` in the reference is a Python integer, which has no width: a version
// whose digits overflow int64 is still a number there, so it reaches the
// upper-bound check and is reported as unsupported rather than as non-numeric.
// Parsing it with strconv.Atoi reports "must be a numeric string" instead.
func TestValidateVersionIsParsedAtArbitraryPrecision(t *testing.T) {
	cases := []struct {
		version string
		want    string
	}{
		{"99999999999999999999",
			"version: Value error, version '99999999999999999999' is not supported by this server (max supported: 1). Please upgrade WenYuan."},
		{"9223372036854775808",
			"version: Value error, version '9223372036854775808' is not supported by this server (max supported: 1). Please upgrade WenYuan."},
		{"-99999999999999999999", "version: Value error, version must be >= 1"},
	}
	for _, tc := range cases {
		structural, _ := Validate(Manifest{Version: tc.version})
		if len(structural) != 1 || structural[0] != tc.want {
			t.Errorf("version %q: structural = %q, want [%q]", tc.version, structural, tc.want)
		}
	}
}

// The trigger object is serialised in the reference's declaration order, not in
// Go's sorted map order: `GET /v1/default/banks/{id}/export` renders this object
// verbatim in its response body, so the key order is observable bytes.
//
// The table lists the fields of the reference's MentalModelTrigger
// (api/http.py:3199-3300), which is live-verified against the reference service.
// Losing the ordering fails this case: encoding/json sorts a map, so the trigger
// comes back as `budget, exclude_mental_model_ids, exclude_mental_models, …`.
func TestManifestRendersTriggerInDeclarationOrder(t *testing.T) {
	want := []string{
		"budget", "mode", "refresh_after_consolidation", "refresh_cron",
		"min_refresh_interval_seconds", "fact_types", "exclude_mental_models",
		"exclude_mental_model_ids", "tags_match", "tag_groups", "include_chunks",
		"recall_max_tokens", "recall_chunks_max_tokens",
		"reflect_search_observations_max_tokens",
		"reflect_search_observations_include_entities", "response_schema", "keep_trace",
	}
	if len(templateTriggerFieldOrder) != len(want) {
		t.Fatalf("trigger order = %v, want %v", templateTriggerFieldOrder, want)
	}
	for index, field := range want {
		if templateTriggerFieldOrder[index] != field {
			t.Fatalf("position %d = %q, want %q", index, templateTriggerFieldOrder[index], field)
		}
	}

	// The order is applied on the wire, and only to the keys present.
	trigger := map[string]any{"mode": "full", "keep_trace": false, "budget": nil}
	manifest := Manifest{
		Version:      "1",
		MentalModels: []MentalModel{{ID: "mm-x", Name: "n", SourceQuery: "q", MaxTokens: 2048, Trigger: trigger}},
	}
	encoded, err := manifest.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		MentalModels []struct {
			Trigger map[string]json.RawMessage `json:"trigger"`
		} `json:"mental_models"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	// Re-read the order from the raw bytes, because a map loses it.
	text := string(encoded)
	budget := strings.Index(text, `"budget"`)
	mode := strings.Index(text, `"mode"`)
	keep := strings.Index(text, `"keep_trace"`)
	if budget < 0 || budget >= mode || mode >= keep {
		t.Fatalf("trigger keys out of declaration order: budget=%d mode=%d keep_trace=%d in %s", budget, mode, keep, text)
	}
	// No key is invented or dropped.
	if len(decoded.MentalModels) != 1 || len(decoded.MentalModels[0].Trigger) != 3 {
		t.Fatalf("trigger = %+v, want exactly the three supplied keys", decoded.MentalModels[0].Trigger)
	}
}
