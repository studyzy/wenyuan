package storage

import (
	"fmt"
	"strings"
)

// ResolveTagGroups rewrites every fuzzy leaf in a compound tag filter into an
// exact-only one, resolved against the bank's tag vocabulary. It is the
// reference's tag resolution, applied before anything evaluates the filter, so
// the evaluators and the SQL builders only ever see literal tags.
//
// A fuzzy leaf's tokens are expanded against the vocabulary: an exact
// (case-insensitive) hit is never widened to its neighbours, otherwise every
// vocabulary tag in the same namespace scoring at or above the similarity floor
// resolves the token, and a token matching nothing comes back as itself so the
// leaf stays unsatisfiable rather than dropping the filter. The expansion then
// composes with the leaf's own match mode.
//
// A filter with no fuzzy leaf is returned unchanged, so an exact request pays
// nothing for the vocabulary the caller already read.
func ResolveTagGroups(groups []TagGroup, vocabulary []string) ([]TagGroup, error) {
	if !usesFuzzyTagResolution(groups) {
		return groups, nil
	}
	out := make([]TagGroup, 0, len(groups))
	for _, group := range groups {
		resolved, err := resolveTagGroup(group, vocabulary)
		if err != nil {
			return nil, err
		}
		out = append(out, resolved)
	}
	return out, nil
}

// NeedsTagResolution reports whether any leaf in the tree asks for fuzzy
// resolution. Callers gate the vocabulary read on it, because resolution costs
// a tag enumeration and the common exact filter must not pay for one.
func NeedsTagResolution(groups []TagGroup) bool { return usesFuzzyTagResolution(groups) }

// ResolveRecallTagScope rewrites a recall query's fuzzy tag leaves into exact
// ones against the bank's tag vocabulary, returning the query with an
// exact-only compound tree. It is the one place recall resolves, so both
// backends and every retrieval arm filter on literal tags and no caller can
// reach a divergent per-row reading by choosing an arm.
//
// The resolution caps are enforced first, so an over-large vocabulary or an
// `exact` leaf whose expansion multiplies past the branch ceiling is refused
// with ErrTagVocabularyTooLarge / ErrTagExactCombinationsTooLarge — either of
// which a caller maps to a 422 — rather than resolved against a truncated
// vocabulary or enumerated past its ceiling. A query with no fuzzy leaf is
// returned unchanged, so an exact request reads no vocabulary.
func ResolveRecallTagScope(q RecallQuery, vocabulary []string) (RecallQuery, error) {
	if !usesFuzzyTagResolution(q.TagGroups) {
		return q, nil
	}
	if err := ValidateTagScope(q, vocabulary); err != nil {
		return q, err
	}
	resolved, err := ResolveTagGroups(q.TagGroups, vocabulary)
	if err != nil {
		// The only failure here is the branch ceiling, which ValidateTagScope
		// already guards; carry the sentinel and the resolver's message so a
		// caller maps it to a 422 like the cap instead of reading the resolver's
		// error as an internal fault.
		return q, &tagScopeCapError{sentinel: ErrTagExactCombinationsTooLarge, message: err.Error()}
	}
	q.TagGroups = resolved
	return q, nil
}

// resolveTagGroup rewrites one node, leaving exact leaves and structure intact.
// A NOT group is resolved like any other: the same leaf means the same thing
// inside or outside the negation, so a wider match excludes more.
func resolveTagGroup(group TagGroup, vocabulary []string) (TagGroup, error) {
	switch {
	case len(group.Not) > 0:
		children, err := resolveTagChildren(group.Not, vocabulary)
		if err != nil {
			return TagGroup{}, err
		}
		return TagGroup{Not: children}, nil
	case len(group.And) > 0:
		children, err := resolveTagChildren(group.And, vocabulary)
		if err != nil {
			return TagGroup{}, err
		}
		return TagGroup{And: children}, nil
	case len(group.Or) > 0:
		children, err := resolveTagChildren(group.Or, vocabulary)
		if err != nil {
			return TagGroup{}, err
		}
		return TagGroup{Or: children}, nil
	default:
		if !strings.EqualFold(group.Resolve, TagResolveFuzzy) {
			return group, nil
		}
		return resolveTagLeaf(group, vocabulary)
	}
}

// resolveTagChildren rewrites each child of a compound node.
func resolveTagChildren(children []TagGroup, vocabulary []string) ([]TagGroup, error) {
	out := make([]TagGroup, 0, len(children))
	for _, child := range children {
		resolved, err := resolveTagGroup(child, vocabulary)
		if err != nil {
			return nil, err
		}
		out = append(out, resolved)
	}
	return out, nil
}

