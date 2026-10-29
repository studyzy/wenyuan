package config

import "testing"

// The mental-model refresh temperature resolves from its own key, inheriting
// the consolidation temperature when the key is unset — the inheritance the
// reference applies because those structured edit calls used the consolidation
// temperature before the key existed (config.py:229-232,
// _resolve_inherited_temperature).
func TestMentalModelRefreshTemperatureInheritsConsolidation(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want *float64
	}{
		{"unset everywhere", nil, ptrFloat64(DefaultTemperatureConsolidation)},
		{"inherits consolidation env", map[string]string{
			EnvLLMTemperatureConsolidation: "0.4",
		}, ptrFloat64(0.4)},
		{"inherits global through consolidation", map[string]string{
			EnvLLMTemperature: "0.7",
		}, ptrFloat64(0.7)},
		{"explicit override", map[string]string{
			EnvLLMTemperatureConsolidation:      "0.4",
			EnvLLMTemperatureMentalModelRefresh: "1.5",
		}, ptrFloat64(1.5)},
		{"explicit omit", map[string]string{
			EnvLLMTemperatureConsolidation:      "0.4",
			EnvLLMTemperatureMentalModelRefresh: "omit",
		}, nil},
		{"inherits consolidation omit", map[string]string{
			EnvLLMTemperatureConsolidation: "omit",
		}, nil},
	}
	for _, tc := range cases {
		cfg := ResolveScopes(envMap(tc.env))
		got := cfg.TemperatureByOperation["mental_model_refresh"]
		if tc.want == nil {
			if got != nil {
				t.Fatalf("%s: mental_model_refresh = %v, want omitted", tc.name, *got)
			}
			continue
		}
		if got == nil || *got != *tc.want {
			t.Fatalf("%s: mental_model_refresh = %v, want %v", tc.name, got, *tc.want)
		}
	}
}

// The key is retained in the resolved scopes like every other temperature key,
// so an explicit value is never dropped from the raw record.
func TestMentalModelRefreshTemperatureKeyRetained(t *testing.T) {
	cfg := ResolveScopes(envMap(map[string]string{
		EnvLLMTemperatureMentalModelRefresh: "0.3",
	}))
	if cfg.Raw[EnvLLMTemperatureMentalModelRefresh] != "0.3" {
		t.Fatalf("raw = %v, want the configured value retained", cfg.Raw)
	}
}

func ptrFloat64(v float64) *float64 { return &v }
