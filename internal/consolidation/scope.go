package consolidation

import (
	"encoding/json"
	"sort"
	"strings"
)

// Observation scopes.
//
// A retained fact may carry an observation_scopes request: a mode name or an
// explicit list of tag groups (see storage.RetainItem.ObservationScopes). Left
// unset, the fact folds under the combined default — its own tag set. A pass
// must fold a fact only with facts of the same scope and stamp the observation
// it produces with that scope, so one pass over a bank never mixes two scopes
// into a single conclusion.
//
// The modes mirror the reference:
//
//	combined (default) — one scope: the fact's own tag set.
//	per_tag            — one scope per tag.
//	all_combinations   — one scope per non-empty subset of the tags.
//	shared             — the single untagged scope, whatever the fact's tags.
//	[["a"],["b"]]      — one scope per group in the explicit list.
//
// An explicit group entry containing * is a glob over the fact's own tags (see
// ExpandScopeGlobs).
//
// An empty tag set resolves to the untagged scope in every mode, so untagged
// facts never fan out.

// scopeModeCombined is the default mode: a fact folds with the others sharing
// its exact tag set.
const scopeModeCombined = "combined"

// resolveScopes returns the concrete scopes a fact folds into, one tag list per
// scope. Each returned scope is canonical (sorted, deduplicated) and the list
// is sorted, so the same request always resolves to the same scopes.
func resolveScopes(tags []string, raw json.RawMessage) [][]string {
	mode, groups := parseObservationScopes(raw)
	canonicalTags := canonicalTags(tags)
	switch mode {
	case "per_tag":
		if len(canonicalTags) == 0 {
			return [][]string{{}}
		}
		out := make([][]string, 0, len(canonicalTags))
		for _, tag := range canonicalTags {
			out = append(out, []string{tag})
		}
		return dedupeScopes(out)
	case "all_combinations":
		if len(canonicalTags) == 0 {
			return [][]string{{}}
		}
		return dedupeScopes(combinations(canonicalTags))
	case "shared":
		return [][]string{{}}
	case scopeModeCombined, "":
		return [][]string{canonicalTags}
	default:
		// An explicit list of tag groups, with any tag glob resolved against the
		// fact's own tags. An empty list — including a list whose every scope
		// dropped — is treated as the combined default, matching the reference's
		// fall-through.
		expanded := ExpandScopeGlobs(groups, canonicalTags)
		if len(expanded) == 0 {
			return [][]string{canonicalTags}
		}
		return dedupeScopes(expanded)
	}
}

// WriteScopesChanged reports whether relabelling a memory moves it to a different
// set of observation scopes, which is the only case in which its observations
// need rebuilding (the reference's consolidator.write_scopes_changed, #5593).
// Under shared or an explicit scope list the tags take no part in where the
// observations go, so a tag change alone moves nothing; and a scope change that
// resolves to the same scopes (combined over [a] -> [["a"]]) rebuilds nothing.
//
// oldScopes/newScopes are raw observation_scopes JSON values (nil when unset).
// The comparison is over exactly the scopes ResolveScopes produces, so the two
// can never disagree.
func WriteScopesChanged(oldTags []string, oldScopes json.RawMessage, newTags []string, newScopes json.RawMessage) bool {
	before, after := WriteScopes(oldTags, oldScopes), WriteScopes(newTags, newScopes)
	if len(before) != len(after) {
		return true
	}
	for key := range before {
		if !after[key] {
			return true
		}
	}
	return false
}

// WriteScopes returns the canonical scopes a memory will write observations to,
// as a set keyed by scopeKey.
func WriteScopes(tags []string, raw json.RawMessage) map[string]bool {
	out := map[string]bool{}
	for _, scope := range resolveScopes(tags, raw) {
		out[scopeKey(scope)] = true
	}
	return out
}

// ExpandScopeGlobs resolves tag globs (topic:*) in an explicit scope list
// against a memory's own tags (#5666). A glob is replaced by every tag it
// matches, so [["topic:*","user:*"]] scopes a memory by its own topic and user
// tags while ignoring the rest (session:, source: ...), which is what lets a
// client or the bank's default_observation_scopes name a scope whose values only
// the server knows, e.g. topic: tags added from entity labels during retain.
//
// Only * marks a glob: ? and [ are common enough in literal tags that treating
// them as metacharacters would silently change existing explicit scopes.
//
// A scope whose globs all matched nothing is dropped rather than kept empty — an
// empty scope is the untagged, bank-wide one, and a memory the pattern missed
// must not become visible to everyone. A scope written empty on purpose ([[]])
// has no globs and is kept. When every scope drops the result is empty, which the
// callers treat as the combined pass over the memory's own tags.
//
// The result is returned unsorted and uncanonicalized, exactly as the reference
// builds it; resolveScopes canonicalizes it for use.
func ExpandScopeGlobs(scopes [][]string, tags []string) [][]string {
	resolved := make([][]string, 0, len(scopes))
	for _, scope := range scopes {
		hasGlob := false
		for _, entry := range scope {
			if strings.Contains(entry, "*") {
				hasGlob = true
				break
			}
		}
		if !hasGlob {
			resolved = append(resolved, scope)
			continue
		}
		expanded := make([]string, 0, len(scope)+len(tags))
		for _, entry := range scope {
			if !strings.Contains(entry, "*") {
				expanded = append(expanded, entry)
				continue
			}
			for _, tag := range tags {
				if globMatch(entry, tag) {
					expanded = append(expanded, tag)
				}
			}
		}
		if len(expanded) > 0 {
			resolved = append(resolved, expanded)
		}
	}
	return resolved
}

