package storage

import (
	"errors"
	"fmt"
	"strings"
)

// Tag-scope caps, mirroring the reference's tag-resolution ceilings. Fuzzy
// resolution folds the request against the bank's tag vocabulary and expands an
// `exact` leaf into one branch per combination of matched tags; both are
// bounded so an oversized request is refused up front — as a validation error
// the caller maps to a 422 — rather than grinding through an enumeration whose
// scale the caller cannot see.
const (
	// MaxTagVocabulary is the number of distinct tags a bank may hold before a
	// fuzzy tag request is refused. Matching against a silently truncated
	// vocabulary would change which memories match with no signal to the
	// caller, so the request is rejected instead of quietly differing.
	MaxTagVocabulary = 5000
	// MaxTagExactCombinations is the ceiling on the branches an `exact` fuzzy
	// leaf may expand into. The branch count is the product of the per-token
	// expansions, so it grows multiplicatively where the other match modes grow
	// additively. It is checked before enumerating: counting only the surviving
	// combinations would walk the whole product first, which is what the
	// ceiling exists to prevent.
	MaxTagExactCombinations = 32
)

var (
	// ErrTagVocabularyTooLarge reports a bank whose distinct tag count exceeds
	// MaxTagVocabulary alongside a request that resolves tags fuzzily. The
	// caller maps it to a 422.
	ErrTagVocabularyTooLarge = errors.New("bank tag vocabulary is too large for fuzzy tag matching")
	// ErrTagExactCombinationsTooLarge reports an `exact` fuzzy tag leaf whose
	// expansion would exceed MaxTagExactCombinations candidate scopes. The
	// caller maps it to a 422.
	ErrTagExactCombinationsTooLarge = errors.New("tag filter expands to too many candidate scopes")
)

// tagScopeCapError is a tag-scope cap violation. It carries the reference's own
// message while unwrapping to the sentinel the caller matches on, so the 422 a
// recall emits names the same numbers and remedy the reference does and the
// mapping stays an errors.Is check.
type tagScopeCapError struct {
	sentinel error
	message  string
}

func (e *tagScopeCapError) Error() string { return e.message }

func (e *tagScopeCapError) Unwrap() error { return e.sentinel }

// tagScopeAllows reports whether a memory is visible under the request's tag
// scoping. The caller's own scope is combined with the request filter so a
// scoped caller can narrow but not widen its view.
func (s *MemoryStore) tagScopeAllows(q RecallQuery, m RecallMatch) bool {
	if len(q.TagGroups) > 0 {
		return anyGroupMatches(q.TagGroups, m.Tags)
	}
	if len(q.Tags) == 0 && q.TagsMatch != TagsMatchExact {
		return true
	}
	return matchTags(q.Tags, m.Tags, q.TagsMatch)
}

// matchTags applies a flat tag-match mode. The base overlap and contains modes
// include untagged memories; the strict variants exclude them; the set-equality
// mode matches only memories whose full tag set equals the request, with an
// empty request selecting the untagged scope.
func matchTags(requested, present []string, mode string) bool {
	switch mode {
	case TagsMatchExact:
		return sameTagSet(requested, present)
	case TagsMatchAll:
		if len(present) == 0 {
			return true
		}
		return containsAll(present, requested)
	case TagsMatchAnyStrict:
		if len(present) == 0 {
			return false
		}
		return overlapsAny(present, requested)
	case TagsMatchAllStrict:
		if len(present) == 0 {
			return false
		}
		return containsAll(present, requested)
	default: // TagsMatchAny and the empty default: overlap, includes untagged.
		if len(present) == 0 {
			return true
		}
		return overlapsAny(present, requested)
	}
}

// anyGroupMatches reports whether a memory satisfies the top-level AND across
// the compound tag groups.
func anyGroupMatches(groups []TagGroup, present []string) bool {
	for _, group := range groups {
		if !groupMatches(group, present) {
			return false
		}
	}
	return true
}

// groupMatches evaluates one compound tag-group node against the tags a memory
// carries.
func groupMatches(group TagGroup, present []string) bool {
	switch {
	case len(group.Not) > 0:
		for _, child := range group.Not {
			if groupMatches(child, present) {
				return false
			}
		}
		return true
	case len(group.And) > 0:
		for _, child := range group.And {
			if !groupMatches(child, present) {
				return false
			}
		}
		return true
	case len(group.Or) > 0:
		for _, child := range group.Or {
			if groupMatches(child, present) {
				return true
			}
		}
		return false
	default:
		return leafMatches(group, present)
	}
}

