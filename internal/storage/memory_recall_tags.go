package storage

import "strings"

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

// leafMatches evaluates one leaf: a literal match against the requested tag, or
// an approximate match by similarity when the leaf resolves fuzzily.
func leafMatches(group TagGroup, present []string) bool {
	requested := group.Tags
	if group.Tag != "" {
		requested = append([]string{group.Tag}, requested...)
	}
	if len(requested) == 0 {
		return true
	}
	if strings.EqualFold(group.Resolve, TagResolveFuzzy) {
		return fuzzyLeafMatch(requested, present)
	}
	return matchTags(requested, present, group.Match)
}

// fuzzyLeafMatch reports whether any requested tag is approximately similar to
// any present tag.
func fuzzyLeafMatch(requested, present []string) bool {
	for _, want := range requested {
		for _, have := range present {
			if similarTag(want, have) {
				return true
			}
		}
	}
	return false
}

// similarTag reports whether two tags are close: one contains the other
// case-insensitively.
func similarTag(a, b string) bool {
	la := strings.ToLower(strings.TrimSpace(a))
	lb := strings.ToLower(strings.TrimSpace(b))
	if la == "" || lb == "" {
		return false
	}
	return strings.Contains(la, lb) || strings.Contains(lb, la)
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
