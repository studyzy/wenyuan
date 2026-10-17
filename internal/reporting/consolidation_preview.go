package reporting

import (
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/studyzy/wenyuan/internal/httpx"
)

// consolidationStrategySpec is one draft consolidation strategy as the preview
// request carries it, normalised to the shape the consolidator parses. Unknown
// keys are ignored, matching the non-strict request model upstream accepts.
type consolidationStrategySpec struct {
	Scopes  []consolidationScopePattern `json:"scopes"`
	Mission *string                     `json:"observations_mission"`
	// intSettings names the per-strategy integer overrides. Their presence (with
	// a usable scope) is what makes a strategy active, so only their presence
	// matters here.
	MaxObservationsPerScope            *int `json:"max_observations_per_scope"`
	SourceFactsMaxTokens               *int `json:"consolidation_source_facts_max_tokens"`
	SourceFactsMaxTokensPerObservation *int `json:"consolidation_source_facts_max_tokens_per_observation"`
}

// consolidationScopePattern is one rule of a strategy. TagsMatch is the
// normalised mode ("all" or "exact"); an omitted/null value is left empty and
// defaults to "all" when the pattern is matched.
type consolidationScopePattern struct {
	Tags      []string `json:"tags"`
	TagsMatch string   `json:"tags_match"`
}

// consolidationScopesPreviewRequest is the preview body after validation.
type consolidationScopesPreviewRequest struct {
	Strategies  []consolidationStrategySpec `json:"strategies"`
	SampleLimit int                         `json:"sample_limit"`
}

// consolidationTagsMatchValues are the accepted values of a rule's tags_match,
// validated at the door exactly as the reference's field validator does.
var consolidationTagsMatchValues = []string{"all", "exact"}

// patternValid reports whether a rule is a usable scope pattern: a non-empty
// list of non-empty string tags. A whitespace-only tag is a non-empty string
// for the reference, so it stays usable here too.
func (p consolidationScopePattern) patternValid() bool {
	if len(p.Tags) == 0 {
		return false
	}
	for _, tag := range p.Tags {
		if tag == "" {
			return false
		}
	}
	return true
}

// decodeConsolidationPreview validates a decoded preview body the way the
// reference's request model does and returns the normalised request plus every
// structured item the framework would emit, in field-definition order. A
// non-empty item list means the request is rejected with a 422.
func decodeConsolidationPreview(raw map[string]any) (consolidationScopesPreviewRequest, []httpx.ValidationError) {
	var req consolidationScopesPreviewRequest
	var errs []httpx.ValidationError

	value, present := raw["strategies"]
	switch {
	case !present:
		errs = append(errs, httpx.Missing([]string{"body", "strategies"}, raw))
	case value == nil:
		errs = append(errs, httpx.TypeMismatch("list_type", []string{"body", "strategies"}, value))
	default:
		entries, ok := value.([]any)
		if !ok {
			errs = append(errs, httpx.TypeMismatch("list_type", []string{"body", "strategies"}, value))
			break
		}
		for i, entry := range entries {
			loc := []string{"body", "strategies", strconv.Itoa(i)}
			obj, ok := entry.(map[string]any)
			if !ok {
				errs = append(errs, consolidationModelAttributesType(loc, entry))
				continue
			}
			spec, specErrs := decodeConsolidationStrategy(loc, obj)
			errs = append(errs, specErrs...)
			req.Strategies = append(req.Strategies, spec)
		}
	}

	req.SampleLimit = 5
	if value, present := raw["sample_limit"]; present {
		loc := []string{"body", "sample_limit"}
		if n, item := decodeConsolidationInt(loc, value); item != nil {
			errs = append(errs, *item)
		} else {
			switch {
			case n < 0:
				errs = append(errs, httpx.GreaterThanEqual(loc, value, 0))
			case n > 50:
				errs = append(errs, httpx.LessThanEqual(loc, value, 50))
			default:
				req.SampleLimit = n
			}
		}
	}
	return req, errs
}

