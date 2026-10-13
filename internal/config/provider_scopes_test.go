package config

import "testing"

// test-strict-schema-per-op-override: a per-operation strict-schema false
// overrides a global true while other operations keep the global value.
func TestStrictSchemaPerOperationOverridesGlobal(t *testing.T) {
	cfg := ResolveScopes(envMap(map[string]string{
		EnvLLMStrictSchema:        "true",
		EnvLLMStrictSchemaReflect: "false",
	}))
	if !cfg.StrictSchema {
		t.Fatal("global strict schema should be true")
	}
	if !cfg.StrictSchemaByOperation["retain"] {
		t.Fatal("retain should inherit the global true")
	}
	if cfg.StrictSchemaByOperation["reflect"] {
		t.Fatal("explicit per-operation false must override the global true")
	}
	if !cfg.StrictSchemaByOperation["consolidation"] {
		t.Fatal("consolidation should inherit the global true")
	}
}

// test-schema-capability-flags-strict-parse: an ambiguous capability-flag
// value is rejected.
func TestCapabilityFlagStrictParse(t *testing.T) {
	for _, value := range []string{"yes", "on", "1"} {
		got, err := ParseCapabilityFlag(EnvLLMSupportsMaxItems, value)
		if err != nil || !got {
			t.Fatalf("value %q should parse true: got %v err %v", value, got, err)
		}
	}
	for _, value := range []string{"maybe", "sometimes", "2"} {
		if _, err := ParseCapabilityFlag(EnvLLMSupportsMaxItems, value); err == nil {
			t.Fatalf("ambiguous value %q must be rejected", value)
		}
	}
	if _, err := ParseCapabilityFlag(EnvLLMSupportsMaxItems, ""); err == nil {
		t.Fatal("blank must be rejected")
	}
}

// test-temperature-omit-sentinel: the omit sentinel removes the parameter for
// unoverridden keys and keeps an explicit value for an overridden key.
func TestTemperatureOmitSentinel(t *testing.T) {
	cfg := ResolveScopes(envMap(map[string]string{
		EnvLLMTemperature:       "omit",
		EnvLLMTemperatureRetain: "0.5",
	}))
	if cfg.TemperatureByOperation["retain"] == nil || *cfg.TemperatureByOperation["retain"] != 0.5 {
		t.Fatalf("explicit retain temperature = %v, want 0.5", cfg.TemperatureByOperation["retain"])
	}
	if cfg.TemperatureByOperation["reflect"] != nil {
		t.Fatalf("reflect should inherit the global omit and be removed, got %v", *cfg.TemperatureByOperation["reflect"])
	}
	if cfg.TemperatureByOperation["verification"] != nil {
		t.Fatal("verification should inherit the global omit")
	}
}

// test-temperature-defaults-per-op: an unset temperature resolves to the
// task-appropriate built-in default.
func TestTemperatureDefaultsPerOperation(t *testing.T) {
	cfg := ResolveScopes(envMap(nil))
	cases := map[string]float64{
		"verification":  DefaultTemperatureVerification,
		"consolidation": DefaultTemperatureConsolidation,
		"retain":        DefaultTemperatureRetain,
		"reflect":       DefaultTemperatureReflect,
	}
	for op, want := range cases {
		got := cfg.TemperatureByOperation[op]
		if got == nil || *got != want {
			t.Fatalf("%s temperature = %v, want %v", op, got, want)
		}
	}
}

// A per-operation temperature override wins for its operation only.
func TestTemperaturePerOperationOverride(t *testing.T) {
	cfg := ResolveScopes(envMap(map[string]string{
		EnvLLMTemperature:        "0.1",
		EnvLLMTemperatureReflect: "0.7",
	}))
	if got := cfg.TemperatureByOperation["reflect"]; got == nil || *got != 0.7 {
		t.Fatalf("reflect = %v, want 0.7", got)
	}
	if got := cfg.TemperatureByOperation["retain"]; got == nil || *got != 0.1 {
		t.Fatalf("retain = %v, want the global 0.1", got)
	}
}

// An unset reasoning effort is not normalized to a concrete level.
func TestUnsetEffortNotInvented(t *testing.T) {
	cfg := FromEnv(envMap(nil))
	if cfg.Provider.ReasoningEffort != "" {
		t.Fatalf("unset reasoning effort became %q", cfg.Provider.ReasoningEffort)
	}
}

// Every scope key is retained during resolution.
func TestEveryScopeKeyRetained(t *testing.T) {
	values := map[string]string{}
	for _, key := range ScopeKeys() {
		values[key] = "1"
	}
	cfg := ResolveScopes(envMap(values))
	if len(cfg.Raw) != len(ScopeKeys()) {
		t.Fatalf("retained %d scope keys, want %d", len(cfg.Raw), len(ScopeKeys()))
	}
}

// The documented per-operation key names are exactly the contract spelling.
func TestScopeKeyNames(t *testing.T) {
	if StrictSchemaKeyFor("reflect") != EnvLLMStrictSchemaReflect {
		t.Fatalf("strict-schema reflect key = %q", StrictSchemaKeyFor("reflect"))
	}
	if TemperatureKeyFor("consolidation") != EnvLLMTemperatureConsolidation {
		t.Fatalf("temperature consolidation key = %q", TemperatureKeyFor("consolidation"))
	}
	if TemperatureKeyFor("verification") != EnvLLMTemperatureVerification {
		t.Fatalf("temperature verification key = %q", TemperatureKeyFor("verification"))
	}
}
