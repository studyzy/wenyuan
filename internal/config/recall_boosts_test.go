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
