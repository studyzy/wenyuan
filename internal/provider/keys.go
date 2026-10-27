package provider

// Config keys the boundary's own error messages name. They are declared here,
// in the contract area, so the no-model error message and the configuration
// contract cannot drift apart.
const (
	// ConfigKeyProvider is the operator key that selects the active provider.
	ConfigKeyProvider = "WENYUAN_API_LLM_PROVIDER"
	// ConfigKeyAPIKey is the operator key holding the chat credential.
	ConfigKeyAPIKey = "WENYUAN_API_LLM_API_KEY"
	// ConfigKeyEmbeddingProvider is the operator key that selects the
	// embedding provider, which is configured independently of chat.
	ConfigKeyEmbeddingProvider = "WENYUAN_API_EMBEDDINGS_PROVIDER"
)

// Scope names used when reporting an unconfigured provider.
const (
	// ScopeChat is the global chat scope.
	ScopeChat = "chat"
	// ScopeEmbedding is the embedding scope.
	ScopeEmbedding = "embedding"
	// ScopeConsolidation is the second, independently configured scope.
	ScopeConsolidation = "consolidation"
)
