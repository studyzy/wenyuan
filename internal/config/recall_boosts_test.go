package config

import "testing"

// ts-0024: the recall-strategy-boost environment variable is honored by its
// exact name and accepts the low, medium and high levels.
func TestFromEnvReadsRecallStrategyBoosts(t *testing.T) {
	cfg := FromEnv(envMap(map[string]string{
		EnvRecallStrategyBoosts: "keyword:high, semantic:low",
	}))
	if cfg.RecallStrategyBoosts["keyword"] != BoostLevelHigh {
		t.Fatalf("keyword boost = %q, want high", cfg.RecallStrategyBoosts["keyword"])
	}
	if cfg.RecallStrategyBoosts["semantic"] != BoostLevelLow {
		t.Fatalf("semantic boost = %q, want low", cfg.RecallStrategyBoosts["semantic"])
	}
}

// An unset or empty value leaves every arm unboosted.
func TestRecallStrategyBoostsUnsetAndEmpty(t *testing.T) {
	if got := FromEnv(envMap(nil)).RecallStrategyBoosts; got != nil {
		t.Fatalf("unset boosts must be nil, got %+v", got)
	}
	if got := ParseStrategyBoosts("   "); got != nil {
		t.Fatalf("empty boosts must be nil, got %+v", got)
	}
}

// Unrecognized arms and levels are ignored.
func TestParseStrategyBoostsIgnoresInvalid(t *testing.T) {
	got := ParseStrategyBoosts("keyword:extreme,semantic:nope,:high,keyword")
	if len(got) != 0 {
		t.Fatalf("invalid pairs must be dropped, got %+v", got)
	}
}

// The recency decay shape is configurable, and an unrecognised name falls back
// to the default rather than failing startup.
func TestResolveRecency(t *testing.T) {
	base := FromEnv(envMap(nil)).Recency
	if base.Function != DefaultRecencyDecayFunction {
		t.Fatalf("default function = %q", base.Function)
	}
	if base.LinearWindowDays != DefaultRecencyDecayLinearWindowDays || base.HalflifeDays != DefaultRecencyDecayHalflifeDays {
		t.Fatalf("default tunings = %+v", base)
	}

	cfg := FromEnv(envMap(map[string]string{
		EnvRecencyDecayFunction:         "EXPONENTIAL",
		EnvRecencyDecayLinearWindowDays: "30",
		EnvRecencyDecayHalflifeDays:     "7.5",
	})).Recency
	if cfg.Function != "exponential" || cfg.LinearWindowDays != 30 || cfg.HalflifeDays != 7.5 {
		t.Fatalf("resolved = %+v", cfg)
	}

	fallback := FromEnv(envMap(map[string]string{EnvRecencyDecayFunction: "sigmoid"})).Recency
	if fallback.Function != DefaultRecencyDecayFunction {
		t.Fatalf("an unrecognised function = %q, want the default", fallback.Function)
	}
	// A non-positive window is refused, because a zero-width window would floor
	// every memory.
	window := FromEnv(envMap(map[string]string{EnvRecencyDecayLinearWindowDays: "0"})).Recency
	if window.LinearWindowDays != DefaultRecencyDecayLinearWindowDays {
		t.Fatalf("a zero window = %v, want the default", window.LinearWindowDays)
	}
}