// resolveTagLeaf expands one fuzzy leaf's tokens and rebuilds it around its own
// match mode: `any`/`any_strict` become one union (OR across tokens and within
// a token are the same OR), `all`/`all_strict` become an AND of one union per
// token, and `exact` becomes an OR of the distinct-tag combinations the tokens
// expand into.
func resolveTagLeaf(leaf TagGroup, vocabulary []string) (TagGroup, error) {
	tokens := leafTokens(leaf)
	if len(tokens) == 0 {
		// Nothing to resolve: for `exact` this is the untagged scope, and for
		// every other mode it means no filtering. Both survive resolution.
		return leaf, nil
	}
	mode := leafMatchMode(leaf.Match)
	expanded := make([][]string, len(tokens))
	for i, token := range tokens {
		expanded[i] = expandTagToken(token, vocabulary)
	}
	switch mode {
	case TagsMatchAny, TagsMatchAnyStrict:
		union := make([]string, 0)
		for _, tags := range expanded {
			for _, tag := range tags {
				if !containsString(union, tag) {
					union = append(union, tag)
				}
			}
		}
		return TagGroup{Tags: union, Match: mode}, nil
	case TagsMatchAll, TagsMatchAllStrict:
		// The caller's AND across tokens is preserved; each conjunct becomes an
		// OR over that token's expansion. The lenient `all` keeps its untagged
		// inclusion by matching each conjunct with the lenient `any`:
		// (untagged ∨ E1) ∧ (untagged ∨ E2) ≡ untagged ∨ (E1 ∧ E2).
		inner := TagsMatchAny
		if mode == TagsMatchAllStrict {
			inner = TagsMatchAnyStrict
		}
		conjuncts := make([]TagGroup, 0, len(expanded))
		for _, tags := range expanded {
			conjuncts = append(conjuncts, TagGroup{Tags: tags, Match: inner})
		}
		if len(conjuncts) == 1 {
			return conjuncts[0], nil
		}
		return TagGroup{And: conjuncts}, nil
	case TagsMatchExact:
		return resolveTagLeafExact(tokens, expanded)
	}
	return leaf, nil
}

// resolveTagLeafExact rewrites a fuzzy `exact` leaf: a memory must carry
// exactly one expansion of every token, so the leaf becomes the OR of the
// distinct-tag combinations the expansions admit. The branch count is checked
// before enumerating, because most combinations may be degenerate and counting
// only the survivors would walk the whole product first.
func resolveTagLeafExact(tokens []string, expanded [][]string) (TagGroup, error) {
	branches := 1
	for _, tags := range expanded {
		branches *= len(tags)
	}
	if branches > MaxTagExactCombinations {
		// The text is the reference's, character for character, because it reaches the
		// client as the 422 detail: it starts with a capital and ends with a period,
		// which ST1005 would otherwise flag. Contract fidelity wins over the style rule.
		//
		//nolint:staticcheck // the reference's own wording
		return TagGroup{}, fmt.Errorf(
			"Fuzzy resolution of an 'exact' tag filter expands to %d candidate scopes, "+
				"above the ceiling of %d. Narrow the tags, or use resolve='exact'.",
			branches, MaxTagExactCombinations)
	}
	combinations := make([][]string, 0)
	var walk func(index int, current []string)
	walk = func(index int, current []string) {
		if index == len(expanded) {
			if hasDuplicateTag(current) || containsTagList(combinations, current) {
				return
			}
			combinations = append(combinations, append([]string(nil), current...))
			return
		}
		for _, tag := range expanded[index] {
			walk(index+1, append(current, tag))
		}
	}
	walk(0, nil)
	if len(combinations) == 0 {
		// Every combination was degenerate. Nothing can equal this scope, so
		// keep the leaf unsatisfiable rather than dropping the filter.
		return TagGroup{Tags: tokens, Match: TagsMatchExact}, nil
	}
	if len(combinations) == 1 {
		return TagGroup{Tags: combinations[0], Match: TagsMatchExact}, nil
	}
	or := make([]TagGroup, 0, len(combinations))
	for _, combination := range combinations {
		or = append(or, TagGroup{Tags: combination, Match: TagsMatchExact})
	}
	return TagGroup{Or: or}, nil
}

// expandTagToken returns the vocabulary tags a token resolves to. An exact
// (case-insensitive) hit wins outright — a token that is already a real tag is
// never widened to its neighbours. Otherwise every vocabulary tag in the same
// namespace whose value is close enough is returned. A token that matches
// nothing comes back as itself, which keeps the leaf unsatisfiable rather than
// empty: an empty expansion would read as "no filtering" and widen the result
// set to the whole bank, the opposite of what the caller asked for.
func expandTagToken(token string, vocabulary []string) []string {
	normalized := normalizeTagToken(token)
	exact := make([]string, 0, 1)
	for _, tag := range vocabulary {
		if normalizeTagToken(tag) == normalized {
			exact = append(exact, tag)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	matches := make([]string, 0)
	for _, tag := range vocabulary {
		if similarTag(token, tag) {
			matches = append(matches, tag)
		}
	}
	if len(matches) == 0 {
		return []string{token}
	}
	return matches
}

// normalizeTagToken casefolds a tag and drops surrounding whitespace for the
// exact-hit test; the stored spelling is what ends up in the rewritten leaf, so
// nothing here changes what is queried.
func normalizeTagToken(tag string) string { return strings.ToLower(strings.TrimSpace(tag)) }

// hasDuplicateTag reports whether a combination names the same tag twice, which
// would let a memory with one tag satisfy two tokens.
func hasDuplicateTag(tags []string) bool {
	seen := make(map[string]bool, len(tags))
	for _, tag := range tags {
		if seen[tag] {
			return true
		}
		seen[tag] = true
	}
	return false
}

// containsTagList reports whether a combination is already collected, keeping
// the OR of combinations free of duplicates.
func containsTagList(lists [][]string, want []string) bool {
	for _, list := range lists {
		if len(list) != len(want) {
			continue
		}
		if sameTagSet(list, want) {
			return true
		}
	}
	return false
}
