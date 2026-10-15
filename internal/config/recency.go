package config

import (
	"strconv"
	"strings"
)

// Recall decay defaults, matching the reference.
const (
	DefaultRecencyDecayFunction         = "linear"
	DefaultRecencyDecayLinearWindowDays = 365.0
	DefaultRecencyDecayHalflifeDays     = 90.0
)

// RecencyDecayFunctions is the validated set of decay shapes.
var RecencyDecayFunctions = []string{"linear", "exponential", "none"}

// RecencyConfig is the resolved age-to-freshness tuning.
type RecencyConfig struct {
	// Function is one of RecencyDecayFunctions.
	Function string
	// LinearWindowDays is the age at which the linear curve reaches its floor.
	LinearWindowDays float64
	// HalflifeDays is the exponential curve's half-life.
	HalflifeDays float64
}

// resolveRecency reads the decay tuning from the environment, falling back to
// the default shape for an unrecognised name rather than failing startup.
func resolveRecency(lookup func(string) (string, bool)) RecencyConfig {
	cfg := RecencyConfig{
		Function:         DefaultRecencyDecayFunction,
		LinearWindowDays: DefaultRecencyDecayLinearWindowDays,
		HalflifeDays:     DefaultRecencyDecayHalflifeDays,
	}
	if value, ok := lookup(EnvRecencyDecayFunction); ok {
		cfg.Function = NormalizeRecencyDecayFunction(value)
	}
	if value, ok := lookup(EnvRecencyDecayLinearWindowDays); ok {
		if days := floatFromString(value); days > 0 {
			cfg.LinearWindowDays = days
		}
	}
	if value, ok := lookup(EnvRecencyDecayHalflifeDays); ok {
		if days := floatFromString(value); days > 0 {
			cfg.HalflifeDays = days
		}
	}
	return cfg
}

// NormalizeRecencyDecayFunction validates a decay name, defaulting an
// unrecognised one.
func NormalizeRecencyDecayFunction(function string) string {
	switch normalized := strings.ToLower(strings.TrimSpace(function)); normalized {
	case "linear", "exponential", "none":
		return normalized
	default:
		return DefaultRecencyDecayFunction
	}
}

// floatFromString parses a decimal value, reporting zero for anything unparseable.
func floatFromString(raw string) float64 {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return 0
	}
	return value
}
