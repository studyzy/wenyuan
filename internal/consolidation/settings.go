package consolidation

import (
	"regexp"
	"strings"
)

// Consolidation settings.
//
// A bank's consolidation behaviour is configured through its bank-config
// overrides, and the pass must honour every one of them: the mission the model
// is given, how much source evidence each call is shown, how many observations a
// scope may hold, how many facts one round folds, and how the model calls are
// batched and parallelised. A named strategy — a set of scope patterns each
// carrying its own mission and limits — overrides the bank-wide values for the
// scopes it claims.

// DefaultMission is the mission a bank that sets none runs with. It mirrors the
// reference's default: track anything durable.
const DefaultMission = "Track anything notable in the new facts — names, numbers, dates, places, events, decisions, claims, relationships, and recurring patterns."

// Reference defaults for the consolidation settings, applied when a bank does
// not override them.
const (
	// DefaultMaxObservationsPerScope is unlimited.
	DefaultMaxObservationsPerScope = -1
	// DefaultSourceFactsMaxTokens bounds the source-fact evidence shown across
	// one consolidation call. -1 is unlimited.
	DefaultSourceFactsMaxTokens = 4096
	// DefaultSourceFactsMaxTokensPerObservation bounds one observation's source
	// facts. -1 is unlimited.
	DefaultSourceFactsMaxTokensPerObservation = 256
	// DefaultMaxMemoriesPerRound bounds how many facts one round folds. 0 is
	// unlimited.
	DefaultMaxMemoriesPerRound = 100
	// DefaultLLMBatchSize is how many facts one model call is given. 1 disables
	// batching.
	DefaultLLMBatchSize = 8
	// DefaultLLMParallelism bounds how many model calls run at once.
	DefaultLLMParallelism = 4
)

// Settings is a bank's consolidation configuration, bank-wide values with the
// per-scope strategy overrides folded in by forScope.
type Settings struct {
	Mission                            string
	MaxObservationsPerScope            int
	SourceFactsMaxTokens               int
	SourceFactsMaxTokensPerObservation int
	MaxMemoriesPerRound                int
	LLMBatchSize                       int
	LLMParallelism                     int
	// Strategies are the named overrides, in priority order.
	Strategies []Strategy
	// ScopeLimits are the deprecated observation_scope_limits rules, in priority
	// order. They are consulted only where no strategy claims the scope.
	ScopeLimits []ScopeLimit
}

// ScopeLimit is one deprecated observation_scope_limits rule: a scope pattern
// and the observation cap it applies there. It mirrors the reference's
// _ScopeLimitRule — exact-cover glob matching, first match wins.
type ScopeLimit struct {
	Globs []string
	Limit int
}

// DefaultSettings returns the reference defaults a bank with no overrides runs
// with.
func DefaultSettings() Settings {
	return Settings{
		Mission:                            DefaultMission,
		MaxObservationsPerScope:            DefaultMaxObservationsPerScope,
		SourceFactsMaxTokens:               DefaultSourceFactsMaxTokens,
		SourceFactsMaxTokensPerObservation: DefaultSourceFactsMaxTokensPerObservation,
		MaxMemoriesPerRound:                DefaultMaxMemoriesPerRound,
		LLMBatchSize:                       DefaultLLMBatchSize,
		LLMParallelism:                     DefaultLLMParallelism,
	}
}

// Strategy is one named consolidation strategy: the scopes it claims, and the
// settings it overrides there. A nil pointer field means the strategy does not
// override that setting.
type Strategy struct {
	Scopes                             []ScopePattern
	Mission                            *string
	MaxObservationsPerScope            *int
	SourceFactsMaxTokens               *int
	SourceFactsMaxTokensPerObservation *int
}

// ScopePattern is one alternative in a strategy's scopes: tag globs and how they
// match a concrete scope. Exact selects the exact-cover match, matching the
// reference's deprecated observation_scope_limits; otherwise the scope need only
// contain every glob (extra tags allowed).
type ScopePattern struct {
	Tags  []string
	Exact bool
}

// matches reports whether the pattern claims a concrete scope.
func (p ScopePattern) matches(scope []string) bool {
	if p.Exact {
		return exactCoverMatch(p.Tags, scope)
	}
	return containmentMatch(p.Tags, scope)
}

// claims reports whether the strategy claims a concrete scope: any one pattern
// matching is enough.
func (s Strategy) claims(scope []string) bool {
	for _, pattern := range s.Scopes {
		if pattern.matches(scope) {
			return true
		}
	}
	return false
}

