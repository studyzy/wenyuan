package reporting

import (
	"regexp"
	"strings"
)

// consolidationStrategySpec is one draft consolidation strategy as the preview
// request carries it. Unknown keys are ignored, matching the non-strict request
// model upstream accepts.
type consolidationStrategySpec struct {
	Scopes  []consolidationScopePattern `json:"scopes"`
	Mission *string                     `json:"observations_mission"`
	// intSettings names the per-strategy integer overrides. Their presence (with
	// a usable scope) is what makes a strategy active, so they are read as raw
	// JSON and only their presence matters here.
	MaxObservationsPerScope            *int `json:"max_observations_per_scope"`
	SourceFactsMaxTokens               *int `json:"consolidation_source_facts_max_tokens"`
	SourceFactsMaxTokensPerObservation *int `json:"consolidation_source_facts_max_tokens_per_observation"`
}

// consolidationScopePattern is one rule of a strategy.
type consolidationScopePattern struct {
	Tags      []string `json:"tags"`
	TagsMatch string   `json:"tags_match"`
}

// consolidationScopesPreviewRequest is the preview body.
type consolidationScopesPreviewRequest struct {
	Strategies  []consolidationStrategySpec `json:"strategies"`
	SampleLimit *int                        `json:"sample_limit"`
}

// patternValid reports whether a rule is a usable scope pattern: a non-empty
// list of non-empty string tags.
func (p consolidationScopePattern) patternValid() bool {
	if len(p.Tags) == 0 {
		return false
	}
	for _, tag := range p.Tags {
		if strings.TrimSpace(tag) == "" {
			return false
		}
	}
	return true
}

// matchMode returns the rule's match mode, defaulting an unknown one to "all"
// rather than dropping the rule.
func (p consolidationScopePattern) matchMode() string {
	if p.TagsMatch == "exact" {
		return "exact"
	}
	return "all"
}

// matches reports whether the rule claims a concrete scope tag set.
func (p consolidationScopePattern) matches(scope []string) bool {
	if p.matchMode() == "exact" {
		return scopeMatchesGlobsExact(p.Tags, scope)
	}
	return scopeContainsGlobs(p.Tags, scope)
}

// scopeContainsGlobs is the containment match: every glob matches some tag;
// extra tags are allowed. The untagged scope never matches.
func scopeContainsGlobs(globs []string, tags []string) bool {
	if len(tags) == 0 {
		return false
	}
	for _, glob := range globs {
		if !anyTagMatches(tags, glob) {
			return false
		}
	}
	return true
}

// scopeMatchesGlobsExact is the exact-cover match: every tag is covered by a
// glob and every glob covers a tag. Untagged scopes never match.
func scopeMatchesGlobsExact(globs []string, tags []string) bool {
	if len(tags) == 0 {
		return false
	}
	for _, tag := range tags {
		if !anyGlobMatches(globs, tag) {
			return false
		}
	}
	for _, glob := range globs {
		if !anyTagMatches(tags, glob) {
			return false
		}
	}
	return true
}

func anyTagMatches(tags []string, glob string) bool {
	for _, tag := range tags {
		if fnmatchCase(tag, glob) {
			return true
		}
	}
	return false
}

func anyGlobMatches(globs []string, tag string) bool {
	for _, glob := range globs {
		if fnmatchCase(tag, glob) {
			return true
		}
	}
	return false
}

// fnmatchCase implements Python's fnmatch.fnmatchcase: shell-style matching with
// `*`, `?` and `[seq]`/`[!seq]`, case-sensitive, no path-separator handling.
func fnmatchCase(name, pattern string) bool {
	matched, err := regexp.MatchString(fnmatchTranslate(pattern), name)
	return err == nil && matched
}