// leafMatches evaluates one leaf: a literal match against the requested tags.
// A `resolve:"fuzzy"` leaf never reaches here: the request's fuzzy leaves are
// rewritten into exact ones against the bank's tag vocabulary before recall, so
// the leaf that is matched always names real tags.
func leafMatches(group TagGroup, present []string) bool {
	requested := group.Tags
	if group.Tag != "" {
		requested = append([]string{group.Tag}, requested...)
	}
	if len(requested) == 0 {
		return true
	}
	// A leaf's own default differs from the flat field's: a compound filter is
	// written to narrow, so an unset mode excludes untagged memories rather than
	// admitting them.
	return matchTags(requested, present, leafMatchMode(group.Match))
}

// leafMatchMode resolves a tag-group leaf's match mode, defaulting to the
// strict any-match the reference uses for a leaf.
func leafMatchMode(mode string) string {
	if strings.TrimSpace(mode) == "" {
		return TagsMatchAnyStrict
	}
	return mode
}

// tagSimilarityThreshold is the trigram Jaccard floor above which two tags are
// considered a fuzzy match. It mirrors the reference's tag-resolution
// similarity floor.
const tagSimilarityThreshold = 0.45

// similarTag reports whether two tags are close: same namespace, and their
// namespace-stripped values share enough trigrams to clear the floor. Case is
// folded and surrounding whitespace ignored. Similarity is measured on the value
// alone because a namespace is shared by every candidate in it, so its trigrams
// would count as agreement between tags that have nothing else in common — and
// the shorter the value, the larger that shared share. Scored whole,
// "name:the" and "name:ts" reach 0.462 while "the" and "ts" score 0.125, so
// every short tag in a namespace would resolve to every other.
func similarTag(a, b string) bool {
	la := strings.ToLower(strings.TrimSpace(a))
	lb := strings.ToLower(strings.TrimSpace(b))
	if la == "" || lb == "" {
		return false
	}
	if la == lb {
		return true
	}
	namespaceA, valueA := splitNamespace(la)
	namespaceB, valueB := splitNamespace(lb)
	if namespaceA != namespaceB {
		return false
	}
	return trigramJaccard(valueA, valueB) >= tagSimilarityThreshold
}

// splitNamespace splits a tag at its first colon, returning an empty namespace
// for a tag with none. It is the basis of the namespace-stripped comparison in
// similarTag, so fuzzy resolution never matches across namespaces.
func splitNamespace(tag string) (string, string) {
	namespace, value, found := strings.Cut(tag, ":")
	if !found {
		return "", tag
	}
	return namespace, value
}

// trigramJaccard returns the similarity two strings score under pg_trgm's
// `similarity(a, b)` — the measure the reference computes in memory and shares
// between entity resolution and fuzzy tag matching (`engine/trigram.py`).
//
// It is the per-word measure, not a whole-string one: pg_trgm splits each string
// into maximal alphanumeric runs and pads every word separately, so a space or a
// punctuation mark inside a tag costs nothing. Scoring the whole padded string
// instead spends grams on the separators and lands on a different number, which
// moves tags across the resolution floor: `mongp`/`mongo` scores 0.50 the way the
// reference measures it and 0.40 the whole-string way, and 0.45 is the floor, so
// the whole-string measure silently stops resolving a tag the reference resolves.
func trigramJaccard(a, b string) float64 {
	return entityNameTrigramSimilarity(a, b)
}

// sameTagSet reports whether two tag lists contain exactly the same values.
func sameTagSet(a, b []string) bool {
	sa := uniqueTags(a)
	sb := uniqueTags(b)
	if len(sa) != len(sb) {
		return false
	}
	for tag := range sa {
		if !sb[tag] {
			return false
		}
	}
	return true
}

func uniqueTags(tags []string) map[string]bool {
	out := map[string]bool{}
	for _, tag := range tags {
		out[tag] = true
	}
	return out
}

func overlapsAny(have, want []string) bool {
	hs := uniqueTags(have)
	for _, tag := range want {
		if hs[tag] {
			return true
		}
	}
	return false
}