// forScope returns the settings as one scope sees them: the first strategy that
// claims the scope overrides the bank-wide values, whole. A later strategy never
// fills in an earlier one's gaps, so a scope always has exactly one strategy
// behind it and the control plane can say which.
func (s Settings) forScope(scope []string) Settings {
	capSet := false
	for _, strategy := range s.Strategies {
		if !strategy.claims(scope) {
			continue
		}
		if strategy.Mission != nil {
			s.Mission = *strategy.Mission
		}
		if strategy.MaxObservationsPerScope != nil {
			s.MaxObservationsPerScope = *strategy.MaxObservationsPerScope
			capSet = true
		}
		if strategy.SourceFactsMaxTokens != nil {
			s.SourceFactsMaxTokens = *strategy.SourceFactsMaxTokens
		}
		if strategy.SourceFactsMaxTokensPerObservation != nil {
			s.SourceFactsMaxTokensPerObservation = *strategy.SourceFactsMaxTokensPerObservation
		}
		break
	}
	s.Strategies = nil
	// The deprecated observation_scope_limits rules are consulted second: a
	// scope whose winning strategy set no cap, or which no strategy claimed,
	// falls back to the first matching rule before the bank-wide value. A
	// strategy that sets a cap wins outright — a later rule never fills its gaps.
	if !capSet {
		for _, limit := range s.ScopeLimits {
			if exactCoverMatch(limit.Globs, scope) {
				s.MaxObservationsPerScope = limit.Limit
				break
			}
		}
	}
	s.ScopeLimits = nil
	return s
}

// settingsFromConfig reads a bank's consolidation settings from its raw config
// map. A missing or malformed value falls back to the default rather than
// failing the pass.
func settingsFromConfig(cfg map[string]any) Settings {
	settings := DefaultSettings()
	if mission, ok := cfg["observations_mission"].(string); ok && strings.TrimSpace(mission) != "" {
		settings.Mission = mission
	}
	if v, ok := configInt(cfg, "max_observations_per_scope"); ok {
		settings.MaxObservationsPerScope = v
	}
	if v, ok := configInt(cfg, "consolidation_source_facts_max_tokens"); ok {
		settings.SourceFactsMaxTokens = v
	}
	if v, ok := configInt(cfg, "consolidation_source_facts_max_tokens_per_observation"); ok {
		settings.SourceFactsMaxTokensPerObservation = v
	}
	if v, ok := configInt(cfg, "consolidation_max_memories_per_round"); ok {
		settings.MaxMemoriesPerRound = v
	}
	if v, ok := configInt(cfg, "consolidation_llm_batch_size"); ok && v >= 1 {
		settings.LLMBatchSize = v
	}
	if v, ok := configInt(cfg, "consolidation_llm_parallelism"); ok && v >= 1 {
		settings.LLMParallelism = v
	}
	settings.Strategies = parseStrategies(cfg["consolidation_strategies"])
	settings.ScopeLimits = parseScopeLimits(cfg["observation_scope_limits"])
	return settings
}

// parseScopeLimits reads the raw deprecated observation_scope_limits list. Like
// parseStrategies it is defensive: the value round-trips as JSON through the
// bank-config API, so a malformed entry is dropped rather than failing the pass.
// List order is priority order; the first entry whose scope exact-covers a
// concrete scope wins.
func parseScopeLimits(raw any) []ScopeLimit {
	entries, ok := raw.([]any)
	if !ok {
		return nil
	}
	var limits []ScopeLimit
	for _, entry := range entries {
		entryMap, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		scopeRaw, ok := entryMap["scope"].([]any)
		if !ok || len(scopeRaw) == 0 {
			continue
		}
		globs := make([]string, 0, len(scopeRaw))
		valid := true
		for _, item := range scopeRaw {
			glob, ok := item.(string)
			if !ok || glob == "" {
				valid = false
				break
			}
			globs = append(globs, glob)
		}
		if !valid {
			continue
		}
		limit, ok := configInt(entryMap, "limit")
		if !ok {
			continue
		}
		limits = append(limits, ScopeLimit{Globs: globs, Limit: limit})
	}
	return limits
}

// configInt reads an integer config value, tolerating the float64 shape a JSON
// round trip produces. Bool is rejected, so true/false never masquerade as one.
func configInt(cfg map[string]any, key string) (int, bool) {
	switch value := cfg[key].(type) {
	case int:
		return value, true
	case int64:
		return int(value), true
	case float64:
		return int(value), true
	default:
		return 0, false
	}
}

