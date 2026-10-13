package config

import "testing"

// test-consolidation-scope-fallback: with every consolidation key unset, the
// role resolves to the global provider settings.
func TestConsolidationScopeFallsBackToGlobal(t *testing.T) {
	global := FromEnv(envMap(map[string]string{
		EnvLLMProvider:        "openai",
		EnvLLMModel:           "global-model",
		EnvLLMMaxRetries:      "5",
		EnvLLMTimeout:         "90",
		EnvLLMReasoningEffort: "medium",
	}))
	scope := ResolveConsolidation(envMap(nil))
	if got := scope.EffectiveProvider(global.Provider); got != "openai" {
		t.Fatalf("effective provider = %q, want the global", got)
	}
	if got := scope.EffectiveModel(global.Provider); got != "global-model" {
		t.Fatalf("effective model = %q, want the global", got)
	}
	if got := scope.EffectiveMaxRetries(global.Provider); got != 5 {
		t.Fatalf("effective retries = %d, want the global", got)
	}
	if got := scope.EffectiveTimeout(global.Provider); got.Seconds() != 90 {
		t.Fatalf("effective timeout = %s, want the global", got)
	}
	if got := scope.EffectiveReasoningEffort(global.Provider); got != "medium" {
		t.Fatalf("effective effort = %q, want the global", got)
	}
}

// test-consolidation-scope-independent: a set consolidation provider or model
// is used for that scope while other scopes keep the global value.
func TestConsolidationScopeIndependent(t *testing.T) {
	global := FromEnv(envMap(map[string]string{
		EnvLLMProvider: "openai",
		EnvLLMModel:    "global-model",
	}))
	scope := ResolveConsolidation(envMap(map[string]string{
		EnvConsolidationProvider: "none",
		EnvConsolidationModel:    "consolidation-model",
	}))
	if got := scope.EffectiveProvider(global.Provider); got != "none" {
		t.Fatalf("consolidation provider = %q", got)
	}
	if got := scope.EffectiveModel(global.Provider); got != "consolidation-model" {
		t.Fatalf("consolidation model = %q", got)
	}
	// The global scope keeps its own value.
	if global.Provider.Provider != "openai" || global.Provider.Model != "global-model" {
		t.Fatal("the global scope was changed by the consolidation scope")
	}
}

// test-consolidation-scope-tuning: each unset consolidation tuning key
// inherits the corresponding global value.
func TestConsolidationScopeTuningInherits(t *testing.T) {
	global := FromEnv(envMap(map[string]string{EnvLLMMaxRetries: "7"}))
	scope := ResolveConsolidation(envMap(map[string]string{
		EnvConsolidationTimeout: "45",
	}))
	if got := scope.EffectiveTimeout(global.Provider); got.Seconds() != 45 {
		t.Fatalf("consolidation timeout = %s", got)
	}
	if got := scope.EffectiveMaxRetries(global.Provider); got != 7 {
		t.Fatalf("consolidation retries = %d, want the inherited 7", got)
	}
}

// The role-specific controls resolve without requiring the consolidation
// behaviour path to exist.
func TestConsolidationRoleSpecificControls(t *testing.T) {
	scope := ResolveConsolidation(envMap(map[string]string{
		EnvConsolidationBatchSize:   "10",
		EnvConsolidationParallelism: "3",
	}))
	if scope.BatchSize == nil || *scope.BatchSize != 10 {
		t.Fatalf("batch size = %v", scope.BatchSize)
	}
	if scope.Parallelism == nil || *scope.Parallelism != 3 {
		t.Fatalf("parallelism = %v", scope.Parallelism)
	}
}

// Unset must stay distinguishable from a concrete reasoning level.
func TestConsolidationUnsetStaysDistinguishable(t *testing.T) {
	scope := ResolveConsolidation(envMap(nil))
	if scope.ReasoningEffort != nil {
		t.Fatalf("unset consolidation effort = %v, want nil", *scope.ReasoningEffort)
	}
	if scope.Provider != nil || scope.Model != nil || scope.MaxRetries != nil {
		t.Fatal("unset consolidation fields must be nil")
	}
}

// Every consolidation key is retained during resolution.
func TestEveryConsolidationKeyRetained(t *testing.T) {
	values := map[string]string{}
	for _, key := range ConsolidationKeys() {
		values[key] = "1"
	}
	scope := ResolveConsolidation(envMap(values))
	if len(scope.Raw) != len(ConsolidationKeys()) {
		t.Fatalf("retained %d consolidation keys, want %d", len(scope.Raw), len(ConsolidationKeys()))
	}
}