// decodeConsolidationStrategy validates one strategy entry.
func decodeConsolidationStrategy(loc []string, obj map[string]any) (consolidationStrategySpec, []httpx.ValidationError) {
	var spec consolidationStrategySpec
	var errs []httpx.ValidationError

	if value, present := obj["scopes"]; present {
		scopesLoc := append(append([]string{}, loc...), "scopes")
		entries, ok := value.([]any)
		switch {
		case value == nil || !ok:
			errs = append(errs, httpx.TypeMismatch("list_type", scopesLoc, value))
		default:
			for j, entry := range entries {
				ruleLoc := append(append([]string{}, scopesLoc...), strconv.Itoa(j))
				ruleObj, ok := entry.(map[string]any)
				if !ok {
					errs = append(errs, consolidationModelAttributesType(ruleLoc, entry))
					continue
				}
				rule, ruleErrs := decodeConsolidationScopePattern(ruleLoc, ruleObj)
				errs = append(errs, ruleErrs...)
				spec.Scopes = append(spec.Scopes, rule)
			}
		}
	}

	if value, present := obj["observations_mission"]; present && value != nil {
		if s, ok := value.(string); ok {
			spec.Mission = &s
		} else {
			errs = append(errs, httpx.ScalarTypeMismatch("string", append(append([]string{}, loc...), "observations_mission"), value))
		}
	}

	settings := []struct {
		name   string
		target **int
	}{
		{"max_observations_per_scope", &spec.MaxObservationsPerScope},
		{"consolidation_source_facts_max_tokens", &spec.SourceFactsMaxTokens},
		{"consolidation_source_facts_max_tokens_per_observation", &spec.SourceFactsMaxTokensPerObservation},
	}
	for _, setting := range settings {
		value, present := obj[setting.name]
		if !present || value == nil {
			continue
		}
		n, item := decodeConsolidationInt(append(append([]string{}, loc...), setting.name), value)
		if item != nil {
			errs = append(errs, *item)
			continue
		}
		*setting.target = &n
	}
	return spec, errs
}

// decodeConsolidationScopePattern validates one rule of a strategy.
func decodeConsolidationScopePattern(loc []string, obj map[string]any) (consolidationScopePattern, []httpx.ValidationError) {
	var rule consolidationScopePattern
	var errs []httpx.ValidationError

	if value, present := obj["tags"]; present {
		tagsLoc := append(append([]string{}, loc...), "tags")
		entries, ok := value.([]any)
		switch {
		case value == nil || !ok:
			errs = append(errs, httpx.TypeMismatch("list_type", tagsLoc, value))
		default:
			for k, entry := range entries {
				s, ok := entry.(string)
				if !ok {
					errs = append(errs, httpx.ScalarTypeMismatch("string",
						append(append([]string{}, tagsLoc...), strconv.Itoa(k)), entry))
					continue
				}
				rule.Tags = append(rule.Tags, s)
			}
		}
	}

	if value, present := obj["tags_match"]; present && value != nil {
		tagsMatchLoc := append(append([]string{}, loc...), "tags_match")
		if s, ok := value.(string); ok {
			if s == consolidationTagsMatchValues[0] || s == consolidationTagsMatchValues[1] {
				rule.TagsMatch = s
			} else {
				errs = append(errs, httpx.ValueError(tagsMatchLoc, value, "must be one of all, exact"))
			}
		} else {
			errs = append(errs, httpx.ScalarTypeMismatch("string", tagsMatchLoc, value))
		}
	}
	return rule, errs
}

// decodeConsolidationInt coerces one JSON value to the framework's `int`, which
// accepts a boolean, an integral number and a numeric string, and rejects a
// fractional number, a non-numeric string and every other kind with the item the
// framework names for it.
func decodeConsolidationInt(loc []string, value any) (int, *httpx.ValidationError) {
	if b, ok := value.(bool); ok {
		if b {
			return 1, nil
		}
		return 0, nil
	}
	if s, ok := value.(string); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			return n, nil
		}
		item := httpx.TypeMismatch("int_parsing", loc, value)
		return 0, &item
	}
	if f, ok := value.(float64); ok && f == math.Trunc(f) {
		return int(f), nil
	}
	item := httpx.ScalarTypeMismatch("int", loc, value)
	return 0, &item
}

// consolidationModelAttributesType builds the item the framework emits when a
// value expected to be a model is not a JSON object.
func consolidationModelAttributesType(loc []string, input any) httpx.ValidationError {
	return httpx.ValidationItem("model_attributes_type", loc,
		"Input should be a valid dictionary or object to extract fields from", input, nil)
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
