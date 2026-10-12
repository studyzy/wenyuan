package httpx

// APIVersion is the API version string advertised by the version endpoint.
const APIVersion = "1.0.0"

// Route namespace and path constants that form the public URL contract.
const (
	NamespaceExt = "/ext"
	NamespaceMCP = "/mcp"

	PathHealth     = "/health"
	PathHealthLive = "/health/live"
	PathReady      = "/health/ready"
	PathVersion    = "/version"
	PathMetrics    = "/metrics"

	PrefixBanksV1 = "/v1/default/banks"
)

// BankRouteSuffixes builds the bank-scoped sub-paths. Kept in the router area
// so no other area hard-codes URL shapes.
const (
	SuffixMemories    = "/memories"
	SuffixRecall      = "/memories/recall"
	SuffixReflect     = "/reflect"
	SuffixAliases     = "/aliases"
	SuffixAliasMember = "/aliases/{alias}"
	SuffixConfig      = "/config"
	SuffixProfile     = "/profile"
	SuffixBackground  = "/background"
	SuffixOperations  = "/operations"
)

// IgnoredQueryParamsAccepted lists the query parameters the bank-scoped routes
// accept. Every other parameter is ignored and reported.
var defaultAcceptedQueryParams = []string{"limit", "offset", "q", "type", "tags", "tags_match", "since", "until"}

// DefaultQueryFilter returns the standard accepted-parameter filter.
func DefaultQueryFilter() QueryFilter {
	return NewQueryFilter(defaultAcceptedQueryParams...)
}