// fnmatchTranslate converts a shell glob into an anchored regular expression.
func fnmatchTranslate(pattern string) string {
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

// scopePreview is the concrete set of existing scopes the preview runs over,
// in the order the store returned them (most populous first).
type scopePreview struct {
	Tags  []string
	Count int
}

// buildConsolidationPreview computes the preview from a draft strategy list and
// the bank's existing scopes, using the same first-strategy-wins rule the
// consolidator applies.
func buildConsolidationPreview(raw []consolidationStrategySpec, scopes []scopePreview, sampleLimit int, complete bool) ConsolidationStrategiesPreview {
	active := make([]bool, len(raw))
	patterns := make([][]*consolidationScopePattern, len(raw))
	hits := make([][][]int, len(raw))
	for i, entry := range raw {
		active[i] = strategyActive(entry)
		for _, rule := range entry.Scopes {
			if rule.patternValid() {
				pattern := rule
				patterns[i] = append(patterns[i], &pattern)
			} else {
				patterns[i] = append(patterns[i], nil)
			}
		}
		for _, pattern := range patterns[i] {
			var ruleHits []int
			if pattern != nil {
				for k, scope := range scopes {
					if pattern.matches(scope.Tags) {
						ruleHits = append(ruleHits, k)
					}
				}
			}
			hits[i] = append(hits[i], ruleHits)
		}
	}

	winners := make([]*int, len(scopes))
	for i := range raw {
		if !active[i] {
			continue
		}
		for _, ruleHits := range hits[i] {
			for _, k := range ruleHits {
				if winners[k] == nil {
					index := i
					winners[k] = &index
				}
			}
		}
	}

	sample := func(indices []int) []StrategyScopePreview {
		out := make([]StrategyScopePreview, 0, sampleLimit)
		for _, k := range indices {
			if len(out) >= sampleLimit {
				break
			}
			out = append(out, StrategyScopePreview{Tags: scopes[k].Tags, Count: scopes[k].Count, HandledBy: winners[k]})
		}
		return out
	}

	strategies := make([]StrategyPreview, 0, len(raw))
	for i := range raw {
		rules := make([]StrategyRulePreview, 0, len(patterns[i]))
		for _, ruleHits := range hits[i] {
			rules = append(rules, StrategyRulePreview{
				MatchCount:       len(ruleHits),
				TakenCount:       countTakenAway(ruleHits, winners, i),
				ObservationCount: sumObservationCounts(ruleHits, scopes),
				Samples:          sample(ruleHits),
			})
		}
		claimed := 0
		for _, winner := range winners {
			if winner != nil && *winner == i {
				claimed++
			}
		}
		strategies = append(strategies, StrategyPreview{Active: active[i], ClaimedCount: claimed, Rules: rules})
	}

	var unclaimed []int
	for k, winner := range winners {
		if winner == nil {
			unclaimed = append(unclaimed, k)
		}
	}
	defaultScopes := DefaultScopesPreview{
		MatchCount:       len(unclaimed),
		ObservationCount: sumObservationCounts(unclaimed, scopes),
		Samples:          sample(unclaimed),
	}
	return ConsolidationStrategiesPreview{
		Strategies: strategies, Default: defaultScopes,
		ScopesScanned: len(scopes), Complete: complete,
	}
}

// strategyActive reports whether the consolidator would keep a draft strategy:
// at least one usable rule, and at least one setting it overrides.
func strategyActive(entry consolidationStrategySpec) bool {
	hasRule := false
	for _, rule := range entry.Scopes {
		if rule.patternValid() {
			hasRule = true
			break
		}
	}
	if !hasRule {
		return false
	}
	if entry.Mission != nil && strings.TrimSpace(*entry.Mission) != "" {
		return true
	}
	return entry.MaxObservationsPerScope != nil ||
		entry.SourceFactsMaxTokens != nil ||
		entry.SourceFactsMaxTokensPerObservation != nil
}

func countTakenAway(hits []int, winners []*int, strategy int) int {
	taken := 0
	for _, k := range hits {
		if winners[k] != nil && *winners[k] != strategy {
			taken++
		}
	}
	return taken
}

func sumObservationCounts(indices []int, scopes []scopePreview) int {
	total := 0
	for _, k := range indices {
		total += scopes[k].Count
	}
	return total
}
