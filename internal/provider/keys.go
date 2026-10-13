package provider

// Config keys the boundary's own error messages name. They are declared here,
// in the contract area, so the no-model error message and the configuration
// contract cannot drift apart.
const (
	// ConfigKeyProvider is the operator key that selects the active provider.
	ConfigKeyProvider = "HINDSIGHT_API_LLM_PROVIDER"
	// ConfigKeyAPIKey is the operator key holding the chat credential.
	ConfigKeyAPIKey = "HINDSIGHT_API_LLM_API_KEY"
)

// Scope names used when reporting an unconfigured provider.
const (
	// ScopeChat is the global chat scope.
	ScopeChat = "chat"
	// ScopeConsolidation is the second, independently configured scope.
	ScopeConsolidation = "consolidation"
)