// parseStrategies reads the raw consolidation_strategies list. It is defensive:
// the value round-trips as JSON through the bank-config API, so a malformed
// entry — or a malformed setting or scope within an otherwise valid entry — is
// dropped rather than failing the pass. An entry naming no usable scope, or
// overriding nothing, is dropped entirely. List order is priority order.
func parseStrategies(raw any) []Strategy {
	entries, ok := raw.([]any)
	if !ok {
		return nil
	}
	var strategies []Strategy
	for _, entry := range entries {
		entryMap, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		scopes := parseScopePatterns(entryMap["scopes"])
		if len(scopes) == 0 {
			continue
		}
		strategy := Strategy{Scopes: scopes}
		if mission, ok := entryMap["observations_mission"].(string); ok && strings.TrimSpace(mission) != "" {
			strategy.Mission = &mission
		}
		strategy.MaxObservationsPerScope = configIntPtr(entryMap, "max_observations_per_scope")
		strategy.SourceFactsMaxTokens = configIntPtr(entryMap, "consolidation_source_facts_max_tokens")
		strategy.SourceFactsMaxTokensPerObservation = configIntPtr(entryMap, "consolidation_source_facts_max_tokens_per_observation")
		if strategy.Mission == nil && strategy.MaxObservationsPerScope == nil &&
			strategy.SourceFactsMaxTokens == nil && strategy.SourceFactsMaxTokensPerObservation == nil {
			continue
		}
		strategies = append(strategies, strategy)
	}
	return strategies
}

// configIntPtr reads an optional integer from a decoded JSON object.
func configIntPtr(obj map[string]any, key string) *int {
	if value, ok := configInt(obj, key); ok {
		return &value
	}
	return nil
}

// parseScopePatterns reads a strategy's scopes list. A pattern with no usable
// tags is dropped; an unknown match mode falls back to the default rather than
// dropping the pattern.
func parseScopePatterns(raw any) []ScopePattern {
	entries, ok := raw.([]any)
	if !ok {
		return nil
	}
	var patterns []ScopePattern
	for _, entry := range entries {
		entryMap, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		tags := stringList(entryMap["tags"])
		if len(tags) == 0 {
			continue
		}
		patterns = append(patterns, ScopePattern{Tags: tags, Exact: entryMap["tags_match"] == "exact"})
	}
	return patterns
}

// stringList reads a JSON array of non-empty strings; an entry that is not one
// makes the whole list unusable, matching the reference's all-or-nothing check.
func stringList(raw any) []string {
	entries, ok := raw.([]any)
	if !ok {
		return nil
	}
	tags := make([]string, 0, len(entries))
	for _, entry := range entries {
		tag, ok := entry.(string)
		if !ok || tag == "" {
			return nil
		}
		tags = append(tags, tag)
	}
	return tags
}

// containmentMatch reports whether every glob matches some tag of the scope.
// Other tags are allowed. The untagged scope never matches.
func containmentMatch(globs []string, tags []string) bool {
	if len(tags) == 0 {
		return false
	}
	for _, glob := range globs {
		if !anyTagMatchesGlob(tags, glob) {
			return false
		}
	}
	return true
}

// exactCoverMatch reports whether every tag is covered by a glob and every glob
// covers a tag. The untagged scope never matches.
func exactCoverMatch(globs []string, tags []string) bool {
	if len(tags) == 0 {
		return false
	}
	for _, tag := range tags {
		if !anyGlobMatchesTag(globs, tag) {
			return false
		}
	}
	for _, glob := range globs {
		if !anyTagMatchesGlob(tags, glob) {
			return false
		}
	}
	return true
}

func anyTagMatchesGlob(tags []string, glob string) bool {
	for _, tag := range tags {
		if matchGlob(tag, glob) {
			return true
		}
	}
	return false
}

func anyGlobMatchesTag(globs []string, tag string) bool {
	for _, glob := range globs {
		if matchGlob(tag, glob) {
			return true
		}
	}
	return false
}

// matchGlob implements shell-style matching with `*`, `?` and `[seq]`/`[!seq]`,
// case-sensitive with no path-separator handling, mirroring fnmatchcase.
func matchGlob(name, pattern string) bool {
	matched, err := regexp.MatchString(globToRegexp(pattern), name)
	return err == nil && matched
}

// globToRegexp converts a shell glob into an anchored regular expression.
func globToRegexp(pattern string) string {
	var b strings.Builder
	b.WriteString("(?s)^")
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch c {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		case '[':
			end := i + 1
			if end < len(pattern) && (pattern[end] == '!' || pattern[end] == '^') {
				end++
			}
			if end < len(pattern) && pattern[end] == ']' {
				end++
			}
			for end < len(pattern) && pattern[end] != ']' {
				end++
			}
			if end >= len(pattern) {
				b.WriteString(regexp.QuoteMeta("["))
				break
			}
			body := pattern[i+1 : end]
			if strings.HasPrefix(body, "!") {
				body = "^" + body[1:]
			}
			b.WriteString("[" + body + "]")
			i = end
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return b.String()
}