func containsAll(have, want []string) bool {
	hs := uniqueTags(have)
	for _, tag := range want {
		if !hs[tag] {
			return false
		}
	}
	return true
}

// tagVocabulary returns the distinct tags the bank's memories carry, which is
// the vocabulary fuzzy resolution folds against.
func (s *MemoryStore) tagVocabulary(bankID string) []string {
	seen := map[string]bool{}
	out := make([]string, 0)
	for _, m := range s.memories[bankID] {
		for _, tag := range m.Tags {
			if !seen[tag] {
				seen[tag] = true
				out = append(out, tag)
			}
		}
	}
	return out
}

// ValidateTagScope reports whether a request's tag scoping stays within the
// tag-resolution caps, given the bank's tag vocabulary. It is exported so a
// backend that owns its own vocabulary read (the database store) can enforce
// the same limits; the in-process store calls it through checkTagScopeCaps.
//
// It returns ErrTagVocabularyTooLarge or ErrTagExactCombinationsTooLarge, both
// of which a caller maps to a 4xx. A request that does not resolve tags fuzzily
// is always accepted.
func ValidateTagScope(q RecallQuery, vocabulary []string) error {
	if !usesFuzzyTagResolution(q.TagGroups) {
		return nil
	}
	if len(vocabulary) > MaxTagVocabulary {
		return &tagScopeCapError{
			sentinel: ErrTagVocabularyTooLarge,
			message: fmt.Sprintf(
				"Bank has %d distinct tags, above the %d that fuzzy tag matching can resolve against. Use resolve='exact'.",
				len(vocabulary), MaxTagVocabulary),
		}
	}
	for _, group := range q.TagGroups {
		if err := checkLeafCombinations(group, vocabulary); err != nil {
			return err
		}
	}
	return nil
}

// usesFuzzyTagResolution reports whether any leaf in the tree asks fuzzy
// resolution. Flat tags carry no resolution option, so only compound groups can.
func usesFuzzyTagResolution(groups []TagGroup) bool {
	for _, group := range groups {
		if group.Resolve == TagResolveFuzzy {
			return true
		}
		if usesFuzzyTagResolution(group.And) || usesFuzzyTagResolution(group.Or) || usesFuzzyTagResolution(group.Not) {
			return true
		}
	}
	return false
}

// checkLeafCombinations walks the group tree and rejects any fuzzy `exact` leaf
// whose combined expansion exceeds the branch ceiling. Only the `exact` match
// mode multiplies its per-token expansions; the `any` and `all` modes union or
// conjunct them, so they grow additively and are not capped.
func checkLeafCombinations(group TagGroup, vocabulary []string) error {
	if group.Resolve == TagResolveFuzzy && group.Match == TagsMatchExact {
		branches := 1
		for _, token := range leafTokens(group) {
			branches *= fuzzyExpansionCount(token, vocabulary)
			if branches > MaxTagExactCombinations {
				return &tagScopeCapError{
					sentinel: ErrTagExactCombinationsTooLarge,
					message: fmt.Sprintf(
						"Fuzzy resolution of an 'exact' tag filter expands to %d candidate scopes, above the ceiling of %d. Narrow the tags, or use resolve='exact'.",
						branches, MaxTagExactCombinations),
				}
			}
		}
	}
	for _, child := range group.And {
		if err := checkLeafCombinations(child, vocabulary); err != nil {
			return err
		}
	}
	for _, child := range group.Or {
		if err := checkLeafCombinations(child, vocabulary); err != nil {
			return err
		}
	}
	for _, child := range group.Not {
		if err := checkLeafCombinations(child, vocabulary); err != nil {
			return err
		}
	}
	return nil
}

// leafTokens returns the tags of one leaf, with the singular Tag folded in
// front of Tags.
func leafTokens(group TagGroup) []string {
	if group.Tag == "" {
		return group.Tags
	}
	return append([]string{group.Tag}, group.Tags...)
}

// fuzzyExpansionCount is how many vocabulary tags a token resolves to: a
// triggering-count of at least one. A token matching nothing comes back as 1 —
// the token itself, which keeps the leaf unsatisfiable rather than empty
// (matching the reference, and this store's fuzzyLeafMatch).
func fuzzyExpansionCount(token string, vocabulary []string) int {
	count := 0
	for _, tag := range vocabulary {
		if similarTag(token, tag) {
			count++
		}
	}
	if count == 0 {
		return 1
	}
	return count
}