// globMatch is Python's fnmatchcase for a pattern whose only metacharacter is *,
// which is the subset ExpandScopeGlobs treats as a glob. * matches any run of
// characters, including none.
func globMatch(pattern, name string) bool {
	// Greedy scan with backtracking at the most recent star, linear in practice
	// and no allocation.
	px, nx := 0, 0
	star, mark := -1, 0
	for nx < len(name) {
		switch {
		case px < len(pattern) && pattern[px] == name[nx]:
			px++
			nx++
		case px < len(pattern) && pattern[px] == '*':
			star, mark = px, nx
			px++
		case star >= 0:
			px = star + 1
			mark++
			nx = mark
		default:
			return false
		}
	}
	for px < len(pattern) && pattern[px] == '*' {
		px++
	}
	return px == len(pattern)
}

// parseObservationScopes decodes a raw observation_scopes value. A JSON string
// is a mode name; a JSON array is an explicit list of tag groups. An absent,
// null or unparseable value is the combined default. An unrecognised scalar is
// also the default, because the reference's mode vocabulary is closed.
func parseObservationScopes(raw json.RawMessage) (string, [][]string) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return "", nil
	}
	if strings.HasPrefix(trimmed, `"`) {
		var mode string
		if err := json.Unmarshal(raw, &mode); err == nil {
			return mode, nil
		}
		return "", nil
	}
	var groups [][]string
	if err := json.Unmarshal(raw, &groups); err == nil {
		return "explicit", groups
	}
	return "", nil
}

// combinations returns every non-empty subset of tags, in a deterministic order
// (by size, then by position). The result backs the all_combinations mode.
func combinations(tags []string) [][]string {
	out := make([][]string, 0)
	for size := 1; size <= len(tags); size++ {
		indices := make([]int, size)
		for i := range indices {
			indices[i] = i
		}
		for {
			scope := make([]string, size)
			for i, index := range indices {
				scope[i] = tags[index]
			}
			out = append(out, scope)
			// Advance the index combination; stop when it wraps past the end.
			position := size - 1
			for position >= 0 && indices[position] == position+len(tags)-size {
				position--
			}
			if position < 0 {
				break
			}
			indices[position]++
			for i := position + 1; i < size; i++ {
				indices[i] = indices[i-1] + 1
			}
		}
	}
	return out
}

// canonicalTags returns tags sorted and deduplicated.
func canonicalTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	out := append([]string(nil), tags...)
	sort.Strings(out)
	kept := out[:1]
	for _, tag := range out[1:] {
		if tag != kept[len(kept)-1] {
			kept = append(kept, tag)
		}
	}
	return kept
}

// dedupeScopes canonicalizes each scope and drops repeats, then sorts the list
// so a request with the same scopes in a different order resolves identically.
func dedupeScopes(scopes [][]string) [][]string {
	seen := map[string]bool{}
	out := make([][]string, 0, len(scopes))
	for _, scope := range scopes {
		canonical := canonicalTags(scope)
		key := scopeKey(canonical)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, canonical)
	}
	sort.Slice(out, func(i, j int) bool { return scopeKey(out[i]) < scopeKey(out[j]) })
	return out
}

// scopeKey is the canonical string identity of a scope, used to bucket facts.
func scopeKey(scope []string) string {
	return strings.Join(scope, "\x1f")
}

// sameScope reports whether two scopes name the same canonical tag set.
func sameScope(a, b []string) bool {
	return scopeKey(canonicalTags(a)) == scopeKey(canonicalTags(b))
}

// factGroup is the set of facts that fold together at one observation scope.
type factGroup struct {
	scope []string
	facts []Fact
}

// groupFactsByScope buckets facts by the concrete scope each folds into. A fact
// that resolves to several scopes (per_tag with more than one tag,
// all_combinations, or an explicit multi-group list) appears in each of its
// scopes' groups, which is what produces one observation per requested scope.
// The groups are ordered by scope, so a run is deterministic.
func groupFactsByScope(facts []Fact) []factGroup {
	byKey := map[string]*factGroup{}
	keys := make([]string, 0)
	for _, fact := range facts {
		for _, scope := range resolveScopes(fact.Tags, fact.ObservationScopes) {
			key := scopeKey(scope)
			group, ok := byKey[key]
			if !ok {
				group = &factGroup{scope: scope}
				byKey[key] = group
				keys = append(keys, key)
			}
			group.facts = append(group.facts, fact)
		}
	}
	sort.Strings(keys)
	groups := make([]factGroup, 0, len(keys))
	for _, key := range keys {
		groups = append(groups, *byKey[key])
	}
	return groups
}
