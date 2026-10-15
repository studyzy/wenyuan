package config

import "strings"

// DefaultVectorIndexMinRows keeps per-bank vector indexes on the bank
// lifecycle: at zero nothing is reconciled, which is the reference's default
// and the right behaviour for a deployment whose bank count is not the problem.
const DefaultVectorIndexMinRows = 0

// DefaultVectorIndexReconcileInterval is how often the sweep runs when
// reconciliation is enabled, in seconds.
const DefaultVectorIndexReconcileInterval = 900

// VectorIndexConfig tunes per-(bank, fact_type) partial vector indexes.
type VectorIndexConfig struct {
	// MinRows is the row count at which a partition earns its own index, and
	// below which it loses one. Zero disables reconciliation entirely.
	MinRows int
	// ReconcileInterval is how often the maintenance loop sweeps banks for
	// coverage. Only consulted when MinRows is positive.
	ReconcileInterval int
}

// resolveVectorIndex reads the vector-index tuning from the environment.
func resolveVectorIndex(lookup func(string) (string, bool)) VectorIndexConfig {
	cfg := VectorIndexConfig{
		MinRows:           intFromEnv(lookup, EnvVectorIndexMinRows, DefaultVectorIndexMinRows),
		ReconcileInterval: intFromEnv(lookup, EnvVectorIndexReconcileInterval, DefaultVectorIndexReconcileInterval),
	}
	if cfg.MinRows < 0 {
		cfg.MinRows = 0
	}
	if cfg.ReconcileInterval <= 0 {
		cfg.ReconcileInterval = DefaultVectorIndexReconcileInterval
	}
	return cfg
}

// VectorExtensionOrDefault names the access method family, defaulting when the
// deployment left the key blank.
func VectorExtensionOrDefault(value string) string {
	if strings.TrimSpace(value) == "" {
		return DefaultVectorExtension
	}
	return strings.TrimSpace(value)
}
