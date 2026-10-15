package config

// ResolveLayers layers configuration overrides and returns the effective map.
//
// Precedence is per key, highest first: the bank's own overrides win, then the
// tenant's, then the process's. A nil layer — or one that does not name a key —
// contributes nothing, so the result holds exactly the keys some layer defined
// and never invents a default. A caller reads a key it needs and applies its own
// shipped default when the key is absent, which is what keeps a deployment with
// no tenant layer resolving byte-for-byte as it did before the layer existed.
//
// The process layer is the deployment's process-level configuration: the values
// resolved once at startup from the environment, the base FromEnv returns. It is
// the bottom of the hierarchy. The call sites pass it as nil for the per-bank
// keys, because the process-level setting each key has is applied by the handler
// as a typed default rather than carried in a map; the resolver does not require
// the layer to be present.
func ResolveLayers(bank, tenant, process map[string]any) map[string]any {
	resolved := make(map[string]any, len(process)+len(tenant)+len(bank))
	// Lowest precedence first, so a later layer overwrites a key an earlier one
	// set. Each layer is applied whole-key: an absent key leaves the value the
	// lower layers produced, including leaving it absent.
	for _, layer := range []map[string]any{process, tenant, bank} {
		for key, value := range layer {
			resolved[key] = value
		}
	}
	return resolved
}
